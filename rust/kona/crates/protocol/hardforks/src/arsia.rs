//! Module containing a [`TxDeposit`] builder for the Arsia network upgrade transactions.

use alloc::{string::String, vec::Vec};
use alloy_eips::eip2718::Encodable2718;
use alloy_primitives::{Address, B256, Bytes, TxKind, U256, address, hex};
use kona_protocol::Predeploys;
use op_alloy_consensus::{TxDeposit, UpgradeDepositSource};

use crate::Hardfork;

/// The Arsia network upgrade transactions.
#[derive(Debug, Default, Clone, Copy)]
pub struct Arsia;

impl Arsia {
    /// L1 Block Deployer Address
    pub const L1_BLOCK_DEPLOYER: Address = address!("4250000000000000000000000000000000000000");

    /// Gas Price Oracle Deployer Address
    pub const GAS_PRICE_ORACLE_DEPLOYER: Address =
        address!("4250000000000000000000000000000000000001");

    /// Operator Fee Vault Deployer Address
    pub const OPERATOR_FEE_VAULT_DEPLOYER: Address =
        address!("4250000000000000000000000000000000000002");

    /// The new L1 Block implementation address
    /// This is computed by using go-ethereum's `crypto.CreateAddress` function,
    /// with the L1 Block Deployer Address and nonce 0.
    pub const ARSIA_L1_BLOCK_ADDRESS: Address =
        address!("e977b52f42ae5ce38e4300f1a83feac4a7f84700");

    /// The new Gas Price Oracle implementation address
    /// This is computed by using go-ethereum's `crypto.CreateAddress` function,
    /// with the Gas Price Oracle Deployer Address and nonce 0.
    pub const ARSIA_GAS_PRICE_ORACLE_ADDRESS: Address =
        address!("d8828b3c2853ec7238b54ad5183906bb9563332e");

    /// The new Operator Fee Vault implementation address
    /// This is computed by using go-ethereum's `crypto.CreateAddress` function,
    /// with the Operator Fee Vault Deployer Address and nonce 0.
    pub const ARSIA_OPERATOR_FEE_VAULT_ADDRESS: Address =
        address!("220280fbf8c32d030c314b4839559706ba7f5691");

    /// The depositor account address for enabling Arsia
    pub const DEPOSITOR_ACCOUNT: Address = address!("DeaDDEaDDeAdDeAdDEAdDEaddeAddEAdDEAd0001");

    /// The Enable Arsia Input Method 4Byte Signature
    pub const ENABLE_ARSIA_INPUT: [u8; 4] = hex!("8f018a7b");

    /// The Operator Fee Vault Recipient Address
    pub const OPERATOR_FEE_VAULT_RECIPIENT: Address =
        address!("2f44bd2a54ac3fb20cd7783cf94334069641dac9");

    /// L1 Block deployment bytecode
    pub const L1_BLOCK_DEPLOYMENT_BYTECODE: &str = include_str!("bytecode/arsia_l1_block.hex");

    /// Gas Price Oracle deployment bytecode
    pub const GAS_PRICE_ORACLE_DEPLOYMENT_BYTECODE: &str = include_str!("bytecode/arsia_gpo.hex");

    /// Operator Fee Vault deployment bytecode
    pub const OPERATOR_FEE_VAULT_DEPLOYMENT_BYTECODE: &str = include_str!("bytecode/arsia_ofv.hex");

    /// Get L1 Block deployment source hash
    pub fn deploy_l1_block_source() -> B256 {
        UpgradeDepositSource { intent: String::from("Arsia: L1 Block Deployment") }.source_hash()
    }

    /// Get Gas Price Oracle deployment source hash
    pub fn deploy_gas_price_oracle_source() -> B256 {
        UpgradeDepositSource { intent: String::from("Arsia: Gas Price Oracle Deployment") }
            .source_hash()
    }

    /// Get Operator Fee Vault deployment source hash
    pub fn deploy_operator_fee_vault_source() -> B256 {
        UpgradeDepositSource { intent: String::from("Arsia: Operator Fee Vault Deployment") }
            .source_hash()
    }

    /// Get L1 Block proxy update source hash
    pub fn update_l1_block_proxy_source() -> B256 {
        UpgradeDepositSource { intent: String::from("Arsia: L1 Block Proxy Update") }.source_hash()
    }

    /// Get Gas Price Oracle proxy update source hash
    pub fn update_gas_price_oracle_proxy_source() -> B256 {
        UpgradeDepositSource { intent: String::from("Arsia: Gas Price Oracle Proxy Update") }
            .source_hash()
    }

    /// Get Operator Fee Vault proxy update source hash
    pub fn update_operator_fee_vault_proxy_source() -> B256 {
        UpgradeDepositSource { intent: String::from("Arsia: Operator Fee Vault Proxy Update") }
            .source_hash()
    }

    /// Get Gas Price Oracle set Arsia source hash
    pub fn set_arsia_source() -> B256 {
        UpgradeDepositSource { intent: String::from("Arsia: Gas Price Oracle Set Arsia") }
            .source_hash()
    }

    /// Returns the L1 Block deployment bytecode.
    pub fn l1_block_deployment_bytecode() -> Bytes {
        Bytes::from(hex::decode(Self::L1_BLOCK_DEPLOYMENT_BYTECODE.trim()).expect("Valid hex"))
    }

    /// Returns the Gas Price Oracle deployment bytecode.
    pub fn gas_price_oracle_deployment_bytecode() -> Bytes {
        Bytes::from(
            hex::decode(Self::GAS_PRICE_ORACLE_DEPLOYMENT_BYTECODE.trim()).expect("Valid hex"),
        )
    }

    /// Returns the Operator Fee Vault deployment bytecode.
    pub fn operator_fee_vault_deployment_bytecode() -> Bytes {
        Bytes::from(
            hex::decode(Self::OPERATOR_FEE_VAULT_DEPLOYMENT_BYTECODE.trim()).expect("Valid hex"),
        )
    }

    /// Returns an iterator over the Arsia upgrade deposit transactions.
    pub fn deposits() -> impl Iterator<Item = TxDeposit> {
        [
            // 1. Deploy new L1Block implementation
            TxDeposit {
                source_hash: Self::deploy_l1_block_source(),
                from: Self::L1_BLOCK_DEPLOYER,
                to: TxKind::Create,
                mint: 0,
                value: U256::ZERO,
                gas_limit: 700_000,
                is_system_transaction: false,
                input: Self::l1_block_deployment_bytecode(),
                eth_value: 0,
                eth_tx_value: None,
            },
            // 2. Deploy new GasPriceOracle implementation
            TxDeposit {
                source_hash: Self::deploy_gas_price_oracle_source(),
                from: Self::GAS_PRICE_ORACLE_DEPLOYER,
                to: TxKind::Create,
                mint: 0,
                value: U256::ZERO,
                gas_limit: 1_800_000,
                is_system_transaction: false,
                input: Self::gas_price_oracle_deployment_bytecode(),
                eth_value: 0,
                eth_tx_value: None,
            },
            // 3. Deploy new OperatorFeeVault implementation
            TxDeposit {
                source_hash: Self::deploy_operator_fee_vault_source(),
                from: Self::OPERATOR_FEE_VAULT_DEPLOYER,
                to: TxKind::Create,
                mint: 0,
                value: U256::ZERO,
                gas_limit: 500_000,
                is_system_transaction: false,
                input: Self::operator_fee_vault_deployment_bytecode(),
                eth_value: 0,
                eth_tx_value: None,
            },
            // 4. Upgrade L1Block proxy
            TxDeposit {
                source_hash: Self::update_l1_block_proxy_source(),
                from: Address::ZERO,
                to: TxKind::Call(Predeploys::L1_BLOCK_INFO),
                mint: 0,
                value: U256::ZERO,
                gas_limit: 50_000,
                is_system_transaction: false,
                input: crate::upgrade_to_calldata(Self::ARSIA_L1_BLOCK_ADDRESS),
                eth_value: 0,
                eth_tx_value: None,
            },
            // 5. Upgrade GasPriceOracle proxy
            TxDeposit {
                source_hash: Self::update_gas_price_oracle_proxy_source(),
                from: Address::ZERO,
                to: TxKind::Call(Predeploys::GAS_PRICE_ORACLE),
                mint: 0,
                value: U256::ZERO,
                gas_limit: 50_000,
                is_system_transaction: false,
                input: crate::upgrade_to_calldata(Self::ARSIA_GAS_PRICE_ORACLE_ADDRESS),
                eth_value: 0,
                eth_tx_value: None,
            },
            // 6. Upgrade OperatorFeeVault proxy
            TxDeposit {
                source_hash: Self::update_operator_fee_vault_proxy_source(),
                from: Address::ZERO,
                to: TxKind::Call(Predeploys::OPERATOR_FEE_VAULT),
                mint: 0,
                value: U256::ZERO,
                gas_limit: 50_000,
                is_system_transaction: false,
                input: crate::upgrade_to_calldata(Self::ARSIA_OPERATOR_FEE_VAULT_ADDRESS),
                eth_value: 0,
                eth_tx_value: None,
            },
            // 7. Enable Arsia in GasPriceOracle
            TxDeposit {
                source_hash: Self::set_arsia_source(),
                from: Self::DEPOSITOR_ACCOUNT,
                to: TxKind::Call(Predeploys::GAS_PRICE_ORACLE),
                mint: 0,
                value: U256::ZERO,
                gas_limit: 100_000,
                is_system_transaction: false,
                input: Bytes::from(Self::ENABLE_ARSIA_INPUT.to_vec()),
                eth_value: 0,
                eth_tx_value: None,
            },
        ]
        .into_iter()
    }
}

impl Hardfork for Arsia {
    /// Constructs the Arsia network upgrade transactions.
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
    use crate::test_utils::check_deployment_code;
    use alloc::vec::Vec;
    use alloy_primitives::{b256, keccak256};

    #[test]
    fn test_arsia_upgrade_txs() {
        let arsia = Arsia;
        let txs: Vec<Bytes> = arsia.txs().collect();
        assert_eq!(txs.len(), 7, "Arsia upgrade should have 7 transactions");
    }

    #[test]
    fn test_deploy_l1_block_source_hash() {
        let expected = b256!("343f879c393b73e52e8c1ecb51a38c7165faf06e25c482ff5f82333e5ae295e6");
        assert_eq!(Arsia::deploy_l1_block_source(), expected);
    }

    #[test]
    fn test_deploy_gas_price_oracle_source_hash() {
        let expected = b256!("fe44184ad58b4cb10db4f9b9aa8aeaec19b2e51d8028bb1ee771cbdd4c1cb5da");
        assert_eq!(Arsia::deploy_gas_price_oracle_source(), expected);
    }

    #[test]
    fn test_deploy_operator_fee_vault_source_hash() {
        let expected = b256!("94ed52865378938e134cc7a90a565d9010f78e08b26363edad62f6da9031ebb8");
        assert_eq!(Arsia::deploy_operator_fee_vault_source(), expected);
    }

    #[test]
    fn test_update_l1_block_proxy_source_hash() {
        let expected = b256!("e353e514c6c2b30b8bba0a78a53aefc37b13a96ea3f21c29d0eed7acc3e17ad2");
        assert_eq!(Arsia::update_l1_block_proxy_source(), expected);
    }

    #[test]
    fn test_update_gas_price_oracle_proxy_source_hash() {
        let expected = b256!("1384418a52a30b6f0c234383ea19f93f3d18923c09dc1a685add20cde372b287");
        assert_eq!(Arsia::update_gas_price_oracle_proxy_source(), expected);
    }

    #[test]
    fn test_update_operator_fee_vault_proxy_source_hash() {
        let expected = b256!("46891cf0872389e54606b9c91fd701d29e6843e13b3f0e03dc15fc687bdac358");
        assert_eq!(Arsia::update_operator_fee_vault_proxy_source(), expected);
    }

    #[test]
    fn test_set_arsia_source_hash() {
        let expected = b256!("04bcc18c47051eb12de7428483c4c5c385be859adaba4f6f73bd596971bbad84");
        assert_eq!(Arsia::set_arsia_source(), expected);
    }

    #[test]
    fn test_verify_enable_arsia_method_signature() {
        // Verify the setArsia() method signature
        let hash = &keccak256("setArsia()")[..4];
        assert_eq!(hash, hex!("8f018a7b"));
        assert_eq!(hash, Arsia::ENABLE_ARSIA_INPUT);
    }

    // TODO: fix this test
    #[ignore]
    #[test]
    fn test_verify_arsia_l1_block_deployment_code_hash() {
        let txs = Arsia::deposits().collect::<Vec<_>>();
        check_deployment_code(
            txs[0].clone(),
            Arsia::ARSIA_L1_BLOCK_ADDRESS,
            hex!("31281c9967b6e01f796812dc6d391b1961787212c22aa0f5cef262bd2373037a").into(),
        );
    }

    // TODO: fix this test
    #[ignore]
    #[test]
    fn test_verify_arsia_gas_price_oracle_deployment_code_hash() {
        let txs = Arsia::deposits().collect::<Vec<_>>();
        check_deployment_code(
            txs[1].clone(),
            Arsia::ARSIA_GAS_PRICE_ORACLE_ADDRESS,
            hex!("0b858803e58087bde3ffdb2ecd4e84cf3cca421d4a81f232f8f89a685a13e332").into(),
        );
    }

    // TODO: fix this test
    #[ignore]
    #[test]
    fn test_verify_arsia_operator_fee_vault_deployment_code_hash() {
        let txs = Arsia::deposits().collect::<Vec<_>>();
        check_deployment_code(
            txs[2].clone(),
            Arsia::ARSIA_OPERATOR_FEE_VAULT_ADDRESS,
            hex!("8fc59a001c3cfde65cb42baba63a9fd8e97a3d19e914df4e8eb129a4a4fbca93").into(),
        );
    }
}
