//! Engine API version selection based on Optimism hardfork activations.
//!
//! Automatically selects the appropriate Engine API method versions based on
//! the rollup configuration and block timestamps. The required method version
//! tracks the L1 (Ethereum) hardfork implied by the active OP hardfork, so
//! versions are selected by querying the L1 fork activations that the rollup
//! config derives from the canonical OP fork → L1 fork mapping in
//! `alloy-op-hardforks`.
//!
//! # Version Mapping
//!
//! - **pre-Cancun (Bedrock, Canyon, Delta)** → V2 methods
//! - **Cancun (Ecotone)** → V3 methods
//! - **Prague (Isthmus)** → V4 methods
//! - **Osaka (Karst)** → V5 `getPayload` (`newPayload`/`forkchoiceUpdated` stay at their V4/V3)
//!
//! Adapted from the [OP Node version providers](https://github.com/ethereum-optimism/optimism/blob/develop/op-node/rollup/types.go#L546).

use alloy_hardforks::EthereumHardforks;
use kona_genesis::RollupConfig;

/// Engine API version for `engine_forkchoiceUpdated` method calls.
///
/// Selects between V2 and V3 based on hardfork activation. V3 is required
/// for Ecotone/Cancun and later hardforks to support new consensus features.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum EngineForkchoiceVersion {
    /// Version 2: Used for Bedrock, Canyon, and Delta hardforks.
    V2,
    /// Version 3: Required for Ecotone/Cancun and later hardforks.
    V3,
}

impl EngineForkchoiceVersion {
    /// Returns the appropriate [`EngineForkchoiceVersion`] for the chain at the given attributes.
    ///
    /// Uses the [`RollupConfig`] to check which L1 hardfork is implied at the given timestamp.
    pub fn from_cfg(cfg: &RollupConfig, timestamp: u64) -> Self {
        // [MANTLE] op-node: `IsEcotone(ts) || IsMantleSkadi(ts)` (rollup/types.go
        // `ForkchoiceUpdatedVersion`). The Skadi disjunct is load-bearing: every OP fork on a
        // Mantle chain is pinned to `mantle_arsia_time`, so between Skadi and Arsia Cancun is
        // still inactive while op-node already requires V3.
        if cfg.is_cancun_active_at_timestamp(timestamp) || cfg.is_mantle_skadi_active(timestamp) {
            // Ecotone+
            Self::V3
        } else {
            // Bedrock, Canyon, Delta
            Self::V2
        }
    }
}

/// Engine API version for `engine_newPayload` method calls.
///
/// Progressive version selection based on hardfork activation:
/// - V2: Basic payload processing
/// - V3: Adds Cancun/Ecotone support
/// - V4: Adds Isthmus hardfork features
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum EngineNewPayloadVersion {
    /// Version 2: Basic payload processing for early hardforks.
    V2,
    /// Version 3: Adds Cancun/Ecotone consensus features.
    V3,
    /// Version 4: Adds Isthmus hardfork support.
    V4,
}

impl EngineNewPayloadVersion {
    /// Returns the appropriate [`EngineNewPayloadVersion`] for the chain at the given timestamp.
    ///
    /// Uses the [`RollupConfig`] to check which L1 hardfork is implied at the given timestamp.
    pub fn from_cfg(cfg: &RollupConfig, timestamp: u64) -> Self {
        // [MANTLE] op-node: `IsIsthmus(ts) || IsMantleSkadi(ts)` (rollup/types.go
        // `NewPayloadVersion`). See `EngineForkchoiceVersion::from_cfg` for why Skadi is not
        // implied by the OP forks here.
        if cfg.is_prague_active_at_timestamp(timestamp) || cfg.is_mantle_skadi_active(timestamp) {
            Self::V4
        } else if cfg.is_cancun_active_at_timestamp(timestamp) {
            Self::V3
        } else {
            Self::V2
        }
    }
}

/// Engine API version for `engine_getPayload` method calls.
///
/// Matches the payload version used for retrieval with the version
/// used during payload construction, ensuring API compatibility.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum EngineGetPayloadVersion {
    /// Version 2: Basic payload retrieval.
    V2,
    /// Version 3: Enhanced payload data for Cancun/Ecotone.
    V3,
    /// Version 4: Extended payload format for Isthmus.
    V4,
    /// Version 5: Osaka (`engine_getPayloadV5`); reuses the V4-shaped envelope.
    V5,
}

impl EngineGetPayloadVersion {
    /// Returns the appropriate [`EngineGetPayloadVersion`] for the chain at the given timestamp.
    ///
    /// Uses the [`RollupConfig`] to check which L1 hardfork is implied at the given timestamp.
    /// Osaka (Karst) bumps only `getPayload` to V5; `newPayload`/`forkchoiceUpdated` are
    /// unchanged.
    pub fn from_cfg(cfg: &RollupConfig, timestamp: u64) -> Self {
        // [MANTLE] op-node's `GetPayloadVersion` reaches V5 through `IsMantleLimb`, not Osaka:
        // Mantle configs never set `karst_time`, so the Osaka branch alone can never fire on a
        // Mantle chain. V4 additionally takes `IsIsthmus(ts) || IsMantleSkadi(ts)`.
        if cfg.is_osaka_active_at_timestamp(timestamp) || cfg.is_mantle_limb_active(timestamp) {
            Self::V5
        } else if cfg.is_prague_active_at_timestamp(timestamp) ||
            cfg.is_mantle_skadi_active(timestamp)
        {
            Self::V4
        } else if cfg.is_cancun_active_at_timestamp(timestamp) {
            Self::V3
        } else {
            Self::V2
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use kona_genesis::{HardForkConfig, MantleHardForkConfig};

    fn cfg() -> RollupConfig {
        RollupConfig {
            hardforks: HardForkConfig {
                ecotone_time: Some(10),
                isthmus_time: Some(20),
                karst_time: Some(30),
                ..Default::default()
            },
            ..Default::default()
        }
    }

    #[test]
    fn forkchoice_version_selects_by_active_hardfork() {
        let cfg = cfg();
        assert_eq!(EngineForkchoiceVersion::from_cfg(&cfg, 5), EngineForkchoiceVersion::V2);
        assert_eq!(EngineForkchoiceVersion::from_cfg(&cfg, 10), EngineForkchoiceVersion::V3);
        assert_eq!(EngineForkchoiceVersion::from_cfg(&cfg, 35), EngineForkchoiceVersion::V3);
    }

    #[test]
    fn new_payload_version_selects_by_active_hardfork() {
        let cfg = cfg();
        assert_eq!(EngineNewPayloadVersion::from_cfg(&cfg, 5), EngineNewPayloadVersion::V2);
        assert_eq!(EngineNewPayloadVersion::from_cfg(&cfg, 15), EngineNewPayloadVersion::V3);
        assert_eq!(EngineNewPayloadVersion::from_cfg(&cfg, 25), EngineNewPayloadVersion::V4);
        // Karst (Osaka) does not bump newPayload.
        assert_eq!(EngineNewPayloadVersion::from_cfg(&cfg, 35), EngineNewPayloadVersion::V4);
    }

    #[test]
    fn get_payload_version_selects_by_active_hardfork() {
        let cfg = cfg();
        assert_eq!(EngineGetPayloadVersion::from_cfg(&cfg, 5), EngineGetPayloadVersion::V2);
        assert_eq!(EngineGetPayloadVersion::from_cfg(&cfg, 15), EngineGetPayloadVersion::V3);
        assert_eq!(EngineGetPayloadVersion::from_cfg(&cfg, 25), EngineGetPayloadVersion::V4);
        // Karst (Osaka) selects the new V5 getPayload, at and after its activation timestamp.
        assert_eq!(EngineGetPayloadVersion::from_cfg(&cfg, 30), EngineGetPayloadVersion::V5);
        assert_eq!(EngineGetPayloadVersion::from_cfg(&cfg, 35), EngineGetPayloadVersion::V5);
    }

    /// `[MANTLE]` A Mantle chain in the `[Skadi, Arsia)` window.
    ///
    /// Every OP fork is pinned to `mantle_arsia_time`, so the OP-derived L1 predicates are all
    /// false here — yet op-node already speaks V3/V4 because its selectors carry an explicit
    /// `|| IsMantleSkadi`. Selecting V2 instead makes kona-node call an engine method op-geth
    /// rejects for the payload it is handed.
    fn mantle_cfg() -> RollupConfig {
        RollupConfig {
            hardforks: HardForkConfig::default(),
            mantle_hardforks: MantleHardForkConfig {
                mantle_skadi_time: Some(100),
                mantle_limb_time: Some(300),
                mantle_arsia_time: Some(200),
                ..MantleHardForkConfig::NONE
            },
            ..Default::default()
        }
    }

    #[test]
    fn mantle_skadi_selects_the_same_versions_as_op_node() {
        let cfg = mantle_cfg();

        // Premise: no OP fork is active before Arsia.
        assert!(!cfg.is_cancun_active_at_timestamp(150));
        assert!(!cfg.is_prague_active_at_timestamp(150));

        // Pre-Skadi.
        assert_eq!(EngineForkchoiceVersion::from_cfg(&cfg, 99), EngineForkchoiceVersion::V2);
        assert_eq!(EngineNewPayloadVersion::from_cfg(&cfg, 99), EngineNewPayloadVersion::V2);
        assert_eq!(EngineGetPayloadVersion::from_cfg(&cfg, 99), EngineGetPayloadVersion::V2);

        // Skadi active, Arsia not yet — the window that used to select V2 across the board.
        for ts in [100, 150, 199] {
            assert_eq!(
                EngineForkchoiceVersion::from_cfg(&cfg, ts),
                EngineForkchoiceVersion::V3,
                "fcu at {ts}",
            );
            assert_eq!(
                EngineNewPayloadVersion::from_cfg(&cfg, ts),
                EngineNewPayloadVersion::V4,
                "newPayload at {ts}",
            );
            assert_eq!(
                EngineGetPayloadVersion::from_cfg(&cfg, ts),
                EngineGetPayloadVersion::V4,
                "getPayload at {ts}",
            );
        }

        // Arsia: the OP forks turn on too; the answers must not regress.
        assert_eq!(EngineForkchoiceVersion::from_cfg(&cfg, 200), EngineForkchoiceVersion::V3);
        assert_eq!(EngineNewPayloadVersion::from_cfg(&cfg, 200), EngineNewPayloadVersion::V4);
        assert_eq!(EngineGetPayloadVersion::from_cfg(&cfg, 200), EngineGetPayloadVersion::V4);
    }

    /// op-node reaches `getPayloadV5` through `IsMantleLimb`, never through Osaka — Mantle
    /// configs leave `karst_time` unset, so the Osaka branch alone is dead code on Mantle.
    #[test]
    fn mantle_limb_selects_get_payload_v5() {
        let cfg = mantle_cfg();
        assert!(!cfg.is_osaka_active_at_timestamp(300), "test premise: Osaka must be inactive");
        assert_eq!(EngineGetPayloadVersion::from_cfg(&cfg, 299), EngineGetPayloadVersion::V4);
        assert_eq!(EngineGetPayloadVersion::from_cfg(&cfg, 300), EngineGetPayloadVersion::V5);
    }

    /// Non-Mantle chains are unaffected by any of the disjuncts above.
    #[test]
    fn op_chains_are_unaffected_by_the_mantle_disjuncts() {
        let cfg = cfg();
        assert_eq!(EngineNewPayloadVersion::from_cfg(&cfg, 5), EngineNewPayloadVersion::V2);
        assert_eq!(EngineNewPayloadVersion::from_cfg(&cfg, 10), EngineNewPayloadVersion::V3);
        assert_eq!(EngineNewPayloadVersion::from_cfg(&cfg, 20), EngineNewPayloadVersion::V4);
        assert_eq!(EngineGetPayloadVersion::from_cfg(&cfg, 30), EngineGetPayloadVersion::V5);
    }
}
