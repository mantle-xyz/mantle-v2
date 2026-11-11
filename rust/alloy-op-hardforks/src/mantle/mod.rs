//! Mantle hardfork starting points

/// Mantle L2 block time in seconds. Only used by `HoodiBvmUpgrade` to detect its activation block
/// as "active at the block timestamp but not one block time earlier"; block production follows the
/// rollup config's `block_time`. Must equal op-geth's `params.MantleBlockTime`.
pub const MANTLE_BLOCK_TIME: u64 = 2;

pub mod mainnet;
pub use mainnet::*;
pub mod sepolia;
pub use sepolia::*;
pub mod hoodi;
pub use hoodi::*;
