//! The base fee update type.

use alloy_primitives::{LogData, U256};
use alloy_sol_types::{SolType, sol};

use crate::{SystemConfig, SystemConfigLog, system::BaseFeeUpdateError};

/// The base fee update type.
#[derive(Debug, Default, Clone, Hash, PartialEq, Eq)]
#[cfg_attr(feature = "serde", derive(serde::Serialize, serde::Deserialize))]
pub struct BaseFeeUpdate {
    /// The base fee value.
    pub base_fee: U256,
}

impl BaseFeeUpdate {
    /// Applies the update to the [`SystemConfig`].
    pub const fn apply(&self, config: &mut SystemConfig) {
        config.base_fee = Some(self.base_fee);
    }
}

impl TryFrom<&SystemConfigLog> for BaseFeeUpdate {
    type Error = BaseFeeUpdateError;

    fn try_from(log: &SystemConfigLog) -> Result<Self, Self::Error> {
        let LogData { data, .. } = &log.log.data;
        if data.len() != 96 {
            return Err(BaseFeeUpdateError::InvalidDataLen(data.len()));
        }

        let Ok(pointer) = <sol!(uint64)>::abi_decode_validate(&data[0..32]) else {
            return Err(BaseFeeUpdateError::PointerDecodingError);
        };
        if pointer != 32 {
            return Err(BaseFeeUpdateError::InvalidDataPointer(pointer));
        }

        let Ok(length) = <sol!(uint64)>::abi_decode_validate(&data[32..64]) else {
            return Err(BaseFeeUpdateError::LengthDecodingError);
        };
        if length != 32 {
            return Err(BaseFeeUpdateError::InvalidDataLength(length));
        }

        let Ok(base_fee) = <sol!(uint256)>::abi_decode_validate(&data[64..96]) else {
            return Err(BaseFeeUpdateError::BaseFeeDecodingError);
        };

        Ok(Self { base_fee })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{CONFIG_UPDATE_EVENT_VERSION_0, CONFIG_UPDATE_TOPIC};
    use alloc::vec;
    use alloy_primitives::{Address, B256, Bytes, Log, LogData, U256, hex};

    #[test]
    fn test_base_fee_update_try_from() {
        let update_type = B256::ZERO;

        let log = Log {
            address: Address::ZERO,
            data: LogData::new_unchecked(
                vec![
                    CONFIG_UPDATE_TOPIC,
                    CONFIG_UPDATE_EVENT_VERSION_0,
                    update_type,
                ],
                hex!("000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000000200000000000000000000000000000000000000000000000000000000000000064").into()
            )
        };

        let system_log = SystemConfigLog::new(log, false);
        let update = BaseFeeUpdate::try_from(&system_log).unwrap();

        assert_eq!(update.base_fee, U256::from(100));
    }

    #[test]
    fn test_base_fee_update_invalid_data_len() {
        let log =
            Log { address: Address::ZERO, data: LogData::new_unchecked(vec![], Bytes::default()) };
        let system_log = SystemConfigLog::new(log, false);
        let err = BaseFeeUpdate::try_from(&system_log).unwrap_err();
        assert_eq!(err, BaseFeeUpdateError::InvalidDataLen(0));
    }

    #[test]
    fn test_base_fee_update_pointer_decoding_error() {
        let log = Log {
            address: Address::ZERO,
            data: LogData::new_unchecked(
                vec![
                    CONFIG_UPDATE_TOPIC,
                    CONFIG_UPDATE_EVENT_VERSION_0,
                    B256::ZERO,
                ],
                hex!("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF00000000000000000000000000000000000000000000000000000000000000200000000000000000000000000000000000000000000000000000000000000064").into()
            )
        };

        let system_log = SystemConfigLog::new(log, false);
        let err = BaseFeeUpdate::try_from(&system_log).unwrap_err();
        assert_eq!(err, BaseFeeUpdateError::PointerDecodingError);
    }

    #[test]
    fn test_base_fee_update_invalid_pointer_length() {
        let log = Log {
            address: Address::ZERO,
            data: LogData::new_unchecked(
                vec![
                    CONFIG_UPDATE_TOPIC,
                    CONFIG_UPDATE_EVENT_VERSION_0,
                    B256::ZERO,
                ],
                hex!("000000000000000000000000000000000000000000000000000000000000002100000000000000000000000000000000000000000000000000000000000000200000000000000000000000000000000000000000000000000000000000000064").into()
            )
        };

        let system_log = SystemConfigLog::new(log, false);
        let err = BaseFeeUpdate::try_from(&system_log).unwrap_err();
        assert_eq!(err, BaseFeeUpdateError::InvalidDataPointer(33));
    }

    #[test]
    fn test_base_fee_update_length_decoding_error() {
        let log = Log {
            address: Address::ZERO,
            data: LogData::new_unchecked(
                vec![
                    CONFIG_UPDATE_TOPIC,
                    CONFIG_UPDATE_EVENT_VERSION_0,
                    B256::ZERO,
                ],
                hex!("0000000000000000000000000000000000000000000000000000000000000020FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF0000000000000000000000000000000000000000000000000000000000000064").into()
            )
        };

        let system_log = SystemConfigLog::new(log, false);
        let err = BaseFeeUpdate::try_from(&system_log).unwrap_err();
        assert_eq!(err, BaseFeeUpdateError::LengthDecodingError);
    }

    #[test]
    fn test_base_fee_update_invalid_data_length() {
        let log = Log {
            address: Address::ZERO,
            data: LogData::new_unchecked(
                vec![
                    CONFIG_UPDATE_TOPIC,
                    CONFIG_UPDATE_EVENT_VERSION_0,
                    B256::ZERO,
                ],
                hex!("000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000000210000000000000000000000000000000000000000000000000000000000000064").into()
            )
        };

        let system_log = SystemConfigLog::new(log, false);
        let err = BaseFeeUpdate::try_from(&system_log).unwrap_err();
        assert_eq!(err, BaseFeeUpdateError::InvalidDataLength(33));
    }

    #[test]
    fn test_base_fee_update_max_value() {
        // uint256 can hold max value (all Fs), unlike uint64/address
        let log = Log {
            address: Address::ZERO,
            data: LogData::new_unchecked(
                vec![
                    CONFIG_UPDATE_TOPIC,
                    CONFIG_UPDATE_EVENT_VERSION_0,
                    B256::ZERO,
                ],
                hex!("00000000000000000000000000000000000000000000000000000000000000200000000000000000000000000000000000000000000000000000000000000020FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF").into()
            )
        };

        let system_log = SystemConfigLog::new(log, false);
        let update = BaseFeeUpdate::try_from(&system_log).unwrap();
        assert_eq!(update.base_fee, U256::MAX);
    }
}
