//! Example that show how to replay a block and trace the execution of each transaction.
//!
//! The EIP3155 trace of each transaction is saved into file `traces/{tx_number}.json`.
#![cfg_attr(not(test), warn(unused_crate_dependencies))]

use alloy_consensus::{
    Eip658Value, Receipt, ReceiptWithBloom, TxEip1559, TxEip2930, TxEip7702, TxLegacy,
    proofs::calculate_receipt_root, transaction::SignerRecoverable,
};
use alloy_eips::{BlockId, Decodable2718, Typed2718};
use alloy_op_hardforks::{
    MANTLE_MAINNET_ARSIA_TIMESTAMP, MANTLE_MAINNET_LIMB_TIMESTAMP, MANTLE_MAINNET_SKADI_TIMESTAMP,
    MANTLE_SEPOLIA_ARSIA_TIMESTAMP, MANTLE_SEPOLIA_LIMB_TIMESTAMP, MANTLE_SEPOLIA_SKADI_TIMESTAMP,
};
use alloy_primitives::{Address, B256, Bytes, U256};
use alloy_provider::{Provider, ProviderBuilder, network::primitives::BlockTransactions};
use alloy_rpc_types::eth::EIP1186AccountProofResponse;
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
    database_interface::WrapDatabaseAsync,
    primitives::{KECCAK_EMPTY, TxKind},
};
use serde_json::Value;
use std::{fmt::Write as _, fs, path::Path, time::Instant};

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
    let rpc_url = mantle_url.parse()?;

    // Create a provider
    let client = ProviderBuilder::<_, _, Optimism>::default().connect_http(rpc_url);

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

    println!(
        "range [{start_block}..={end_block}] = {total_range} blocks; {} already done, {} to \
         go; concurrency={concurrency}; progress={progress_path}",
        total_range - todo.len(),
        todo.len()
    );

    let progress = std::sync::Arc::new(std::sync::Mutex::new(
        std::fs::OpenOptions::new().create(true).append(true).open(&progress_path)?,
    ));
    let started = Instant::now();
    let mut mismatched: Vec<u64> = Vec::new();
    let mut errored: Vec<u64> = Vec::new();
    let mut n = 0usize;

    let mut stream = stream::iter(todo.iter().copied())
        .map(|i| {
            let client = client.clone();
            async move {
                let mut out = String::new();
                let r = process_block(
                    i,
                    chain_id,
                    spec_override,
                    state_verify,
                    export_cache_db,
                    client,
                    &mut out,
                )
                .await;
                (i, r, out)
            }
        })
        .buffer_unordered(concurrency);

    while let Some((block, res, out)) = stream.next().await {
        n += 1;
        match res {
            Ok(true) => {
                record_progress(&progress, block, "OK")?;
                if verbose {
                    print!("{out}");
                }
            }
            Ok(false) => {
                mismatched.push(block);
                record_progress(&progress, block, "MISMATCH")?;
                println!("{out}");
            }
            Err(e) => {
                // A failing block is a finding, not a reason to abandon the range.
                println!("{out}\n==== BLOCK {block} ERROR ====\n{e}");
                errored.push(block);
                record_progress(&progress, block, "ERROR")?;
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
    // Fetch the transaction-rich block
    let block = client
        .get_block_by_number(block_number.into())
        .await
        .expect("Failed to get parent block")
        .expect("Block not found");

    // [MANTLE] Pick the fork from the block's own timestamp. `OP_SPEC` is only an override
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
    let cache_db: CacheDB<_> = CacheDB::new(state_db);
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
        })
        .modify_cfg_chained(|c| {
            c.chain_id = chain_id;
            // [MANTLE] Must be `set_spec_and_mainnet_gas_params`, NOT `c.spec = spec`.
            // revm 40 split the gas parameters out of the spec: assigning the field on an
            // already-built `CfgEnv` leaves `gas_params` at `OpSpecId::default()`'s values,
            // so the EIP-7623 calldata floor never applies and every gas comparison this
            // harness makes is silently wrong (the same 192-gas class of bug that showed up
            // in op-revm's bvm_eth replay fixtures). Upstream marks the bare setter
            // `#[deprecated(note = "Use CfgEnv::set_spec_and_mainnet_gas_params instead")]`.
            c.set_spec_and_mainnet_gas_params(spec);
            // [MANTLE] EIP-7825 override -- NOT optional. The revm fork used to patch the
            // accessor to return `u64::MAX`; that patch was removed (revm `1903a86a`) and the
            // override moved downstream, so every site that builds its own `CfgEnv` must set
            // it. Replaying mainnet block 100,437,956 hit a real transaction with
            // `gas_limit = 54_000_000` and died with
            //   Transaction(Base(TxGasLimitGreaterThanCap { gas_limit: 54000000, cap: 16777216 }))
            // Mantle has no per-transaction cap, so leaving this unset rejects transactions
            // the chain actually accepted.
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
    // [MANTLE] Collected to compute `receiptsRoot` and compare it with the block header.
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

        // [MANTLE] `depositNonce` must be DERIVED, not read back from the canonical receipt
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

        // [MANTLE] Build the typed envelope so `receiptsRoot` can be derived. The type byte
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
        // [MANTLE] Compare logs BYTE-FOR-BYTE and IN ORDER, not by count. Receipt RLP is
        // order-sensitive; `logsBloom` is NOT (a permuted log list yields an identical
        // bloom). So counting alone, even next to the bloom check, blesses both a permutation
        // and a wrong topic. Do not reuse op-revm's fixture-test unordered matching here.
        let logs_ok = compare_logs_ordered(&receipt_logs, oc["logs"].as_array());
        let bloom_ok = local_bloom_hex.eq_ignore_ascii_case(&oc_bloom);
        // [MANTLE] `depositNonce` is not in the receipt RLP (see `build_receipt_envelope`),
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

    // [MANTLE] The claim that used to sit here -- "receiptsRoot will equal on-chain" -- was
    // never verified: the four per-field checks miss the RLP encoding, the type byte and the
    // deposit-only fields. Compute the root and compare it with the header instead.
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

    // Verify account states using eth_getProof if enabled
    if state_verify {
        if let Err(e) = verify_storage_with_proof(&state, client.clone(), block_number, out).await {
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
            revm::database_interface::WrapDatabaseAsync<
                revm::database::AlloyDB<op_alloy_network::Optimism, P>,
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
/// `[MANTLE]` `deposit_receipt_version` is always `None` here: Mantle sets it to `None`
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
                // [MANTLE] BOTH must be `None`, and they are bound together: op-alloy's
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
/// `[MANTLE]` Mantle's ladder is Skadi -> Limb -> Arsia and does NOT line up with upstream
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
        // [MANTLE] The SDM post-exec transaction type. SDM is a Karst+ feature and Mantle
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
async fn verify_storage_with_proof<DB>(
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
            // Collect all storage slot keys (batch query)
            let storage_keys: Vec<B256> = account.storage.keys().map(|k| B256::from(*k)).collect();

            // Call eth_getProof once to get account state and all storage slot proofs
            let proof_result: Result<EIP1186AccountProofResponse, _> =
                client.get_proof(*address, storage_keys.clone()).block_id(block_id).await;

            match proof_result {
                Ok(proof) => {
                    // Verify basic account information
                    if proof.balance != account.info.balance {
                        balance_mismatches += 1;
                        logln!(
                            out,
                            "  ❌ Balance mismatch for {}: remote={}, local={}",
                            address,
                            proof.balance,
                            account.info.balance
                        );
                    }
                    if proof.nonce != account.info.nonce {
                        nonce_mismatches += 1;
                        logln!(
                            out,
                            "  ❌ Nonce mismatch for {}: remote={}, local={}",
                            address,
                            proof.nonce,
                            account.info.nonce
                        );
                    }

                    // Compare code_hash with compatibility for empty code representations
                    // Both KECCAK_EMPTY and zero hash represent "no code"
                    let remote_is_empty =
                        proof.code_hash == KECCAK_EMPTY || proof.code_hash == B256::ZERO;
                    let local_is_empty = account.info.code_hash == KECCAK_EMPTY ||
                        account.info.code_hash == B256::ZERO;

                    if !(remote_is_empty && local_is_empty) &&
                        proof.code_hash != account.info.code_hash
                    {
                        code_hash_mismatches += 1;
                        logln!(
                            out,
                            "  ❌ Code hash mismatch for {}: remote={}, local={}",
                            address,
                            proof.code_hash,
                            account.info.code_hash
                        );
                    }

                    // Verify each storage slot (if any)
                    if !storage_keys.is_empty() {
                        for storage_proof in &proof.storage_proof {
                            let key = storage_proof.key;
                            let value_from_proof = storage_proof.value;

                            let key_str = format!("{:?}", key);
                            let key_str_clean =
                                key_str.trim_start_matches("Hash(0x").trim_end_matches(")");

                            if let Ok(key_u256) = U256::from_str_radix(key_str_clean, 16) {
                                if let Some(cached_value) = account.storage.get(&key_u256) {
                                    if value_from_proof != *cached_value {
                                        total_failed += 1;
                                        logln!(
                                            out,
                                            "  ❌ Storage mismatch for {} at slot {}: remote={}, local={}",
                                            address,
                                            key,
                                            value_from_proof,
                                            cached_value
                                        );
                                    }
                                }
                            }
                        }
                    }
                }
                Err(e) => {
                    logln!(out, "⚠️  Failed to get proof for account {}: {}", address, e);
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
