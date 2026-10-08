//! `[MANTLE]` Module containing a [`TxDeposit`] builder for the Skadi network upgrade transactions.
//!
//! Port of `op-node/rollup/derive/skadi_upgrade_transactions.go`
//! (`MantleSkadiNetworkUpgradeTransactions`).
//!
//! Mantle skips every OP upgrade bundle, so the EIP-4788 beacon-block-root contract and the
//! EIP-2935 block-hash-history contract — which OP deploys at Ecotone and Isthmus respectively —
//! are instead deployed together at Skadi. The deployer addresses, creation bytecode and gas
//! limits are byte-identical to OP's; only the `UpgradeDepositSource` intents differ, which is
//! what makes the source hashes (and therefore the deposit hashes) Mantle-specific.

use alloc::{string::String, vec::Vec};
use alloy_eips::eip2718::Encodable2718;
use alloy_primitives::{B256, Bytes, TxKind, U256};
use op_alloy_consensus::{TxDeposit, UpgradeDepositSource};

use crate::{Ecotone, Hardfork, Isthmus};

/// The Skadi network upgrade transactions.
#[derive(Debug, Default, Clone, Copy)]
pub struct Skadi;

impl Skadi {
    /// Gas limit for both deployments, matching op-node's `Gas: 250_000`.
    pub const DEPLOYMENT_GAS: u64 = 250_000;

    /// Returns the source hash for the EIP-4788 beacon block root contract deployment.
    ///
    /// Distinct from [`Ecotone::beacon_roots_source`] — same contract, different intent.
    pub fn beacon_block_roots_source() -> B256 {
        UpgradeDepositSource { intent: String::from("Skadi: EIP-4788 Contract Deployment") }
            .source_hash()
    }

    /// Returns the source hash for the EIP-2935 block hash history contract deployment.
    ///
    /// Distinct from [`Isthmus::block_hash_history_contract_source`] — same contract, different
    /// intent.
    pub fn block_hash_history_contract_source() -> B256 {
        UpgradeDepositSource { intent: String::from("Skadi: EIP-2935 Contract Deployment") }
            .source_hash()
    }

    /// Returns the EIP-4788 creation data.
    ///
    /// Reuses [`Ecotone`]'s bytecode: op-node's `beaconBlockRootDeploymentBytecode` literal is
    /// byte-identical to the Ecotone one, so keeping a second copy would only create a way for
    /// them to drift. Pinned by `skadi_reuses_op_creation_bytecode`.
    pub fn eip4788_creation_data() -> Bytes {
        Ecotone::eip4788_creation_data()
    }

    /// Returns the EIP-2935 creation data. Reuses [`Isthmus`]'s bytecode — see
    /// [`Self::eip4788_creation_data`].
    pub fn eip2935_creation_data() -> Bytes {
        Isthmus::eip2935_creation_data()
    }

    /// Returns an iterator over the Skadi upgrade deposit transactions, in op-node's order.
    pub fn deposits() -> impl Iterator<Item = TxDeposit> {
        [
            // 1. Deploy the EIP-4788 beacon block root contract.
            TxDeposit {
                source_hash: Self::beacon_block_roots_source(),
                from: Ecotone::EIP4788_FROM,
                to: TxKind::Create,
                mint: 0,
                value: U256::ZERO,
                gas_limit: Self::DEPLOYMENT_GAS,
                is_system_transaction: false,
                input: Self::eip4788_creation_data(),
                // [MANTLE] BVM_ETH fields: upgrade txs carry no BVM_ETH semantics.
                eth_value: U256::ZERO,
                eth_tx_value: None,
            },
            // 2. Deploy the EIP-2935 block hash history contract.
            TxDeposit {
                source_hash: Self::block_hash_history_contract_source(),
                from: Isthmus::EIP2935_FROM,
                to: TxKind::Create,
                mint: 0,
                value: U256::ZERO,
                gas_limit: Self::DEPLOYMENT_GAS,
                is_system_transaction: false,
                input: Self::eip2935_creation_data(),
                eth_value: U256::ZERO,
                eth_tx_value: None,
            },
        ]
        .into_iter()
    }
}

impl Hardfork for Skadi {
    /// Constructs the Skadi network upgrade transactions.
    ///
    /// `upgrade_gas` stays at the trait default of 0: op-node appends these to the block's
    /// transaction list without touching the gas limit (see `attributes.go`).
    fn txs(&self) -> impl Iterator<Item = Bytes> {
        Self::deposits().map(|tx| {
            let mut encoded = Vec::new();
            tx.encode_2718(&mut encoded);
            Bytes::from(encoded)
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alloc::vec::Vec;
    use alloy_primitives::{address, b256};

    #[test]
    fn test_skadi_upgrade_txs() {
        assert_eq!(Skadi.txs().collect::<Vec<_>>().len(), 2);
    }

    /// The two source hashes are the whole point of this fork existing separately from
    /// Ecotone/Isthmus: same contracts, Mantle-specific deposit hashes.
    ///
    /// These values were computed independently of both kona and op-node, with
    /// `cast keccak` over `keccak256(be64(2) ++ keccak256(intent))` — the formula in
    /// `op-node/rollup/derive/deposit_source.go`. If this test fails, the intent string has
    /// drifted from `skadi_upgrade_transactions.go` and every Skadi activation block will
    /// disagree with op-node.
    #[test]
    fn test_skadi_source_hashes_match_op_node() {
        assert_eq!(
            Skadi::beacon_block_roots_source(),
            b256!("195dba1b4300952493316fd4adf4166cd799a984832fce9e56f35871ebcb9ec0"),
        );
        assert_eq!(
            Skadi::block_hash_history_contract_source(),
            b256!("70b82adac2a44af2eb5a58d508fe512dd3112ef13cbbd7debcdafa189cfb0484"),
        );
    }

    /// Reusing OP's intents would be the easy mistake: it compiles, deploys the right code, and
    /// produces the wrong deposit hash.
    #[test]
    fn test_skadi_source_hashes_differ_from_op_forks() {
        assert_ne!(Skadi::beacon_block_roots_source(), Ecotone::beacon_roots_source());
        assert_ne!(
            Skadi::block_hash_history_contract_source(),
            Isthmus::block_hash_history_contract_source(),
        );
    }

    /// Deployer addresses come from `op-core/predeploys`; op-node uses the same constants.
    #[test]
    fn test_skadi_deployers_and_gas() {
        let txs = Skadi::deposits().collect::<Vec<_>>();
        assert_eq!(txs[0].from, address!("0B799C86a49DEeb90402691F1041aa3AF2d3C875"));
        assert_eq!(txs[1].from, address!("3462413Af4609098e1E27A490f554f260213D685"));
        for tx in &txs {
            assert_eq!(tx.to, TxKind::Create);
            assert_eq!(tx.gas_limit, 250_000);
            assert_eq!(tx.mint, 0);
            assert_eq!(tx.value, U256::ZERO);
            assert!(!tx.is_system_transaction);
        }
    }

    /// op-node's Skadi bytecode literals are byte-identical to the Ecotone/Isthmus ones. This
    /// asserts the reuse is real, so that a future change to either OP fork's bytecode cannot
    /// silently change Skadi's — it will fail here instead.
    #[test]
    fn skadi_reuses_op_creation_bytecode() {
        assert_eq!(Skadi::eip4788_creation_data(), Ecotone::eip4788_creation_data());
        assert_eq!(Skadi::eip2935_creation_data(), Isthmus::eip2935_creation_data());
        // Prefixes of op-node's `beaconBlockRootDeploymentBytecode` /
        // `skadiBlockHashDeploymentBytecode` literals.
        assert!(
            Skadi::eip4788_creation_data()
                .starts_with(&alloy_primitives::hex!("60618060095f395ff3"))
        );
        assert!(
            Skadi::eip2935_creation_data()
                .starts_with(&alloy_primitives::hex!("60538060095f395ff3"))
        );
    }

    /// Skadi's upgrade transactions do not extend the block gas limit, matching op-node's
    /// `attributes.go`, which appends them without adjusting `gasLimit`.
    #[test]
    fn skadi_adds_no_upgrade_gas() {
        assert_eq!(Skadi.upgrade_gas(), 0);
    }
}
