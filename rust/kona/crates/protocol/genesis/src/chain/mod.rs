//! Module containing the chain config.

/// OP Mainnet chain ID.
pub const OP_MAINNET_CHAIN_ID: u64 = 10;

/// OP Sepolia chain ID.
pub const OP_SEPOLIA_CHAIN_ID: u64 = 11155420;

/// Base Mainnet chain ID.
pub const BASE_MAINNET_CHAIN_ID: u64 = 8453;

/// Base Sepolia chain ID.
pub const BASE_SEPOLIA_CHAIN_ID: u64 = 84532;

/// `[MANTLE]` Ethereum L1 mainnet chain ID.
///
/// op-node's `MantleArsiaL1ChainConfigByChainID` overrides the blob schedule for this L1 and
/// returns `nil` for every other, so the Arsia-era pin is scoped by this constant.
pub const ETHEREUM_MAINNET_CHAIN_ID: u64 = 1;

/// `[MANTLE]` Mantle Mainnet chain ID.
pub const MANTLE_MAINNET_CHAIN_ID: u64 = 5000;

/// `[MANTLE]` Mantle Sepolia chain ID.
pub const MANTLE_SEPOLIA_CHAIN_ID: u64 = 5003;

mod addresses;
pub use addresses::AddressList;

mod config;
pub use config::{ChainConfig, L1ChainConfig};

mod altda;
pub use altda::AltDAConfig;

mod hardfork;
pub use hardfork::HardForkConfig;

// [MANTLE] Mantle-specific hardfork configuration registered on RollupConfig.
mod mantle_hardfork;
pub use mantle_hardfork::{MantleForkOrderError, MantleHardForkConfig};

mod roles;
pub use roles::Roles;
