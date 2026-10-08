//! Module containing fee parameters.

use alloy_eips::eip1559::BaseFeeParams;

use crate::{
    BASE_MAINNET_CHAIN_ID, BASE_SEPOLIA_CHAIN_ID, MANTLE_MAINNET_CHAIN_ID, MANTLE_SEPOLIA_CHAIN_ID,
    OP_SEPOLIA_CHAIN_ID,
};

/// Base fee max change denominator for Optimism Mainnet as defined in the Optimism
/// [transaction costs](https://docs.optimism.io/app-developers/transactions/fees) doc.
pub const OP_MAINNET_EIP1559_DEFAULT_BASE_FEE_MAX_CHANGE_DENOMINATOR: u64 = 50;

/// Base fee max change denominator for Optimism Mainnet as defined in the Optimism Canyon
/// hardfork.
pub const OP_MAINNET_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR_CANYON: u64 = 250;

/// Base fee max change denominator for Optimism Mainnet as defined in the Optimism
/// [transaction costs](https://docs.optimism.io/app-developers/transactions/fees) doc.
pub const OP_MAINNET_EIP1559_DEFAULT_ELASTICITY_MULTIPLIER: u64 = 6;

/// Base fee max change denominator for Optimism Sepolia as defined in the Optimism
/// [transaction costs](https://docs.optimism.io/app-developers/transactions/fees) doc.
pub const OP_SEPOLIA_EIP1559_DEFAULT_BASE_FEE_MAX_CHANGE_DENOMINATOR: u64 = 50;

/// Base fee max change denominator for Optimism Sepolia as defined in the Optimism Canyon
/// hardfork.
pub const OP_SEPOLIA_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR_CANYON: u64 = 250;

/// Base fee max change denominator for Optimism Sepolia as defined in the Optimism
/// [transaction costs](https://docs.optimism.io/app-developers/transactions/fees) doc.
pub const OP_SEPOLIA_EIP1559_DEFAULT_ELASTICITY_MULTIPLIER: u64 = 6;

/// Base fee max change denominator for Base Sepolia as defined in the Optimism
/// [transaction costs](https://docs.optimism.io/app-developers/transactions/fees) doc.
pub const BASE_SEPOLIA_EIP1559_DEFAULT_ELASTICITY_MULTIPLIER: u64 = 10;

/// Base fee max change denominator for Base Sepolia as defined in the Optimism Canyon
/// hardfork.
pub const BASE_SEPOLIA_EIP1559_DEFAULT_BASE_FEE_MAX_CHANGE_DENOMINATOR: u64 = 50;

/// Base fee max change denominator for Base Sepolia as defined in the Optimism Canyon
/// hardfork.
pub const BASE_SEPOLIA_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR_CANYON: u64 = 250;

/// Base fee max change denominator for Base Mainnet as defined in the Optimism
/// [transaction costs](https://docs.optimism.io/app-developers/transactions/fees) doc.
pub const BASE_MAINNET_EIP1559_DEFAULT_ELASTICITY_MULTIPLIER: u64 = 6;

/// Base fee max change denominator for Base Mainnet as defined in the Optimism Canyon
/// hardfork.
pub const BASE_MAINNET_EIP1559_DEFAULT_BASE_FEE_MAX_CHANGE_DENOMINATOR: u64 = 50;

/// Base fee max change denominator for Base Mainnet as defined in the Optimism Canyon
/// hardfork.
pub const BASE_MAINNET_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR_CANYON: u64 = 250;

/// Get the base fee parameters for Optimism Sepolia.
pub const OP_SEPOLIA_BASE_FEE_PARAMS: BaseFeeParams = BaseFeeParams {
    max_change_denominator: OP_SEPOLIA_EIP1559_DEFAULT_BASE_FEE_MAX_CHANGE_DENOMINATOR as u128,
    elasticity_multiplier: OP_SEPOLIA_EIP1559_DEFAULT_ELASTICITY_MULTIPLIER as u128,
};

/// Get the base fee parameters for Base Sepolia.
pub const BASE_SEPOLIA_BASE_FEE_PARAMS: BaseFeeParams = BaseFeeParams {
    max_change_denominator: OP_SEPOLIA_EIP1559_DEFAULT_BASE_FEE_MAX_CHANGE_DENOMINATOR as u128,
    elasticity_multiplier: BASE_SEPOLIA_EIP1559_DEFAULT_ELASTICITY_MULTIPLIER as u128,
};

/// Get the base fee parameters for Optimism Mainnet.
pub const OP_MAINNET_BASE_FEE_PARAMS: BaseFeeParams = BaseFeeParams {
    max_change_denominator: OP_MAINNET_EIP1559_DEFAULT_BASE_FEE_MAX_CHANGE_DENOMINATOR as u128,
    elasticity_multiplier: OP_MAINNET_EIP1559_DEFAULT_ELASTICITY_MULTIPLIER as u128,
};

/// Get the base fee parameters for Optimism Sepolia.
pub const OP_SEPOLIA_BASE_FEE_PARAMS_CANYON: BaseFeeParams = BaseFeeParams {
    max_change_denominator: OP_SEPOLIA_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR_CANYON as u128,
    elasticity_multiplier: OP_SEPOLIA_EIP1559_DEFAULT_ELASTICITY_MULTIPLIER as u128,
};

/// Get the base fee parameters for Base Sepolia.
pub const BASE_SEPOLIA_BASE_FEE_PARAMS_CANYON: BaseFeeParams = BaseFeeParams {
    max_change_denominator: OP_SEPOLIA_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR_CANYON as u128,
    elasticity_multiplier: BASE_SEPOLIA_EIP1559_DEFAULT_ELASTICITY_MULTIPLIER as u128,
};

/// Get the base fee parameters for Optimism Mainnet.
pub const OP_MAINNET_BASE_FEE_PARAMS_CANYON: BaseFeeParams = BaseFeeParams {
    max_change_denominator: OP_MAINNET_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR_CANYON as u128,
    elasticity_multiplier: OP_MAINNET_EIP1559_DEFAULT_ELASTICITY_MULTIPLIER as u128,
};

/// `[MANTLE]` Elasticity multiplier for Mantle.
///
/// Three independent sources agree on 2, and none of them on the 4 this used to hold:
///   * `op-node/rollup/mantle_types.go::AlignOpWithMantle` falls back to `EIP1559Elasticity: 2`
///     when the rollup config carries no `ChainOpConfig`;
///   * `packages/contracts-bedrock/deploy-config/mantle-{mainnet,sepolia}.json` set
///     `"eip1559Elasticity": 2`;
///   * the live sepolia-qa3 rollup config embedded in this crate's executor fixtures carries
///     `{"eip1559Elasticity": 2, "eip1559Denominator": 8, "eip1559DenominatorCanyon": 8}`.
///
/// The old `{4, 50}` pair appears nowhere in op-node, op-geth or any deploy config. 50 is the
/// *devnet* denominator (whose elasticity is 10, not 4), so the pair looks like two values
/// crossed from different chains.
///
/// This only governs chains whose rollup config omits `chain_op_config` — real Mantle configs
/// carry it — but when it does bite it silently produces a different base fee than op-node, and
/// therefore a different block.
pub const MANTLE_EIP1559_ELASTICITY_MULTIPLIER: u64 = 2;

/// `[MANTLE]` Base fee max change denominator for Mantle.
/// See [`MANTLE_EIP1559_ELASTICITY_MULTIPLIER`] for the provenance of this value.
pub const MANTLE_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR: u64 = 8;

/// `[MANTLE]` Base fee parameters for Mantle.
pub const MANTLE_BASE_FEE_PARAMS: BaseFeeParams = BaseFeeParams {
    max_change_denominator: MANTLE_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR as u128,
    elasticity_multiplier: MANTLE_EIP1559_ELASTICITY_MULTIPLIER as u128,
};

/// `[MANTLE]` Base fee config for Mantle.
/// Mantle has no historical change to the denominator, so canyon uses the same denominator.
pub const MANTLE_BASE_FEE_CONFIG: BaseFeeConfig = BaseFeeConfig {
    eip1559_elasticity: MANTLE_EIP1559_ELASTICITY_MULTIPLIER,
    eip1559_denominator: MANTLE_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR,
    eip1559_denominator_canyon: MANTLE_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR,
};

/// Returns the [`BaseFeeParams`] for the given chain id.
pub const fn base_fee_params(chain_id: u64) -> BaseFeeParams {
    match chain_id {
        OP_SEPOLIA_CHAIN_ID => OP_SEPOLIA_BASE_FEE_PARAMS,
        BASE_SEPOLIA_CHAIN_ID => BASE_SEPOLIA_BASE_FEE_PARAMS,
        MANTLE_MAINNET_CHAIN_ID | MANTLE_SEPOLIA_CHAIN_ID => MANTLE_BASE_FEE_PARAMS,
        _ => OP_MAINNET_BASE_FEE_PARAMS,
    }
}

/// Returns the [`BaseFeeParams`] for the given chain id, for canyon hardfork.
pub const fn base_fee_params_canyon(chain_id: u64) -> BaseFeeParams {
    match chain_id {
        OP_SEPOLIA_CHAIN_ID => OP_SEPOLIA_BASE_FEE_PARAMS_CANYON,
        BASE_SEPOLIA_CHAIN_ID => BASE_SEPOLIA_BASE_FEE_PARAMS_CANYON,
        // Mantle has no historical change to the denominator, so use the same params as
        // base_fee_params.
        MANTLE_MAINNET_CHAIN_ID | MANTLE_SEPOLIA_CHAIN_ID => MANTLE_BASE_FEE_PARAMS,
        _ => OP_MAINNET_BASE_FEE_PARAMS_CANYON,
    }
}

/// Returns the [`BaseFeeConfig`] for the given chain id.
pub const fn base_fee_config(chain_id: u64) -> BaseFeeConfig {
    match chain_id {
        OP_SEPOLIA_CHAIN_ID => OP_SEPOLIA_BASE_FEE_CONFIG,
        BASE_MAINNET_CHAIN_ID => BASE_MAINNET_BASE_FEE_CONFIG,
        BASE_SEPOLIA_CHAIN_ID => BASE_SEPOLIA_BASE_FEE_CONFIG,
        MANTLE_MAINNET_CHAIN_ID | MANTLE_SEPOLIA_CHAIN_ID => MANTLE_BASE_FEE_CONFIG,
        _ => OP_MAINNET_BASE_FEE_CONFIG,
    }
}

/// Get the base fee parameters for Optimism Sepolia.
pub const OP_SEPOLIA_BASE_FEE_CONFIG: BaseFeeConfig = BaseFeeConfig {
    eip1559_elasticity: OP_SEPOLIA_EIP1559_DEFAULT_ELASTICITY_MULTIPLIER,
    eip1559_denominator: OP_SEPOLIA_EIP1559_DEFAULT_BASE_FEE_MAX_CHANGE_DENOMINATOR,
    eip1559_denominator_canyon: OP_SEPOLIA_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR_CANYON,
};

/// Get the base fee parameters for Base Sepolia.
pub const BASE_SEPOLIA_BASE_FEE_CONFIG: BaseFeeConfig = BaseFeeConfig {
    eip1559_elasticity: BASE_SEPOLIA_EIP1559_DEFAULT_ELASTICITY_MULTIPLIER,
    eip1559_denominator: BASE_SEPOLIA_EIP1559_DEFAULT_BASE_FEE_MAX_CHANGE_DENOMINATOR,
    eip1559_denominator_canyon: BASE_SEPOLIA_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR_CANYON,
};

/// Get the base fee parameters for Optimism Mainnet.
pub const OP_MAINNET_BASE_FEE_CONFIG: BaseFeeConfig = BaseFeeConfig {
    eip1559_elasticity: OP_MAINNET_EIP1559_DEFAULT_ELASTICITY_MULTIPLIER,
    eip1559_denominator: OP_MAINNET_EIP1559_DEFAULT_BASE_FEE_MAX_CHANGE_DENOMINATOR,
    eip1559_denominator_canyon: OP_MAINNET_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR_CANYON,
};

/// Get the base fee parameters for Base Mainnet.
pub const BASE_MAINNET_BASE_FEE_CONFIG: BaseFeeConfig = BaseFeeConfig {
    eip1559_elasticity: BASE_MAINNET_EIP1559_DEFAULT_ELASTICITY_MULTIPLIER,
    eip1559_denominator: BASE_MAINNET_EIP1559_DEFAULT_BASE_FEE_MAX_CHANGE_DENOMINATOR,
    eip1559_denominator_canyon: BASE_MAINNET_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR_CANYON,
};

/// Optimism Base Fee Config.
#[derive(Debug, Copy, Clone, Eq, PartialEq)]
#[cfg_attr(feature = "arbitrary", derive(arbitrary::Arbitrary))]
#[cfg_attr(feature = "serde", derive(serde::Serialize, serde::Deserialize))]
pub struct BaseFeeConfig {
    /// EIP 1559 Elasticity Parameter
    #[cfg_attr(
        feature = "serde",
        serde(rename = "eip1559Elasticity", alias = "eip1559_elasticity")
    )]
    pub eip1559_elasticity: u64,
    /// EIP 1559 Denominator
    #[cfg_attr(
        feature = "serde",
        serde(rename = "eip1559Denominator", alias = "eip1559_denominator")
    )]
    pub eip1559_denominator: u64,
    /// EIP 1559 Denominator for the Canyon hardfork
    #[cfg_attr(
        feature = "serde",
        serde(rename = "eip1559DenominatorCanyon", alias = "eip1559_denominator_canyon")
    )]
    pub eip1559_denominator_canyon: u64,
}

impl BaseFeeConfig {
    /// Get the base fee parameters for Optimism Mainnet
    pub const fn optimism() -> Self {
        Self {
            eip1559_elasticity: OP_MAINNET_EIP1559_DEFAULT_ELASTICITY_MULTIPLIER,
            eip1559_denominator: OP_MAINNET_EIP1559_DEFAULT_BASE_FEE_MAX_CHANGE_DENOMINATOR,
            eip1559_denominator_canyon: OP_MAINNET_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR_CANYON,
        }
    }

    /// Returns the [`BaseFeeParams`] before Canyon hardfork.
    pub const fn pre_canyon_params(&self) -> BaseFeeParams {
        BaseFeeParams {
            max_change_denominator: self.eip1559_denominator as u128,
            elasticity_multiplier: self.eip1559_elasticity as u128,
        }
    }

    /// Returns the [`BaseFeeParams`] since Canyon hardfork.
    pub const fn post_canyon_params(&self) -> BaseFeeParams {
        BaseFeeParams {
            max_change_denominator: self.eip1559_denominator_canyon as u128,
            elasticity_multiplier: self.eip1559_elasticity as u128,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::OP_MAINNET_CHAIN_ID;

    #[test]
    fn test_base_fee_params_from_chain_id() {
        assert_eq!(base_fee_params(OP_MAINNET_CHAIN_ID), OP_MAINNET_BASE_FEE_PARAMS);
        assert_eq!(base_fee_params(OP_SEPOLIA_CHAIN_ID), OP_SEPOLIA_BASE_FEE_PARAMS);
        assert_eq!(base_fee_params(BASE_MAINNET_CHAIN_ID), OP_MAINNET_BASE_FEE_PARAMS);
        assert_eq!(base_fee_params(BASE_SEPOLIA_CHAIN_ID), BASE_SEPOLIA_BASE_FEE_PARAMS);
        assert_eq!(base_fee_params(0), OP_MAINNET_BASE_FEE_PARAMS);
    }

    /// `[MANTLE]` Pins the fallback base-fee params to what op-node actually uses.
    ///
    /// `AlignOpWithMantle` installs `{Elasticity: 2, Denominator: 8, DenominatorCanyon: 8}` when
    /// the rollup config has no `ChainOpConfig`, and both production deploy configs agree. A
    /// mismatch here changes the base fee on any chain that omits the field — a silent consensus
    /// divergence, since nothing else would complain.
    #[test]
    fn mantle_base_fee_fallback_matches_op_node() {
        assert_eq!(MANTLE_EIP1559_ELASTICITY_MULTIPLIER, 2);
        assert_eq!(MANTLE_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR, 8);

        // Mantle has no historical denominator change, so Canyon reuses the same value —
        // mirroring `AlignOpWithMantle`'s `dCanyon := c.ChainOpConfig.EIP1559Denominator`.
        assert_eq!(
            MANTLE_BASE_FEE_CONFIG.eip1559_denominator_canyon,
            MANTLE_BASE_FEE_CONFIG.eip1559_denominator,
        );

        for chain_id in [MANTLE_MAINNET_CHAIN_ID, MANTLE_SEPOLIA_CHAIN_ID] {
            assert_eq!(base_fee_config(chain_id), MANTLE_BASE_FEE_CONFIG);
            assert_eq!(base_fee_params(chain_id), MANTLE_BASE_FEE_PARAMS);
        }

        // `BaseFeeParams` stores the denominator, not the elasticity, as `max_change_denominator`
        // — an easy pair to transpose.
        assert_eq!(MANTLE_BASE_FEE_PARAMS.max_change_denominator, 8);
        assert_eq!(MANTLE_BASE_FEE_PARAMS.elasticity_multiplier, 2);
    }

    #[test]
    fn test_base_fee_params_canyon_from_chain_id() {
        assert_eq!(base_fee_params_canyon(OP_MAINNET_CHAIN_ID), OP_MAINNET_BASE_FEE_PARAMS_CANYON);
        assert_eq!(base_fee_params_canyon(OP_SEPOLIA_CHAIN_ID), OP_SEPOLIA_BASE_FEE_PARAMS_CANYON);
        assert_eq!(
            base_fee_params_canyon(BASE_MAINNET_CHAIN_ID),
            OP_MAINNET_BASE_FEE_PARAMS_CANYON
        );
        assert_eq!(
            base_fee_params_canyon(BASE_SEPOLIA_CHAIN_ID),
            BASE_SEPOLIA_BASE_FEE_PARAMS_CANYON
        );
        assert_eq!(base_fee_params_canyon(0), OP_MAINNET_BASE_FEE_PARAMS_CANYON);
    }

    #[test]
    #[cfg(feature = "serde")]
    fn test_base_fee_config_ser() {
        let config = OP_MAINNET_BASE_FEE_CONFIG;
        let raw_str = serde_json::to_string(&config).unwrap();
        assert_eq!(
            raw_str,
            r#"{"eip1559Elasticity":6,"eip1559Denominator":50,"eip1559DenominatorCanyon":250}"#
        );
    }

    #[test]
    #[cfg(feature = "serde")]
    fn test_base_fee_config_deser() {
        let raw_str: &'static str =
            r#"{"eip1559Elasticity":6,"eip1559Denominator":50,"eip1559DenominatorCanyon":250}"#;
        let config: BaseFeeConfig = serde_json::from_str(raw_str).unwrap();
        assert_eq!(config, OP_MAINNET_BASE_FEE_CONFIG);
    }
}
