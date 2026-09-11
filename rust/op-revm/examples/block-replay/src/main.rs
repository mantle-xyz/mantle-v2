//! Replays a range of Mantle blocks through the local `op-revm` and compares receipts,
//! `receiptsRoot`, logs, deposit nonces and post-state against a reference archive node.
//! Configured entirely through the environment; see `.env.example`.
#![cfg_attr(not(test), warn(unused_crate_dependencies))]

use alloy_consensus::{
    Eip658Value, Receipt, ReceiptWithBloom, Transaction as _, TxEip1559, TxEip2930, TxEip7702,
    TxLegacy, proofs::calculate_receipt_root, transaction::SignerRecoverable,
};
use alloy_eips::{
    BlockId, Decodable2718, Typed2718,
    eip2935::{HISTORY_SERVE_WINDOW, HISTORY_STORAGE_ADDRESS},
    eip4788::BEACON_ROOTS_ADDRESS,
};
use alloy_op_hardforks::{
    MANTLE_MAINNET_ARSIA_TIMESTAMP, MANTLE_MAINNET_LIMB_TIMESTAMP, MANTLE_MAINNET_SKADI_TIMESTAMP,
    MANTLE_SEPOLIA_ARSIA_TIMESTAMP, MANTLE_SEPOLIA_LIMB_TIMESTAMP, MANTLE_SEPOLIA_SKADI_TIMESTAMP,
};
use alloy_primitives::{Address, B256, Bytes, U256, keccak256};
use alloy_provider::{
    Provider, ProviderBuilder,
    network::primitives::BlockTransactions,
    transport::{
        RpcError, TransportError,
        layers::{RateLimitRetryPolicy, RetryBackoffLayer},
    },
};
use alloy_rpc_client::RpcClient;
use dotenv::dotenv;
use futures::stream::{self, StreamExt};
use op_alloy_consensus::{OpDepositReceipt, OpReceiptEnvelope, OpTxEnvelope, TxDeposit};
use op_alloy_network::Optimism;
use op_revm::{
    OpTransaction,
    api::{builder::OpBuilder, default_ctx::DefaultOp},
    spec::OpSpecId,
    transaction::deposit::DepositTransactionParts,
};
use revm::{
    Context, ExecuteCommitEvm,
    context::tx::TxEnv,
    context_interface::{ContextTr, JournalTr, either::Either},
    database::{AlloyDB, CacheDB, StateBuilder},
    database_interface::{DatabaseRef, WrapDatabaseAsync},
    primitives::{
        KECCAK_EMPTY, StorageKey, StorageValue, TxKind,
        eip4844::BLOB_BASE_FEE_UPDATE_FRACTION_PRAGUE, hardfork::SpecId,
    },
    state::{AccountInfo, Bytecode},
};
use serde_json::Value;
use std::{fmt::Write as _, fs, future::IntoFuture, path::Path, time::Instant};

/// Per-request timeout. Long enough that a genuinely slow historical read still lands,
/// short enough that a lost response is noticed within one block's worth of work.
const DEFAULT_REQUEST_TIMEOUT_SECS: u64 = 30;
/// Ceiling on a whole block. Generous: it exists to break a hang, not to pace the run.
const DEFAULT_BLOCK_TIMEOUT_SECS: u64 = 300;
/// Detect a silently dead connection instead of waiting for the request timeout.
const TCP_KEEPALIVE_SECS: u64 = 30;
/// Recycle pooled connections; a long run otherwise keeps stale ones around.
const POOL_IDLE_TIMEOUT_SECS: u64 = 60;
/// EIP-4788 ring-buffer length. Not exported by `alloy-eips`.
const BEACON_ROOTS_HISTORY_BUFFER_LENGTH: u64 = 8191;
/// Consecutive block failures before the shard gives up. A node that has gone away fails
/// every block; the progress file makes resuming cheap once it is back.
const MAX_CONSECUTIVE_ERRORS: usize = 50;
/// Retries per request. A read is idempotent, so the only cost of retrying is latency.
const MAX_RPC_RETRIES: u32 = 5;
/// Fixed base delay before the first retry, in milliseconds.
const RETRY_INITIAL_BACKOFF_MS: u64 = 200;
/// The retry layer doubles as a client-side rate limiter. This tool talks to a dedicated
/// reference node, so the limiter is deliberately set high enough to never bind.
const RETRY_COMPUTE_UNITS_PER_SECOND: u64 = 1_000_000;

/// Reports an empty account as absent.
///
/// `AlloyDB` answers `basic_ref` with `Some(AccountInfo)` for every address, because
/// `eth_getBalance` / `eth_getTransactionCount` / `eth_getCode` return zeros for an address
/// that is not in the trie -- JSON-RPC cannot express "absent". revm then journals such an
/// account as loaded and existing, and every rule keyed on existence takes the wrong
/// branch: EIP-7702's authority refund, EIP-161 empty-account deletion, CREATE collision
/// checks, SELFDESTRUCT.
///
/// Post-EIP-161 the mapping is exact rather than heuristic: an account present in the trie
/// cannot be empty, so empty and absent are the same state.
#[derive(Debug, Clone)]
struct AbsentIfEmpty<DB>(DB);

impl<DB: DatabaseRef> DatabaseRef for AbsentIfEmpty<DB> {
    type Error = DB::Error;

    fn basic_ref(&self, address: Address) -> Result<Option<AccountInfo>, Self::Error> {
        Ok(self.0.basic_ref(address)?.filter(|info| !info.is_empty()))
    }

    fn code_by_hash_ref(&self, code_hash: B256) -> Result<Bytecode, Self::Error> {
        self.0.code_by_hash_ref(code_hash)
    }

    fn storage_ref(
        &self,
        address: Address,
        index: StorageKey,
    ) -> Result<StorageValue, Self::Error> {
        self.0.storage_ref(address, index)
    }

    fn block_hash_ref(&self, number: u64) -> Result<B256, Self::Error> {
        self.0.block_hash_ref(number)
    }
}

/// The endpoint in a form that is safe to log.
///
/// Scheme, host and port are what an operator needs and carry nothing secret. The rest is
/// dropped unless the path is empty, because a proxied endpoint can put a bearer token in
/// the path -- and a credential written to a log outlives the log. Erring towards dropping
/// costs a path; erring the other way costs the token. A direct node URL has no path, so
/// the common case still prints in full.
fn loggable_endpoint(url: &reqwest::Url) -> String {
    let host = url.host_str().unwrap_or("<unparsed-host>");
    let mut out = match url.port() {
        Some(port) => format!("{}://{host}:{port}", url.scheme()),
        None => format!("{}://{host}", url.scheme()),
    };
    let bare = url.path().trim_matches('/').is_empty() &&
        url.query().is_none() &&
        url.username().is_empty() &&
        url.password().is_none();
    if !bare {
        out.push_str("/<redacted>");
    }
    out
}

/// Buffer one block's output instead of printing it. With concurrent workers, direct
/// `println!` from inside a block interleaves with every other in-flight block and the log
/// becomes unreadable -- so each block builds its own buffer and the driver emits it whole.
macro_rules! logln {
    ($out:expr, $($arg:tt)*) => {{ let _ = writeln!($out, $($arg)*); }};
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    // Set up the HTTP transport which is consumed by the RPC client.
    dotenv().ok();
    let mantle_url = std::env::var("MANTLE_URL").unwrap();
    let chain_id = std::env::var("CHAIN_ID").unwrap().parse()?;
    let rpc_url: reqwest::Url = mantle_url.parse()?;

    // The default client has no request timeout, no retries and no TCP keepalive. Without
    // them one lost response hangs the whole run: the process sleeps on an established
    // socket, the progress file stops growing, and nothing is logged.
    let timeout_secs: u64 = std::env::var("REQUEST_TIMEOUT_SECS")
        .ok()
        .and_then(|v| v.parse().ok())
        .filter(|v| *v > 0)
        .unwrap_or(DEFAULT_REQUEST_TIMEOUT_SECS);
    let http = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(timeout_secs))
        .tcp_keepalive(std::time::Duration::from_secs(TCP_KEEPALIVE_SECS))
        .pool_idle_timeout(std::time::Duration::from_secs(POOL_IDLE_TIMEOUT_SECS))
        .build()?;

    // The default `RateLimitRetryPolicy` retries only HTTP 429/503 and a `Custom` error
    // whose message contains "429 Too Many Requests"; a reqwest timeout is a `Custom` error
    // with a different message and would not be retried. Every request here is a read, so
    // retrying any transport failure is safe.
    let retry = RetryBackoffLayer::new_with_policy(
        MAX_RPC_RETRIES,
        RETRY_INITIAL_BACKOFF_MS,
        RETRY_COMPUTE_UNITS_PER_SECOND,
        RateLimitRetryPolicy::default()
            .or(|err: &TransportError| matches!(err, RpcError::Transport(_))),
    );

    // Keep a loggable form of the endpoint before the URL is consumed; see the startup line.
    let endpoint = loggable_endpoint(&rpc_url);

    // Create a provider
    let client = ProviderBuilder::<_, _, Optimism>::default()
        .connect_client(RpcClient::builder().layer(retry).http_with_client(http, rpc_url));

    // Params
    let start_block =
        std::env::var("START_BLOCK").expect("START_BLOCK must be set").parse::<u64>()?;
    let end_block = std::env::var("END_BLOCK").expect("END_BLOCK must be set").parse::<u64>()?;
    // `OP_SPEC` is an OVERRIDE, not a default. Leave it unset and each block's fork is
    // derived from its own timestamp (see `spec_for_timestamp`) -- the only way to replay a
    // range that crosses a fork boundary.
    let spec_override = match std::env::var("OP_SPEC") {
        Ok(v) if !v.trim().is_empty() => Some(v.parse::<OpSpecId>().expect(
            "Invalid OP_SPEC. Valid: Bedrock, Regolith, Canyon, Ecotone, Fjord, Granite, \
             Holocene, Isthmus, Jovian, Osaka, Arsia, Karst, Lagoon. Leave unset to derive \
             the fork from each block's timestamp.",
        )),
        _ => None,
    };

    // Check if state verification is enabled
    let state_verify =
        std::env::var("STATE_VERIFY").unwrap_or_else(|_| "false".to_string()).to_lowercase() ==
            "true";

    // Check if cache_db export is enabled
    // Default to false if not set
    let export_cache_db =
        std::env::var("EXPORT_CACHE_DB").unwrap_or_else(|_| "false".to_string()).to_lowercase() ==
            "true";

    if export_cache_db {
        println!("⚠️  EXPORT_CACHE_DB is enabled - cache_db will be exported");
    }

    // Bounded block-level concurrency. Blocks are independent -- each rebuilds its own
    // `AlloyDB` from `block-1` and shares no state -- so this is the main lever. Batching
    // cannot replace it: most of a block's round-trips are `AlloyDB`'s lazy account/slot
    // fetches, discovered one at a time during execution, and they must NOT be prefetched
    // (a second state source risks a false MATCH).
    //
    // Measured ceiling: each fetch goes through `WrapDatabaseAsync` ->
    // `tokio::task::block_in_place`, which occupies a whole worker thread for its duration,
    // so in-process concurrency is capped by the worker count (`hw.ncpu`). Beyond ~8 it
    // stops helping; running several processes over disjoint ranges scales linearly instead.
    //
    // Values above 1 also put every buffered block in one task (see the stream below),
    // which is where `block_in_place` can lose a wakeup. Prefer more processes.
    let concurrency: usize = std::env::var("CONCURRENCY")
        .ok()
        .and_then(|v| v.parse().ok())
        .filter(|v| *v > 0)
        .unwrap_or(1);

    // Resume support: a long range WILL be interrupted (token expiry, sleep, a code change).
    let progress_path =
        std::env::var("PROGRESS_FILE").unwrap_or_else(|_| "replay-progress.txt".to_string());
    let done = load_progress(&progress_path)?;
    let todo: Vec<u64> = (start_block..=end_block).filter(|b| !done.contains(b)).collect();
    let total_range = (end_block - start_block + 1) as usize;

    // Only mismatching blocks print detail; a full-range run would otherwise emit tens of GB,
    // most of it two 512-char bloom strings per transaction that only matter on failure.
    let verbose = std::env::var("VERBOSE").map(|v| v == "true").unwrap_or(false);

    // Ceiling on one block, not one request: a block issues one round of reads per touched
    // account, so a legitimately heavy block needs far longer than `REQUEST_TIMEOUT_SECS`.
    let block_timeout_secs: u64 = std::env::var("BLOCK_TIMEOUT_SECS")
        .ok()
        .and_then(|v| v.parse().ok())
        .filter(|v| *v > 0)
        .unwrap_or(DEFAULT_BLOCK_TIMEOUT_SECS);

    // Everything this process was told to do, on one line. Shards are distinguished only by
    // their environment, which `ps` does not show, so without this the only way to tell what
    // a running shard is working on is to read /proc/<pid>/environ.
    //
    // Host and port only, never the whole URL: behind an authenticating proxy the path
    // carries a bearer token, and a URL in a log or in an error message leaks it.
    println!(
        "chain={chain_id} endpoint={endpoint} spec={} state_verify={state_verify} \
         range [{start_block}..={end_block}] = {total_range} blocks; {} already done, {} to \
         go; concurrency={concurrency}; progress={progress_path}",
        spec_override.map_or_else(|| "per-block".to_string(), |s| format!("{s:?}")),
        total_range - todo.len(),
        todo.len()
    );

    let progress = std::sync::Arc::new(std::sync::Mutex::new(
        std::fs::OpenOptions::new().create(true).append(true).open(&progress_path)?,
    ));
    let started = Instant::now();
    let mut mismatched: Vec<u64> = Vec::new();
    let mut errored: Vec<u64> = Vec::new();
    let mut consecutive_errors = 0usize;
    let mut n = 0usize;

    let mut stream = stream::iter(todo.iter().copied())
        .map(|i| {
            let client = client.clone();
            // Hard per-block timeout.
            //
            // `buffer_unordered` does not spawn: every buffered block is polled inside the
            // single consuming task, while `AlloyDB`'s reads go through `WrapDatabaseAsync`
            // -> `tokio::task::block_in_place`. That combination can lose a wakeup and stall
            // the run with nothing in flight, which a per-request timeout cannot catch.
            // `tokio::spawn` is not an alternative: revm's `Context` is `!Send`.
            //
            // The timeout registers a timer, so a stuck block becomes a recorded error and
            // the range carries on instead of stalling silently.
            async move {
                let mut out = String::new();
                let r = match tokio::time::timeout(
                    std::time::Duration::from_secs(block_timeout_secs),
                    process_block(
                        i,
                        chain_id,
                        spec_override,
                        state_verify,
                        export_cache_db,
                        client,
                        &mut out,
                    ),
                )
                .await
                {
                    Ok(r) => r,
                    Err(_) => Err(anyhow::anyhow!("block timed out after {block_timeout_secs}s")),
                };
                (i, r, out)
            }
        })
        .buffer_unordered(concurrency);

    while let Some((block, res, out)) = stream.next().await {
        n += 1;
        match res {
            Ok(true) => {
                consecutive_errors = 0;
                record_progress(&progress, block, "OK")?;
                if verbose {
                    print!("{out}");
                }
            }
            Ok(false) => {
                consecutive_errors = 0;
                mismatched.push(block);
                record_progress(&progress, block, "MISMATCH")?;
                println!("{out}");
            }
            Err(e) => {
                // A failing block is a finding, not a reason to abandon the range -- but a
                // reference node that has gone away fails every block, and marking millions
                // of them ERROR is worse than stopping.
                println!("{out}\n==== BLOCK {block} ERROR ====\n{e}");
                errored.push(block);
                record_progress(&progress, block, "ERROR")?;
                consecutive_errors += 1;
                if consecutive_errors >= MAX_CONSECUTIVE_ERRORS {
                    anyhow::bail!(
                        "aborting: {consecutive_errors} consecutive block failures, last at \
                         {block}. The reference node is probably unreachable; the progress \
                         file lets this resume once it is back."
                    );
                }
            }
        }
        if n % 100 == 0 || n == todo.len() {
            let el = started.elapsed().as_secs_f64();
            println!(
                "  [progress] {n}/{} blocks  {:.2}s/block  elapsed {:.0}s  eta {:.0}s",
                todo.len(),
                el / n as f64,
                el,
                el / n as f64 * (todo.len() - n) as f64
            );
        }
    }
    mismatched.sort_unstable();
    if !errored.is_empty() {
        errored.sort_unstable();
        println!("\n==== {} block(s) ERRORED: {errored:?} ====", errored.len());
    }

    // ===== Range summary: which blocks (if any) diverge from on-chain =====
    let total = end_block - start_block + 1;
    println!("\n==== REPLAY SUMMARY [{start_block}..={end_block}] ({total} blocks) ====");
    if mismatched.is_empty() {
        println!("ALL BLOCKS MATCH ✅ (every receipt's status/gas/cumGas/logs/bloom == on-chain)");
    } else {
        println!("MISMATCH ❌ at {} / {total} block(s): {:?}", mismatched.len(), mismatched);
    }

    Ok(())
}

async fn process_block(
    block_number: u64,
    chain_id: u64,
    spec_override: Option<OpSpecId>,
    state_verify: bool,
    export_cache_db: bool,
    client: impl Provider<Optimism> + Clone,
    out: &mut String,
) -> anyhow::Result<bool> {
    // Fetch the transaction-rich block. Errors, not panics: a reference node that goes
    // away mid-run would otherwise kill the whole shard, bypassing the per-block error
    // handling that exists precisely so a long range survives a transient outage.
    let block = client
        .get_block_by_number(block_number.into())
        .await
        .map_err(|e| anyhow::anyhow!("block {block_number}: fetch failed: {e}"))?
        .ok_or_else(|| anyhow::anyhow!("block {block_number}: not found"))?;

    // Pick the fork from the block's own timestamp. `OP_SPEC` is only an override
    // for experiments -- a range that crosses a fork boundary cannot be replayed with one
    // hard-coded spec.
    let spec =
        spec_override.unwrap_or_else(|| spec_for_timestamp(chain_id, block.header.timestamp));
    logln!(
        out,
        "Fetched block number: {block_number}  ts={}  spec={spec:?}{}",
        block.header.timestamp,
        if spec_override.is_some() { "  (OP_SPEC override)" } else { "" }
    );
    let previous_block_number = block_number - 1;

    // Use the previous block state as the db with caching
    let prev_id: BlockId = previous_block_number.into();
    // SAFETY: This cannot fail since this is in the top-level tokio runtime

    let state_db = WrapDatabaseAsync::new(AlloyDB::new(client.clone(), prev_id)).unwrap();
    let mut cache_db: CacheDB<_> = CacheDB::new(AbsentIfEmpty(state_db));

    // Pre-block system writes. A node applies these before any transaction, so a replay
    // that starts from the parent state and only executes transactions feeds the previous
    // block's values to anything that reads them. Same shape as an unset `prevrandao`:
    // identical gas, a different result, so no gas mismatch points at it.
    //
    // Writing the storage slots is equivalent to the system call: both predeploys reject
    // every non-system caller, so nothing but this write can change them. That also makes
    // the result checkable -- these slots must equal what the node reports for this block.
    let eth_spec = spec.into_eth_spec();
    if eth_spec.is_enabled_in(SpecId::CANCUN) &&
        let Some(parent_beacon_root) = block.header.parent_beacon_block_root
    {
        // EIP-4788: timestamp at `timestamp % N`, parent beacon root at `timestamp % N + N`.
        let idx = block.header.timestamp % BEACON_ROOTS_HISTORY_BUFFER_LENGTH;
        cache_db.insert_account_storage(
            BEACON_ROOTS_ADDRESS,
            StorageKey::from(idx),
            StorageValue::from(block.header.timestamp),
        )?;
        cache_db.insert_account_storage(
            BEACON_ROOTS_ADDRESS,
            StorageKey::from(idx + BEACON_ROOTS_HISTORY_BUFFER_LENGTH),
            StorageValue::from_be_bytes(parent_beacon_root.0),
        )?;
    }
    if eth_spec.is_enabled_in(SpecId::PRAGUE) {
        // EIP-2935: parent block hash at `(block.number - 1) % HISTORY_SERVE_WINDOW`.
        let idx = (block.header.number - 1) % HISTORY_SERVE_WINDOW as u64;
        cache_db.insert_account_storage(
            HISTORY_STORAGE_ADDRESS,
            StorageKey::from(idx),
            StorageValue::from_be_bytes(block.header.parent_hash.0),
        )?;
    }

    let mut state = StateBuilder::new_with_database(cache_db).build();
    let ctx = Context::op()
        .with_db(&mut state)
        .modify_block_chained(|b| {
            b.number = U256::from(block.header.number);
            b.beneficiary = block.header.beneficiary;
            b.timestamp = U256::from(block.header.timestamp);

            b.difficulty = block.header.difficulty;
            b.gas_limit = block.header.gas_limit;
            b.basefee = block.header.base_fee_per_gas.unwrap_or_default();
            // `block.prevrandao` is post-Merge randomness, not a derived value: leaving it
            // unset feeds the EVM zero instead of the header's `mixHash`. Execution costs
            // the same, so a contract that hashes it produces a different log topic with
            // matching gas -- a `receiptsRoot` mismatch with no gas mismatch to point at it.
            b.prevrandao = Some(block.header.mix_hash);
            // Mantle's headers carry `excessBlobGas = 0`, which happens to equal revm's
            // default, so this changes nothing today -- but the value belongs to the header,
            // not to a default. `slot_num` is deliberately left alone: EIP-7843 arrives with
            // Amsterdam and Mantle's top fork maps to `SpecId::OSAKA`, so nothing reads it.
            b.set_blob_excess_gas_and_price(
                block.header.excess_blob_gas.unwrap_or_default(),
                BLOB_BASE_FEE_UPDATE_FRACTION_PRAGUE,
            );
        })
        .modify_cfg_chained(|c| {
            c.chain_id = chain_id;
            // Must be `set_spec_and_mainnet_gas_params`, NOT `c.spec = spec`.
            // revm 40 split the gas parameters out of the spec: assigning the field on an
            // already-built `CfgEnv` leaves `gas_params` at `OpSpecId::default()`'s values,
            // so the EIP-7623 calldata floor never applies and every gas comparison this
            // harness makes is silently wrong (the same 192-gas class of bug that showed up
            // in op-revm's bvm_eth replay fixtures). Upstream marks the bare setter
            // `#[deprecated(note = "Use CfgEnv::set_spec_and_mainnet_gas_params instead")]`.
            c.set_spec_and_mainnet_gas_params(spec);
            // EIP-7825 override -- NOT optional. Mantle has no per-transaction gas cap, and
            // the override lives downstream of revm, so every site building its own `CfgEnv`
            // must set it. Left unset, real transactions are rejected: mainnet block
            // 100,437,956 carries one with `gas_limit = 54_000_000` against a cap of
            // 16,777,216.
            c.tx_gas_limit_cap = Some(u64::MAX);
        });

    let mut evm = ctx.build_op();

    let txs = block.transactions.len();
    logln!(out, "Found {txs} transactions.");

    // let console_bar = Arc::new(ProgressBar::new(txs as u64));
    let start = Instant::now();

    // Fill in CfgEnv
    let BlockTransactions::Hashes(transactions) = block.transactions else {
        panic!("Wrong transaction type")
    };

    // Running cumulative gas used, mirroring how op-reth fills receipts.
    let mut cumulative_gas_used: u64 = 0;
    let mut all_receipts_match = true;
    // Collected to compute `receiptsRoot` and compare it with the block header.
    // The per-field checks below (status / cumulativeGasUsed / logs / logsBloom) do NOT
    // cover the RLP encoding, the transaction-type byte, or the deposit-only
    // `depositNonce` / `depositReceiptVersion` fields -- all three land in the root and
    // nowhere else.
    let mut local_receipts: Vec<OpReceiptEnvelope> = Vec::with_capacity(transactions.len());

    for tx_hash in transactions.iter() {
        logln!(out, "tx_hash: {tx_hash}");
        let raw_tx = client
            .clone()
            .client()
            .request::<&[B256; 1], Bytes>("debug_getRawTransaction", &[*tx_hash])
            .await
            .expect("Block not found");
        let tx = OpTxEnvelope::decode_2718(&mut raw_tx.as_ref()).unwrap();

        let caller = tx.recover_signer().unwrap();
        let optx = prepare_tx_env(&tx, caller, raw_tx)?;
        evm.0.modify_tx(|etx| {
            *etx = optx;
        });

        let is_deposit = tx.is_deposit();
        logln!(out, "is_deposit: {is_deposit}");

        // `depositNonce` must be DERIVED, not read back from the canonical receipt
        // -- reading it would make the root comparison circular. Read BEFORE executing:
        // verified on mainnet block 89,718,944 that the receipt's `depositNonce` (143581)
        // equals the sender's nonce in the parent block, and one less than its nonce after
        // this block.
        let deposit_nonce = if tx.is_deposit() {
            Some(
                evm.0
                    .ctx
                    .journal_mut()
                    .load_account(caller)
                    .map_err(|e| anyhow::anyhow!("block {block_number}: load caller: {e:?}"))?
                    .info
                    .nonce,
            )
        } else {
            None
        };

        let res = evm.replay_commit();

        if let Err(ref res) = res {
            logln!(out, "Got error: {res:?}");
        }

        // Don't `.unwrap()`: a transaction that fails to execute is a finding, and the bare
        // panic loses which block and which transaction it happened on.
        let exec = res.map_err(|e| {
            anyhow::anyhow!("block {block_number} tx {tx_hash}: replay_commit failed: {e:?}")
        })?;
        let actual_gas_used = exec.tx_gas_used();
        let is_success = exec.is_success();
        // Terms behind `tx_gas_used`, reported when the gas comparison fails.
        //
        // `tx_gas_used()` is `max(total_gas_spent - inner_refunded, floor_gas)`, so a gas
        // mismatch is one of three things: a different spend, a different refund, or the
        // floor binding differently. Reading the accessors the implementation itself uses
        // keeps the breakdown from drifting away from it.
        let gas_spent = exec.gas().total_gas_spent();
        let gas_refunded = exec.gas().inner_refunded();
        let gas_floor = exec.gas().floor_gas();

        // Extract logs exactly as op-reth's receipt builder does: `result.into_logs()`.
        // A failed deposit's persisted BVM_ETH mint/transfer logs live in
        // `ExecutionResult::Halt { logs }`, so `into_logs()` surfaces them. Using the
        // real `into_logs()` path means this proxy cannot bless a fix whose logs never
        // reach the receipt builder (e.g. logs stashed only in `OpHaltReason`).
        let receipt_logs: Vec<_> = exec.into_logs();

        cumulative_gas_used += actual_gas_used;

        // Build the receipt exactly as op-reth would and derive its logsBloom.
        let local_receipt = Receipt {
            status: Eip658Value::Eip658(is_success),
            cumulative_gas_used,
            logs: receipt_logs.clone(),
        };
        let local_bloom = local_receipt.bloom_slow();

        // Build the typed envelope so `receiptsRoot` can be derived. The type byte
        // and, for deposits, `depositNonce` / `depositReceiptVersion` are part of the RLP and
        // are not checked by any of the per-field comparisons below.
        // `deposit_receipt_version` is `None` on Mantle -- see MANTLE_CHANGES.md 3.7
        // (commit 760129f), and confirmed by the canonical receipts, which carry
        // `depositNonce` but no `depositReceiptVersion`.
        local_receipts.push(build_receipt_envelope(&tx, local_receipt.clone(), local_bloom)?);
        let local_bloom_hex = format!("0x{}", alloy_primitives::hex::encode(local_bloom));

        // Fetch the canonical on-chain receipt and compare field-by-field.
        let oc: Value =
            client.clone().client().request("eth_getTransactionReceipt", &[*tx_hash]).await?;
        let oc_logs = oc["logs"].as_array().map(|a| a.len()).unwrap_or(0);
        let oc_bloom = oc["logsBloom"].as_str().unwrap_or("").to_string();
        let oc_status = u64::from_str_radix(
            oc["status"].as_str().unwrap_or("0x0").trim_start_matches("0x"),
            16,
        )
        .unwrap_or(0);
        let oc_cum = u64::from_str_radix(
            oc["cumulativeGasUsed"].as_str().unwrap_or("0x0").trim_start_matches("0x"),
            16,
        )
        .unwrap_or(0);
        // Per-tx gas, kept from the pre-merge gas check so a single diverging tx is
        // visible even when the cumulative total happens to line up.
        let oc_gas = u64::from_str_radix(
            oc["gasUsed"].as_str().unwrap_or("0x0").trim_start_matches("0x"),
            16,
        )
        .unwrap_or(0);

        let status_ok = (is_success as u64) == oc_status;
        let gas_ok = actual_gas_used == oc_gas;
        let cum_ok = cumulative_gas_used == oc_cum;
        // Compare logs BYTE-FOR-BYTE and IN ORDER, not by count. Receipt RLP is
        // order-sensitive; `logsBloom` is NOT (a permuted log list yields an identical
        // bloom). So counting alone, even next to the bloom check, blesses both a permutation
        // and a wrong topic. Do not reuse op-revm's fixture-test unordered matching here.
        let logs_ok = compare_logs_ordered(&receipt_logs, oc["logs"].as_array());
        let bloom_ok = local_bloom_hex.eq_ignore_ascii_case(&oc_bloom);
        // `depositNonce` is not in the receipt RLP (see `build_receipt_envelope`),
        // so `receiptsRoot` cannot check it. Compare it directly: the harness derives it from
        // the sender's pre-execution nonce and the canonical receipt reports it.
        let deposit_nonce_ok = match deposit_nonce {
            Some(local) => {
                let onchain = oc["depositNonce"]
                    .as_str()
                    .and_then(|v| u64::from_str_radix(v.trim_start_matches("0x"), 16).ok());
                let ok = onchain == Some(local);
                logln!(
                    out,
                    "  depNonce local={local} onchain={} {}",
                    onchain.map_or_else(|| "-".to_string(), |v| v.to_string()),
                    if ok { "✅" } else { "❌" }
                );
                ok
            }
            None => true,
        };
        let receipt_ok = status_ok && gas_ok && cum_ok && logs_ok && bloom_ok && deposit_nonce_ok;
        all_receipts_match &= receipt_ok;

        logln!(out, "RECEIPT_CHECK tx={tx_hash} is_deposit={is_deposit}");
        logln!(
            out,
            "  status  local={} onchain={} {}",
            is_success as u64,
            oc_status,
            if status_ok { "✅" } else { "❌" }
        );
        logln!(
            out,
            "  gas     local={} onchain={} {}",
            actual_gas_used,
            oc_gas,
            if gas_ok { "✅" } else { "❌" }
        );
        if !gas_ok {
            // Which term diverged, plus the inputs behind intrinsic gas so the numbers can
            // be recomputed by hand. Computed here so a matching block pays nothing.
            let input = tx.input();
            let nonzero = input.iter().filter(|b| **b != 0).count() as u64;
            let zero = input.len() as u64 - nonzero;
            let access_list = tx.access_list();
            let (al_addrs, al_slots) = access_list.map_or((0, 0), |l| {
                (l.len(), l.iter().map(|item| item.storage_keys.len()).sum::<usize>())
            });
            logln!(
                out,
                "          spent={} refunded={} floor={} -> max(spent-refunded, floor); \
                 delta={}",
                gas_spent,
                gas_refunded,
                gas_floor,
                actual_gas_used as i128 - oc_gas as i128
            );
            logln!(
                out,
                "          tx_type={:#04x} calldata={}B nonzero={} zero={} tokens={} \
                 access_list={}/{} auths={}",
                tx.ty(),
                input.len(),
                nonzero,
                zero,
                4 * nonzero + zero,
                al_addrs,
                al_slots,
                tx.authorization_list().map_or(0, |a| a.len())
            );
        }
        logln!(
            out,
            "  cumGas  local={} onchain={} {}",
            cumulative_gas_used,
            oc_cum,
            if cum_ok { "✅" } else { "❌" }
        );
        logln!(
            out,
            "  logs    local={} onchain={} bytewise={} {}",
            receipt_logs.len(),
            oc_logs,
            logs_ok,
            if logs_ok { "✅" } else { "❌" }
        );
        if !logs_ok {
            report_log_diff(&receipt_logs, oc["logs"].as_array());
        }
        logln!(out, "  bloom   match={} {}", bloom_ok, if bloom_ok { "✅" } else { "❌" });
        logln!(out, "    local_bloom={local_bloom_hex}");
        logln!(out, "    chain_bloom={oc_bloom}");
        logln!(out, "  --- receipt {}", if receipt_ok { "MATCH✅" } else { "MISMATCH❌" });
    }

    // Compare the root itself, not just the per-field checks: those miss the RLP encoding,
    // the type byte and the deposit-only fields.
    let local_receipts_root = calculate_receipt_root(&local_receipts);
    let root_ok = local_receipts_root == block.header.receipts_root;
    all_receipts_match &= root_ok;
    logln!(
        out,
        "  receiptsRoot local={local_receipts_root} onchain={} {}",
        block.header.receipts_root,
        if root_ok { "✅" } else { "❌" }
    );

    logln!(
        out,
        "==== BLOCK {block_number} ALL RECEIPTS {} ====",
        if all_receipts_match {
            "MATCH ✅ (receiptsRoot verified against the header)"
        } else {
            "MISMATCH ❌"
        }
    );

    // Verify account states against the reference node if enabled
    if state_verify {
        if let Err(e) =
            verify_state_with_plain_reads(&state, client.clone(), block_number, out).await
        {
            logln!(out, "⚠️  Error during verification: {}", e);
        }
    }

    // Export cache_db data if enabled
    if export_cache_db {
        if let Err(e) = export_cache_db_data(&state, block_number).await {
            logln!(out, "⚠️  Error during cache_db export: {}", e);
        } else {
            logln!(out, "--- cache_db exported✅");
        }
    }

    let elapsed = start.elapsed();
    logln!(out, "Finished block {block_number}. Total CPU time: {:.6}s", elapsed.as_secs_f64());

    Ok(all_receipts_match)
}

/// Export cache_db data to JSON file
async fn export_cache_db_data<P: alloy_provider::Provider<op_alloy_network::Optimism> + Clone>(
    state: &revm::database::State<
        revm::database::CacheDB<
            AbsentIfEmpty<
                revm::database_interface::WrapDatabaseAsync<
                    revm::database::AlloyDB<op_alloy_network::Optimism, P>,
                >,
            >,
        >,
    >,
    block_number: u64,
) -> anyhow::Result<()> {
    // Create output directory if it doesn't exist
    let output_dir = Path::new("cache_db_exports");
    if !output_dir.exists() {
        fs::create_dir_all(output_dir)?;
    }

    // Access CacheDB's cache directly since State.database is pub
    // Cache already supports serde serialization when serde feature is enabled
    // Serialize the cache to JSON
    let json = serde_json::to_string_pretty(&state.database.cache)?;

    // Write to file
    let file_path = output_dir.join(format!("block_{}.json", block_number));
    fs::write(&file_path, json)?;

    println!("Exported cache_db data to: {}", file_path.display());

    Ok(())
}

/// Blocks already recorded as processed, so an interrupted run resumes. Any recorded outcome
/// (OK / MISMATCH / ERROR) counts as done -- re-running a known mismatch just reproduces it.
fn load_progress(path: &str) -> anyhow::Result<std::collections::HashSet<u64>> {
    let mut set = std::collections::HashSet::new();
    if let Ok(txt) = fs::read_to_string(path) {
        for line in txt.lines() {
            if let Some(v) = line.split_whitespace().next().and_then(|s| s.parse::<u64>().ok()) {
                set.insert(v);
            }
        }
    }
    Ok(set)
}

/// Append-only and flushed per block: a crash loses at most the block in flight.
fn record_progress(
    file: &std::sync::Arc<std::sync::Mutex<std::fs::File>>,
    block: u64,
    outcome: &str,
) -> anyhow::Result<()> {
    use std::io::Write as _;
    let mut f = file.lock().expect("progress file mutex poisoned");
    writeln!(f, "{block} {outcome}")?;
    f.flush()?;
    Ok(())
}

/// Wrap a receipt in its typed OP envelope so `calculate_receipt_root` produces the same
/// RLP the chain does.
///
/// `deposit_receipt_version` is always `None` here: Mantle sets it to `None`
/// (MANTLE_CHANGES.md 3.7, commit 760129f) and the canonical receipts confirm it -- they
/// carry `depositNonce` but no `depositReceiptVersion`.
fn build_receipt_envelope(
    tx: &OpTxEnvelope,
    receipt: Receipt,
    bloom: alloy_primitives::Bloom,
) -> anyhow::Result<OpReceiptEnvelope> {
    let with_bloom = ReceiptWithBloom { receipt, logs_bloom: bloom };
    Ok(match tx {
        OpTxEnvelope::Legacy(_) => OpReceiptEnvelope::Legacy(with_bloom),
        OpTxEnvelope::Eip2930(_) => OpReceiptEnvelope::Eip2930(with_bloom),
        OpTxEnvelope::Eip1559(_) => OpReceiptEnvelope::Eip1559(with_bloom),
        OpTxEnvelope::Eip7702(_) => OpReceiptEnvelope::Eip7702(with_bloom),
        OpTxEnvelope::Deposit(_) => OpReceiptEnvelope::Deposit(ReceiptWithBloom {
            receipt: OpDepositReceipt {
                inner: with_bloom.receipt,
                // BOTH must be `None`, and they are bound together: op-alloy's
                // `rlp_encode_fields_with_bloom` emits each field only when it is `Some`, so
                // a `Some` nonce adds a field to the receipt RLP and changes `receiptsRoot`.
                // Canyon introduced `depositReceiptVersion` precisely to stop the
                // Regolith-era nonce from entering the root, and Mantle's canonical receipts
                // carry `depositNonce` in the RPC response but no `depositReceiptVersion` --
                // so the nonce is NOT part of the root here. Measured: filling `Some(nonce)`
                // produced 0xb6ba6253... for block 89,718,944 against the header's
                // 0x9e201354....
                //
                // The derived nonce is still verified, just not through the root -- see
                // `deposit_nonce_ok` in the per-transaction comparison.
                deposit_nonce: None,
                deposit_receipt_version: None,
            },
            logs_bloom: with_bloom.logs_bloom,
        }),
        OpTxEnvelope::PostExec(_) => anyhow::bail!(
            "SDM PostExec receipt encountered; SDM is Karst+ and Mantle registers neither \
             Karst nor Lagoon, so this should be unreachable"
        ),
    })
}

/// Byte-for-byte, order-sensitive comparison of locally produced logs against the canonical
/// receipt's `logs` array.
fn compare_logs_ordered(local: &[alloy_primitives::Log], onchain: Option<&Vec<Value>>) -> bool {
    let Some(onchain) = onchain else { return local.is_empty() };
    if local.len() != onchain.len() {
        return false;
    }
    local.iter().zip(onchain.iter()).all(|(l, o)| log_matches(l, o))
}

fn log_matches(local: &alloy_primitives::Log, onchain: &Value) -> bool {
    let addr_ok = onchain["address"]
        .as_str()
        .and_then(|a| a.parse::<Address>().ok())
        .is_some_and(|a| a == local.address);
    let topics_ok = match onchain["topics"].as_array() {
        Some(ts) => {
            ts.len() == local.topics().len() &&
                ts.iter().zip(local.topics().iter()).all(|(t, lt)| {
                    t.as_str().and_then(|t| t.parse::<B256>().ok()).is_some_and(|t| t == *lt)
                })
        }
        None => local.topics().is_empty(),
    };
    let data_ok = onchain["data"]
        .as_str()
        .and_then(|d| d.parse::<Bytes>().ok())
        .is_some_and(|d| d == local.data.data);
    addr_ok && topics_ok && data_ok
}

/// Print the first differing log so a failure is actionable instead of just `false`.
fn report_log_diff(local: &[alloy_primitives::Log], onchain: Option<&Vec<Value>>) {
    let empty = Vec::new();
    let onchain = onchain.unwrap_or(&empty);
    if local.len() != onchain.len() {
        println!("    log count differs: local={} onchain={}", local.len(), onchain.len());
    }
    for (i, (l, o)) in local.iter().zip(onchain.iter()).enumerate() {
        if !log_matches(l, o) {
            println!("    first differing log at index {i}:");
            println!(
                "      local  addr={} topics={:?} data={}",
                l.address,
                l.topics(),
                l.data.data
            );
            println!(
                "      onchain addr={} topics={} data={}",
                o["address"], o["topics"], o["data"]
            );
            return;
        }
    }
}

/// Map a block timestamp to the Mantle fork active at it.
///
/// Mantle's ladder is Skadi -> Limb -> Arsia and does NOT line up with upstream
/// OP's: Skadi maps to `ISTHMUS`, **Limb maps to `OSAKA`**, Arsia to `ARSIA`. The copy of
/// this function in `mantle-reth/crates/integration-tests/tests/replay.rs` handles only
/// Skadi and Arsia -- replaying a Limb-era block with that version picks `ISTHMUS` and gets
/// the wrong gas schedule. Verified against mainnet: block 90,482,350 sits in the Limb era
/// and only matches on-chain with the OSAKA arm present.
///
/// Pre-Skadi falls through to `BEDROCK`, which is WRONG for Mantle (it had MantleBaseFee ->
/// Everest -> Euboea before Skadi). That range is therefore not replayable by this harness;
/// the boundary is mainnet block 84,073,844. reth's production `cutoff_height` is
/// 87,373,000, so nothing reth serves is affected.
fn spec_for_timestamp(chain_id: u64, ts: u64) -> OpSpecId {
    let (skadi, limb, arsia) = match chain_id {
        5000 => (
            MANTLE_MAINNET_SKADI_TIMESTAMP,
            MANTLE_MAINNET_LIMB_TIMESTAMP,
            MANTLE_MAINNET_ARSIA_TIMESTAMP,
        ),
        5003 => (
            MANTLE_SEPOLIA_SKADI_TIMESTAMP,
            MANTLE_SEPOLIA_LIMB_TIMESTAMP,
            MANTLE_SEPOLIA_ARSIA_TIMESTAMP,
        ),
        other => panic!(
            "no Mantle fork schedule for chain id {other}; only 5000 (mainnet) and 5003 \
             (sepolia) are known. Set OP_SPEC to override."
        ),
    };
    if ts >= arsia {
        OpSpecId::ARSIA
    } else if ts >= limb {
        OpSpecId::OSAKA
    } else if ts >= skadi {
        OpSpecId::ISTHMUS
    } else {
        OpSpecId::BEDROCK
    }
}

/// Prepare the transaction environment for the given transaction.
pub fn prepare_tx_env(
    tx: &OpTxEnvelope,
    caller: Address,
    encoded: Bytes,
) -> anyhow::Result<OpTransaction<TxEnv>> {
    let base = match tx {
        OpTxEnvelope::Legacy(tx) => tx.tx().to_tx_env(caller),
        OpTxEnvelope::Eip1559(tx) => tx.tx().to_tx_env(caller),
        OpTxEnvelope::Eip2930(tx) => tx.tx().to_tx_env(caller),
        OpTxEnvelope::Eip7702(tx) => tx.tx().to_tx_env(caller),
        OpTxEnvelope::Deposit(tx) => {
            let TxDeposit {
                to,
                value,
                gas_limit,
                input,
                source_hash: _,
                from: _,
                mint: _,
                is_system_transaction: _,
                eth_value: _,
                eth_tx_value: _,
            } = tx.inner();
            TxEnv {
                tx_type: tx.ty(),
                caller,
                gas_limit: *gas_limit,
                kind: *to,
                value: *value,
                data: input.clone(),
                ..Default::default()
            }
        }
        // The SDM post-exec transaction type. SDM is a Karst+ feature and Mantle
        // registers neither Karst nor Lagoon, so a real Mantle block can never contain one.
        // If this fires, an assumption behind the whole replay is wrong -- surface it loudly
        // rather than replaying it as something else.
        OpTxEnvelope::PostExec(_) => anyhow::bail!(
            "encountered an SDM PostExec transaction (type {:#04x}); SDM is Karst+ and Mantle \
             registers neither Karst nor Lagoon, so this should be unreachable",
            tx.ty()
        ),
    };

    let deposit = if let OpTxEnvelope::Deposit(tx) = tx {
        DepositTransactionParts {
            source_hash: tx.source_hash,
            mint: Some(tx.mint),
            is_system_transaction: tx.is_system_transaction,
            eth_value: Some(tx.eth_value),
            eth_tx_value: tx.eth_tx_value,
        }
    } else {
        Default::default()
    };

    Ok(OpTransaction { base, enveloped_tx: Some(encoded), deposit })
}

trait ToTxEnv {
    fn to_tx_env(&self, caller: Address) -> TxEnv;
}

impl ToTxEnv for TxLegacy {
    fn to_tx_env(&self, caller: Address) -> TxEnv {
        TxEnv {
            tx_type: self.ty(),
            caller,
            gas_limit: self.gas_limit,
            gas_price: self.gas_price,
            kind: self.to,
            value: self.value,
            data: self.input.clone(),
            nonce: self.nonce,
            chain_id: self.chain_id,
            ..Default::default()
        }
    }
}

impl ToTxEnv for TxEip1559 {
    fn to_tx_env(&self, caller: Address) -> TxEnv {
        TxEnv {
            tx_type: self.ty(),
            caller,
            gas_limit: self.gas_limit,
            gas_price: self.max_fee_per_gas,
            kind: self.to,
            value: self.value,
            data: self.input.clone(),
            nonce: self.nonce,
            chain_id: Some(self.chain_id),
            gas_priority_fee: Some(self.max_priority_fee_per_gas),
            access_list: self.access_list.clone(),
            ..Default::default()
        }
    }
}

impl ToTxEnv for TxEip2930 {
    fn to_tx_env(&self, caller: Address) -> TxEnv {
        TxEnv {
            tx_type: self.ty(),
            caller,
            gas_limit: self.gas_limit,
            gas_price: self.gas_price,
            kind: self.to,
            value: self.value,
            data: self.input.clone(),
            chain_id: Some(self.chain_id),
            nonce: self.nonce,
            access_list: self.access_list.clone(),
            ..Default::default()
        }
    }
}

impl ToTxEnv for TxEip7702 {
    fn to_tx_env(&self, caller: Address) -> TxEnv {
        TxEnv {
            tx_type: self.ty(),
            caller,
            gas_limit: self.gas_limit,
            gas_price: self.max_fee_per_gas,
            kind: TxKind::Call(self.to),
            value: self.value,
            data: self.input.clone(),
            nonce: self.nonce,
            chain_id: Some(self.chain_id),
            gas_priority_fee: Some(self.max_priority_fee_per_gas),
            access_list: self.access_list.clone(),
            authorization_list: self
                .authorization_list
                .iter()
                .map(|auth| Either::Left(auth.clone()))
                .collect(),
            ..Default::default()
        }
    }
}

// ============================================================================
// Block State Verification Functions
// ============================================================================

/// Batch verify account storage slots and state using eth_getProof
async fn verify_state_with_plain_reads<DB>(
    state: &revm::database::State<DB>,
    client: impl Provider<Optimism> + Clone,
    block_number: u64,
    out: &mut String,
) -> anyhow::Result<()> {
    let block_id: BlockId = block_number.into();
    let accounts = &state.cache.accounts;

    let mut total_failed = 0;
    let mut balance_mismatches = 0;
    let mut nonce_mismatches = 0;
    let mut code_hash_mismatches = 0;

    for (address, cache_account) in accounts {
        if let Some(account) = &cache_account.account {
            let slots: Vec<U256> = account.storage.keys().copied().collect();
            let local_code_hash = account.info.code_hash;
            // Both KECCAK_EMPTY and the zero hash mean "no code". EOAs are the majority,
            // and for them there is nothing to fetch.
            let local_is_empty = local_code_hash == KECCAK_EMPTY || local_code_hash == B256::ZERO;

            // One round of parallel plain reads, replacing one `eth_getProof`. Only the
            // values inside the proof response were ever read, never the proof itself, so
            // the server-side trie work was wasted; plain reads return the same values, keep
            // one round per account, and are served from plain-state/changeset tables. The
            // slot key also stays the `U256` we asked for instead of being parsed back out
            // of `JsonStorageKey`'s `Debug` form.
            let (balance, nonce, code, slot_values) = tokio::join!(
                client.get_balance(*address).block_id(block_id).into_future(),
                client.get_transaction_count(*address).block_id(block_id).into_future(),
                async {
                    if local_is_empty {
                        None
                    } else {
                        Some(client.get_code_at(*address).block_id(block_id).await)
                    }
                },
                futures::future::join_all(slots.iter().map(|slot| {
                    client.get_storage_at(*address, *slot).block_id(block_id).into_future()
                }))
            );

            match balance {
                Ok(remote) if remote != account.info.balance => {
                    balance_mismatches += 1;
                    logln!(
                        out,
                        "  ❌ Balance mismatch for {}: remote={}, local={}",
                        address,
                        remote,
                        account.info.balance
                    );
                }
                Err(e) => logln!(out, "⚠️  Failed to read balance of {}: {}", address, e),
                _ => {}
            }

            match nonce {
                Ok(remote) if remote != account.info.nonce => {
                    nonce_mismatches += 1;
                    logln!(
                        out,
                        "  ❌ Nonce mismatch for {}: remote={}, local={}",
                        address,
                        remote,
                        account.info.nonce
                    );
                }
                Err(e) => logln!(out, "⚠️  Failed to read nonce of {}: {}", address, e),
                _ => {}
            }

            match code {
                Some(Ok(bytes)) => {
                    let remote_code_hash =
                        if bytes.is_empty() { KECCAK_EMPTY } else { keccak256(&bytes) };
                    if remote_code_hash != local_code_hash {
                        code_hash_mismatches += 1;
                        logln!(
                            out,
                            "  ❌ Code hash mismatch for {}: remote={}, local={}",
                            address,
                            remote_code_hash,
                            local_code_hash
                        );
                    }
                }
                Some(Err(e)) => {
                    logln!(out, "⚠️  Failed to read code of {}: {}", address, e)
                }
                None => {}
            }

            for (slot, value) in slots.iter().zip(slot_values) {
                match value {
                    Ok(remote) => {
                        if let Some(local) = account.storage.get(slot) &&
                            remote != *local
                        {
                            total_failed += 1;
                            logln!(
                                out,
                                "  ❌ Storage mismatch for {} at slot {}: remote={}, local={}",
                                address,
                                slot,
                                remote,
                                local
                            );
                        }
                    }
                    Err(e) => {
                        logln!(out, "⚠️  Failed to read slot {} of {}: {}", slot, address, e)
                    }
                }
            }
        }
    }

    // Print concise verification result (similar to gas used verification)
    let all_match = total_failed == 0 &&
        balance_mismatches == 0 &&
        nonce_mismatches == 0 &&
        code_hash_mismatches == 0;

    if all_match {
        logln!(out, "--- State verification: passed✅");
    } else {
        logln!(out, "--- State verification: failed❌");
        logln!(
            out,
            "  Balance mismatches: {}, Nonce mismatches: {}, Code hash mismatches: {}, Storage mismatches: {}",
            balance_mismatches,
            nonce_mismatches,
            code_hash_mismatches,
            total_failed
        );
    }

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::loggable_endpoint;

    #[track_caller]
    fn check(url: &str, expected: &str) {
        assert_eq!(loggable_endpoint(&url.parse().unwrap()), expected, "for {url}");
    }

    #[test]
    fn direct_endpoints_are_logged_in_full() {
        check(
            "http://mantle-op-reth-rpc41.mainnet-qa1:8545",
            "http://mantle-op-reth-rpc41.mainnet-qa1:8545",
        );
        check(
            "http://mantle-op-reth-rpc41.mainnet-qa1:8545/",
            "http://mantle-op-reth-rpc41.mainnet-qa1:8545",
        );
        check("https://rpc.example.org", "https://rpc.example.org");
    }

    #[test]
    fn anything_that_could_carry_a_credential_is_redacted() {
        // A token in the path, which is what an authenticating proxy does.
        check(
            "https://proxy.example.org/target/node:8545/eyJhbGciOiJFZERTQSJ9.e30.sig",
            "https://proxy.example.org/<redacted>",
        );
        // A key in the path, the shape most hosted providers use.
        check("https://eth.example.org/v2/0123456789abcdef", "https://eth.example.org/<redacted>");
        // A key in the query.
        check(
            "https://eth.example.org/?apikey=0123456789abcdef",
            "https://eth.example.org/<redacted>",
        );
        // Credentials in userinfo.
        check("https://user:pass@eth.example.org", "https://eth.example.org/<redacted>");
    }
}
