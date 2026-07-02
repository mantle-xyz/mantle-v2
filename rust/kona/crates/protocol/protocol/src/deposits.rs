//! Contains deposit transaction types and helper methods.

use alloc::vec::Vec;
use alloy_eips::eip2718::Encodable2718;
use alloy_primitives::{Address, B256, Bytes, Log, TxKind, U256, b256};
use op_alloy_consensus::{TxDeposit, UserDepositSource};

/// Deposit log event abi signature.
pub const DEPOSIT_EVENT_ABI: &str = "TransactionDeposited(address,address,uint256,bytes)";

/// Deposit event abi hash.
///
/// This is the keccak256 hash of the deposit event ABI signature.
/// `keccak256("TransactionDeposited(address,address,uint256,bytes)")`
pub const DEPOSIT_EVENT_ABI_HASH: B256 =
    b256!("b3813568d9991fc951961fcb4c784893574240a28925604d09fc577c55bb7c32");

/// The initial version of the deposit event log.
pub const DEPOSIT_EVENT_VERSION_0: B256 = B256::ZERO;

/// The version 1 of the deposit event log.
///
/// [MANTLE] BVM_ETH: Mantle's `OptimismPortal` emits `DEPOSIT_VERSION = 1` for every user
/// deposit. The v1 `opaqueData` carries two extra 32-byte fields (`eth_value` = `msg.value`
/// and `eth_tx_value`) between `value` and `gasLimit`. See [`unmarshal_deposit_version1`].
pub const DEPOSIT_EVENT_VERSION_1: B256 =
    b256!("0000000000000000000000000000000000000000000000000000000000000001");

/// An [`TxDeposit`] validation error.
#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum DepositError {
    /// Unexpected number of deposit event log topics.
    #[error("Unexpected number of deposit event log topics: {0}")]
    UnexpectedTopicsLen(usize),
    /// Invalid deposit event selector.
    /// Expected: [`B256`] (deposit event selector), Actual: [`B256`] (event log topic).
    #[error("Invalid deposit event selector: {1}, expected {0}")]
    InvalidSelector(B256, B256),
    /// Incomplete opaqueData slice header (incomplete length).
    #[error("Incomplete opaqueData slice header (incomplete length): {0}")]
    IncompleteOpaqueData(usize),
    /// The log data is not aligned to 32 bytes.
    #[error("Unaligned log data, expected multiple of 32 bytes, got: {0}")]
    UnalignedData(usize),
    /// Failed to decode the `from` field of the deposit event (the second topic).
    #[error("Failed to decode the `from` address of the deposit log topic: {0}")]
    FromDecode(B256),
    /// Failed to decode the `to` field of the deposit event (the third topic).
    #[error("Failed to decode the `to` address of the deposit log topic: {0}")]
    ToDecode(B256),
    /// Invalid opaque data content offset.
    #[error("Invalid u64 opaque data content offset: {0}")]
    InvalidOpaqueDataOffset(Bytes),
    /// Invalid opaque data content length.
    #[error("Invalid u64 opaque data content length: expected {expected}, actual {actual}")]
    InvalidOpaqueDataLength {
        /// Expected length.
        expected: usize,
        /// Actual length.
        actual: usize,
    },
    /// Invalid opaque data.
    #[error("Invalid opaque data padding. Not all zeros or incorrect length: {0}")]
    InvalidOpaqueDataPadding(Bytes),
    /// Opaque content length overflow.
    #[error("Opaque content length overflow: {0}")]
    OpaqueContentOverflow(Bytes),
    /// Opaque data length exceeds the deposit log event data length.
    /// Specified: [usize] (data length), Actual: [usize] (opaque data length).
    #[error("Specified opaque data length {1} exceeds the deposit log event data length {0}")]
    OpaqueDataOverflow(u64, usize),
    /// Opaque data padding overflow.
    #[error("Opaque data padding overflow")]
    OpaqueDataPaddingOverflow,
    /// Opaque data with padding exceeds the specified data length.
    /// Specified: [usize] (data length), Actual: [usize] (opaque data length).
    #[error("Opaque data with padding exceeds the specified data length: {1} > {0}")]
    PaddedOpaqueDataOverflow(usize, u64),
    /// An invalid deposit version.
    #[error("Invalid deposit version: {0}")]
    InvalidVersion(B256),
    /// Unexpected opaque data length.
    #[error("Unexpected opaque data length: {0}")]
    UnexpectedOpaqueDataLen(usize),
    /// Failed to decode the deposit mint value.
    #[error("Failed to decode the u128 deposit mint value: {0}")]
    MintDecode(Bytes),
    /// Failed to decode the deposit gas value.
    #[error("Failed to decode the u64 deposit gas value: {0}")]
    GasDecode(Bytes),
    /// [MANTLE] Failed to decode the deposit `eth_value` (BVM_ETH mint value).
    #[error("Failed to decode the u128 deposit eth_value: {0}")]
    EthValueDecode(Bytes),
    /// [MANTLE] Failed to decode the deposit `eth_tx_value` (BVM_ETH tx value).
    #[error("Failed to decode the u128 deposit eth_tx_value: {0}")]
    EthTxValueDecode(Bytes),
}

/// Derives a deposit transaction from an EVM log event emitted by the deposit contract.
///
/// The emitted log must be in format:
/// ```solidity
/// event TransactionDeposited(
///    address indexed from,
///    address indexed to,
///    uint256 indexed version,
///    bytes opaqueData
/// );
/// ```
pub fn decode_deposit(block_hash: B256, index: usize, log: &Log) -> Result<Bytes, DepositError> {
    let topics = log.data.topics();
    if topics.len() != 4 {
        return Err(DepositError::UnexpectedTopicsLen(topics.len()));
    }
    if topics[0] != DEPOSIT_EVENT_ABI_HASH {
        return Err(DepositError::InvalidSelector(DEPOSIT_EVENT_ABI_HASH, topics[0]));
    }
    if log.data.data.len() < 64 {
        return Err(DepositError::IncompleteOpaqueData(log.data.data.len()));
    }
    if log.data.data.len() % 32 != 0 {
        return Err(DepositError::UnalignedData(log.data.data.len()));
    }

    // Validate the `from` address.
    let mut from_bytes = [0u8; 20];
    from_bytes.copy_from_slice(&topics[1].as_slice()[12..]);
    if topics[1].iter().take(12).any(|&b| b != 0) {
        return Err(DepositError::FromDecode(topics[1]));
    }

    // Validate the `to` address.
    let mut to_bytes = [0u8; 20];
    to_bytes.copy_from_slice(&topics[2].as_slice()[12..]);
    if topics[2].iter().take(12).any(|&b| b != 0) {
        return Err(DepositError::ToDecode(topics[2]));
    }

    let from = Address::from(from_bytes);
    let to = Address::from(to_bytes);
    let version = log.data.topics()[3];

    // Solidity serializes the event's Data field as follows:
    //
    // ```solidity
    // abi.encode(abi.encodPacked(uint256 mint, uint256 value, uint64 gasLimit, uint8 isCreation, bytes data))
    // ```
    //
    // The opaqueData will be packed as shown below:
    //
    // ------------------------------------------------------------
    // | offset | 256 byte content                                |
    // ------------------------------------------------------------
    // | 0      | [0; 24] . {U64 big endian, hex encoded offset}  |
    // ------------------------------------------------------------
    // | 32     | [0; 24] . {U64 big endian, hex encoded length}  |
    // ------------------------------------------------------------

    let opaque_content_offset: U256 = U256::from_be_slice(&log.data.data[0..32]);
    if opaque_content_offset != U256::from(32) {
        return Err(DepositError::InvalidOpaqueDataOffset(Bytes::copy_from_slice(
            &log.data.data[0..32],
        )));
    }

    // The next 32 bytes indicate the length of the opaqueData content.
    let opaque_content_len: U256 = U256::from_be_slice(&log.data.data[32..64]);
    let opaque_content_len: u64 = opaque_content_len.try_into().map_err(|_| {
        DepositError::OpaqueContentOverflow(Bytes::copy_from_slice(&log.data.data[32..64]))
    })?;

    let opaque_data_ceil_32: u64 = (opaque_content_len.saturating_add(31) / 32).saturating_mul(32);

    // Ensure that the remaining data is only zeros.
    // The padding ends at the next multiple of 32 after the opaque data.
    let Some(padding_end): Option<u64> = 64_u64.checked_add(opaque_data_ceil_32) else {
        return Err(DepositError::OpaqueDataPaddingOverflow);
    };

    // The remaining data is the opaqueData which is tightly packed and then padded to 32 bytes by
    // the EVM.
    let Some(opaque_data) = &log.data.data.get(64..64 + opaque_content_len as usize) else {
        return Err(DepositError::InvalidOpaqueDataLength {
            expected: opaque_content_len as usize,
            actual: log.data.data.len().saturating_sub(64),
        });
    };

    if !(opaque_content_len.is_multiple_of(32) ||
        log.data
            .data
            .get((64 + opaque_content_len) as usize..padding_end as usize)
            .is_some_and(|data| data.iter().all(|&b| b == 0)))
    {
        return Err(DepositError::InvalidOpaqueDataPadding(Bytes::copy_from_slice(
            &log.data.data[(64 + opaque_content_len) as usize..],
        )));
    }

    let source = UserDepositSource::new(block_hash, index as u64);

    let mut deposit_tx = TxDeposit {
        from,
        is_system_transaction: false,
        source_hash: source.source_hash(),
        ..Default::default()
    };

    // [MANTLE] Route by deposit event version. Upstream OP only emits v0; Mantle's
    // `OptimismPortal` emits v1 (BVM_ETH) for every user deposit.
    if version == DEPOSIT_EVENT_VERSION_0 {
        unmarshal_deposit_version0(&mut deposit_tx, to, opaque_data)?;
    } else if version == DEPOSIT_EVENT_VERSION_1 {
        unmarshal_deposit_version1(&mut deposit_tx, to, opaque_data)?;
    } else {
        return Err(DepositError::InvalidVersion(version));
    }

    // Re-encode the deposit transaction
    let mut buffer = Vec::with_capacity(deposit_tx.eip2718_encoded_length());
    deposit_tx.encode_2718(&mut buffer);
    Ok(Bytes::from(buffer))
}

/// Unmarshals a deposit transaction from the opaque data.
pub(crate) fn unmarshal_deposit_version0(
    tx: &mut TxDeposit,
    to: Address,
    data: &[u8],
) -> Result<(), DepositError> {
    if data.len() < 32 + 32 + 8 + 1 {
        return Err(DepositError::UnexpectedOpaqueDataLen(data.len()));
    }

    let mut offset = 0;

    let raw_mint: [u8; 16] = data[offset + 16..offset + 32].try_into().map_err(|_| {
        DepositError::MintDecode(Bytes::copy_from_slice(&data[offset + 16..offset + 32]))
    })?;
    tx.mint = u128::from_be_bytes(raw_mint);
    offset += 32;

    // uint256 value
    tx.value = U256::from_be_slice(&data[offset..offset + 32]);
    offset += 32;

    // uint64 gas
    let raw_gas: [u8; 8] = data[offset..offset + 8]
        .try_into()
        .map_err(|_| DepositError::GasDecode(Bytes::copy_from_slice(&data[offset..offset + 8])))?;
    tx.gas_limit = u64::from_be_bytes(raw_gas);
    offset += 8;

    // uint8 isCreation
    // isCreation: If the boolean byte is 1 then dep.To will stay nil,
    // and it will create a contract using L2 account nonce to determine the created address.
    if data[offset] == 0 {
        tx.to = TxKind::Call(to);
    } else {
        tx.to = TxKind::Create;
    }
    offset += 1;

    // The remainder of the opaqueData is the transaction data (without length prefix).
    // The data may be padded to a multiple of 32 bytes
    let tx_data_len = data.len() - offset;

    // Remaining bytes fill the data
    tx.input = Bytes::copy_from_slice(&data[offset..offset + tx_data_len]);

    Ok(())
}

/// [MANTLE] Unmarshals a version-1 deposit transaction from the opaque data.
///
/// Mantle's `OptimismPortal` packs the `TransactionDeposited` event's `opaqueData` as:
/// `abi.encodePacked(_mntValue, _mntTxValue, msg.value, _ethTxValue, _gasLimit, _isCreation,
/// _data)`. This mirrors op-node's `unmarshalDepositVersion1`:
///
/// | offset | bytes | field        | -> `TxDeposit`             |
/// |--------|-------|--------------|----------------------------|
/// | 0      | 32    | `_mntValue`  | `mint` (u128, low 16 bytes)|
/// | 32     | 32    | `_mntTxValue`| `value` (U256)             |
/// | 64     | 32    | `msg.value`  | `eth_value` (u128)         |
/// | 96     | 32    | `_ethTxValue`| `eth_tx_value` (0 => None) |
/// | 128    | 8     | `_gasLimit`  | `gas_limit` (u64)          |
/// | 136    | 1     | `_isCreation`| `to` (Create/Call)         |
/// | 137..  | rest  | `_data`      | `input`                    |
pub(crate) fn unmarshal_deposit_version1(
    tx: &mut TxDeposit,
    to: Address,
    data: &[u8],
) -> Result<(), DepositError> {
    if data.len() < 32 + 32 + 32 + 32 + 8 + 1 {
        return Err(DepositError::UnexpectedOpaqueDataLen(data.len()));
    }

    let mut offset = 0;

    // u128 mint (MNT value)
    tx.mint = decode_u128_field(data, offset, DepositError::MintDecode)?.unwrap_or(0);
    offset += 32;

    // uint256 value (MNT tx value)
    tx.value = U256::from_be_slice(&data[offset..offset + 32]);
    offset += 32;

    // u128 eth_value (BVM_ETH mint = msg.value)
    tx.eth_value = decode_u128_field(data, offset, DepositError::EthValueDecode)?.unwrap_or(0);
    offset += 32;

    // u128 eth_tx_value (BVM_ETH tx value; 0 is represented as None)
    tx.eth_tx_value = decode_u128_field(data, offset, DepositError::EthTxValueDecode)?;
    offset += 32;

    // uint64 gas
    tx.gas_limit = decode_u8_field(data, offset, DepositError::GasDecode)?;
    offset += 8;

    // uint8 isCreation
    // isCreation: If the boolean byte is 1 then dep.To will stay nil,
    // and it will create a contract using L2 account nonce to determine the created address.
    if data[offset] == 0 {
        tx.to = TxKind::Call(to);
    } else {
        tx.to = TxKind::Create;
    }
    offset += 1;

    // The remainder of the opaqueData is the transaction data (without length prefix).
    // The data may be padded to a multiple of 32 bytes
    let tx_data_len = data.len() - offset;

    // Remaining bytes fill the data
    tx.input = Bytes::copy_from_slice(&data[offset..offset + tx_data_len]);

    Ok(())
}

/// [MANTLE] Decodes a 32-byte big-endian field as a `u128` (low 16 bytes), returning `None`
/// when the value is zero (matching op-node's `nil`-when-zero convention for mint values).
fn decode_u128_field<E>(
    data: &[u8],
    offset: usize,
    error_fn: impl FnOnce(Bytes) -> E,
) -> Result<Option<u128>, E> {
    let raw_value: [u8; 16] = data[offset + 16..offset + 32]
        .try_into()
        .map_err(|_| error_fn(Bytes::copy_from_slice(&data[offset + 16..offset + 32])))?;
    let value = u128::from_be_bytes(raw_value);
    Ok(if value == 0 { None } else { Some(value) })
}

/// [MANTLE] Decodes an 8-byte big-endian field as a `u64` (used for `gasLimit`).
fn decode_u8_field<E>(
    data: &[u8],
    offset: usize,
    error_fn: impl FnOnce(Bytes) -> E,
) -> Result<u64, E> {
    let raw_value: [u8; 8] = data[offset..offset + 8]
        .try_into()
        .map_err(|_| error_fn(Bytes::copy_from_slice(&data[offset..offset + 8])))?;
    Ok(u64::from_be_bytes(raw_value))
}

#[cfg(test)]
mod test {
    use super::*;
    use alloc::vec;
    use alloy_primitives::{LogData, U64, U128, address, b256, hex};

    #[test]
    fn test_decode_deposit_invalid_first_topic() {
        let log = Log {
            address: Address::default(),
            data: LogData::new_unchecked(
                vec![B256::default(), B256::default(), B256::default(), B256::default()],
                Bytes::default(),
            ),
        };
        let err: DepositError = decode_deposit(B256::default(), 0, &log).unwrap_err();
        assert_eq!(err, DepositError::InvalidSelector(DEPOSIT_EVENT_ABI_HASH, B256::default()));
    }

    #[test]
    fn test_decode_deposit_incomplete_data() {
        let log = Log {
            address: Address::default(),
            data: LogData::new_unchecked(
                vec![DEPOSIT_EVENT_ABI_HASH, B256::default(), B256::default(), B256::default()],
                Bytes::from(vec![0u8; 63]),
            ),
        };
        let err = decode_deposit(B256::default(), 0, &log).unwrap_err();
        assert_eq!(err, DepositError::IncompleteOpaqueData(63));
    }

    #[test]
    fn test_decode_deposit_unaligned_data() {
        let log = Log {
            address: Address::default(),
            data: LogData::new_unchecked(
                vec![DEPOSIT_EVENT_ABI_HASH, B256::default(), B256::default(), B256::default()],
                Bytes::from(vec![0u8; 65]),
            ),
        };
        let err = decode_deposit(B256::default(), 0, &log).unwrap_err();
        assert_eq!(err, DepositError::UnalignedData(65));
    }

    #[test]
    fn test_decode_deposit_invalid_from() {
        let invalid_from =
            b256!("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF");
        let log = Log {
            address: Address::default(),
            data: LogData::new_unchecked(
                vec![DEPOSIT_EVENT_ABI_HASH, invalid_from, B256::default(), B256::default()],
                Bytes::from(vec![0u8; 64]),
            ),
        };
        let err = decode_deposit(B256::default(), 0, &log).unwrap_err();
        assert_eq!(err, DepositError::FromDecode(invalid_from));
    }

    #[test]
    fn test_decode_deposit_invalid_to() {
        let invalid_to = b256!("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF");
        let log = Log {
            address: Address::default(),
            data: LogData::new_unchecked(
                vec![DEPOSIT_EVENT_ABI_HASH, B256::default(), invalid_to, B256::default()],
                Bytes::from(vec![0u8; 64]),
            ),
        };
        let err = decode_deposit(B256::default(), 0, &log).unwrap_err();
        assert_eq!(err, DepositError::ToDecode(invalid_to));
    }

    #[test]
    fn test_decode_deposit_invalid_opaque_data_offset() {
        let log = Log {
            address: Address::default(),
            data: LogData::new_unchecked(
                vec![DEPOSIT_EVENT_ABI_HASH, B256::default(), B256::default(), B256::default()],
                Bytes::from(vec![0u8; 64]),
            ),
        };
        let err = decode_deposit(B256::default(), 0, &log).unwrap_err();
        assert_eq!(err, DepositError::InvalidOpaqueDataOffset(Bytes::from(vec![0u8; 32])));
    }

    #[test]
    fn test_decode_deposit_opaque_data_overflow() {
        let mut data = vec![0u8; 128];
        let offset: [u8; 8] = U64::from(32).to_be_bytes();
        data[24..32].copy_from_slice(&offset);
        // The first 64 bytes of the data are identifiers so
        // if this test was to be valid, the data length would be 64 not 128.
        let len: [u8; 8] = U64::from(128).to_be_bytes();
        data[56..64].copy_from_slice(&len);
        let log = Log {
            address: Address::default(),
            data: LogData::new_unchecked(
                vec![DEPOSIT_EVENT_ABI_HASH, B256::default(), B256::default(), B256::default()],
                Bytes::from(data),
            ),
        };
        let err = decode_deposit(B256::default(), 0, &log).unwrap_err();
        assert_eq!(err, DepositError::InvalidOpaqueDataLength { expected: 128, actual: 64 });
    }

    #[test]
    fn test_decode_deposit_padded_overflow() {
        let mut data = vec![0u8; 256];
        let offset: [u8; 8] = U64::from(32).to_be_bytes();
        data[24..32].copy_from_slice(&offset);
        let len: [u8; 8] = U64::from(64).to_be_bytes();
        data[56..64].copy_from_slice(&len);
        let log = Log {
            address: Address::default(),
            data: LogData::new_unchecked(
                vec![DEPOSIT_EVENT_ABI_HASH, B256::default(), B256::default(), B256::default()],
                Bytes::from(data),
            ),
        };
        let err = decode_deposit(B256::default(), 0, &log).unwrap_err();
        assert_eq!(err, DepositError::UnexpectedOpaqueDataLen(64));
    }

    #[test]
    fn test_decode_deposit_invalid_version() {
        let mut data = vec![0u8; 128];
        let offset: [u8; 8] = U64::from(32).to_be_bytes();
        data[24..32].copy_from_slice(&offset);
        let len: [u8; 8] = U64::from(64).to_be_bytes();
        data[56..64].copy_from_slice(&len);
        // [MANTLE] v0 and v1 are supported; use v2 as a genuinely unsupported version.
        let version = b256!("0000000000000000000000000000000000000000000000000000000000000002");
        let log = Log {
            address: Address::default(),
            data: LogData::new_unchecked(
                vec![DEPOSIT_EVENT_ABI_HASH, B256::default(), B256::default(), version],
                Bytes::from(data),
            ),
        };
        let err = decode_deposit(B256::default(), 0, &log).unwrap_err();
        assert_eq!(err, DepositError::InvalidVersion(version));
    }

    #[test]
    fn test_decode_deposit_empty_succeeds() {
        let valid_to = b256!("000000000000000000000000bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb");
        let valid_from = b256!("000000000000000000000000FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF");
        let mut data = vec![0u8; 192];
        let offset: [u8; 8] = U64::from(32).to_be_bytes();
        data[24..32].copy_from_slice(&offset);
        let len: [u8; 8] = U64::from(128).to_be_bytes();
        data[56..64].copy_from_slice(&len);
        let log = Log {
            address: Address::default(),
            data: LogData::new_unchecked(
                vec![DEPOSIT_EVENT_ABI_HASH, valid_from, valid_to, B256::default()],
                Bytes::from(data),
            ),
        };
        let tx = decode_deposit(B256::default(), 0, &log).unwrap();
        // [MANTLE] Hex includes one extra `80` byte (eth_value=0) before the
        // input field, and the outer list length is f888 (1 byte longer than
        // upstream OP's f887). Trailing `eth_tx_value: None` is omitted.
        let raw_hex = hex!(
            "7ef888a0ed428e1c45e1d9561b62834e1a2d3015a0caae3bfdc16b4da059ac885b01a14594ffffffffffffffffffffffffffffffffffffffff94bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb8080808080b700000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
        );
        let expected = Bytes::from(raw_hex);
        assert_eq!(tx, expected);
    }

    #[test]
    fn test_decode_deposit_invalid_offset() {
        let mut data = vec![0u8; 128];
        let offset: [u8; 16] = U128::MAX.to_be_bytes();
        data[16..32].copy_from_slice(&offset);
        let len: [u8; 8] = U64::from(128).to_be_bytes();
        data[56..64].copy_from_slice(&len);
        let log = Log {
            address: Address::default(),
            data: LogData::new_unchecked(
                vec![DEPOSIT_EVENT_ABI_HASH, B256::default(), B256::default(), B256::default()],
                Bytes::from(data.clone()),
            ),
        };
        let err = decode_deposit(B256::default(), 0, &log).unwrap_err();
        let bytes = Bytes::from(data.get(0..32).unwrap().to_vec());
        assert_eq!(err, DepositError::InvalidOpaqueDataOffset(bytes));
    }

    #[test]
    fn test_decode_deposit_invalid_length() {
        let mut data = vec![0u8; 128];
        let offset: [u8; 8] = U64::from(32).to_be_bytes();
        data[24..32].copy_from_slice(&offset);
        let len: [u8; 16] = U128::MAX.to_be_bytes();
        data[48..64].copy_from_slice(&len);
        let log = Log {
            address: Address::default(),
            data: LogData::new_unchecked(
                vec![DEPOSIT_EVENT_ABI_HASH, B256::default(), B256::default(), B256::default()],
                Bytes::from(data.clone()),
            ),
        };
        let err = decode_deposit(B256::default(), 0, &log).unwrap_err();
        assert_eq!(
            err,
            DepositError::OpaqueContentOverflow(Bytes::from(data.get(32..64).unwrap().to_vec()))
        );
    }

    #[test]
    fn test_invalid_opaque_data_length() {
        let mut data = vec![0u8; 192];
        let offset: [u8; 8] = U64::from(32).to_be_bytes();
        data[24..32].copy_from_slice(&offset);
        let len: [u8; 8] = U64::from(129).to_be_bytes();
        data[56..64].copy_from_slice(&len);
        // Copy the u128 mint value
        let mint: [u8; 16] = 10_u128.to_be_bytes();
        data[80..96].copy_from_slice(&mint);
        // Copy the tx value
        let value: [u8; 32] = U256::from(100).to_be_bytes();
        data[96..128].copy_from_slice(&value);
        // Copy the gas limit
        let gas: [u8; 8] = 1000_u64.to_be_bytes();
        data[128..136].copy_from_slice(&gas);
        // Copy the isCreation flag
        data[136] = 1;
        let from = address!("1111111111111111111111111111111111111111");
        let mut from_bytes = vec![0u8; 32];
        from_bytes[12..32].copy_from_slice(from.as_slice());
        let to = address!("2222222222222222222222222222222222222222");
        let mut to_bytes = vec![0u8; 32];
        to_bytes[12..32].copy_from_slice(to.as_slice());
        let log = Log {
            address: Address::default(),
            data: LogData::new_unchecked(
                vec![
                    DEPOSIT_EVENT_ABI_HASH,
                    B256::from_slice(&from_bytes),
                    B256::from_slice(&to_bytes),
                    B256::default(),
                ],
                Bytes::from(data),
            ),
        };
        let err = decode_deposit(B256::default(), 0, &log).unwrap_err();
        assert_eq!(err, DepositError::InvalidOpaqueDataLength { expected: 129, actual: 128 });
    }

    #[test]
    fn test_opaque_data_padding_overflow() {
        let mut data = vec![0u8; 192];
        let offset: [u8; 8] = U64::from(32).to_be_bytes();
        data[24..32].copy_from_slice(&offset);
        let len: [u8; 8] = U64::MAX.to_be_bytes();
        data[56..64].copy_from_slice(&len);
        // Copy the u128 mint value
        let mint: [u8; 16] = 10_u128.to_be_bytes();
        data[80..96].copy_from_slice(&mint);
        // Copy the tx value
        let value: [u8; 32] = U256::from(100).to_be_bytes();
        data[96..128].copy_from_slice(&value);
        // Copy the gas limit
        let gas: [u8; 8] = 1000_u64.to_be_bytes();
        data[128..136].copy_from_slice(&gas);
        // Copy the isCreation flag
        data[136] = 1;
        let from = address!("1111111111111111111111111111111111111111");
        let mut from_bytes = vec![0u8; 32];
        from_bytes[12..32].copy_from_slice(from.as_slice());
        let to = address!("2222222222222222222222222222222222222222");
        let mut to_bytes = vec![0u8; 32];
        to_bytes[12..32].copy_from_slice(to.as_slice());
        let log = Log {
            address: Address::default(),
            data: LogData::new_unchecked(
                vec![
                    DEPOSIT_EVENT_ABI_HASH,
                    B256::from_slice(&from_bytes),
                    B256::from_slice(&to_bytes),
                    B256::default(),
                ],
                Bytes::from(data.clone()),
            ),
        };
        let err = decode_deposit(B256::default(), 0, &log).unwrap_err();
        assert_eq!(err, DepositError::OpaqueDataPaddingOverflow);
    }

    #[test]
    fn test_invalid_opaque_data_padding() {
        let mut data = vec![0u8; 192];
        let offset: [u8; 8] = U64::from(32).to_be_bytes();
        data[24..32].copy_from_slice(&offset);
        let len: [u8; 8] = U64::from(127).to_be_bytes();
        data[56..64].copy_from_slice(&len);
        // Copy the u128 mint value
        let mint: [u8; 16] = 10_u128.to_be_bytes();
        data[80..96].copy_from_slice(&mint);
        // Copy the tx value
        let value: [u8; 32] = U256::from(100).to_be_bytes();
        data[96..128].copy_from_slice(&value);
        // Copy the gas limit
        let gas: [u8; 8] = 1000_u64.to_be_bytes();
        data[128..136].copy_from_slice(&gas);
        // Copy the isCreation flag
        data[136] = 1;
        // Mess up the padding
        data[191] = 1;
        let from = address!("1111111111111111111111111111111111111111");
        let mut from_bytes = vec![0u8; 32];
        from_bytes[12..32].copy_from_slice(from.as_slice());
        let to = address!("2222222222222222222222222222222222222222");
        let mut to_bytes = vec![0u8; 32];
        to_bytes[12..32].copy_from_slice(to.as_slice());
        let log = Log {
            address: Address::default(),
            data: LogData::new_unchecked(
                vec![
                    DEPOSIT_EVENT_ABI_HASH,
                    B256::from_slice(&from_bytes),
                    B256::from_slice(&to_bytes),
                    B256::default(),
                ],
                Bytes::from(data.clone()),
            ),
        };
        let err = decode_deposit(B256::default(), 0, &log).unwrap_err();
        assert_eq!(
            err,
            DepositError::InvalidOpaqueDataPadding(Bytes::from(data.get(191..).unwrap().to_vec()))
        );
    }

    #[test]
    fn test_decode_deposit_full_succeeds() {
        let mut data = vec![0u8; 192];
        let offset: [u8; 8] = U64::from(32).to_be_bytes();
        data[24..32].copy_from_slice(&offset);
        let len: [u8; 8] = U64::from(128).to_be_bytes();
        data[56..64].copy_from_slice(&len);
        // Copy the u128 mint value
        let mint: [u8; 16] = 10_u128.to_be_bytes();
        data[80..96].copy_from_slice(&mint);
        // Copy the tx value
        let value: [u8; 32] = U256::from(100).to_be_bytes();
        data[96..128].copy_from_slice(&value);
        // Copy the gas limit
        let gas: [u8; 8] = 1000_u64.to_be_bytes();
        data[128..136].copy_from_slice(&gas);
        // Copy the isCreation flag
        data[136] = 1;
        let from = address!("1111111111111111111111111111111111111111");
        let mut from_bytes = vec![0u8; 32];
        from_bytes[12..32].copy_from_slice(from.as_slice());
        let to = address!("2222222222222222222222222222222222222222");
        let mut to_bytes = vec![0u8; 32];
        to_bytes[12..32].copy_from_slice(to.as_slice());
        let log = Log {
            address: Address::default(),
            data: LogData::new_unchecked(
                vec![
                    DEPOSIT_EVENT_ABI_HASH,
                    B256::from_slice(&from_bytes),
                    B256::from_slice(&to_bytes),
                    B256::default(),
                ],
                Bytes::from(data),
            ),
        };
        let tx = decode_deposit(B256::default(), 0, &log).unwrap();
        // [MANTLE] Hex includes one extra `80` byte (eth_value=0) before the
        // input field, and the outer list length is f876 (1 byte longer than
        // upstream OP's f875). Trailing `eth_tx_value: None` is omitted.
        let raw_hex = hex!(
            "7ef876a0ed428e1c45e1d9561b62834e1a2d3015a0caae3bfdc16b4da059ac885b01a145941111111111111111111111111111111111111111800a648203e88080b700000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
        );
        let expected = Bytes::from(raw_hex);
        assert_eq!(tx, expected);
    }

    #[test]
    fn test_unmarshal_deposit_version0_invalid_len() {
        let data = vec![0u8; 72];
        let mut tx = TxDeposit::default();
        let to = address!("5555555555555555555555555555555555555555");
        let err = unmarshal_deposit_version0(&mut tx, to, &data).unwrap_err();
        assert_eq!(err, DepositError::UnexpectedOpaqueDataLen(72));

        // Data must have at least length 73
        let data = vec![0u8; 73];
        let mut tx = TxDeposit::default();
        let to = address!("5555555555555555555555555555555555555555");
        unmarshal_deposit_version0(&mut tx, to, &data).unwrap();
    }

    #[test]
    fn test_unmarshal_deposit_version0() {
        let mut data = vec![0u8; 192];
        let offset: [u8; 8] = U64::from(32).to_be_bytes();
        data[24..32].copy_from_slice(&offset);
        let len: [u8; 8] = U64::from(128).to_be_bytes();
        data[56..64].copy_from_slice(&len);
        // Copy the u128 mint value
        let mint: [u8; 16] = 10_u128.to_be_bytes();
        data[80..96].copy_from_slice(&mint);
        // Copy the tx value
        let value: [u8; 32] = U256::from(100).to_be_bytes();
        data[96..128].copy_from_slice(&value);
        // Copy the gas limit
        let gas: [u8; 8] = 1000_u64.to_be_bytes();
        data[128..136].copy_from_slice(&gas);
        // Copy the isCreation flag
        data[136] = 1;
        let mut tx = TxDeposit {
            from: address!("1111111111111111111111111111111111111111"),
            to: TxKind::Call(address!("2222222222222222222222222222222222222222")),
            value: U256::from(100),
            gas_limit: 1000,
            mint: 10,
            ..Default::default()
        };
        let to = address!("5555555555555555555555555555555555555555");
        unmarshal_deposit_version0(&mut tx, to, &data).unwrap();
        assert_eq!(tx.to, TxKind::Call(address!("5555555555555555555555555555555555555555")));
    }

    #[test]
    fn test_unmarshal_deposit_version1_invalid_len() {
        // Data must have at least length 137 (32+32+32+32+8+1)
        let data = vec![0u8; 136];
        let mut tx = TxDeposit::default();
        let to = address!("5555555555555555555555555555555555555555");
        let err = unmarshal_deposit_version1(&mut tx, to, &data).unwrap_err();
        assert_eq!(err, DepositError::UnexpectedOpaqueDataLen(136));

        // Data with exactly 137 bytes should succeed
        let data = vec![0u8; 137];
        let mut tx = TxDeposit::default();
        let to = address!("5555555555555555555555555555555555555555");
        unmarshal_deposit_version1(&mut tx, to, &data).unwrap();
    }

    #[test]
    fn test_unmarshal_deposit_version1_basic() {
        let mut data = vec![0u8; 256];

        // u128 mint mnt value (32 bytes, but only last 16 bytes are used)
        let mint: [u8; 16] = 1000_u128.to_be_bytes();
        data[16..32].copy_from_slice(&mint);

        // uint256 value (32 bytes)
        let value: [u8; 32] = U256::from(200).to_be_bytes();
        data[32..64].copy_from_slice(&value);

        // u128 eth_value (32 bytes, but only last 16 bytes are used)
        let eth_value: [u8; 16] = 500_u128.to_be_bytes();
        data[80..96].copy_from_slice(&eth_value);

        // u128 eth_tx_value (32 bytes, but only last 16 bytes are used)
        let eth_tx_value: [u8; 16] = 300_u128.to_be_bytes();
        data[112..128].copy_from_slice(&eth_tx_value);

        // uint64 gas (8 bytes)
        let gas: [u8; 8] = 21000_u64.to_be_bytes();
        data[128..136].copy_from_slice(&gas);

        // uint8 isCreation (1 byte)
        data[136] = 0; // Call transaction

        // Transaction data (remaining bytes)
        let tx_data = b"hello world";
        data[137..137 + tx_data.len()].copy_from_slice(tx_data);

        let mut tx = TxDeposit {
            from: address!("1111111111111111111111111111111111111111"),
            ..Default::default()
        };
        let to = address!("2222222222222222222222222222222222222222");

        unmarshal_deposit_version1(&mut tx, to, &data).unwrap();

        assert_eq!(tx.mint, 1000);
        assert_eq!(tx.value, U256::from(200));
        assert_eq!(tx.eth_value, 500);
        assert_eq!(tx.eth_tx_value, Some(300));
        assert_eq!(tx.gas_limit, 21000);
        assert_eq!(tx.to, TxKind::Call(to));
        // The input includes all remaining data (including padding)
        assert!(tx.input.starts_with(tx_data));
        assert_eq!(tx.input.len(), data.len() - 137);
    }

    #[test]
    fn test_unmarshal_deposit_version1_with_creation() {
        let mut data = vec![0u8; 200];

        // u128 mint mnt value
        let mint: [u8; 16] = 5000_u128.to_be_bytes();
        data[16..32].copy_from_slice(&mint);

        // uint256 value
        let value: [u8; 32] = U256::from(1000).to_be_bytes();
        data[32..64].copy_from_slice(&value);

        // u128 eth_value
        let eth_value: [u8; 16] = 2000_u128.to_be_bytes();
        data[80..96].copy_from_slice(&eth_value);

        // u128 eth_tx_value
        let eth_tx_value: [u8; 16] = 1500_u128.to_be_bytes();
        data[112..128].copy_from_slice(&eth_tx_value);

        // uint64 gas
        let gas: [u8; 8] = 50000_u64.to_be_bytes();
        data[128..136].copy_from_slice(&gas);

        // uint8 isCreation = 1 (contract creation)
        data[136] = 1;

        // Contract creation data
        let contract_data = b"contract bytecode";
        data[137..137 + contract_data.len()].copy_from_slice(contract_data);

        let mut tx = TxDeposit::default();
        let to = address!("3333333333333333333333333333333333333333");

        unmarshal_deposit_version1(&mut tx, to, &data).unwrap();

        assert_eq!(tx.mint, 5000);
        assert_eq!(tx.value, U256::from(1000));
        assert_eq!(tx.eth_value, 2000);
        assert_eq!(tx.eth_tx_value, Some(1500));
        assert_eq!(tx.gas_limit, 50000);
        assert_eq!(tx.to, TxKind::Create);
        // The input includes all remaining data (including padding)
        assert!(tx.input.starts_with(contract_data));
        assert_eq!(tx.input.len(), data.len() - 137);
    }

    #[test]
    fn test_unmarshal_deposit_version1_zero_values() {
        let mut data = vec![0u8; 200];

        // All fields are zero except gas and isCreation
        // uint64 gas
        let gas: [u8; 8] = 21000_u64.to_be_bytes();
        data[128..136].copy_from_slice(&gas);

        // uint8 isCreation = 0
        data[136] = 0;

        let mut tx = TxDeposit::default();
        let to = address!("4444444444444444444444444444444444444444");

        unmarshal_deposit_version1(&mut tx, to, &data).unwrap();

        assert_eq!(tx.mint, 0);
        assert_eq!(tx.value, U256::ZERO);
        assert_eq!(tx.eth_value, 0);
        assert_eq!(tx.eth_tx_value, None); // decode_u128_field returns None for zero
        assert_eq!(tx.gas_limit, 21000);
        assert_eq!(tx.to, TxKind::Call(to));
    }

    #[test]
    fn test_decode_deposit_version1_full() {
        // Test decode_deposit with a complete version 1 deposit event
        // Based on Mantle deposit transaction format
        let valid_to = b256!("000000000000000000000000787b795fe6e43c17c668de16730c3f690feb8231");
        let valid_from = b256!("000000000000000000000000671730450c132914662dfa6beda90f8a1a4cf84a");

        // opaque data: 32+32+32+32+8+1 = 137 bytes (no tx data for simplicity)
        // padded to next multiple of 32: 160 bytes
        // total data: 64 (header) + 160 (padded opaque) = 224 bytes
        let mut data = vec![0u8; 224];

        // Set offset to 32
        let offset: [u8; 8] = U64::from(32).to_be_bytes();
        data[24..32].copy_from_slice(&offset);

        // Set length to 137 (32+32+32+32+8+1, no tx data)
        let len: [u8; 8] = U64::from(137).to_be_bytes();
        data[56..64].copy_from_slice(&len);

        // u128 mint mnt value (50 ETH in wei = 0x2b5e3af16b1880000)
        let mint: [u8; 16] = 50000000000000000000_u128.to_be_bytes();
        data[80..96].copy_from_slice(&mint);

        // uint256 value (0)
        let value: [u8; 32] = U256::ZERO.to_be_bytes();
        data[96..128].copy_from_slice(&value);

        // u128 eth_value (0)
        let eth_value: [u8; 16] = 0_u128.to_be_bytes();
        data[144..160].copy_from_slice(&eth_value);

        // u128 eth_tx_value (0)
        let eth_tx_value: [u8; 16] = 0_u128.to_be_bytes();
        data[176..192].copy_from_slice(&eth_tx_value);

        // uint64 gas (100000)
        let gas: [u8; 8] = 100000_u64.to_be_bytes();
        data[192..200].copy_from_slice(&gas);

        // uint8 isCreation (0 - call transaction)
        data[200] = 0;

        // No transaction data, bytes 201-224 are padding (already zeros)

        let block_hash = b256!("9df6538112a7d9be1fc686657317a3ae92b3cd9be39812a649051fac43957da0");

        let log = Log {
            address: Address::default(),
            data: LogData::new_unchecked(
                vec![DEPOSIT_EVENT_ABI_HASH, valid_from, valid_to, DEPOSIT_EVENT_VERSION_1],
                Bytes::from(data),
            ),
        };

        let tx = decode_deposit(block_hash, 0, &log).unwrap();

        // Verify the encoded transaction is valid
        assert!(!tx.is_empty());
    }
}
