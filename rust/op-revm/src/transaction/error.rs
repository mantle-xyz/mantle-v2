//! Contains the `[OpTransactionError]` type.
use core::fmt::Display;
use revm::context_interface::{
    result::{EVMError, InvalidTransaction},
    transaction::TransactionError,
};
use std::string::{String, ToString};

/// Optimism transaction validation error.
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
#[cfg_attr(feature = "serde", derive(serde::Serialize, serde::Deserialize))]
pub enum OpTransactionError {
    /// Base transaction error.
    Base(InvalidTransaction),
    /// System transactions are not supported post-regolith hardfork.
    ///
    /// Before the Regolith hardfork, there was a special field in the `Deposit` transaction
    /// type that differentiated between `system` and `user` deposit transactions. This field
    /// was deprecated in the Regolith hardfork, and this error is thrown if a `Deposit` transaction
    /// is found with this field set to `true` after the hardfork activation.
    ///
    /// In addition, this error is internal, and bubbles up into an
    /// [`OpHaltReason::FailedDeposit`][crate::OpHaltReason::FailedDeposit] error in the `revm`
    /// handler for the consumer to easily handle. This is due to a state transition rule on OP
    /// Stack chains where, if for any reason a deposit transaction fails, the transaction
    /// must still be included in the block, the sender nonce is bumped, the `mint` value persists,
    /// and special gas accounting rules are applied. Normally on L1, [`EVMError::Transaction`]
    /// errors are cause for non-inclusion, so a special [`OpHaltReason`][crate::OpHaltReason]
    /// variant was introduced to handle this case for failed deposit transactions.
    DepositSystemTxPostRegolith,
    /// Deposit transaction halts bubble up to the global main return handler, wiping state and
    /// only increasing the nonce + persisting the mint value.
    ///
    /// This is a catch-all error for any deposit transaction that results in an
    /// [`OpHaltReason`][crate::OpHaltReason] error post-regolith hardfork. This allows for a
    /// consumer to easily handle special cases where a deposit transaction fails during
    /// validation, but must still be included in the block.
    ///
    /// In addition, this error is internal, and bubbles up into an
    /// [`OpHaltReason::FailedDeposit`][crate::OpHaltReason::FailedDeposit] error in the `revm`
    /// handler for the consumer to easily handle. This is due to a state transition rule on OP
    /// Stack chains where, if for any reason a deposit transaction fails, the transaction
    /// must still be included in the block, the sender nonce is bumped, the `mint` value persists,
    /// and special gas accounting rules are applied. Normally on L1, [`EVMError::Transaction`]
    /// errors are cause for non-inclusion, so a special [`OpHaltReason`][crate::OpHaltReason]
    /// variant was introduced to handle this case for failed deposit transactions.
    HaltedDepositPostRegolith,
    /// BVM ETH operation errors
    BvmEth(BvmEthError),
    /// Missing enveloped transaction bytes for non-deposit transaction.
    ///
    /// Non-deposit transactions on Optimism must have `enveloped_tx` field set
    /// to properly calculate L1 costs.
    MissingEnvelopedTx,
    /// Computed L1 cost cannot be represented in runtime gas arithmetic.
    TxL1CostOutOfRange,
}

impl TransactionError for OpTransactionError {}

impl Display for OpTransactionError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        match self {
            Self::Base(error) => error.fmt(f),
            Self::DepositSystemTxPostRegolith => {
                write!(f, "deposit system transactions post regolith hardfork are not supported")
            }
            Self::HaltedDepositPostRegolith => {
                write!(
                    f,
                    "deposit transaction halted post-regolith; error will be bubbled up to main return handler"
                )
            }
            Self::BvmEth(error) => error.fmt(f),
            Self::MissingEnvelopedTx => {
                write!(f, "missing enveloped transaction bytes for non-deposit transaction")
            }
            Self::TxL1CostOutOfRange => {
                write!(f, "tx l1 cost is out of range for u64 gas arithmetic")
            }
        }
    }
}

impl core::error::Error for OpTransactionError {}

impl From<InvalidTransaction> for OpTransactionError {
    fn from(value: InvalidTransaction) -> Self {
        Self::Base(value)
    }
}

impl<DBError> From<OpTransactionError> for EVMError<DBError, OpTransactionError> {
    fn from(value: OpTransactionError) -> Self {
        Self::Transaction(value)
    }
}
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
#[cfg_attr(feature = "serde", derive(serde::Serialize, serde::Deserialize))]
/// BVM ETH operation errors
pub enum BvmEthError {
    /// EthTxValueTooLarge means that the eth tx value is too large.
    EthTxValueTooLarge,
    /// NonceOverflow means that the nonce overflow.
    NonceOverflow,
    /// DBError means that the database error.
    DBError(String),
    /// InsufficientFunds means that the insufficient BVM ETH funds.
    InsufficientFunds,
}

impl TransactionError for BvmEthError {}

impl Display for BvmEthError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        match self {
            Self::EthTxValueTooLarge => write!(f, "eth tx value is too large"),
            Self::NonceOverflow => write!(f, "nonce overflow"),
            Self::DBError(error) => write!(f, "database error: {}", error),
            Self::InsufficientFunds => write!(f, "insufficient BVM ETH funds"),
        }
    }
}

impl core::error::Error for BvmEthError {}

impl From<BvmEthError> for OpTransactionError {
    fn from(value: BvmEthError) -> Self {
        Self::BvmEth(value)
    }
}

impl<DBError> From<BvmEthError> for EVMError<DBError, OpTransactionError> {
    fn from(value: BvmEthError) -> Self {
        Self::Transaction(OpTransactionError::BvmEth(value))
    }
}

/// Convert a database error to a BVM ETH operation error
pub fn db_error<E: Display>(error: E) -> OpTransactionError {
    OpTransactionError::BvmEth(BvmEthError::DBError(error.to_string()))
}

#[cfg(test)]
mod test {
    use super::*;
    use std::string::ToString;

    #[test]
    fn test_display_op_errors() {
        assert_eq!(
            OpTransactionError::Base(InvalidTransaction::NonceTooHigh { tx: 2, state: 1 })
                .to_string(),
            "nonce 2 too high, expected 1"
        );
        assert_eq!(
            OpTransactionError::DepositSystemTxPostRegolith.to_string(),
            "deposit system transactions post regolith hardfork are not supported"
        );
        assert_eq!(
            OpTransactionError::HaltedDepositPostRegolith.to_string(),
            "deposit transaction halted post-regolith; error will be bubbled up to main return handler"
        );
        assert_eq!(
            OpTransactionError::MissingEnvelopedTx.to_string(),
            "missing enveloped transaction bytes for non-deposit transaction"
        );
        assert_eq!(
            OpTransactionError::TxL1CostOutOfRange.to_string(),
            "tx l1 cost is out of range for u64 gas arithmetic"
        );
    }

    #[cfg(feature = "serde")]
    #[test]
    fn test_serialize_json_op_transaction_error() {
        let response = r#""DepositSystemTxPostRegolith""#;

        let op_transaction_error: OpTransactionError = serde_json::from_str(response).unwrap();
        assert_eq!(op_transaction_error, OpTransactionError::DepositSystemTxPostRegolith);
    }
}
