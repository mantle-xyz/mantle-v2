//! Arsia L1 Block Info transaction types.
//!
//! [MANTLE] Arsia is a Mantle hardfork stacked on top of OP Jovian. At the
//! L1-attributes ABI level it introduces only a new function selector —
//! `setL1BlockValuesArsia()` = `0x49e72383` — while keeping the exact same
//! 174-byte payload layout as Jovian (verified by reverse-engineering the
//! `arsia_l1_block.hex` dispatcher in `kona-hardforks`). Mantle execution
//! semantics (BVM_ETH, token_ratio, DA footprint accounting, Arsia fee
//! validation) are handled in `mantle-elysium revm`; this module concerns
//! only the L1 attributes calldata codec.

use crate::{
    DecodeError, L1BlockInfoJovian,
    info::{
        L1BlockInfoBedrockBaseFields, L1BlockInfoEcotoneBaseFields,
        bedrock_base::ambassador_impl_L1BlockInfoBedrockBaseFields,
        ecotone_base::ambassador_impl_L1BlockInfoEcotoneBaseFields,
        isthmus::{L1BlockInfoIsthmusBaseFields, ambassador_impl_L1BlockInfoIsthmusBaseFields},
        jovian::{L1BlockInfoJovianBaseFields, ambassador_impl_L1BlockInfoJovianBaseFields},
    },
};
use alloc::vec::Vec;
use alloy_primitives::{Address, B256, Bytes};
use ambassador::{self, Delegate};

/// Represents the fields within an Arsia L1 block info transaction.
///
/// Arsia Binary Format (byte-for-byte identical to Jovian, only the selector
/// differs).
///
/// +---------+--------------------------+
/// | Bytes   | Field                    |
/// +---------+--------------------------+
/// | 4       | Function signature       |
/// | 4       | `BaseFeeScalar`          |
/// | 4       | `BlobBaseFeeScalar`      |
/// | 8       | `SequenceNumber`         |
/// | 8       | Timestamp                |
/// | 8       | `L1BlockNumber`          |
/// | 32      | `BaseFee`                |
/// | 32      | `BlobBaseFee`            |
/// | 32      | `BlockHash`              |
/// | 32      | `BatcherHash`            |
/// | 4       | `OperatorFeeScalar`      |
/// | 8       | `OperatorFeeConstant`    |
/// | 2       | `DAFootprintGasScalar`   |
/// +---------+--------------------------+
#[derive(Debug, Clone, Hash, Eq, PartialEq, Default, Copy, Delegate)]
#[cfg_attr(feature = "serde", derive(serde::Serialize, serde::Deserialize))]
#[allow(clippy::duplicated_attributes)]
#[delegate(L1BlockInfoBedrockBaseFields, target = "base")]
#[delegate(L1BlockInfoEcotoneBaseFields, target = "base")]
#[delegate(L1BlockInfoIsthmusBaseFields, target = "base")]
#[delegate(L1BlockInfoJovianBaseFields, target = "base")]
pub struct L1BlockInfoArsia {
    /// Fields inherited from Jovian.
    #[cfg_attr(feature = "serde", serde(flatten))]
    pub base: L1BlockInfoJovian,
}

/// Accessors to fields available in Arsia and later.
pub trait L1BlockInfoArsiaBaseFields: L1BlockInfoJovianBaseFields {}

impl L1BlockInfoArsiaBaseFields for L1BlockInfoArsia {}

/// Accessors for all Arsia fields.
pub trait L1BlockInfoArsiaFields:
    L1BlockInfoJovianBaseFields + L1BlockInfoArsiaBaseFields
{
}

impl L1BlockInfoArsiaFields for L1BlockInfoArsia {}

impl L1BlockInfoArsia {
    /// The type byte identifier for the L1 scalar format in Arsia (same as Jovian).
    pub const L1_SCALAR: u8 = L1BlockInfoJovian::L1_SCALAR;

    /// The length of an L1 info transaction in Arsia (same as Jovian: 178 bytes).
    pub const L1_INFO_TX_LEN: usize = L1BlockInfoJovian::L1_INFO_TX_LEN;

    /// The 4 byte selector of "`setL1BlockValuesArsia()`".
    pub const L1_INFO_TX_SELECTOR: [u8; 4] = [0x49, 0xe7, 0x23, 0x83];

    /// Encodes the [`L1BlockInfoArsia`] object into Ethereum transaction calldata.
    ///
    /// Writes the Arsia selector followed by the Jovian-format payload.
    pub fn encode_calldata(&self) -> Bytes {
        let mut buf = Vec::with_capacity(Self::L1_INFO_TX_LEN);
        self.encode_calldata_header(&mut buf);
        self.encode_calldata_body(&mut buf);
        buf.into()
    }

    /// Encodes the header (the Arsia function selector) into `buf`.
    pub fn encode_calldata_header(&self, buf: &mut Vec<u8>) {
        buf.extend_from_slice(Self::L1_INFO_TX_SELECTOR.as_ref());
    }

    /// Encodes the body (everything after the selector) into `buf`.
    ///
    /// Delegates to the Jovian body encoder since the payload layout is identical.
    pub fn encode_calldata_body(&self, buf: &mut Vec<u8>) {
        self.base.encode_calldata_body(buf);
    }

    /// Decodes the [`L1BlockInfoArsia`] object from Ethereum transaction calldata.
    pub fn decode_calldata(r: &[u8]) -> Result<Self, DecodeError> {
        if r.len() != Self::L1_INFO_TX_LEN {
            return Err(DecodeError::InvalidArsiaLength(Self::L1_INFO_TX_LEN, r.len()));
        }
        if r[..4] != Self::L1_INFO_TX_SELECTOR {
            return Err(DecodeError::InvalidSelector);
        }
        Self::decode_calldata_body(r)
    }

    /// Decodes the body of the [`L1BlockInfoArsia`] object.
    ///
    /// Delegates to the Jovian body decoder (payload layout is identical) and
    /// wraps the result in an `L1BlockInfoArsia`. The caller is responsible
    /// for validating length and selector beforehand (see [`Self::decode_calldata`]).
    pub fn decode_calldata_body(r: &[u8]) -> Result<Self, DecodeError> {
        // The Jovian body decoder reads payload bytes starting at offset 4 and
        // ignores the selector slot; it works unchanged for Arsia calldata
        // because the payload layouts are byte-for-byte identical.
        let base = L1BlockInfoJovian::decode_calldata_body(r)?;
        Ok(Self { base })
    }

    /// Construct from all values (mirrors `L1BlockInfoJovian::new`).
    #[allow(clippy::too_many_arguments)]
    pub const fn new(
        number: u64,
        time: u64,
        base_fee: u64,
        block_hash: B256,
        sequence_number: u64,
        batcher_address: Address,
        blob_base_fee: u128,
        blob_base_fee_scalar: u32,
        base_fee_scalar: u32,
        operator_fee_scalar: u32,
        operator_fee_constant: u64,
        da_footprint_gas_scalar: u16,
    ) -> Self {
        Self {
            base: L1BlockInfoJovian::new(
                number,
                time,
                base_fee,
                block_hash,
                sequence_number,
                batcher_address,
                blob_base_fee,
                blob_base_fee_scalar,
                base_fee_scalar,
                operator_fee_scalar,
                operator_fee_constant,
                da_footprint_gas_scalar,
            ),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alloc::vec;
    use alloy_primitives::keccak256;

    #[test]
    fn test_arsia_function_selector() {
        // The 4-byte selector must equal keccak256("setL1BlockValuesArsia()")[..4].
        // If this test fails, either the contract ABI changed upstream or the
        // const was mistyped.
        assert_eq!(
            keccak256("setL1BlockValuesArsia()")[..4].to_vec(),
            L1BlockInfoArsia::L1_INFO_TX_SELECTOR
        );
    }

    #[test]
    fn test_arsia_tx_length_matches_jovian() {
        // Arsia and Jovian must share an identical 178-byte calldata length.
        assert_eq!(L1BlockInfoArsia::L1_INFO_TX_LEN, 178);
        assert_eq!(L1BlockInfoArsia::L1_INFO_TX_LEN, L1BlockInfoJovian::L1_INFO_TX_LEN);
    }

    #[test]
    fn test_arsia_decode_calldata_invalid_length() {
        let r = vec![0u8; 1];
        assert_eq!(
            L1BlockInfoArsia::decode_calldata(&r),
            Err(DecodeError::InvalidArsiaLength(L1BlockInfoArsia::L1_INFO_TX_LEN, r.len()))
        );
    }

    #[test]
    fn test_arsia_decode_calldata_invalid_selector() {
        // Correct length, wrong selector — must reject before decoding the body.
        let mut r = vec![0u8; L1BlockInfoArsia::L1_INFO_TX_LEN];
        r[..4].copy_from_slice(&[0xde, 0xad, 0xbe, 0xef]);
        assert_eq!(
            L1BlockInfoArsia::decode_calldata(&r),
            Err(DecodeError::InvalidSelector)
        );
    }

    #[test]
    fn test_arsia_roundtrip_calldata_encoding() {
        // Synthetic round-trip: encode → decode → assert equal. Proves that
        // the Arsia encoder + decoder are inverses on the Jovian-identical
        // payload layout.
        let info = L1BlockInfoArsia::new(
            1,
            2,
            3,
            B256::from([4; 32]),
            5,
            Address::from_slice(&[6; 20]),
            7,
            8,
            9,
            10,
            11,
            12,
        );

        let calldata = info.encode_calldata();
        // First 4 bytes must be the Arsia selector, not Jovian's.
        assert_eq!(&calldata[..4], &L1BlockInfoArsia::L1_INFO_TX_SELECTOR);

        let decoded = L1BlockInfoArsia::decode_calldata(&calldata).unwrap();
        assert_eq!(info, decoded);
    }
}
