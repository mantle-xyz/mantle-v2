//! Rollup Config Types

use crate::{
    AltDAConfig, BaseFeeConfig, ChainGenesis, HardForkConfig, MANTLE_BASE_FEE_CONFIG,
    MantleForkOrderError, MantleHardForkConfig,
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

/// `[MANTLE]` Default base fee config for serde when `chain_op_config` is missing — uses the
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
    /// checking the `chain_id`. This approach is more flexible and works for:
    /// - Mantle Mainnet (`chain_id` 5000)
    /// - Mantle Sepolia (`chain_id` 5003)
    /// - Custom Mantle testnets with different `chain_ids`
    /// - Any chain that adopts Mantle hardforks
    #[inline]
    pub const fn is_mantle(&self) -> bool {
        self.mantle_hardforks.has_any_hardfork()
    }
}

impl RollupConfig {
    /// Returns true if Regolith is active at the given timestamp.
    ///
    /// Note: Unlike other hardfork checks, this method does not check `mantle_arsia`.
    /// For Mantle chains, it returns true if `mantle_skadi` is active, or if `regolith_time`
    /// is satisfied (even before `mantle_arsia`).
    pub fn is_regolith_active(&self, timestamp: u64) -> bool {
        // [MANTLE] No Mantle gate: op-node does not realign Regolith, and Mantle configs give it
        // genesis offset 0 (op-chain-ops/genesis/mantle_config.go), so the config value governs.
        self.hardforks.regolith_time.is_some_and(|t| timestamp >= t) ||
            self.is_canyon_active(timestamp)
    }

    /// Returns true if the timestamp marks the first Regolith block.
    pub fn is_first_regolith_block(&self, timestamp: u64) -> bool {
        self.is_regolith_active(timestamp) &&
            !self.is_regolith_active(timestamp.saturating_sub(self.block_time))
    }
    /// `[MANTLE]` Activation of an OP hardfork on a Mantle chain.
    ///
    /// op-node forces **every** OP fork from Canyon through Jovian to activate exactly at
    /// `mantle_arsia_time`, and does so twice: `op-chain-ops/genesis/mantle_config.go` writes
    /// `CanyonTime = EcotoneTime = MantleArsiaTime` into the generated configs, and
    /// `op-node/rollup/mantle_types.go::AlignOpWithMantle` overwrites
    /// Canyon/Delta/Ecotone/Fjord/Granite/Holocene/Isthmus/Jovian at load time — **ignoring
    /// whatever the rollup JSON says**. Mirroring that here is what keeps derivation aligned;
    /// honouring the per-fork timestamps instead would diverge on any config where they differ.
    ///
    /// Returns `None` for non-Mantle chains, where the standard OP schedule applies.
    ///
    /// Regolith is deliberately **not** included: `AlignOpWithMantle` leaves it alone and
    /// `mantle_config.go` gives it offset 0, so it is simply active from genesis.
    fn mantle_op_fork_active(&self, timestamp: u64) -> Option<bool> {
        self.is_mantle().then(|| self.is_mantle_arsia_active(timestamp))
    }

    /// Returns true if Canyon is active at the given timestamp.
    pub fn is_canyon_active(&self, timestamp: u64) -> bool {
        if let Some(active) = self.mantle_op_fork_active(timestamp) {
            return active;
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
        if let Some(active) = self.mantle_op_fork_active(timestamp) {
            return active;
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
        if let Some(active) = self.mantle_op_fork_active(timestamp) {
            return active;
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
        if let Some(active) = self.mantle_op_fork_active(timestamp) {
            return active;
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
        if let Some(active) = self.mantle_op_fork_active(timestamp) {
            return active;
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
        if let Some(active) = self.mantle_op_fork_active(timestamp) {
            return active;
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
        if let Some(active) = self.mantle_op_fork_active(timestamp) {
            return active;
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
    /// Defers to the hardfork where SDM is activated, matching op-node's `IsSDM`.
    #[must_use]
    pub fn is_sdm_active(&self, timestamp: u64) -> bool {
        self.is_lagoon_active(timestamp)
    }

    /// Returns true if Jovian is active at the given timestamp.
    pub fn is_jovian_active(&self, timestamp: u64) -> bool {
        if let Some(active) = self.mantle_op_fork_active(timestamp) {
            return active;
        }
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
            self.is_lagoon_active(timestamp)
    }

    /// Returns true if the timestamp marks the first Karst block.
    pub fn is_first_karst_block(&self, timestamp: u64) -> bool {
        self.is_karst_active(timestamp) &&
            !self.is_karst_active(timestamp.saturating_sub(self.block_time))
    }

    /// Returns true if Lagoon is active at the given timestamp.
    pub fn is_lagoon_active(&self, timestamp: u64) -> bool {
        self.hardforks.lagoon_time.is_some_and(|t| timestamp >= t)
    }

    /// Returns true if `timestamp` is `fork`'s activation block — `fork` is active at `timestamp`
    /// but was not active at the previous block. Mirrors op-node's `IsActivationBlockForFork`.
    pub fn is_fork_activation_block(&self, fork: OpHardfork, timestamp: u64) -> bool {
        let activation = self.op_fork_activation(fork);
        activation.active_at_timestamp(timestamp) &&
            !activation.active_at_timestamp(timestamp.saturating_sub(self.block_time))
    }

    /// Returns true if the timestamp marks the first Lagoon block.
    pub fn is_first_lagoon_block(&self, timestamp: u64) -> bool {
        self.is_lagoon_active(timestamp) &&
            !self.is_lagoon_active(timestamp.saturating_sub(self.block_time))
    }

    /// Returns true if the interop feature is active at the given timestamp.
    ///
    /// Defers to the hardfork where interop is activated, but kept as a separate feature gate —
    /// mirroring op-node's `IsInterop` — so interop can diverge from the fork if its activation is
    /// ever decoupled. Interop-feature code should gate on this, not on the raw fork accessor.
    pub fn is_interop_active(&self, timestamp: u64) -> bool {
        self.is_lagoon_active(timestamp)
    }

    /// Returns true if the timestamp marks the first interop-active block.
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

    /// `[MANTLE]` Validates the Mantle hardfork schedule, as op-node does at startup.
    ///
    /// See [`MantleHardForkConfig::check_fork_order`]. Call this wherever a `RollupConfig` is
    /// loaded from an untrusted source (a rollup.json on disk); configs built in code or taken
    /// from the registry are covered by tests instead.
    pub fn check_mantle_fork_order(&self) -> Result<(), MantleForkOrderError> {
        self.mantle_hardforks.check_fork_order()
    }

    /// Returns true if Mantle Elysium is active at the given timestamp.
    pub fn is_mantle_elysium_active(&self, timestamp: u64) -> bool {
        self.mantle_hardforks.mantle_elysium_time.is_some_and(|t| timestamp >= t)
    }

    /// Returns true if the timestamp marks the first Mantle Elysium block.
    pub fn is_first_mantle_elysium_block(&self, timestamp: u64) -> bool {
        self.is_mantle_elysium_active(timestamp) &&
            !self.is_mantle_elysium_active(timestamp.saturating_sub(self.block_time))
    }

    /// `[MANTLE]` Whether the L1-info `BlobBaseFee` must be computed with the Arsia-era pinned
    /// blob schedule instead of L1's real one.
    ///
    /// op-node: `isMantleArsiaButNotFirstBlock(cfg, t) && !isMantleElysiumButNotFirstBlock(cfg, t)`
    /// (`derive/l1_block_info.go:508`). Both halves use the "active, but not on the activation
    /// block itself" form, because the L1-info transaction is emitted *before* the fork's upgrade
    /// transactions run.
    ///
    /// Note this says nothing about which L1 chain the pin applies to — op-node's
    /// `MantleArsiaL1ChainConfigByChainID` returns a config for Ethereum mainnet only and `nil`
    /// everywhere else, so on Sepolia the pin is a no-op. That part is enforced at the call site.
    pub fn is_mantle_arsia_blob_schedule_pinned(&self, timestamp: u64) -> bool {
        let arsia =
            self.is_mantle_arsia_active(timestamp) && !self.is_first_mantle_arsia_block(timestamp);
        let elysium = self.is_mantle_elysium_active(timestamp) &&
            !self.is_first_mantle_elysium_block(timestamp);
        arsia && !elysium
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

    /// Computes the absolute L2 block number that a timestamp falls in.
    ///
    /// The computation uses floor division. A timestamp between two blocks therefore resolves
    /// to the earlier block.
    pub const fn block_number_from_timestamp(&self, timestamp: u64) -> u64 {
        self.genesis.l2.number.saturating_add(
            timestamp.saturating_sub(self.genesis.l2_time).saturating_div(self.block_time),
        )
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
        } else {
            // Every later L1 fork activates with the OP fork that implies it (Bedrock for
            // London through Paris); L1 forks without an L2 equivalent never activate.
            OpHardfork::activating_op_fork(fork)
                .map(|op_fork| self.op_fork_activation(op_fork))
                .unwrap_or(ForkCondition::Never)
        }
    }
}

impl RollupConfig {
    /// `[MANTLE]` `ForkCondition` counterpart of [`Self::mantle_op_fork_active`], for the
    /// `OpHardforks` trait path.
    ///
    /// Without this, a Mantle chain has **two disagreeing views of the same fork**: the inherent
    /// `is_*_active` methods honour the Arsia alignment while `op_fork_activation` — and
    /// therefore every `OpHardforks` / `EthereumHardforks` trait predicate built on it — reads
    /// the raw per-fork timestamps that `AlignOpWithMantle` overwrites in op-node. Any consumer
    /// reaching a Mantle config through the trait would then resolve a different fork than
    /// derivation does.
    ///
    /// Returns `None` for non-Mantle chains.
    fn mantle_op_fork_condition(&self) -> Option<ForkCondition> {
        self.is_mantle().then(|| {
            self.mantle_hardforks
                .mantle_arsia_time
                .map(ForkCondition::Timestamp)
                .unwrap_or(ForkCondition::Never)
        })
    }
}

impl OpHardforks for RollupConfig {
    fn op_fork_activation(&self, fork: OpHardfork) -> ForkCondition {
        // [MANTLE] Canyon..Jovian are exactly the forks `AlignOpWithMantle` overwrites with
        // `MantleArsiaTime`. Bedrock and Regolith are left alone (Regolith is active from
        // genesis on Mantle), and Karst/Lagoon are newer than the alignment list, so Mantle
        // configs leave them unset and they fall through to the standard schedule.
        if matches!(
            fork,
            OpHardfork::Canyon |
                OpHardfork::Ecotone |
                OpHardfork::Fjord |
                OpHardfork::Granite |
                OpHardfork::Holocene |
                OpHardfork::Isthmus |
                OpHardfork::Jovian
        ) && let Some(condition) = self.mantle_op_fork_condition()
        {
            return condition;
        }

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
                .unwrap_or_else(|| self.op_fork_activation(OpHardfork::Lagoon)),
            OpHardfork::Lagoon => self
                .hardforks
                .lagoon_time
                .map(ForkCondition::Timestamp)
                .unwrap_or(ForkCondition::Never),
            _ => ForkCondition::Never,
        }
    }

    // [MANTLE] Wire the Mantle predicates into the `OpHardforks` trait.
    //
    // The trait's defaults all return `false`, so without these overrides any consumer that
    // takes `impl OpHardforks` — notably `alloy_op_evm::spec_by_timestamp_after_bedrock`, which
    // `evm_env_for_op_next_block` calls — would classify a Mantle `RollupConfig` as a plain OP
    // chain and resolve JOVIAN where ARSIA is required. That is a consensus divergence.
    //
    // Before the v1.7.0 sync this could not bite: kona's executor built its `CfgEnv` by calling
    // `RollupConfig::revm_spec_id` directly. v1.7.0 replaced that with
    // `evm_env_for_op_next_block(.., self.config, ..)`, which routes through the trait instead.
    // These overrides delegate to the inherent methods, so both paths agree by construction.
    fn is_mantle(&self) -> bool {
        // Deliberately duplicates the one-line body of the inherent `RollupConfig::is_mantle`
        // rather than calling it. `Self::is_mantle(self)` would resolve to the inherent method
        // today (inherent impls win path resolution), but it silently becomes unbounded
        // recursion if that method is ever removed or renamed — a stack overflow inside the
        // fault-proof program. Keep both bodies in sync.
        self.mantle_hardforks.has_any_hardfork()
    }

    fn is_mantle_skadi_active_at_timestamp(&self, timestamp: u64) -> bool {
        self.is_mantle_skadi_active(timestamp)
    }

    fn is_mantle_limb_active_at_timestamp(&self, timestamp: u64) -> bool {
        self.is_mantle_limb_active(timestamp)
    }

    fn is_mantle_arsia_active_at_timestamp(&self, timestamp: u64) -> bool {
        self.is_mantle_arsia_active(timestamp)
    }

    fn is_mantle_elysium_active_at_timestamp(&self, timestamp: u64) -> bool {
        self.is_mantle_elysium_active(timestamp)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
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

    /// `[MANTLE]` The inherent `is_*_active` methods and the `OpHardforks` / `EthereumHardforks`
    /// trait predicates must never disagree.
    ///
    /// They are two independent code paths over the same config: the inherent ones route
    /// through `mantle_op_fork_active`, the trait ones through `op_fork_activation`. Derivation
    /// uses the first, `spec_by_timestamp_after_bedrock` and the engine-version selectors use
    /// the second. Before `mantle_op_fork_condition` existed, the trait path read the raw
    /// per-fork timestamps that `AlignOpWithMantle` overwrites — so a config whose
    /// `ecotone_time` differed from `mantle_arsia_time` resolved two different forks at once.
    #[test]
    fn test_mantle_inherent_and_trait_fork_predicates_agree() {
        // Deliberately hostile: every OP fork timestamp disagrees with `mantle_arsia_time`,
        // which is exactly what `AlignOpWithMantle` discards.
        let config = RollupConfig {
            hardforks: HardForkConfig {
                regolith_time: Some(0),
                canyon_time: Some(10),
                delta_time: Some(11),
                ecotone_time: Some(12),
                fjord_time: Some(13),
                granite_time: Some(14),
                holocene_time: Some(15),
                isthmus_time: Some(16),
                jovian_time: Some(17),
                ..Default::default()
            },
            mantle_hardforks: MantleHardForkConfig {
                mantle_arsia_time: Some(100),
                ..MantleHardForkConfig::NONE
            },
            ..Default::default()
        };

        for ts in [0, 9, 10, 12, 16, 17, 50, 99, 100, 101, 1_000] {
            assert_eq!(
                config.is_canyon_active(ts),
                config.is_canyon_active_at_timestamp(ts),
                "canyon disagrees at {ts}",
            );
            assert_eq!(
                config.is_ecotone_active(ts),
                config.is_ecotone_active_at_timestamp(ts),
                "ecotone disagrees at {ts}",
            );
            assert_eq!(
                config.is_isthmus_active(ts),
                config.is_isthmus_active_at_timestamp(ts),
                "isthmus disagrees at {ts}",
            );
            assert_eq!(
                config.is_jovian_active(ts),
                config.is_jovian_active_at_timestamp(ts),
                "jovian disagrees at {ts}",
            );
        }

        // And the alignment is real, not both-paths-wrong-the-same-way: the raw config says
        // Ecotone at 12, the aligned answer is Arsia at 100.
        assert!(!config.is_ecotone_active(99));
        assert!(config.is_ecotone_active(100));

        // The L1 fork mapping follows: Cancun rides Ecotone, Prague rides Isthmus.
        assert!(!config.is_cancun_active_at_timestamp(99));
        assert!(config.is_cancun_active_at_timestamp(100));
        assert!(!config.is_prague_active_at_timestamp(99));
        assert!(config.is_prague_active_at_timestamp(100));

        // Non-Mantle chains keep the standard per-fork schedule.
        let op_config = RollupConfig {
            hardforks: config.hardforks,
            mantle_hardforks: MantleHardForkConfig::NONE,
            ..Default::default()
        };
        assert!(op_config.is_ecotone_active(12));
        assert!(op_config.is_ecotone_active_at_timestamp(12));
    }

    #[test]
    fn test_mantle_is_active_methods() {
        // [MANTLE] On a Mantle chain every OP fork from Canyon through Jovian activates exactly
        // at `mantle_arsia_time`, whatever the per-fork timestamps say — see
        // `mantle_op_fork_active`. Regolith is the one exception: op-node leaves it alone.
        //
        // This test previously asserted the opposite for Ecotone/Isthmus (active from Skadi),
        // which put the chain in the impossible state "Ecotone active, Canyon inactive" and
        // diverged from op-node for the whole Skadi..Arsia window (~8 months on mainnet).
        let config = RollupConfig {
            hardforks: HardForkConfig {
                regolith_time: Some(10),
                canyon_time: Some(20),
                ecotone_time: Some(30),
                fjord_time: Some(40),
                holocene_time: Some(50),
                isthmus_time: Some(60),
                jovian_time: Some(70),
                ..Default::default()
            },
            mantle_hardforks: MantleHardForkConfig {
                mantle_skadi_time: Some(100),
                mantle_limb_time: Some(150),
                mantle_arsia_time: Some(200),
                ..Default::default()
            },
            ..Default::default()
        };

        // Regolith follows its own timestamp, on Mantle as elsewhere.
        assert!(config.is_regolith_active(10));
        assert!(!config.is_regolith_active(9));

        // Pre-Arsia — including the whole Skadi..Arsia window — every aligned fork is inactive,
        // even though each one's own timestamp has long passed.
        for t in [99, 100, 150, 199] {
            assert!(!config.is_canyon_active(t), "canyon must be inactive pre-Arsia at {t}");
            assert!(!config.is_delta_active(t), "delta must be inactive pre-Arsia at {t}");
            assert!(!config.is_ecotone_active(t), "ecotone must be inactive pre-Arsia at {t}");
            assert!(!config.is_fjord_active(t), "fjord must be inactive pre-Arsia at {t}");
            assert!(!config.is_granite_active(t), "granite must be inactive pre-Arsia at {t}");
            assert!(!config.is_holocene_active(t), "holocene must be inactive pre-Arsia at {t}");
            assert!(!config.is_isthmus_active(t), "isthmus must be inactive pre-Arsia at {t}");
            assert!(!config.is_jovian_active(t), "jovian must be inactive pre-Arsia at {t}");
        }

        // At and after Arsia they all switch on together.
        for t in [200, 250] {
            assert!(config.is_canyon_active(t));
            assert!(config.is_delta_active(t));
            assert!(config.is_ecotone_active(t));
            assert!(config.is_fjord_active(t));
            assert!(config.is_granite_active(t));
            assert!(config.is_holocene_active(t));
            assert!(config.is_isthmus_active(t));
            assert!(config.is_jovian_active(t));
        }

        // The alignment keys off `mantle_arsia_time`, not the per-fork timestamps: a Mantle
        // chain whose Arsia is far in the future keeps them all off regardless.
        let late_arsia = RollupConfig {
            mantle_hardforks: MantleHardForkConfig {
                mantle_arsia_time: Some(1_000),
                ..config.mantle_hardforks
            },
            ..config
        };
        assert!(!late_arsia.is_ecotone_active(999));
        assert!(late_arsia.is_ecotone_active(1_000));

        // Non-Mantle chains are untouched by any of this.
        let op_config = RollupConfig {
            hardforks: HardForkConfig {
                regolith_time: Some(10),
                canyon_time: Some(20),
                ecotone_time: Some(30),
                ..Default::default()
            },
            ..Default::default()
        };
        assert!(!op_config.is_mantle());
        assert!(op_config.is_regolith_active(15));
        assert!(op_config.is_canyon_active(25));
        assert!(op_config.is_ecotone_active(35));
    }

    #[test]
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
        assert!(!config.is_lagoon_active(0));
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
    fn test_lagoon_active() {
        let mut config = RollupConfig::default();
        assert!(!config.is_lagoon_active(0));
        config.hardforks.lagoon_time = Some(10);
        assert!(config.is_lagoon_active(10));
        assert!(!config.is_lagoon_active(9));
    }

    #[test]
    fn test_first_lagoon_block() {
        let mut config = RollupConfig { block_time: 2, ..Default::default() };
        config.hardforks.lagoon_time = Some(120);
        assert!(!config.is_first_lagoon_block(118));
        assert!(config.is_first_lagoon_block(120));
        assert!(!config.is_first_lagoon_block(122));
    }

    #[test]
    fn test_interop_feature_tracks_lagoon() {
        // The interop feature gate rides Lagoon today.
        let mut config = RollupConfig { block_time: 2, ..Default::default() };
        config.hardforks.lagoon_time = Some(120);
        assert_eq!(config.is_interop_active(119), config.is_lagoon_active(119));
        assert_eq!(config.is_interop_active(120), config.is_lagoon_active(120));
        assert!(config.is_first_interop_block(120));
        assert!(!config.is_first_interop_block(122));
    }

    #[test]
    fn test_sdm_rides_lagoon() {
        let mut config = RollupConfig::default();
        // Jovian/Karst alone must not activate SDM — only Lagoon does.
        config.hardforks.jovian_time = Some(10);
        config.hardforks.karst_time = Some(20);
        assert!(config.is_jovian_active(10));
        assert!(!config.is_sdm_active(10));
        assert!(config.is_karst_active(20));
        assert!(!config.is_sdm_active(20));

        // Schedule Lagoon and SDM must follow.
        config.hardforks.lagoon_time = Some(30);
        assert!(!config.is_sdm_active(29));
        assert!(config.is_sdm_active(30));
        assert!(config.is_sdm_active(31));
    }

    #[test]
    fn test_lagoon_stacks_prior_forks() {
        let mut config = RollupConfig::default();
        assert!(!config.is_lagoon_active(0));
        config.hardforks.lagoon_time = Some(10);
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
        assert!(config.is_lagoon_active(10));
        assert!(!config.is_lagoon_active(9));
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
                keep_karst_upgrade_gas: false,
                lagoon_time: Some(120),
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

        // Lagoon
        assert!(!cfg.is_first_lagoon_block(118));
        assert!(cfg.is_first_lagoon_block(120));
        assert!(!cfg.is_first_lagoon_block(122));
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

    #[test]
    fn test_compute_block_number_from_time_non_zero_genesis() {
        // OP Mainnet, whose L2 genesis is the last block of the legacy OVM chain.
        let cfg = RollupConfig {
            genesis: ChainGenesis {
                l2: BlockNumHash { number: 105235063, ..Default::default() },
                l2_time: 1686068903,
                ..Default::default()
            },
            block_time: 2,
            ..Default::default()
        };

        assert_eq!(cfg.block_number_from_timestamp(1686068903), 105235063);
        assert_eq!(cfg.block_number_from_timestamp(1686068905), 105235064);
        // 1788303126 falls between two blocks.
        assert_eq!(cfg.block_number_from_timestamp(1788303126), 156352174);
        assert_eq!(cfg.block_number_from_timestamp(1788303127), 156352175);
        // A timestamp before genesis clamps to the genesis block.
        assert_eq!(cfg.block_number_from_timestamp(0), 105235063);
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
