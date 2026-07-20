//! Rollup Config Types

use crate::{
    AltDAConfig, BaseFeeConfig, ChainGenesis, HardForkConfig, MANTLE_BASE_FEE_CONFIG,
    MantleHardForkConfig,
};
use alloy_chains::Chain;
use alloy_hardforks::{EthereumHardfork, EthereumHardforks, ForkCondition};
use alloy_op_hardforks::{OpHardfork, OpHardforks};
use alloy_primitives::Address;

/// The max rlp bytes per channel for the Bedrock hardfork.
pub const MAX_RLP_BYTES_PER_CHANNEL_BEDROCK: u64 = 10_000_000;

/// The max rlp bytes per channel for the Fjord hardfork.
pub const MAX_RLP_BYTES_PER_CHANNEL_FJORD: u64 = 100_000_000;

/// The max sequencer drift when the Fjord hardfork is active.
pub const FJORD_MAX_SEQUENCER_DRIFT: u64 = 1800;

/// The channel timeout once the Granite hardfork is active.
pub const GRANITE_CHANNEL_TIMEOUT: u64 = 50;

#[cfg(feature = "serde")]
const fn default_granite_channel_timeout() -> u64 {
    GRANITE_CHANNEL_TIMEOUT
}

/// The max sequencer drift needs to be changes for some chains, e.g. those that build only on
/// finalized L1 blocks, where L1 finality delays can exceed the standard
/// [`FJORD_MAX_SEQUENCER_DRIFT`].
#[cfg(all(feature = "serde", feature = "rollup_config_override"))]
const fn default_fjord_max_sequencer_drift() -> u64 {
    FJORD_MAX_SEQUENCER_DRIFT
}

/// [MANTLE] Default base fee config for serde when `chain_op_config` is missing — uses the
/// Mantle params instead of OP defaults.
#[cfg(feature = "serde")]
const fn default_mantle_base_fee_config() -> BaseFeeConfig {
    MANTLE_BASE_FEE_CONFIG
}

/// The Rollup configuration.
#[derive(Debug, Clone, Eq, PartialEq)]
#[cfg_attr(feature = "serde", derive(serde::Serialize, serde::Deserialize))]
pub struct RollupConfig {
    /// The genesis state of the rollup.
    pub genesis: ChainGenesis,
    /// The block time of the L2, in seconds.
    pub block_time: u64,
    /// Sequencer batches may not be more than `MaxSequencerDrift` seconds after
    /// the L1 timestamp of the sequencing window end.
    ///
    /// Note: When L1 has many 1 second consecutive blocks, and L2 grows at fixed 2 seconds,
    /// the L2 time may still grow beyond this difference.
    ///
    /// Note: After the Fjord hardfork, this value becomes a constant of `1800`.
    pub max_sequencer_drift: u64,
    /// The sequencer window size.
    pub seq_window_size: u64,
    /// Number of L1 blocks between when a channel can be opened and when it can be closed.
    pub channel_timeout: u64,
    /// The channel timeout after the Granite hardfork.
    #[cfg_attr(feature = "serde", serde(default = "default_granite_channel_timeout"))]
    pub granite_channel_timeout: u64,
    /// The max sequencer drift after the Fjord hardfork.
    #[cfg(feature = "rollup_config_override")]
    #[cfg_attr(feature = "serde", serde(default = "default_fjord_max_sequencer_drift"))]
    pub fjord_max_sequencer_drift: u64,
    /// The L1 chain ID
    pub l1_chain_id: u64,
    /// The L2 chain ID
    pub l2_chain_id: Chain,
    /// Hardfork timestamps.
    #[cfg_attr(feature = "serde", serde(flatten))]
    pub hardforks: HardForkConfig,
    /// Mantle-specific hardfork timestamps.
    #[cfg_attr(feature = "serde", serde(flatten))]
    pub mantle_hardforks: MantleHardForkConfig,
    /// `batch_inbox_address` is the L1 address that batches are sent to.
    pub batch_inbox_address: Address,
    /// `deposit_contract_address` is the L1 address that deposits are sent to.
    pub deposit_contract_address: Address,
    /// `l1_system_config_address` is the L1 address that the system config is stored at.
    pub l1_system_config_address: Address,
    /// The superchain config address.
    #[cfg_attr(feature = "serde", serde(skip_serializing_if = "Option::is_none"))]
    pub superchain_config_address: Option<Address>,
    /// `blobs_enabled_l1_timestamp` is the timestamp to start reading blobs as a batch data
    /// source. Optional.
    #[cfg_attr(
        feature = "serde",
        serde(rename = "blobs_data", skip_serializing_if = "Option::is_none")
    )]
    pub blobs_enabled_l1_timestamp: Option<u64>,
    /// `da_challenge_address` is the L1 address that the data availability challenge contract is
    /// stored at.
    #[cfg_attr(feature = "serde", serde(skip_serializing_if = "Option::is_none"))]
    pub da_challenge_address: Option<Address>,
    /// `alt_da_config` is the chain-specific DA config for the rollup.
    #[cfg_attr(feature = "serde", serde(rename = "alt_da"))]
    pub alt_da_config: Option<AltDAConfig>,
    /// `chain_op_config` is the chain-specific EIP1559 config for the rollup.
    #[cfg_attr(feature = "serde", serde(default = "default_mantle_base_fee_config"))]
    pub chain_op_config: BaseFeeConfig,
}

#[cfg(feature = "arbitrary")]
impl<'a> arbitrary::Arbitrary<'a> for RollupConfig {
    fn arbitrary(u: &mut arbitrary::Unstructured<'a>) -> arbitrary::Result<Self> {
        use crate::{
            BASE_SEPOLIA_BASE_FEE_CONFIG, MANTLE_BASE_FEE_CONFIG, OP_MAINNET_BASE_FEE_CONFIG,
            OP_SEPOLIA_BASE_FEE_CONFIG,
        };
        let chain_op_config = match u32::arbitrary(u)? % 4 {
            0 => OP_MAINNET_BASE_FEE_CONFIG,
            1 => OP_SEPOLIA_BASE_FEE_CONFIG,
            2 => BASE_SEPOLIA_BASE_FEE_CONFIG,
            _ => MANTLE_BASE_FEE_CONFIG,
        };

        Ok(Self {
            genesis: ChainGenesis::arbitrary(u)?,
            block_time: u.arbitrary()?,
            max_sequencer_drift: u.arbitrary()?,
            seq_window_size: u.arbitrary()?,
            channel_timeout: u.arbitrary()?,
            granite_channel_timeout: u.arbitrary()?,
            #[cfg(feature = "rollup_config_override")]
            fjord_max_sequencer_drift: u.arbitrary()?,
            l1_chain_id: u.arbitrary()?,
            l2_chain_id: u.arbitrary()?,
            hardforks: HardForkConfig::arbitrary(u)?,
            mantle_hardforks: MantleHardForkConfig::arbitrary(u)?,
            batch_inbox_address: Address::arbitrary(u)?,
            deposit_contract_address: Address::arbitrary(u)?,
            l1_system_config_address: Address::arbitrary(u)?,
            superchain_config_address: Option::<Address>::arbitrary(u)?,
            blobs_enabled_l1_timestamp: Option::<u64>::arbitrary(u)?,
            da_challenge_address: Option::<Address>::arbitrary(u)?,
            chain_op_config,
            alt_da_config: Option::<AltDAConfig>::arbitrary(u)?,
        })
    }
}

// Need to manually implement Default because [`BaseFeeParams`] has no Default impl.
impl Default for RollupConfig {
    fn default() -> Self {
        Self {
            genesis: ChainGenesis::default(),
            block_time: 0,
            max_sequencer_drift: 0,
            seq_window_size: 0,
            channel_timeout: 0,
            granite_channel_timeout: GRANITE_CHANNEL_TIMEOUT,
            #[cfg(feature = "rollup_config_override")]
            fjord_max_sequencer_drift: FJORD_MAX_SEQUENCER_DRIFT,
            l1_chain_id: 0,
            l2_chain_id: Chain::from_id(0),
            hardforks: HardForkConfig::default(),
            mantle_hardforks: MantleHardForkConfig::default(),
            batch_inbox_address: Address::ZERO,
            deposit_contract_address: Address::ZERO,
            l1_system_config_address: Address::ZERO,
            superchain_config_address: None,
            blobs_enabled_l1_timestamp: None,
            da_challenge_address: None,
            alt_da_config: None,
            // [MANTLE] Default to Mantle base fee config; OP variants override via ChainConfig.
            chain_op_config: MANTLE_BASE_FEE_CONFIG,
        }
    }
}

// [MANTLE] Mantle-specific predicate methods on RollupConfig.
impl RollupConfig {
    /// Returns true if this is a Mantle chain or a chain that uses Mantle hardforks.
    ///
    /// This method checks if any Mantle-specific hardfork is configured, rather than
    /// checking the chain_id. This approach is more flexible and works for:
    /// - Mantle Mainnet (chain_id 5000)
    /// - Mantle Sepolia (chain_id 5003)
    /// - Custom Mantle testnets with different chain_ids
    /// - Any chain that adopts Mantle hardforks
    #[inline]
    pub const fn is_mantle(&self) -> bool {
        self.mantle_hardforks.has_any_hardfork()
    }
}

#[cfg(feature = "revm")]
impl RollupConfig {
    /// Returns the active [`op_revm::OpSpecId`] for the executor.
    ///
    /// ## Takes
    /// - `timestamp`: The timestamp of the executing block.
    ///
    /// ## Returns
    /// The active [`op_revm::OpSpecId`] for the executor.
    pub fn spec_id(&self, timestamp: u64) -> op_revm::OpSpecId {
        if self.is_interop_active(timestamp) {
            op_revm::OpSpecId::INTEROP
        // [MANTLE] mantle-elysium op-revm v19 has no `KARST` variant (added in v20).
        // Skip the KARST arm so post-Karst timestamps fall through to JOVIAN. See
        // MANTLE_CHANGES.md §2.2 for the wider op-revm v19↔v20 reconciliation rule.
        // } else if self.is_karst_active(timestamp) {
        //     op_revm::OpSpecId::KARST
        } else if self.is_jovian_active(timestamp) {
            op_revm::OpSpecId::JOVIAN
        } else if self.is_isthmus_active(timestamp) {
            op_revm::OpSpecId::ISTHMUS
        } else if self.is_holocene_active(timestamp) {
            op_revm::OpSpecId::HOLOCENE
        } else if self.is_fjord_active(timestamp) {
            op_revm::OpSpecId::FJORD
        } else if self.is_ecotone_active(timestamp) {
            op_revm::OpSpecId::ECOTONE
        } else if self.is_canyon_active(timestamp) {
            op_revm::OpSpecId::CANYON
        } else if self.is_regolith_active(timestamp) {
            op_revm::OpSpecId::REGOLITH
        } else {
            op_revm::OpSpecId::BEDROCK
        }
    }

    /// Returns the active [`op_revm::OpSpecId`] for the revm executor.
    ///
    /// This method is specifically for revm EVM execution, determining which
    /// EVM execution rules to use.
    ///
    /// ## Mantle-specific logic:
    /// - Before `mantle_limb`: returns `ISTHMUS`
    /// - `mantle_limb` and after: returns `OSAKA`
    ///
    /// ## Standard OP Stack logic:
    /// Uses the latest active hardfork.
    ///
    /// ## Takes
    /// - `timestamp`: The timestamp of the executing block.
    ///
    /// ## Returns
    /// The active [`op_revm::OpSpecId`] for the revm executor.
    pub fn revm_spec_id(&self, timestamp: u64) -> op_revm::OpSpecId {
        // Special handling for Mantle chains
        if self.is_mantle() {
            return self.mantle_spec_id(timestamp);
        }

        // Standard OP Stack logic
        if self.is_interop_active(timestamp) {
            op_revm::OpSpecId::INTEROP
        } else if self.is_jovian_active(timestamp) {
            op_revm::OpSpecId::JOVIAN
        } else if self.is_isthmus_active(timestamp) {
            op_revm::OpSpecId::ISTHMUS
        } else if self.is_holocene_active(timestamp) {
            op_revm::OpSpecId::HOLOCENE
        } else if self.is_fjord_active(timestamp) {
            op_revm::OpSpecId::FJORD
        } else if self.is_ecotone_active(timestamp) {
            op_revm::OpSpecId::ECOTONE
        } else if self.is_canyon_active(timestamp) {
            op_revm::OpSpecId::CANYON
        } else if self.is_regolith_active(timestamp) {
            op_revm::OpSpecId::REGOLITH
        } else {
            op_revm::OpSpecId::BEDROCK
        }
    }

    /// Returns the active [`op_revm::OpSpecId`] for Mantle chains (revm execution).
    ///
    /// ## Mantle revm logic:
    /// - Before `mantle_limb`: uses `ISTHMUS`
    /// - `mantle_limb` and after: uses `OSAKA`
    fn mantle_spec_id(&self, timestamp: u64) -> op_revm::OpSpecId {
        if self.is_mantle_arsia_active(timestamp) {
            op_revm::OpSpecId::ARSIA
        } else if self.is_mantle_limb_active(timestamp) {
            op_revm::OpSpecId::OSAKA
        } else {
            op_revm::OpSpecId::ISTHMUS
        }
    }
}

impl RollupConfig {
    /// Returns true if Regolith is active at the given timestamp.
    ///
    /// Note: Unlike other hardfork checks, this method does not check mantle_arsia.
    /// For Mantle chains, it returns true if mantle_skadi is active, or if regolith_time
    /// is satisfied (even before mantle_arsia).
    pub fn is_regolith_active(&self, timestamp: u64) -> bool {
        if self.is_mantle() && self.is_mantle_skadi_active(timestamp) {
            return true;
        }
        self.hardforks.regolith_time.is_some_and(|t| timestamp >= t) ||
            self.is_canyon_active(timestamp)
    }

    /// Returns true if the timestamp marks the first Regolith block.
    pub fn is_first_regolith_block(&self, timestamp: u64) -> bool {
        self.is_regolith_active(timestamp) &&
            !self.is_regolith_active(timestamp.saturating_sub(self.block_time))
    }

    /// Returns true if Canyon is active at the given timestamp.
    pub fn is_canyon_active(&self, timestamp: u64) -> bool {
        // Mantle: before mantle_arsia, no advanced OP Stack features are active
        if self.is_mantle() && !self.is_mantle_arsia_active(timestamp) {
            return false;
        }
        self.hardforks.canyon_time.is_some_and(|t| timestamp >= t) ||
            self.is_delta_active(timestamp)
    }

    /// Returns true if the timestamp marks the first Canyon block.
    pub fn is_first_canyon_block(&self, timestamp: u64) -> bool {
        self.is_canyon_active(timestamp) &&
            !self.is_canyon_active(timestamp.saturating_sub(self.block_time))
    }

    /// Returns true if Delta is active at the given timestamp.
    pub fn is_delta_active(&self, timestamp: u64) -> bool {
        // Mantle: before mantle_arsia, no advanced OP Stack features are active
        if self.is_mantle() && !self.is_mantle_arsia_active(timestamp) {
            return false;
        }
        self.hardforks.delta_time.is_some_and(|t| timestamp >= t) ||
            self.is_ecotone_active(timestamp)
    }

    /// Returns true if the timestamp marks the first Delta block.
    pub fn is_first_delta_block(&self, timestamp: u64) -> bool {
        self.is_delta_active(timestamp) &&
            !self.is_delta_active(timestamp.saturating_sub(self.block_time))
    }

    /// Returns true if Ecotone is active at the given timestamp.
    pub fn is_ecotone_active(&self, timestamp: u64) -> bool {
        // Mantle: before mantle_arsia, no advanced OP Stack features are active
        if self.is_mantle() && self.is_mantle_skadi_active(timestamp) {
            return true;
        }
        self.hardforks.ecotone_time.is_some_and(|t| timestamp >= t) ||
            self.is_fjord_active(timestamp)
    }

    /// Returns true if the timestamp marks the first Ecotone block.
    pub fn is_first_ecotone_block(&self, timestamp: u64) -> bool {
        self.is_ecotone_active(timestamp) &&
            !self.is_ecotone_active(timestamp.saturating_sub(self.block_time))
    }

    /// Returns true if Fjord is active at the given timestamp.
    pub fn is_fjord_active(&self, timestamp: u64) -> bool {
        // Mantle: before mantle_arsia, no advanced OP Stack features are active
        if self.is_mantle() && !self.is_mantle_arsia_active(timestamp) {
            return false;
        }
        self.hardforks.fjord_time.is_some_and(|t| timestamp >= t) ||
            self.is_granite_active(timestamp)
    }

    /// Returns true if the timestamp marks the first Fjord block.
    pub fn is_first_fjord_block(&self, timestamp: u64) -> bool {
        self.is_fjord_active(timestamp) &&
            !self.is_fjord_active(timestamp.saturating_sub(self.block_time))
    }

    /// Returns true if Granite is active at the given timestamp.
    pub fn is_granite_active(&self, timestamp: u64) -> bool {
        // Mantle: before mantle_arsia, no advanced OP Stack features are active
        if self.is_mantle() && !self.is_mantle_arsia_active(timestamp) {
            return false;
        }
        self.hardforks.granite_time.is_some_and(|t| timestamp >= t) ||
            self.is_holocene_active(timestamp)
    }

    /// Returns true if the timestamp marks the first Granite block.
    pub fn is_first_granite_block(&self, timestamp: u64) -> bool {
        self.is_granite_active(timestamp) &&
            !self.is_granite_active(timestamp.saturating_sub(self.block_time))
    }

    /// Returns true if Holocene is active at the given timestamp.
    pub fn is_holocene_active(&self, timestamp: u64) -> bool {
        // Mantle: before mantle_arsia, no advanced OP Stack features are active
        if self.is_mantle() && !self.is_mantle_arsia_active(timestamp) {
            return false;
        }
        self.hardforks.holocene_time.is_some_and(|t| timestamp >= t) ||
            self.is_isthmus_active(timestamp)
    }

    /// Returns true if the timestamp marks the first Holocene block.
    pub fn is_first_holocene_block(&self, timestamp: u64) -> bool {
        self.is_holocene_active(timestamp) &&
            !self.is_holocene_active(timestamp.saturating_sub(self.block_time))
    }

    /// Returns true if the pectra blob schedule is active at the given timestamp.
    pub fn is_pectra_blob_schedule_active(&self, timestamp: u64) -> bool {
        self.hardforks.pectra_blob_schedule_time.is_some_and(|t| timestamp >= t)
    }

    /// Returns true if the timestamp marks the first pectra blob schedule block.
    pub fn is_first_pectra_blob_schedule_block(&self, timestamp: u64) -> bool {
        self.is_pectra_blob_schedule_active(timestamp) &&
            !self.is_pectra_blob_schedule_active(timestamp.saturating_sub(self.block_time))
    }

    /// Returns true if Isthmus is active at the given timestamp.
    pub fn is_isthmus_active(&self, timestamp: u64) -> bool {
        if self.is_mantle() && self.is_mantle_skadi_active(timestamp) {
            return true;
        }
        self.hardforks.isthmus_time.is_some_and(|t| timestamp >= t) ||
            self.is_jovian_active(timestamp)
    }

    /// Returns true if the timestamp marks the first Isthmus block.
    pub fn is_first_isthmus_block(&self, timestamp: u64) -> bool {
        self.is_isthmus_active(timestamp) &&
            !self.is_isthmus_active(timestamp.saturating_sub(self.block_time))
    }

    /// Returns true if SDM post-exec transactions are active at the given timestamp.
    ///
    /// SDM is currently unscheduled and must not activate as part of Jovian or Karst.
    #[must_use]
    pub const fn is_sdm_active(&self, _timestamp: u64) -> bool {
        false
    }

    /// Returns true if Jovian is active at the given timestamp.
    pub fn is_jovian_active(&self, timestamp: u64) -> bool {
        self.hardforks.jovian_time.is_some_and(|t| timestamp >= t) ||
            self.is_karst_active(timestamp)
    }

    /// Returns true if the timestamp marks the first Jovian block.
    pub fn is_first_jovian_block(&self, timestamp: u64) -> bool {
        self.is_jovian_active(timestamp) &&
            !self.is_jovian_active(timestamp.saturating_sub(self.block_time))
    }

    /// Returns true if Karst is active at the given timestamp.
    pub fn is_karst_active(&self, timestamp: u64) -> bool {
        self.hardforks.karst_time.is_some_and(|t| timestamp >= t) ||
            self.is_interop_active(timestamp)
    }

    /// Returns true if the timestamp marks the first Karst block.
    pub fn is_first_karst_block(&self, timestamp: u64) -> bool {
        self.is_karst_active(timestamp) &&
            !self.is_karst_active(timestamp.saturating_sub(self.block_time))
    }

    /// Returns true if Interop is active at the given timestamp.
    pub fn is_interop_active(&self, timestamp: u64) -> bool {
        self.hardforks.interop_time.is_some_and(|t| timestamp >= t)
    }

    /// Returns true if the timestamp marks the first Interop block.
    pub fn is_first_interop_block(&self, timestamp: u64) -> bool {
        self.is_interop_active(timestamp) &&
            !self.is_interop_active(timestamp.saturating_sub(self.block_time))
    }

    /// Returns true if Mantle Skadi is active at the given timestamp.
    pub fn is_mantle_skadi_active(&self, timestamp: u64) -> bool {
        self.mantle_hardforks.mantle_skadi_time.is_some_and(|t| timestamp >= t)
    }

    /// Returns true if Mantle Limb is active at the given timestamp.
    pub fn is_mantle_limb_active(&self, timestamp: u64) -> bool {
        self.mantle_hardforks.mantle_limb_time.is_some_and(|t| timestamp >= t)
    }

    /// Returns true if Mantle Arsia is active at the given timestamp.
    pub fn is_mantle_arsia_active(&self, timestamp: u64) -> bool {
        self.mantle_hardforks.mantle_arsia_time.is_some_and(|t| timestamp >= t)
    }

    /// Returns true if the timestamp marks the first Mantle Arsia block.
    pub fn is_first_mantle_arsia_block(&self, timestamp: u64) -> bool {
        self.is_mantle_arsia_active(timestamp) &&
            !self.is_mantle_arsia_active(timestamp.saturating_sub(self.block_time))
    }

    /// Returns true if a DA Challenge proxy Address is provided in the rollup config and the
    /// address is not zero.
    pub fn is_alt_da_enabled(&self) -> bool {
        self.da_challenge_address.is_some_and(|addr| !addr.is_zero())
    }

    /// Returns the max sequencer drift for the given timestamp.
    pub fn max_sequencer_drift(&self, timestamp: u64) -> u64 {
        if self.is_fjord_active(timestamp) {
            #[cfg(feature = "rollup_config_override")]
            return self.fjord_max_sequencer_drift;
            #[cfg(not(feature = "rollup_config_override"))]
            return FJORD_MAX_SEQUENCER_DRIFT;
        }
        self.max_sequencer_drift
    }

    /// Returns the max rlp bytes per channel for the given timestamp.
    pub fn max_rlp_bytes_per_channel(&self, timestamp: u64) -> u64 {
        if self.is_fjord_active(timestamp) {
            MAX_RLP_BYTES_PER_CHANNEL_FJORD
        } else {
            MAX_RLP_BYTES_PER_CHANNEL_BEDROCK
        }
    }

    /// Returns the channel timeout for the given timestamp.
    pub fn channel_timeout(&self, timestamp: u64) -> u64 {
        if self.is_granite_active(timestamp) {
            self.granite_channel_timeout
        } else {
            self.channel_timeout
        }
    }

    /// Returns the [`HardForkConfig`] using [`RollupConfig`] timestamps.
    #[deprecated(since = "0.1.0", note = "Use the `hardforks` field instead.")]
    pub const fn hardfork_config(&self) -> HardForkConfig {
        self.hardforks
    }

    /// Computes a block number from a timestamp, relative to the L2 genesis time and the block
    /// time.
    ///
    /// This function assumes that the timestamp is aligned with the block time, and uses floor
    /// division in its computation.
    pub const fn block_number_from_timestamp(&self, timestamp: u64) -> u64 {
        timestamp.saturating_sub(self.genesis.l2_time).saturating_div(self.block_time)
    }

    /// Checks the scalar value in Ecotone.
    pub fn check_ecotone_l1_system_config_scalar(scalar: [u8; 32]) -> Result<(), &'static str> {
        let version_byte = scalar[0];
        match version_byte {
            0 => {
                if scalar[1..28] != [0; 27] {
                    return Err("Bedrock scalar padding not empty");
                }
                Ok(())
            }
            1 => {
                if scalar[1..24] != [0; 23] {
                    return Err("Invalid version 1 scalar padding");
                }
                Ok(())
            }
            _ => {
                // ignore the event if it's an unknown scalar format
                Err("Unrecognized scalar version")
            }
        }
    }
}

impl EthereumHardforks for RollupConfig {
    fn ethereum_fork_activation(&self, fork: EthereumHardfork) -> ForkCondition {
        if fork <= EthereumHardfork::Berlin {
            // We assume that OP chains were launched with all forks before Berlin activated.
            ForkCondition::Block(0)
        } else if fork <= EthereumHardfork::Paris {
            // Bedrock activates all hardforks up to Paris.
            self.op_fork_activation(OpHardfork::Bedrock)
        } else if fork <= EthereumHardfork::Shanghai {
            // Canyon activates Shanghai hardfork.
            self.op_fork_activation(OpHardfork::Canyon)
        } else if fork <= EthereumHardfork::Cancun {
            // Ecotone activates Cancun hardfork.
            self.op_fork_activation(OpHardfork::Ecotone)
        } else if fork <= EthereumHardfork::Prague {
            // Isthmus activates Prague hardfork.
            self.op_fork_activation(OpHardfork::Isthmus)
        } else {
            ForkCondition::Never
        }
    }
}

impl OpHardforks for RollupConfig {
    fn op_fork_activation(&self, fork: OpHardfork) -> ForkCondition {
        match fork {
            OpHardfork::Bedrock => ForkCondition::Block(0),
            OpHardfork::Regolith => self
                .hardforks
                .regolith_time
                .map(ForkCondition::Timestamp)
                .unwrap_or_else(|| self.op_fork_activation(OpHardfork::Canyon)),
            OpHardfork::Canyon => self
                .hardforks
                .canyon_time
                .map(ForkCondition::Timestamp)
                .unwrap_or_else(|| self.op_fork_activation(OpHardfork::Ecotone)),
            OpHardfork::Ecotone => self
                .hardforks
                .ecotone_time
                .map(ForkCondition::Timestamp)
                .unwrap_or_else(|| self.op_fork_activation(OpHardfork::Fjord)),
            OpHardfork::Fjord => self
                .hardforks
                .fjord_time
                .map(ForkCondition::Timestamp)
                .unwrap_or_else(|| self.op_fork_activation(OpHardfork::Granite)),
            OpHardfork::Granite => self
                .hardforks
                .granite_time
                .map(ForkCondition::Timestamp)
                .unwrap_or_else(|| self.op_fork_activation(OpHardfork::Holocene)),
            OpHardfork::Holocene => self
                .hardforks
                .holocene_time
                .map(ForkCondition::Timestamp)
                .unwrap_or_else(|| self.op_fork_activation(OpHardfork::Isthmus)),
            OpHardfork::Isthmus => self
                .hardforks
                .isthmus_time
                .map(ForkCondition::Timestamp)
                .unwrap_or_else(|| self.op_fork_activation(OpHardfork::Jovian)),
            OpHardfork::Jovian => self
                .hardforks
                .jovian_time
                .map(ForkCondition::Timestamp)
                .unwrap_or_else(|| self.op_fork_activation(OpHardfork::Karst)),
            OpHardfork::Karst => self
                .hardforks
                .karst_time
                .map(ForkCondition::Timestamp)
                .unwrap_or_else(|| self.op_fork_activation(OpHardfork::Interop)),
            OpHardfork::Interop => self
                .hardforks
                .interop_time
                .map(ForkCondition::Timestamp)
                .unwrap_or(ForkCondition::Never),
            _ => ForkCondition::Never,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[cfg(feature = "serde")]
    use alloy_eips::BlockNumHash;
    use alloy_primitives::address;
    #[cfg(feature = "serde")]
    use alloy_primitives::{U256, b256};

    #[test]
    #[cfg(feature = "arbitrary")]
    fn test_arbitrary_rollup_config() {
        use arbitrary::Arbitrary;
        use rand::Rng;
        let mut bytes = [0u8; 1024];
        rand::rng().fill(bytes.as_mut_slice());
        RollupConfig::arbitrary(&mut arbitrary::Unstructured::new(&bytes)).unwrap();
    }

    #[test]
    #[cfg(feature = "revm")]
    fn test_spec_id() {
        // Test standard OP Stack spec_id (used by kona protocol logic)
        let mut config = RollupConfig {
            hardforks: HardForkConfig { regolith_time: Some(10), ..Default::default() },
            ..Default::default()
        };
        assert_eq!(config.spec_id(0), op_revm::OpSpecId::BEDROCK);
        assert_eq!(config.spec_id(10), op_revm::OpSpecId::REGOLITH);
        config.hardforks.canyon_time = Some(20);
        assert_eq!(config.spec_id(20), op_revm::OpSpecId::CANYON);
        config.hardforks.ecotone_time = Some(30);
        assert_eq!(config.spec_id(30), op_revm::OpSpecId::ECOTONE);
        config.hardforks.fjord_time = Some(40);
        assert_eq!(config.spec_id(40), op_revm::OpSpecId::FJORD);
        config.hardforks.holocene_time = Some(50);
        assert_eq!(config.spec_id(50), op_revm::OpSpecId::HOLOCENE);
        config.hardforks.isthmus_time = Some(60);
        assert_eq!(config.spec_id(60), op_revm::OpSpecId::ISTHMUS);
        config.hardforks.jovian_time = Some(70);
        assert_eq!(config.spec_id(70), op_revm::OpSpecId::JOVIAN);
        config.hardforks.karst_time = Some(80);
        assert_eq!(config.spec_id(80), op_revm::OpSpecId::KARST);
        config.hardforks.interop_time = Some(90);
        assert_eq!(config.spec_id(90), op_revm::OpSpecId::INTEROP);
    }

    #[test]
    #[cfg(feature = "revm")]
    fn test_revm_spec_id() {
        // Test standard OP Stack revm_spec_id (used by revm executor)
        let mut config = RollupConfig {
            hardforks: HardForkConfig { regolith_time: Some(10), ..Default::default() },
            ..Default::default()
        };
        assert_eq!(config.revm_spec_id(0), op_revm::OpSpecId::BEDROCK);
        assert_eq!(config.revm_spec_id(10), op_revm::OpSpecId::REGOLITH);
        config.hardforks.canyon_time = Some(20);
        assert_eq!(config.revm_spec_id(20), op_revm::OpSpecId::CANYON);
        config.hardforks.ecotone_time = Some(30);
        assert_eq!(config.revm_spec_id(30), op_revm::OpSpecId::ECOTONE);
        config.hardforks.fjord_time = Some(40);
        assert_eq!(config.revm_spec_id(40), op_revm::OpSpecId::FJORD);
        config.hardforks.holocene_time = Some(50);
        assert_eq!(config.revm_spec_id(50), op_revm::OpSpecId::HOLOCENE);
        config.hardforks.isthmus_time = Some(60);
        assert_eq!(config.revm_spec_id(60), op_revm::OpSpecId::ISTHMUS);
    }

    #[test]
    #[cfg(feature = "revm")]
    fn test_mantle_spec_id() {
        // Test Mantle spec_id (kona protocol logic)
        let config = RollupConfig {
            hardforks: HardForkConfig {
                regolith_time: Some(10),
                canyon_time: Some(20),
                ecotone_time: Some(30),
                fjord_time: Some(40),
                holocene_time: Some(50),
                isthmus_time: Some(60),
                ..Default::default()
            },
            mantle_hardforks: MantleHardForkConfig {
                mantle_limb_time: Some(100),
                mantle_arsia_time: Some(200),
                ..Default::default()
            },
            ..Default::default()
        };

        // Before mantle_arsia: should use BEDROCK for kona features
        // Note: is_regolith_active checks regolith_time even before mantle_arsia,
        // and ecotone/isthmus check hardforks times when mantle_skadi is not active,
        // so we need to use timestamps before regolith_time and their hardfork times
        assert_eq!(config.spec_id(0), op_revm::OpSpecId::BEDROCK);
        assert_eq!(config.spec_id(5), op_revm::OpSpecId::BEDROCK); // Before regolith_time (10)

        // After hardforks times but before mantle_arsia: ecotone and isthmus may be active
        // based on their hardforks times (no arsia gate on ecotone/isthmus directly)
        assert_eq!(config.spec_id(50), op_revm::OpSpecId::ECOTONE); // ecotone_time is 30
        assert_eq!(config.spec_id(150), op_revm::OpSpecId::ISTHMUS); // isthmus_time is 60

        // At and after mantle_arsia: should use standard OP Stack logic
        assert_eq!(config.spec_id(200), op_revm::OpSpecId::ISTHMUS);
        assert_eq!(config.spec_id(300), op_revm::OpSpecId::ISTHMUS);
    }

    #[test]
    #[cfg(feature = "revm")]
    fn test_mantle_revm_spec_id() {
        // Test Mantle revm_spec_id (revm executor logic)
        let config = RollupConfig {
            mantle_hardforks: MantleHardForkConfig {
                mantle_limb_time: Some(100),
                mantle_arsia_time: Some(200),
                ..Default::default()
            },
            ..Default::default()
        };

        // Before mantle_limb: should use ISTHMUS
        assert_eq!(config.revm_spec_id(0), op_revm::OpSpecId::ISTHMUS);
        assert_eq!(config.revm_spec_id(50), op_revm::OpSpecId::ISTHMUS);
        assert_eq!(config.revm_spec_id(99), op_revm::OpSpecId::ISTHMUS);

        // At and after mantle_limb but before mantle_arsia: should use OSAKA
        assert_eq!(config.revm_spec_id(100), op_revm::OpSpecId::OSAKA);
        assert_eq!(config.revm_spec_id(150), op_revm::OpSpecId::OSAKA);

        // After mantle_arsia: should use ARSIA (mantle_spec_id checks arsia first)
        assert_eq!(config.revm_spec_id(200), op_revm::OpSpecId::ARSIA);
        assert_eq!(config.revm_spec_id(300), op_revm::OpSpecId::ARSIA);
    }

    #[test]
    #[cfg(feature = "revm")]
    fn test_mantle_is_active_methods() {
        // Test that is_xxx_active() methods return false before mantle_arsia
        // (except ecotone and isthmus which are active when mantle_skadi is active)
        let config = RollupConfig {
            hardforks: HardForkConfig {
                regolith_time: Some(10),
                canyon_time: Some(20),
                ecotone_time: Some(30),
                fjord_time: Some(40),
                holocene_time: Some(50),
                isthmus_time: Some(60),
                ..Default::default()
            },
            mantle_hardforks: MantleHardForkConfig {
                mantle_limb_time: Some(100),
                mantle_arsia_time: Some(200),
                ..Default::default()
            },
            ..Default::default()
        };

        // Before mantle_arsia and without mantle_skadi: ecotone and isthmus are active
        // based on their hardfork times (they don't have arsia gate directly).
        // Canyon/Fjord/Granite/Holocene are arsia-gated and remain inactive.
        assert!(config.is_regolith_active(150)); // regolith_time is 10, so active at 150
        assert!(!config.is_canyon_active(150));
        assert!(config.is_ecotone_active(150)); // ecotone_time is 30
        assert!(!config.is_fjord_active(150));
        assert!(!config.is_holocene_active(150));
        assert!(config.is_isthmus_active(150)); // isthmus_time is 60

        // Test with mantle_skadi active
        let config_with_skadi = RollupConfig {
            hardforks: HardForkConfig {
                regolith_time: Some(10),
                canyon_time: Some(20),
                ecotone_time: Some(30),
                fjord_time: Some(40),
                holocene_time: Some(50),
                isthmus_time: Some(60),
                ..Default::default()
            },
            mantle_hardforks: MantleHardForkConfig {
                mantle_skadi_time: Some(100),
                mantle_limb_time: Some(100),
                mantle_arsia_time: Some(200),
                ..Default::default()
            },
            ..Default::default()
        };

        // Before mantle_skadi: ecotone active via ecotone_time, isthmus inactive (isthmus_time=60 > 25)
        assert!(!config_with_skadi.is_ecotone_active(25)); // ecotone_time is 30, so not active at 25
        assert!(!config_with_skadi.is_isthmus_active(25));

        // Before mantle_skadi but after ecotone_time: ecotone active via hardfork time
        assert!(config_with_skadi.is_ecotone_active(50)); // ecotone_time is 30
        assert!(!config_with_skadi.is_isthmus_active(50)); // isthmus_time is 60, so still inactive

        // After mantle_skadi: ecotone and isthmus active via skadi
        assert!(config_with_skadi.is_regolith_active(150));
        assert!(!config_with_skadi.is_canyon_active(150));
        assert!(config_with_skadi.is_ecotone_active(150)); // skadi activates ecotone
        assert!(!config_with_skadi.is_fjord_active(150));
        assert!(!config_with_skadi.is_holocene_active(150));
        assert!(config_with_skadi.is_isthmus_active(150)); // skadi activates isthmus

        // After mantle_arsia: OP Stack features should be active based on their times
        assert!(config.is_regolith_active(250));
        assert!(config.is_canyon_active(250));
        assert!(config.is_ecotone_active(250));
        assert!(config.is_fjord_active(250));
        assert!(config.is_holocene_active(250));
        assert!(config.is_isthmus_active(250));

        // Non-Mantle chain should work normally
        let op_config = RollupConfig {
            hardforks: HardForkConfig {
                regolith_time: Some(10),
                canyon_time: Some(20),
                ecotone_time: Some(30),
                ..Default::default()
            },
            ..Default::default()
        };
        assert!(op_config.is_regolith_active(15));
        assert!(op_config.is_canyon_active(25));
        assert!(op_config.is_ecotone_active(35));
    }

    #[test]
    #[cfg(feature = "revm")]
    fn test_is_mantle() {
        // Test with Mantle hardforks configured
        let config_with_mantle = RollupConfig {
            mantle_hardforks: MantleHardForkConfig {
                mantle_limb_time: Some(100),
                ..Default::default()
            },
            ..Default::default()
        };
        assert!(config_with_mantle.is_mantle());

        // Test with multiple Mantle hardforks
        let config_with_multiple = RollupConfig {
            mantle_hardforks: MantleHardForkConfig {
                mantle_base_fee_time: Some(50),
                mantle_limb_time: Some(100),
                mantle_arsia_time: Some(200),
                ..Default::default()
            },
            ..Default::default()
        };
        assert!(config_with_multiple.is_mantle());

        // Test with no Mantle hardforks (standard OP Stack)
        let config_without_mantle = RollupConfig {
            hardforks: HardForkConfig {
                ecotone_time: Some(100),
                fjord_time: Some(200),
                ..Default::default()
            },
            ..Default::default()
        };
        assert!(!config_without_mantle.is_mantle());

        // Test default config (no hardforks)
        let default_config = RollupConfig::default();
        assert!(!default_config.is_mantle());

        // Test with only mantle_arsia_time
        let config_arsia_only = RollupConfig {
            mantle_hardforks: MantleHardForkConfig {
                mantle_arsia_time: Some(500),
                ..Default::default()
            },
            ..Default::default()
        };
        assert!(config_arsia_only.is_mantle());
    }

    #[test]
    fn test_regolith_active() {
        let mut config = RollupConfig::default();
        assert!(!config.is_regolith_active(0));
        config.hardforks.regolith_time = Some(10);
        assert!(config.is_regolith_active(10));
        assert!(!config.is_regolith_active(9));
    }

    #[test]
    fn test_canyon_active() {
        let mut config = RollupConfig::default();
        assert!(!config.is_canyon_active(0));
        config.hardforks.canyon_time = Some(10);
        assert!(config.is_regolith_active(10));
        assert!(config.is_canyon_active(10));
        assert!(!config.is_canyon_active(9));
    }

    #[test]
    fn test_delta_active() {
        let mut config = RollupConfig::default();
        assert!(!config.is_delta_active(0));
        config.hardforks.delta_time = Some(10);
        assert!(config.is_regolith_active(10));
        assert!(config.is_canyon_active(10));
        assert!(config.is_delta_active(10));
        assert!(!config.is_delta_active(9));
    }

    #[test]
    fn test_ecotone_active() {
        let mut config = RollupConfig::default();
        assert!(!config.is_ecotone_active(0));
        config.hardforks.ecotone_time = Some(10);
        assert!(config.is_regolith_active(10));
        assert!(config.is_canyon_active(10));
        assert!(config.is_delta_active(10));
        assert!(config.is_ecotone_active(10));
        assert!(!config.is_ecotone_active(9));
    }

    #[test]
    fn test_fjord_active() {
        let mut config = RollupConfig::default();
        assert!(!config.is_fjord_active(0));
        config.hardforks.fjord_time = Some(10);
        assert!(config.is_regolith_active(10));
        assert!(config.is_canyon_active(10));
        assert!(config.is_delta_active(10));
        assert!(config.is_ecotone_active(10));
        assert!(config.is_fjord_active(10));
        assert!(!config.is_fjord_active(9));
    }

    #[test]
    fn test_granite_active() {
        let mut config = RollupConfig::default();
        assert!(!config.is_granite_active(0));
        config.hardforks.granite_time = Some(10);
        assert!(config.is_regolith_active(10));
        assert!(config.is_canyon_active(10));
        assert!(config.is_delta_active(10));
        assert!(config.is_ecotone_active(10));
        assert!(config.is_fjord_active(10));
        assert!(config.is_granite_active(10));
        assert!(!config.is_granite_active(9));
    }

    #[test]
    fn test_holocene_active() {
        let mut config = RollupConfig::default();
        assert!(!config.is_holocene_active(0));
        config.hardforks.holocene_time = Some(10);
        assert!(config.is_regolith_active(10));
        assert!(config.is_canyon_active(10));
        assert!(config.is_delta_active(10));
        assert!(config.is_ecotone_active(10));
        assert!(config.is_fjord_active(10));
        assert!(config.is_granite_active(10));
        assert!(config.is_holocene_active(10));
        assert!(!config.is_holocene_active(9));
    }

    #[test]
    fn test_pectra_blob_schedule_active() {
        let mut config = RollupConfig::default();
        config.hardforks.pectra_blob_schedule_time = Some(10);
        // Pectra blob schedule is a unique fork, not included in the hierarchical ordering. Its
        // activation does not imply the activation of any other forks.
        assert!(!config.is_regolith_active(10));
        assert!(!config.is_canyon_active(10));
        assert!(!config.is_delta_active(10));
        assert!(!config.is_ecotone_active(10));
        assert!(!config.is_fjord_active(10));
        assert!(!config.is_granite_active(10));
        assert!(!config.is_holocene_active(0));
        assert!(config.is_pectra_blob_schedule_active(10));
        assert!(!config.is_pectra_blob_schedule_active(9));
    }

    #[test]
    fn test_isthmus_active() {
        let mut config = RollupConfig::default();
        assert!(!config.is_isthmus_active(0));
        config.hardforks.isthmus_time = Some(10);
        assert!(config.is_regolith_active(10));
        assert!(config.is_canyon_active(10));
        assert!(config.is_delta_active(10));
        assert!(config.is_ecotone_active(10));
        assert!(config.is_fjord_active(10));
        assert!(config.is_granite_active(10));
        assert!(config.is_holocene_active(10));
        assert!(!config.is_pectra_blob_schedule_active(10));
        assert!(config.is_isthmus_active(10));
        assert!(!config.is_isthmus_active(9));
    }

    #[test]
    fn test_jovian_active() {
        let mut config = RollupConfig::default();
        assert!(!config.is_interop_active(0));
        config.hardforks.jovian_time = Some(10);
        assert!(config.is_regolith_active(10));
        assert!(config.is_canyon_active(10));
        assert!(config.is_delta_active(10));
        assert!(config.is_ecotone_active(10));
        assert!(config.is_fjord_active(10));
        assert!(config.is_granite_active(10));
        assert!(config.is_holocene_active(10));
        assert!(!config.is_pectra_blob_schedule_active(10));
        assert!(config.is_isthmus_active(10));
        assert!(config.is_jovian_active(10));
        assert!(!config.is_jovian_active(9));
    }

    #[test]
    fn test_karst_active() {
        let mut config = RollupConfig::default();
        assert!(!config.is_karst_active(0));
        config.hardforks.karst_time = Some(10);
        assert!(config.is_regolith_active(10));
        assert!(config.is_canyon_active(10));
        assert!(config.is_delta_active(10));
        assert!(config.is_ecotone_active(10));
        assert!(config.is_fjord_active(10));
        assert!(config.is_granite_active(10));
        assert!(config.is_holocene_active(10));
        assert!(!config.is_pectra_blob_schedule_active(10));
        assert!(config.is_isthmus_active(10));
        assert!(config.is_jovian_active(10));
        assert!(config.is_karst_active(10));
        assert!(!config.is_karst_active(9));
    }

    #[test]
    fn test_sdm_disabled_after_jovian_and_karst() {
        let mut config = RollupConfig::default();
        config.hardforks.jovian_time = Some(10);
        config.hardforks.karst_time = Some(20);

        assert!(config.is_jovian_active(10));
        assert!(!config.is_sdm_active(10));
        assert!(config.is_karst_active(20));
        assert!(!config.is_sdm_active(20));
    }

    #[test]
    fn test_interop_active() {
        let mut config = RollupConfig::default();
        assert!(!config.is_interop_active(0));
        config.hardforks.interop_time = Some(10);
        assert!(config.is_regolith_active(10));
        assert!(config.is_canyon_active(10));
        assert!(config.is_delta_active(10));
        assert!(config.is_ecotone_active(10));
        assert!(config.is_fjord_active(10));
        assert!(config.is_granite_active(10));
        assert!(config.is_holocene_active(10));
        assert!(!config.is_pectra_blob_schedule_active(10));
        assert!(config.is_isthmus_active(10));
        assert!(config.is_karst_active(10));
        assert!(config.is_interop_active(10));
        assert!(!config.is_interop_active(9));
    }

    #[test]
    fn test_is_first_fork_block() {
        let cfg = RollupConfig {
            hardforks: HardForkConfig {
                regolith_time: Some(10),
                canyon_time: Some(20),
                delta_time: Some(30),
                ecotone_time: Some(40),
                fjord_time: Some(50),
                granite_time: Some(60),
                holocene_time: Some(70),
                pectra_blob_schedule_time: Some(80),
                isthmus_time: Some(90),
                jovian_time: Some(100),
                karst_time: Some(110),
                interop_time: Some(120),
            },
            block_time: 2,
            ..Default::default()
        };

        // Regolith
        assert!(!cfg.is_first_regolith_block(8));
        assert!(cfg.is_first_regolith_block(10));
        assert!(!cfg.is_first_regolith_block(12));

        // Canyon
        assert!(!cfg.is_first_canyon_block(18));
        assert!(cfg.is_first_canyon_block(20));
        assert!(!cfg.is_first_canyon_block(22));

        // Delta
        assert!(!cfg.is_first_delta_block(28));
        assert!(cfg.is_first_delta_block(30));
        assert!(!cfg.is_first_delta_block(32));

        // Ecotone
        assert!(!cfg.is_first_ecotone_block(38));
        assert!(cfg.is_first_ecotone_block(40));
        assert!(!cfg.is_first_ecotone_block(42));

        // Fjord
        assert!(!cfg.is_first_fjord_block(48));
        assert!(cfg.is_first_fjord_block(50));
        assert!(!cfg.is_first_fjord_block(52));

        // Granite
        assert!(!cfg.is_first_granite_block(58));
        assert!(cfg.is_first_granite_block(60));
        assert!(!cfg.is_first_granite_block(62));

        // Holocene
        assert!(!cfg.is_first_holocene_block(68));
        assert!(cfg.is_first_holocene_block(70));
        assert!(!cfg.is_first_holocene_block(72));

        // Pectra blob schedule
        assert!(!cfg.is_first_pectra_blob_schedule_block(78));
        assert!(cfg.is_first_pectra_blob_schedule_block(80));
        assert!(!cfg.is_first_pectra_blob_schedule_block(82));

        // Isthmus
        assert!(!cfg.is_first_isthmus_block(88));
        assert!(cfg.is_first_isthmus_block(90));
        assert!(!cfg.is_first_isthmus_block(92));

        // Jovian
        assert!(!cfg.is_first_jovian_block(98));
        assert!(cfg.is_first_jovian_block(100));
        assert!(!cfg.is_first_jovian_block(102));

        // Karst
        assert!(!cfg.is_first_karst_block(108));
        assert!(cfg.is_first_karst_block(110));
        assert!(!cfg.is_first_karst_block(112));

        // Interop
        assert!(!cfg.is_first_interop_block(118));
        assert!(cfg.is_first_interop_block(120));
        assert!(!cfg.is_first_interop_block(122));
    }

    #[test]
    fn test_alt_da_enabled() {
        let mut config = RollupConfig::default();
        assert!(!config.is_alt_da_enabled());
        config.da_challenge_address = Some(Address::ZERO);
        assert!(!config.is_alt_da_enabled());
        config.da_challenge_address = Some(address!("0000000000000000000000000000000000000001"));
        assert!(config.is_alt_da_enabled());
    }

    #[test]
    fn test_granite_channel_timeout() {
        let mut config = RollupConfig {
            channel_timeout: 100,
            hardforks: HardForkConfig { granite_time: Some(10), ..Default::default() },
            ..Default::default()
        };
        assert_eq!(config.channel_timeout(0), 100);
        assert_eq!(config.channel_timeout(10), GRANITE_CHANNEL_TIMEOUT);
        config.hardforks.granite_time = None;
        assert_eq!(config.channel_timeout(10), 100);
    }

    #[test]
    fn test_max_sequencer_drift() {
        let mut config = RollupConfig { max_sequencer_drift: 100, ..Default::default() };
        assert_eq!(config.max_sequencer_drift(0), 100);
        config.hardforks.fjord_time = Some(10);
        assert_eq!(config.max_sequencer_drift(0), 100);
        assert_eq!(config.max_sequencer_drift(10), FJORD_MAX_SEQUENCER_DRIFT);
    }

    fn expected_rollup_config() -> RollupConfig {
        use crate::{OP_MAINNET_BASE_FEE_CONFIG, SystemConfig};
        RollupConfig {
            genesis: ChainGenesis {
                l1: BlockNumHash {
                    hash: b256!("481724ee99b1f4cb71d826e2ec5a37265f460e9b112315665c977f4050b0af54"),
                    number: 10,
                },
                l2: BlockNumHash {
                    hash: b256!("88aedfbf7dea6bfa2c4ff315784ad1a7f145d8f650969359c003bbed68c87631"),
                    number: 0,
                },
                l2_time: 1725557164,
                system_config: Some(SystemConfig {
                    batcher_address: address!("c81f87a644b41e49b3221f41251f15c6cb00ce03"),
                    overhead: U256::ZERO,
                    scalar: U256::from(0xf4240),
                    gas_limit: 30_000_000,
                    base_fee: None,
                    base_fee_scalar: Some(1234),
                    blob_base_fee_scalar: Some(5678),
                    eip1559_denominator: Some(10),
                    eip1559_elasticity: Some(20),
                    operator_fee_scalar: Some(30),
                    operator_fee_constant: Some(40),
                    min_base_fee: Some(50),
                    da_footprint_gas_scalar: Some(10),
                }),
            },
            block_time: 2,
            max_sequencer_drift: 600,
            seq_window_size: 3600,
            channel_timeout: 300,
            granite_channel_timeout: GRANITE_CHANNEL_TIMEOUT,
            #[cfg(feature = "rollup_config_override")]
            fjord_max_sequencer_drift: FJORD_MAX_SEQUENCER_DRIFT,
            l1_chain_id: 3151908,
            l2_chain_id: Chain::from_id(1337),
            hardforks: HardForkConfig {
                regolith_time: Some(0),
                canyon_time: Some(0),
                delta_time: Some(0),
                ecotone_time: Some(0),
                fjord_time: Some(0),
                ..Default::default()
            },
            mantle_hardforks: MantleHardForkConfig::default(),
            batch_inbox_address: address!("ff00000000000000000000000000000000042069"),
            deposit_contract_address: address!("08073dc48dde578137b8af042bcbc1c2491f1eb2"),
            l1_system_config_address: address!("94ee52a9d8edd72a85dea7fae3ba6d75e4bf1710"),
            superchain_config_address: None,
            blobs_enabled_l1_timestamp: None,
            da_challenge_address: None,
            chain_op_config: OP_MAINNET_BASE_FEE_CONFIG,
            alt_da_config: None,
        }
    }

    #[test]
    #[cfg(feature = "serde")]
    fn test_deserialize_reference_rollup_config() {
        let raw: &str = r#"
        {
          "genesis": {
            "l1": {
              "hash": "0x481724ee99b1f4cb71d826e2ec5a37265f460e9b112315665c977f4050b0af54",
              "number": 10
            },
            "l2": {
              "hash": "0x88aedfbf7dea6bfa2c4ff315784ad1a7f145d8f650969359c003bbed68c87631",
              "number": 0
            },
            "l2_time": 1725557164,
            "system_config": {
              "batcherAddr": "0xc81f87a644b41e49b3221f41251f15c6cb00ce03",
              "overhead": "0x0000000000000000000000000000000000000000000000000000000000000000",
              "scalar": "0x00000000000000000000000000000000000000000000000000000000000f4240",
              "gasLimit": 30000000,
              "baseFeeScalar": 1234,
              "blobBaseFeeScalar": 5678,
              "eip1559Denominator": 10,
              "eip1559Elasticity": 20,
              "operatorFeeScalar": 30,
              "operatorFeeConstant": 40,
              "minBaseFee": 50,
              "daFootprintGasScalar": 10
            }
          },
          "block_time": 2,
          "max_sequencer_drift": 600,
          "seq_window_size": 3600,
          "channel_timeout": 300,
          "l1_chain_id": 3151908,
          "l2_chain_id": 1337,
          "regolith_time": 0,
          "canyon_time": 0,
          "delta_time": 0,
          "ecotone_time": 0,
          "fjord_time": 0,
          "batch_inbox_address": "0xff00000000000000000000000000000000042069",
          "deposit_contract_address": "0x08073dc48dde578137b8af042bcbc1c2491f1eb2",
          "l1_system_config_address": "0x94ee52a9d8edd72a85dea7fae3ba6d75e4bf1710",
          "chain_op_config": {
            "eip1559Elasticity": 6,
            "eip1559Denominator": 50,
            "eip1559DenominatorCanyon": 250
            },
          "alt_da": null
        }
        "#;

        let expected = expected_rollup_config();
        let deserialized: RollupConfig = serde_json::from_str(raw).unwrap();
        assert_eq!(deserialized, expected);
    }

    #[test]
    fn test_rollup_config_unknown_field() {
        let raw: &str = r#"
        {
          "genesis": {
            "l1": {
              "hash": "0x481724ee99b1f4cb71d826e2ec5a37265f460e9b112315665c977f4050b0af54",
              "number": 10
            },
            "l2": {
              "hash": "0x88aedfbf7dea6bfa2c4ff315784ad1a7f145d8f650969359c003bbed68c87631",
              "number": 0
            },
            "l2_time": 1725557164,
            "system_config": {
              "batcherAddr": "0xc81f87a644b41e49b3221f41251f15c6cb00ce03",
              "overhead": "0x0000000000000000000000000000000000000000000000000000000000000000",
              "scalar": "0x00000000000000000000000000000000000000000000000000000000000f4240",
              "gasLimit": 30000000,
              "baseFeeScalar": 1234,
              "blobBaseFeeScalar": 5678,
              "eip1559Denominator": 10,
              "eip1559Elasticity": 20,
              "operatorFeeScalar": 30,
              "operatorFeeConstant": 40,
              "minBaseFee": 50,
              "daFootprintGasScalar": 10
            }
          },
          "block_time": 2,
          "max_sequencer_drift": 600,
          "seq_window_size": 3600,
          "channel_timeout": 300,
          "l1_chain_id": 3151908,
          "l2_chain_id": 1337,
          "regolith_time": 0,
          "canyon_time": 0,
          "delta_time": 0,
          "ecotone_time": 0,
          "fjord_time": 0,
          "batch_inbox_address": "0xff00000000000000000000000000000000042069",
          "deposit_contract_address": "0x08073dc48dde578137b8af042bcbc1c2491f1eb2",
          "l1_system_config_address": "0x94ee52a9d8edd72a85dea7fae3ba6d75e4bf1710",
          "chain_op_config": {
            "eip1559_elasticity": 6,
            "eip1559_denominator": 50,
            "eip1559_denominator_canyon": 250
          },
          "unknown_field": "unknown"
        }
        "#;

        let expected = expected_rollup_config();
        let deserialized: RollupConfig = serde_json::from_str(raw).unwrap();
        assert_eq!(deserialized, expected);
    }

    #[test]
    fn test_compute_block_number_from_time() {
        let cfg = RollupConfig {
            genesis: ChainGenesis { l2_time: 10, ..Default::default() },
            block_time: 2,
            ..Default::default()
        };

        assert_eq!(cfg.block_number_from_timestamp(20), 5);
        assert_eq!(cfg.block_number_from_timestamp(30), 10);
    }

    #[cfg(feature = "rollup_config_override")]
    mod rollup_config_override_tests {
        use super::*;

        #[test]
        fn test_max_sequencer_drift_override() {
            let mut config = RollupConfig {
                max_sequencer_drift: 100,
                fjord_max_sequencer_drift: 2892,
                hardforks: HardForkConfig { fjord_time: Some(10), ..Default::default() },
                ..Default::default()
            };
            assert_eq!(config.max_sequencer_drift(0), 100);
            assert_eq!(config.max_sequencer_drift(10), 2892);
            config.fjord_max_sequencer_drift = 3600;
            assert_eq!(config.max_sequencer_drift(10), 3600);
        }

        #[test]
        #[cfg(feature = "serde")]
        fn test_serde_fjord_max_sequencer_drift_override() {
            // Default value survives round-trip.
            let config = RollupConfig::default();
            assert_eq!(config.fjord_max_sequencer_drift, FJORD_MAX_SEQUENCER_DRIFT);
            let serialized = serde_json::to_string(&config).unwrap();
            let deserialized: RollupConfig = serde_json::from_str(&serialized).unwrap();
            assert_eq!(deserialized.fjord_max_sequencer_drift, FJORD_MAX_SEQUENCER_DRIFT);

            // Custom value survives round-trip.
            let mut config = config;
            config.fjord_max_sequencer_drift = 2892;
            let serialized = serde_json::to_string(&config).unwrap();
            let deserialized: RollupConfig = serde_json::from_str(&serialized).unwrap();
            assert_eq!(deserialized.fjord_max_sequencer_drift, 2892);
        }
    }
}
