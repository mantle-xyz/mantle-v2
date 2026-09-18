//! [Header] assembly logic for the [`StatelessL2Builder`].

use super::StatelessL2Builder;
use crate::{
    ExecutorError, ExecutorResult, TrieDBError, TrieDBProvider,
    util::{encode_holocene_eip_1559_params, encode_jovian_eip_1559_params},
};
use alloc::vec::Vec;
use alloy_consensus::{EMPTY_OMMER_ROOT_HASH, Header, Sealed, TxReceipt};
use alloy_eips::{Encodable2718, eip7685::EMPTY_REQUESTS_HASH};
use alloy_evm::{EvmFactory, block::BlockExecutionResult};
use alloy_op_evm::block::receipt_builder::OpReceiptBuilder;
use alloy_primitives::{B256, Sealable, U256, logs_bloom};
use alloy_trie::EMPTY_ROOT_HASH;
use kona_genesis::RollupConfig;
use kona_mpt::{TrieHinter, ordered_trie_with_encoder};
use kona_protocol::{OutputRoot, Predeploys};
use op_alloy_rpc_types_engine::OpPayloadAttributes;
use revm::{context::BlockEnv, database::BundleState};

impl<P, H, Evm, R> StatelessL2Builder<'_, P, H, Evm, R>
where
    P: TrieDBProvider,
    H: TrieHinter,
    Evm: EvmFactory,
    R: OpReceiptBuilder,
    R::Receipt: Encodable2718,
{
    /// Seals the block executed from the given [`OpPayloadAttributes`] and [`BlockEnv`], returning
    /// the computed [Header].
    pub(crate) fn seal_block(
        &mut self,
        attrs: &OpPayloadAttributes,
        parent_hash: B256,
        block_env: &BlockEnv,
        ex_result: &BlockExecutionResult<R::Receipt>,
        bundle: BundleState,
    ) -> ExecutorResult<Sealed<Header>> {
        let timestamp = block_env.timestamp.saturating_to::<u64>();

        // Compute the roots for the block header.
        let state_root = self.trie_db.state_root(&bundle)?;
        let transactions_root = ordered_trie_with_encoder(
            // SAFETY: The OP Stack protocol will never generate a payload attributes with an empty
            // transactions field. Panicking here is the desired behavior, as it indicates a severe
            // protocol violation.
            attrs.transactions.as_ref().expect("Transactions must be non-empty"),
            |tx, buf| buf.put_slice(tx.as_ref()),
        )
        .root();
        let receipts_root = compute_receipts_root(
            self.factory.receipt_builder(),
            &ex_result.receipts,
            self.config,
            timestamp,
        );
        // [MANTLE] Skadi turns on the Shanghai/Cancun/Prague header shape ahead of the OP forks,
        // which `AlignOpWithMantle` pins to `mantle_arsia_time`
        // (`op-chain-ops/genesis/mantle_config.go::alignEthWithMantle` sets
        // `ShanghaiTime = CancunTime = PragueTime = MantleSkadiTime`). Every OP predicate below
        // is therefore false for the whole `[Skadi, Arsia)` window while op-geth is already
        // emitting these fields — 10.9M blocks on Mantle Sepolia, every one of which would get a
        // different block hash. Verified against block 25552264 onward; see MANTLE_CHANGES.md
        // §3.2c.
        let mantle_skadi = self.config.is_mantle_skadi_active(timestamp);

        // Skadi takes the Isthmus branch, not the Canyon one: the chain's `withdrawalsRoot` in
        // the window is the L2ToL1MessagePasser storage root, not `EMPTY_ROOT_HASH`.
        let withdrawals_root = if self.config.is_isthmus_active(timestamp) || mantle_skadi {
            Some(self.message_passer_account()?)
        } else if self.config.is_canyon_active(timestamp) {
            Some(EMPTY_ROOT_HASH)
        } else {
            None
        };

        // Compute the logs bloom from the receipts generated during block execution.
        let logs_bloom = logs_bloom(ex_result.receipts.iter().flat_map(|r| r.logs()));

        // Compute Cancun fields, if active.
        // [MANTLE] Skadi takes the Ecotone branch — `(Some(0), Some(0))`, matching the chain —
        // not the Jovian one, which is gated on Arsia.
        let (blob_gas_used, excess_blob_gas) = if self.config.is_jovian_active(timestamp) {
            (Some(ex_result.blob_gas_used), Some(0))
        } else if self.config.is_ecotone_active(timestamp) || mantle_skadi {
            (Some(0), Some(0))
        } else {
            Default::default()
        };

        // At holocene activation, the base fee parameters from the payload are placed
        // into the Header's `extra_data` field.
        //
        // If the payload's `eip_1559_params` are equal to `0`, then the header's `extraData`
        // field is set to the encoded canyon base fee parameters.
        let encoded_base_fee_params = match self.config {
            config if config.is_jovian_active(timestamp) => {
                let extra_data = encode_jovian_eip_1559_params(self.config, attrs)?;
                Ok(extra_data)
            }
            config if config.is_holocene_active(timestamp) => {
                encode_holocene_eip_1559_params(self.config, attrs)
            }
            _ => Ok(Default::default()),
        }?;

        // The requests hash on the OP Stack, if Isthmus is active, is always the empty SHA256 hash.
        // [MANTLE] Also on from Skadi — see the note above `withdrawals_root`.
        let requests_hash = (self.config.is_isthmus_active(timestamp) || mantle_skadi)
            .then_some(EMPTY_REQUESTS_HASH);

        // Construct the new header.
        let header = Header {
            parent_hash,
            ommers_hash: EMPTY_OMMER_ROOT_HASH,
            beneficiary: attrs.payload_attributes.suggested_fee_recipient,
            state_root,
            transactions_root,
            receipts_root,
            withdrawals_root,
            requests_hash,
            logs_bloom,
            difficulty: U256::ZERO,
            number: block_env.number.saturating_to::<u64>(),
            gas_limit: attrs.gas_limit.ok_or(ExecutorError::MissingGasLimit)?,
            gas_used: ex_result.gas_used,
            timestamp,
            mix_hash: attrs.payload_attributes.prev_randao,
            nonce: Default::default(),
            base_fee_per_gas: Some(block_env.basefee),
            blob_gas_used,
            excess_blob_gas: excess_blob_gas.and_then(|x| x.try_into().ok()),
            parent_beacon_block_root: attrs.payload_attributes.parent_beacon_block_root,
            extra_data: encoded_base_fee_params,
            block_access_list_hash: Default::default(),
            slot_number: Default::default(),
        }
        .seal_slow();

        Ok(header)
    }

    /// Computes the current output root of the latest executed block, based on the parent header
    /// and the underlying state trie.
    ///
    /// **CONSTRUCTION:**
    /// ```text
    /// output_root = keccak256(version_byte .. payload)
    /// payload = state_root .. withdrawal_storage_root .. latest_block_hash
    /// ```
    pub fn compute_output_root(&mut self) -> ExecutorResult<B256> {
        let parent_number = self.trie_db.parent_block_header().number;

        info!(
            target: "block_builder",
            parent_state_root = ?self.trie_db.parent_block_header().state_root,
            parent_block_number = parent_number,
            "Computing output root",
        );

        let storage_root = self.message_passer_account()?;
        let parent_header = self.trie_db.parent_block_header();

        // Construct the raw output and hash it.
        let output_root_hash =
            OutputRoot::from_parts(parent_header.state_root, storage_root, parent_header.seal())
                .hash();

        info!(
            target: "block_builder",
            parent_block_number = parent_number,
            output_root = ?output_root_hash,
            "Computed output root",
        );

        // Hash the output and return
        Ok(output_root_hash)
    }

    /// Fetches the L2 to L1 message passer account from the cache or underlying trie.
    fn message_passer_account(&mut self) -> Result<B256, TrieDBError> {
        match self.trie_db.storage_roots().get(&Predeploys::L2_TO_L1_MESSAGE_PASSER) {
            Some(storage_root) => Ok(storage_root.blind()),
            None => Ok(self
                .trie_db
                .get_trie_account(&Predeploys::L2_TO_L1_MESSAGE_PASSER)?
                .ok_or(TrieDBError::MissingAccountInfo)?
                .storage_root),
        }
    }
}

/// Computes the receipts root from the given set of receipts.
///
/// Generic over the receipt builder so non-OP receipt shapes (e.g. Celo's CIP-64 receipt) can
/// plug in their own [`OpReceiptBuilder`]. From Regolith activation up to (but not including)
/// Canyon activation, op-geth/op-erigon compute the receipts-trie root from a deposit-receipt
/// encoding that omits the deposit nonce; this function reproduces that encoding by delegating
/// nonce-stripping to [`OpReceiptBuilder::strip_deposit_nonce`], which OP Stack implementations
/// override and other chains inherit as a no-op.
///
/// `[MANTLE]` On a Mantle chain the stripping is unconditional, so `_timestamp` is unused there.
/// The parameter is kept to hold upstream's signature — a future Mantle fork that changes the
/// receipt encoding will need it back, and keeping it avoids a gratuitous merge conflict on the
/// next subtree sync.
pub fn compute_receipts_root<R>(
    receipt_builder: &R,
    receipts: &[R::Receipt],
    config: &RollupConfig,
    _timestamp: u64,
) -> B256
where
    R: OpReceiptBuilder,
    R::Receipt: Encodable2718,
{
    // [MANTLE] **Consensus.** Upstream gates deposit-nonce stripping on the OP Regolith..Canyon
    // window (`is_regolith_active(timestamp) && !is_canyon_active(timestamp)`). Mantle strips for
    // the chain's whole history instead.
    //
    // This was gated on `is_mantle_skadi_active(timestamp)`, which is wrong at the bottom end:
    // every block before Skadi then kept the deposit nonce in the receipt encoding and got a
    // `receipts_root` the chain disagrees with. Measured on `sepolia-testnet-qa1` at blocks
    // 20000000 and 25552263 (Skadi - 1) — both reproduce only once stripping is unconditional.
    //
    // "Unconditional" is a property of Mantle's op-geth, not a translation of upstream's window.
    // A naive translation would give `[0, Arsia)` (Mantle sets `regolith_time = 0` and
    // `canyon_time = mantle_arsia_time`), but that is wrong at the top end. The oracle is
    // `src/op-geth/core/types/receipt.go`, where `Receipts.EncodeIndex` folds `DepositTxType`
    // into the same arm as the ordinary typed transactions:
    //
    //     case AccessListTxType, DynamicFeeTxType, BlobTxType, SetCodeTxType, DepositTxType:
    //         rlp.Encode(w, data)     // data = {status, cumulativeGas, bloom, logs}
    //
    // Upstream op-geth gives `DepositTxType` its own arm and appends the nonce (and version) when
    // `DepositReceiptVersion != nil`. Mantle has no such field at all — `DepositReceiptVersion`
    // occurs **zero** times in the whole of `src/op-geth` as of `cbf7cd33d`, which includes the
    // Elysium work. So no Mantle block at any height carries the nonce in the receipts trie, and
    // no scheduled fork changes that.
    //
    // Cross-checked on sepolia-testnet-qa1 at blocks 20000000 and 25552263 (pre-Skadi), 28000000
    // (in-window) and 43000000 (post-Arsia). See MANTLE_CHANGES.md §3.2j.
    //
    // NOTE: the `else` arm does **not** restore upstream's `[Regolith, Canyon)` rule — it never
    // stripped for non-Mantle chains before this change either (the old gate was also a Mantle
    // predicate). A real OP chain in that window would compute the wrong `receipts_root` here.
    // Out of scope for this tree, but do not read the `else` arm as "upstream semantics".
    if config.is_mantle() {
        let receipts = receipts
            .iter()
            .cloned()
            .map(|mut receipt| {
                receipt_builder.strip_deposit_nonce(&mut receipt);
                receipt
            })
            .collect::<Vec<_>>();

        ordered_trie_with_encoder(receipts.as_ref(), |receipt, mut buf| {
            receipt.encode_2718(&mut buf)
        })
        .root()
    } else {
        ordered_trie_with_encoder(receipts, |receipt, mut buf| receipt.encode_2718(&mut buf)).root()
    }
}

#[cfg(test)]
mod mantle_receipts_root_tests {
    use super::compute_receipts_root;
    use alloy_consensus::{Receipt, ReceiptWithBloom};
    use alloy_op_evm::block::OpAlloyReceiptBuilder;
    use kona_genesis::{HardForkConfig, MantleHardForkConfig, RollupConfig};
    use op_alloy_consensus::{OpDepositReceipt, OpReceiptEnvelope};

    /// A Mantle config whose Skadi and Arsia activations are far apart, so a timestamp-dependent
    /// gate cannot accidentally agree with an unconditional one.
    fn mantle_config() -> RollupConfig {
        RollupConfig {
            hardforks: HardForkConfig {
                regolith_time: Some(0),
                canyon_time: Some(100),
                ecotone_time: Some(100),
                isthmus_time: Some(100),
                ..Default::default()
            },
            mantle_hardforks: MantleHardForkConfig {
                mantle_skadi_time: Some(40),
                mantle_limb_time: Some(70),
                mantle_arsia_time: Some(100),
                ..MantleHardForkConfig::NONE
            },
            ..Default::default()
        }
    }

    fn deposit_receipt_with_nonce() -> OpReceiptEnvelope {
        OpReceiptEnvelope::Deposit(ReceiptWithBloom::new(
            OpDepositReceipt {
                inner: Receipt { status: true.into(), cumulative_gas_used: 21_000, logs: vec![] },
                deposit_nonce: Some(0xdead_beef),
                deposit_receipt_version: None,
            },
            Default::default(),
        ))
    }

    /// `[MANTLE]` **Consensus regression guard for the pre-Skadi `receipts_root`.**
    ///
    /// The gate used to be `is_mantle_skadi_active(timestamp)`, which left every block before
    /// Skadi encoding the deposit nonce into the receipts trie. Mantle's op-geth never does, at
    /// any height — measured on sepolia-testnet-qa1 blocks 20000000 and 25552263 (pre-Skadi),
    /// 28000000 (in-window) and 43000000 (post-Arsia).
    ///
    /// The assertion that matters is that the root is *timestamp-independent*: if anyone
    /// reintroduces a fork gate here, the pre-Skadi sample diverges from the rest.
    #[test]
    fn mantle_strips_deposit_nonce_at_every_height() {
        let config = mantle_config();
        let builder = OpAlloyReceiptBuilder::default();
        let receipts = [deposit_receipt_with_nonce()];

        // 0 and 39 are pre-Skadi, 40..99 is the `[Skadi, Arsia)` window, 100+ is post-Arsia.
        let roots = [0u64, 39, 40, 70, 99, 100, 1_000]
            .map(|ts| compute_receipts_root(&builder, &receipts, &config, ts));

        assert!(
            roots.iter().all(|r| *r == roots[0]),
            "Mantle receipts root must not depend on the fork schedule, got {roots:?}",
        );

        // ...and it is the *stripped* encoding, not merely a consistent one. Build the same root
        // from a receipt that never carried a nonce and require equality.
        let mut stripped = deposit_receipt_with_nonce();
        if let OpReceiptEnvelope::Deposit(d) = &mut stripped {
            d.receipt.deposit_nonce = None;
        }
        let stripped_root = compute_receipts_root(&builder, &[stripped], &config, 0);
        assert_eq!(roots[0], stripped_root, "Mantle must strip the deposit nonce");

        // Negative control baked in: the un-stripped encoding is a *different* root, so the
        // assertion above is not vacuously true for this fixture.
        let unstripped_root = {
            use alloy_eips::Encodable2718;
            kona_mpt::ordered_trie_with_encoder(&receipts, |r, mut buf| r.encode_2718(&mut buf))
                .root()
        };
        assert_ne!(
            roots[0], unstripped_root,
            "fixture is degenerate: stripping the nonce changed nothing",
        );
    }
}
