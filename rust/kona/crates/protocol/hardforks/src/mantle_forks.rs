//! Contains Mantle-specific hardforks.

use crate::Arsia;

/// Mantle-specific Hardforks
///
/// This type is used to encapsulate Mantle-specific hardfork transactions.
/// It exposes methods that return hardfork upgrade transactions
/// as [`alloy_primitives::Bytes`].
///
/// # Example
///
/// Build arsia hardfork upgrade transaction:
/// ```rust
/// use kona_hardforks::{Hardfork, MantleHardforks};
/// let arsia_upgrade_tx = MantleHardforks::ARSIA.txs();
/// assert_eq!(arsia_upgrade_tx.collect::<Vec<_>>().len(), 7);
/// ```
#[derive(Debug, Default, Clone, Copy)]
#[non_exhaustive]
pub struct MantleHardforks;

impl MantleHardforks {
    /// The Arsia hardfork upgrade transactions.
    pub const ARSIA: Arsia = Arsia;
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::Hardfork;
    use alloc::vec::Vec;

    #[test]
    fn test_mantle_hardforks() {
        let arsia_upgrade_tx = MantleHardforks::ARSIA.txs();
        assert_eq!(arsia_upgrade_tx.collect::<Vec<_>>().len(), 7);
    }
}
