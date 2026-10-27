//! Mantle Hoodi chain constants

/// Mantle Hoodi chain ID.
///
/// Mantle Hoodi's mainline fork times come from its genesis and rollup configuration. Only
/// `HoodiBvmUpgrade` has a bundled activation time, see [`MANTLE_HOODI_BVM_UPGRADE_TIMESTAMP`].
pub const MANTLE_HOODI_CHAIN_ID: u64 = 50002;

/// `HoodiBvmUpgrade` activation timestamp for Mantle Hoodi. `None` until scheduled.
///
/// Bundled into op-reth's built-in `mantle-hoodi` chain spec. Must equal op-geth's
/// `MantleHoodiUpgradeConfig.MantleHoodiBvmUpgradeTime`, which op-node also reads.
pub const MANTLE_HOODI_BVM_UPGRADE_TIMESTAMP: Option<u64> = None;
