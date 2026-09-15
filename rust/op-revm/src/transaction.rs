//! Contains the `[OpTransaction]` type and its implementation.
pub mod abstraction;
pub mod bvm_eth;
pub mod deposit;
pub mod error;

pub use abstraction::{OpTransaction, OpTxTr};
pub use error::OpTransactionError;

use crate::fast_lz::flz_compress_len;

/// <https://github.com/ethereum-optimism/op-geth/blob/647c346e2bef36219cc7b47d76b1cb87e7ca29e4/core/types/rollup_cost.go#L79>
const L1_COST_FASTLZ_COEF: u64 = 836_500;

/// <https://github.com/ethereum-optimism/op-geth/blob/647c346e2bef36219cc7b47d76b1cb87e7ca29e4/core/types/rollup_cost.go#L78>
/// Inverted to be used with `saturating_sub`.
const L1_COST_INTERCEPT: u64 = 42_585_600;

/// <https://github.com/ethereum-optimism/op-geth/blob/647c346e2bef36219cc7b47d76b1cb87e7ca29e4/core/types/rollup_cost.go#82>
const MIN_TX_SIZE_SCALED: u64 = 100 * 1_000_000;

/// Estimates the compressed size of a transaction.
pub fn estimate_tx_compressed_size(input: &[u8]) -> u64 {
    estimate_tx_compressed_size_with_delta(input, 0)
}

/// Like [`estimate_tx_compressed_size`] but adds `fastlz_delta` to the compressed length
/// before applying the formula (e.g. +80 for signature overhead to align with geth).
pub fn estimate_tx_compressed_size_with_delta(input: &[u8], fastlz_delta: u64) -> u64 {
    let fastlz_size = flz_compress_len(input) as u64;
    let fastlz_size = fastlz_size.saturating_add(fastlz_delta);

    fastlz_size
        .saturating_mul(L1_COST_FASTLZ_COEF)
        .saturating_sub(L1_COST_INTERCEPT)
        .max(MIN_TX_SIZE_SCALED)
}
