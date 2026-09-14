//! Deposit Transaction type.

use super::OpTxType;
use alloc::vec::Vec;
use alloy_consensus::{Sealable, Transaction, Typed2718};
use alloy_eips::{
    eip2718::{Decodable2718, Eip2718Error, Eip2718Result, Encodable2718, IsTyped2718},
    eip2930::AccessList,
};
use alloy_primitives::{Address, B256, Bytes, ChainId, Signature, TxHash, TxKind, U256, keccak256};
use alloy_rlp::{BufMut, Decodable, Encodable, Header};
use core::mem;

/// Deposit transactions, also known as deposits are initiated on L1, and executed on L2.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Default)]
#[cfg_attr(feature = "arbitrary", derive(arbitrary::Arbitrary))]
#[cfg_attr(feature = "serde", derive(serde::Serialize, serde::Deserialize))]
#[cfg_attr(feature = "serde", serde(rename_all = "camelCase"))]
pub struct TxDeposit {
    /// Hash that uniquely identifies the source of the deposit.
    pub source_hash: B256,
    /// The address of the sender account.
    pub from: Address,
    /// The address of the recipient account, or the null (zero-length) address if the deposited
    /// transaction is a contract creation.
    #[cfg_attr(feature = "serde", serde(default, skip_serializing_if = "TxKind::is_create"))]
    pub to: TxKind,
    /// The ETH value to mint on L2.
    #[cfg_attr(feature = "serde", serde(default, with = "alloy_serde::quantity"))]
    pub mint: u128,
    ///  The ETH value to send to the recipient account.
    pub value: U256,
    /// The gas limit for the L2 transaction.
    #[cfg_attr(feature = "serde", serde(with = "alloy_serde::quantity", rename = "gas"))]
    pub gas_limit: u64,
    /// Field indicating if this transaction is exempt from the L2 gas limit.
    #[cfg_attr(
        feature = "serde",
        serde(
            default,
            with = "alloy_serde::quantity",
            rename = "isSystemTx",
            skip_serializing_if = "core::ops::Not::not"
        )
    )]
    pub is_system_transaction: bool,
    /// `[MANTLE]` `BVM_ETH`: ETH value to mint on L2 (0 = no mint). Added by the
    /// Mantle protocol; serialised between `is_system_transaction` and
    /// `input` in the RLP wire format.
    ///
    /// `U256`, not `u128`: `OptimismPortal.depositTransaction` takes `_ethValue` as an
    /// unbounded `uint256` and op-node decodes the full 32-byte word into a `big.Int`.
    /// Narrowing diverged from op-node at or above 2^128. Widening is wire-compatible — RLP
    /// encodes integers as minimal big-endian, so the two are byte-identical below 2^128.
    #[cfg_attr(
        feature = "serde",
        // [MANTLE] No `alloy_serde::quantity` wrapper: `U256` already serialises as a
        // hex quantity, and the wrapper only supports the primitive uints.
        serde(default, rename = "ethValue")
    )]
    pub eth_value: U256,
    /// Input has two uses depending if transaction is Create or Call (if `to` field is None or
    /// Some).
    pub input: Bytes,
    /// `[MANTLE]` `BVM_ETH`: ETH value to transfer to recipient (None = no transfer).
    /// Optional trailing field — see `decode_optional_u256_from_rlp`. `U256` for the same
    /// reason as [`Self::eth_value`].
    #[cfg_attr(
        feature = "serde",
        serde(default, rename = "ethTxValue", skip_serializing_if = "Option::is_none")
    )]
    pub eth_tx_value: Option<U256>,
}

impl TxDeposit {
    /// Decodes the inner [`TxDeposit`] fields from RLP bytes.
    ///
    /// NOTE: This assumes a RLP header has already been decoded, and _just_ decodes the following
    /// RLP fields in the following order:
    ///
    /// - `source_hash`
    /// - `from`
    /// - `to`
    /// - `mint`
    /// - `value`
    /// - `gas_limit`
    /// - `is_system_transaction`
    /// - `eth_value`        (Mantle `BVM_ETH` mint amount)
    /// - `input`
    /// - `eth_tx_value`     (Mantle `BVM_ETH` tx value, optional / trailing)
    pub fn rlp_decode_fields(buf: &mut &[u8]) -> alloy_rlp::Result<Self> {
        Ok(Self {
            source_hash: Decodable::decode(buf)?,
            from: Decodable::decode(buf)?,
            to: Decodable::decode(buf)?,
            mint: Decodable::decode(buf)?,
            value: Decodable::decode(buf)?,
            gas_limit: Decodable::decode(buf)?,
            is_system_transaction: Decodable::decode(buf)?,
            eth_value: Decodable::decode(buf)?,
            input: Decodable::decode(buf)?,
            eth_tx_value: Self::decode_optional_u256_from_rlp(buf)?,
        })
    }

    /// Mantle `BVM_ETH`: decode optional trailing u128 field. Returns `None` when the buffer is
    /// empty (legacy deposit without `eth_tx_value`); otherwise decodes a u128.
    ///
    /// `[MANTLE]` Visibility restored to `pub` for ABI parity with upstream
    /// mantle-xyz/op-alloy@main — downstream crates may rely on calling this
    /// helper directly when constructing custom decoders.
    pub fn decode_optional_u256_from_rlp(buf: &mut &[u8]) -> alloy_rlp::Result<Option<U256>> {
        if buf.is_empty() {
            return Ok(None);
        }
        Ok(Some(Decodable::decode(buf)?))
    }

    /// Decodes the transaction from RLP bytes.
    pub fn rlp_decode(buf: &mut &[u8]) -> alloy_rlp::Result<Self> {
        let header = Header::decode(buf)?;
        if !header.list {
            return Err(alloy_rlp::Error::UnexpectedString);
        }
        if header.payload_length > buf.len() {
            return Err(alloy_rlp::Error::InputTooShort);
        }

        // [MANTLE] Decode fields strictly within this RLP payload so trailing bytes (e.g. the
        // next tx in a stream) are never interpreted as the optional `eth_tx_value` field of
        // the current tx. Equivalent to mantle-xyz/op-alloy commit 6637567 — supports
        // stream-style decoding without rejecting trailing data.
        let (payload, rest) = buf.split_at(header.payload_length);
        let mut payload_buf = payload;
        let this = Self::rlp_decode_fields(&mut payload_buf)?;

        if !payload_buf.is_empty() {
            return Err(alloy_rlp::Error::UnexpectedLength);
        }
        *buf = rest;

        Ok(this)
    }

    /// Outputs the length of the transaction's fields, without a RLP header or length of the
    /// eip155 fields.
    pub(crate) fn rlp_encoded_fields_length(&self) -> usize {
        self.source_hash.length() +
            self.from.length() +
            self.to.length() +
            self.mint.length() +
            self.value.length() +
            self.gas_limit.length() +
            self.is_system_transaction.length() +
            self.eth_value.length() +
            self.input.0.length() +
            self.eth_tx_value.map_or(0, |eth_tx_value| eth_tx_value.length())
    }

    /// Encodes only the transaction's fields into the desired buffer, without a RLP header.
    /// <https://github.com/ethereum-optimism/specs/blob/main/specs/protocol/deposits.md#the-deposited-transaction-type>
    pub(crate) fn rlp_encode_fields(&self, out: &mut dyn alloy_rlp::BufMut) {
        self.source_hash.encode(out);
        self.from.encode(out);
        self.to.encode(out);
        self.mint.encode(out);
        self.value.encode(out);
        self.gas_limit.encode(out);
        self.is_system_transaction.encode(out);
        self.eth_value.encode(out);
        self.input.encode(out);
        if let Some(eth_tx_value) = self.eth_tx_value {
            eth_tx_value.encode(out);
        }
    }

    /// Calculates a heuristic for the in-memory size of the [`TxDeposit`] transaction.
    #[inline]
    pub fn size(&self) -> usize {
        mem::size_of::<B256>() + // source_hash
        mem::size_of::<Address>() + // from
        self.to.size() + // to
        mem::size_of::<u128>() + // mint
        mem::size_of::<U256>() + // value
        mem::size_of::<u128>() + // gas_limit
        mem::size_of::<bool>() + // is_system_transaction
        mem::size_of::<U256>() + // eth_value (Mantle BVM_ETH)
        self.input.len() + // input
        mem::size_of::<Option<u128>>() // eth_tx_value (Mantle BVM_ETH)
    }

    /// Get the transaction type
    pub(crate) const fn tx_type(&self) -> OpTxType {
        OpTxType::Deposit
    }

    /// Create an rlp header for the transaction.
    fn rlp_header(&self) -> Header {
        Header { list: true, payload_length: self.rlp_encoded_fields_length() }
    }

    /// RLP encodes the transaction.
    pub fn rlp_encode(&self, out: &mut dyn BufMut) {
        self.rlp_header().encode(out);
        self.rlp_encode_fields(out);
    }

    /// Get the length of the transaction when RLP encoded.
    pub fn rlp_encoded_length(&self) -> usize {
        self.rlp_header().length_with_payload()
    }

    /// Get the length of the transaction when EIP-2718 encoded. This is the
    /// 1 byte type flag + the length of the RLP encoded transaction.
    pub fn eip2718_encoded_length(&self) -> usize {
        self.rlp_encoded_length() + 1
    }

    fn network_header(&self) -> Header {
        Header { list: false, payload_length: self.eip2718_encoded_length() }
    }

    /// Get the length of the transaction when network encoded. This is the
    /// EIP-2718 encoded length with an outer RLP header.
    pub fn network_encoded_length(&self) -> usize {
        self.network_header().length_with_payload()
    }

    /// Network encode the transaction with the given signature.
    pub fn network_encode(&self, out: &mut dyn BufMut) {
        self.network_header().encode(out);
        self.encode_2718(out);
    }

    /// Calculate the transaction hash.
    pub fn tx_hash(&self) -> TxHash {
        let mut buf = Vec::with_capacity(self.eip2718_encoded_length());
        self.encode_2718(&mut buf);
        keccak256(&buf)
    }

    /// Returns the signature for the optimism deposit transactions, which don't include a
    /// signature.
    pub const fn signature() -> Signature {
        Signature::new(U256::ZERO, U256::ZERO, false)
    }
}

impl Typed2718 for TxDeposit {
    fn ty(&self) -> u8 {
        OpTxType::Deposit as u8
    }
}

impl IsTyped2718 for TxDeposit {
    fn is_type(ty: u8) -> bool {
        OpTxType::Deposit as u8 == ty
    }
}

impl Transaction for TxDeposit {
    fn chain_id(&self) -> Option<ChainId> {
        None
    }

    fn nonce(&self) -> u64 {
        0u64
    }

    fn gas_limit(&self) -> u64 {
        self.gas_limit
    }

    fn gas_price(&self) -> Option<u128> {
        None
    }

    fn max_fee_per_gas(&self) -> u128 {
        0
    }

    fn max_priority_fee_per_gas(&self) -> Option<u128> {
        None
    }

    fn max_fee_per_blob_gas(&self) -> Option<u128> {
        None
    }

    fn priority_fee_or_price(&self) -> u128 {
        0
    }

    fn effective_gas_price(&self, _: Option<u64>) -> u128 {
        0
    }

    fn is_dynamic_fee(&self) -> bool {
        false
    }

    fn kind(&self) -> TxKind {
        self.to
    }

    fn is_create(&self) -> bool {
        self.to.is_create()
    }

    fn value(&self) -> U256 {
        self.value
    }

    fn input(&self) -> &Bytes {
        &self.input
    }

    fn access_list(&self) -> Option<&AccessList> {
        None
    }

    fn blob_versioned_hashes(&self) -> Option<&[B256]> {
        None
    }

    fn authorization_list(&self) -> Option<&[alloy_eips::eip7702::SignedAuthorization]> {
        None
    }
}

impl Encodable2718 for TxDeposit {
    fn type_flag(&self) -> Option<u8> {
        Some(OpTxType::Deposit as u8)
    }

    fn encode_2718_len(&self) -> usize {
        self.eip2718_encoded_length()
    }

    fn encode_2718(&self, out: &mut dyn alloy_rlp::BufMut) {
        out.put_u8(self.tx_type() as u8);
        self.rlp_encode(out);
    }
}

impl Decodable2718 for TxDeposit {
    fn typed_decode(ty: u8, data: &mut &[u8]) -> Eip2718Result<Self> {
        let ty: OpTxType = ty.try_into().map_err(|_| Eip2718Error::UnexpectedType(ty))?;
        if ty != OpTxType::Deposit as u8 {
            return Err(Eip2718Error::UnexpectedType(ty as u8));
        }
        let tx = Self::decode(data)?;
        Ok(tx)
    }

    fn fallback_decode(_data: &mut &[u8]) -> Eip2718Result<Self> {
        // Deposits have no untyped form: reaching untyped dispatch means the 0x7E tag was absent,
        // so reject rather than resurrect the type from the body.
        Err(Eip2718Error::UnexpectedType(OpTxType::Deposit as u8))
    }
}

impl Encodable for TxDeposit {
    fn encode(&self, out: &mut dyn BufMut) {
        Header { list: true, payload_length: self.rlp_encoded_fields_length() }.encode(out);
        self.rlp_encode_fields(out);
    }

    fn length(&self) -> usize {
        let payload_length = self.rlp_encoded_fields_length();
        Header { list: true, payload_length }.length() + payload_length
    }
}

impl Decodable for TxDeposit {
    fn decode(data: &mut &[u8]) -> alloy_rlp::Result<Self> {
        Self::rlp_decode(data)
    }
}

impl Sealable for TxDeposit {
    fn hash_slow(&self) -> B256 {
        self.tx_hash()
    }
}

#[cfg(feature = "alloy-compat")]
impl From<TxDeposit> for alloy_rpc_types_eth::TransactionRequest {
    fn from(tx: TxDeposit) -> Self {
        let TxDeposit {
            source_hash: _,
            from,
            to,
            mint: _,
            value,
            gas_limit,
            is_system_transaction: _,
            input,
            // Mantle BVM_ETH fields: target is alloy_rpc_types_eth::TransactionRequest,
            // a standard Ethereum type with no BVM_ETH concept — ignore.
            eth_value: _,
            eth_tx_value: _,
        } = tx;

        Self {
            from: Some(from),
            to: Some(to),
            value: Some(value),
            gas: Some(gas_limit),
            input: input.into(),
            ..Default::default()
        }
    }
}

/// A trait representing a deposit transaction with specific attributes.
pub trait DepositTransaction: Transaction {
    /// Returns the hash that uniquely identifies the source of the deposit.
    ///
    /// # Returns
    /// An `Option<B256>` containing the source hash if available.
    fn source_hash(&self) -> Option<B256>;

    /// Returns the optional mint value of the deposit transaction.
    ///
    /// # Returns
    /// An `u128` representing the ETH value to mint on L2, if any.
    fn mint(&self) -> u128;

    /// Indicates whether the transaction is exempt from the L2 gas limit.
    ///
    /// # Returns
    /// A `bool` indicating if the transaction is a system transaction.
    fn is_system_transaction(&self) -> bool;
}

impl DepositTransaction for TxDeposit {
    #[inline]
    fn source_hash(&self) -> Option<B256> {
        Some(self.source_hash)
    }

    #[inline]
    fn mint(&self) -> u128 {
        self.mint
    }

    #[inline]
    fn is_system_transaction(&self) -> bool {
        self.is_system_transaction
    }
}

/// Deposit transactions don't have a signature, however, we include an empty signature in the
/// response for better compatibility.
///
/// This function can be used as `serialize_with` serde attribute for the [`TxDeposit`] and will
/// flatten [`TxDeposit::signature`] into response.
#[cfg(feature = "serde")]
pub fn serde_deposit_tx_rpc<T: serde::Serialize, S: serde::Serializer>(
    value: &T,
    serializer: S,
) -> Result<S::Ok, S::Error> {
    use serde::Serialize;

    #[derive(Serialize)]
    struct SerdeHelper<'a, T> {
        #[serde(flatten)]
        value: &'a T,
        #[serde(flatten)]
        signature: Signature,
    }

    SerdeHelper { value, signature: TxDeposit::signature() }.serialize(serializer)
}

#[cfg(test)]
mod tests {
    use super::*;
    use alloy_primitives::{address, b256, hex};
    use alloy_rlp::BytesMut;

    #[test]
    fn test_deposit_transaction_trait() {
        let tx = TxDeposit {
            source_hash: B256::with_last_byte(42),
            from: Address::default(),
            to: TxKind::default(),
            mint: 100,
            value: U256::from(1000),
            gas_limit: 50000,
            is_system_transaction: true,
            input: Bytes::default(),
            eth_value: U256::from(100u128),
            eth_tx_value: Some(U256::from(100u128)),
        };

        assert_eq!(tx.source_hash(), Some(B256::with_last_byte(42)));
        assert_eq!(tx.mint(), 100);
        assert!(tx.is_system_transaction());
    }

    #[test]
    fn test_deposit_transaction_without_mint() {
        let tx = TxDeposit {
            source_hash: B256::default(),
            from: Address::default(),
            to: TxKind::default(),
            mint: 0,
            value: U256::default(),
            gas_limit: 50000,
            is_system_transaction: false,
            input: Bytes::default(),
            eth_value: U256::from(100u128),
            eth_tx_value: Some(U256::from(100u128)),
        };

        assert_eq!(tx.source_hash(), Some(B256::default()));
        assert_eq!(tx.mint(), 0);
        assert!(!tx.is_system_transaction());
    }

    #[test]
    fn test_deposit_transaction_to_contract() {
        let contract_address = Address::with_last_byte(0xFF);
        let tx = TxDeposit {
            source_hash: B256::default(),
            from: Address::default(),
            to: TxKind::Call(contract_address),
            mint: 200,
            value: U256::from(500),
            gas_limit: 100000,
            is_system_transaction: false,
            input: Bytes::from_static(&[1, 2, 3]),
            eth_value: U256::from(100u128),
            eth_tx_value: Some(U256::from(100u128)),
        };

        assert_eq!(tx.source_hash(), Some(B256::default()));
        assert_eq!(tx.mint(), 200);
        assert!(!tx.is_system_transaction());
        assert_eq!(tx.kind(), TxKind::Call(contract_address));
    }

    #[test]
    fn test_rlp_roundtrip() {
        // [MANTLE] Original test used a hex literal captured from upstream-OP
        // mainnet deposit traffic — that hex no longer roundtrips after the
        // Mantle BVM_ETH wire-format addition (`eth_value: u128` + trailing
        // `eth_tx_value: Option<u128>`). Replaced with a synthetic encode →
        // decode → encode roundtrip that proves the encoder and decoder are
        // inverses on the current Mantle field set.
        let original = TxDeposit {
            source_hash: b256!("44bae9d41b8380d781187b426c6fe43df5fb2fb57bd4466ef6a701e1f01e0156"),
            from: address!("deaddeaddeaddeaddeaddeaddeaddeaddead0001"),
            to: TxKind::Call(address!("4200000000000000000000000000000000000015")),
            mint: 0,
            value: U256::ZERO,
            gas_limit: 150_000_000,
            is_system_transaction: true,
            eth_value: U256::from(100u128),
            input: Bytes::from_static(&hex!(
                "015d8eb900000000000000000000000000000000000000000000000000000000008057650000000000000000000000000000000000000000000000000000000063d96d10000000000000000000000000000000000000000000000000000000000009f35273d89754a1e0387b89520d989d3be9c37c1f32495a88faf1ea05c61121ab0d1900000000000000000000000000000000000000000000000000000000000000010000000000000000000000002d679b567db6187c0c8323fa982cfb88b74dbcc7000000000000000000000000000000000000000000000000000000000000083400000000000000000000000000000000000000000000000000000000000f4240"
            )),
            eth_tx_value: Some(U256::from(100u128)),
        };

        let mut encoded = BytesMut::default();
        original.encode(&mut encoded);
        let decoded = TxDeposit::decode(&mut encoded.as_ref()).unwrap();
        assert_eq!(decoded, original);

        let mut re_encoded = BytesMut::default();
        decoded.encode(&mut re_encoded);
        assert_eq!(re_encoded.as_ref(), encoded.as_ref());
    }

    #[test]
    fn test_encode_decode_fields() {
        let original = TxDeposit {
            source_hash: B256::default(),
            from: Address::default(),
            to: TxKind::default(),
            mint: 100,
            value: U256::default(),
            gas_limit: 50000,
            is_system_transaction: true,
            input: Bytes::default(),
            eth_value: U256::from(100u128),
            eth_tx_value: Some(U256::from(100u128)),
        };

        let mut buffer = BytesMut::new();
        original.rlp_encode_fields(&mut buffer);
        let decoded = TxDeposit::rlp_decode_fields(&mut &buffer[..]).expect("Failed to decode");

        assert_eq!(original, decoded);
    }

    #[test]
    fn test_encode_with_and_without_header() {
        let tx_deposit = TxDeposit {
            source_hash: B256::default(),
            from: Address::default(),
            to: TxKind::default(),
            mint: 100,
            value: U256::default(),
            gas_limit: 50000,
            is_system_transaction: true,
            input: Bytes::default(),
            eth_value: U256::from(100u128),
            eth_tx_value: Some(U256::from(100u128)),
        };

        let mut buffer_with_header = BytesMut::new();
        tx_deposit.encode(&mut buffer_with_header);

        let mut buffer_without_header = BytesMut::new();
        tx_deposit.rlp_encode_fields(&mut buffer_without_header);

        assert!(buffer_with_header.len() > buffer_without_header.len());
    }

    #[test]
    fn test_payload_length() {
        let tx_deposit = TxDeposit {
            source_hash: B256::default(),
            from: Address::default(),
            to: TxKind::default(),
            mint: 100,
            value: U256::default(),
            gas_limit: 50000,
            is_system_transaction: true,
            input: Bytes::default(),
            eth_value: U256::from(100u128),
            eth_tx_value: Some(U256::from(100u128)),
        };

        assert!(tx_deposit.size() > tx_deposit.rlp_encoded_fields_length());
    }

    #[test]
    fn test_encode_inner_with_and_without_header() {
        let tx_deposit = TxDeposit {
            source_hash: B256::default(),
            from: Address::default(),
            to: TxKind::default(),
            mint: 100,
            value: U256::default(),
            gas_limit: 50000,
            is_system_transaction: true,
            input: Bytes::default(),
            eth_value: U256::from(100u128),
            eth_tx_value: Some(U256::from(100u128)),
        };

        let mut buffer_with_header = BytesMut::new();
        tx_deposit.network_encode(&mut buffer_with_header);

        let mut buffer_without_header = BytesMut::new();
        tx_deposit.encode_2718(&mut buffer_without_header);

        assert!(buffer_with_header.len() > buffer_without_header.len());
    }

    #[test]
    fn test_payload_length_header() {
        let tx_deposit = TxDeposit {
            source_hash: B256::default(),
            from: Address::default(),
            to: TxKind::default(),
            mint: 100,
            value: U256::default(),
            gas_limit: 50000,
            is_system_transaction: true,
            input: Bytes::default(),
            eth_value: U256::from(100u128),
            eth_tx_value: Some(U256::from(100u128)),
        };

        let total_len = tx_deposit.network_encoded_length();
        let len_without_header = tx_deposit.eip2718_encoded_length();

        assert!(total_len > len_without_header);
    }
    #[test]
    fn test_deposit_tx_roundtrip() {
        // [MANTLE] Original test pinned a basescan-captured hex literal that
        // pre-dates the BVM_ETH wire-format addition. Rewritten as a
        // synthetic encode → decode → encode roundtrip so the test exercises
        // the current Mantle field set (eth_value + optional eth_tx_value).
        let txs = [
            TxDeposit {
                source_hash: b256!(
                    "871ec5fb6afe7e5ae950bbb4cfd7d7cb277b413e67da806d50834a814b14c9f4"
                ),
                from: address!("deaddeaddeaddeaddeaddeaddeaddeaddead0001"),
                to: TxKind::Call(address!("4200000000000000000000000000000000000015")),
                mint: 0,
                value: U256::ZERO,
                gas_limit: 1_000_000,
                is_system_transaction: false,
                eth_value: U256::from(100u128),
                input: Bytes::from_static(&hex!(
                    "440a5e20000008dd00101c12000000000000000400000000681c941f0000000001566261000000000000000000000000000000000000000000000000000000005f629c020000000000000000000000000000000000000000000000000000000000000001937badfbcce566e0ba932a3f7659644aa0c6ef019541d3134a1d8cb9f84d45c70000000000000000000000005050f69a9786f081509234f1a7f4684b5e5b76c9"
                )),
                eth_tx_value: Some(U256::from(100u128)),
            },
            TxDeposit {
                source_hash: b256!(
                    "0000000000000000000000000000000000000000000000000000000000000001"
                ),
                from: Address::ZERO,
                to: TxKind::Create,
                mint: 7,
                value: U256::from(11_u64),
                gas_limit: 21_000,
                is_system_transaction: false,
                eth_value: U256::ZERO,
                input: Bytes::new(),
                eth_tx_value: None,
            },
        ];

        for original in &txs {
            // encode_2718 (with eip2718 header) round-trip
            let mut encoded = BytesMut::new();
            original.encode_2718(&mut encoded);
            let decoded = TxDeposit::decode_2718(&mut encoded.as_ref()).unwrap();
            assert_eq!(&decoded, original, "encode_2718/decode_2718 not symmetric");

            // rlp_encode / rlp_decode field-level round-trip
            let mut encoded_fields = BytesMut::new();
            original.rlp_encode(&mut encoded_fields);
            let decoded_fields = TxDeposit::rlp_decode(&mut encoded_fields.as_ref()).unwrap();
            assert_eq!(&decoded_fields, original, "rlp_encode/rlp_decode not symmetric");
        }
    }

    // [MANTLE] Coverage tests for the BVM_ETH `eth_value` / `eth_tx_value` fields.
    // Port of mantle-xyz/op-alloy@3dc9696 ("test: update test case"). Exercises
    // zero / max boundary values and EIP-2718 round-trips through the new wire
    // format.
    #[test]
    fn test_eth_value_zero() {
        let tx_deposit = TxDeposit {
            source_hash: B256::default(),
            from: Address::default(),
            to: TxKind::default(),
            mint: 100,
            value: U256::default(),
            gas_limit: 50000,
            is_system_transaction: true,
            input: Bytes::default(),
            eth_value: U256::ZERO, // Test zero value
            eth_tx_value: Some(U256::from(100u128)),
        };

        let mut buffer = BytesMut::new();
        tx_deposit.rlp_encode_fields(&mut buffer);
        let decoded = TxDeposit::rlp_decode_fields(&mut &buffer[..]).expect("Failed to decode");

        assert_eq!(tx_deposit, decoded);
        assert_eq!(decoded.eth_value, U256::from(0u128));
    }

    #[test]
    fn test_eth_value_and_eth_tx_value_both_zero() {
        let tx_deposit = TxDeposit {
            source_hash: B256::default(),
            from: Address::default(),
            to: TxKind::default(),
            mint: 100,
            value: U256::default(),
            gas_limit: 50000,
            is_system_transaction: true,
            input: Bytes::default(),
            eth_value: U256::ZERO,
            eth_tx_value: Some(U256::from(0u128)), // Test zero value
        };

        let mut buffer = BytesMut::new();
        tx_deposit.rlp_encode_fields(&mut buffer);
        let decoded = TxDeposit::rlp_decode_fields(&mut &buffer[..]).expect("Failed to decode");

        assert_eq!(tx_deposit, decoded);
        assert_eq!(decoded.eth_value, U256::from(0u128));
        assert_eq!(decoded.eth_tx_value, Some(U256::from(0u128)));
    }

    #[test]
    fn test_eth_value_max() {
        let tx_deposit = TxDeposit {
            source_hash: B256::default(),
            from: Address::default(),
            to: TxKind::default(),
            mint: 100,
            value: U256::default(),
            gas_limit: 50000,
            is_system_transaction: true,
            input: Bytes::default(),
            eth_value: U256::from(u128::MAX), // Test maximum value
            eth_tx_value: Some(U256::from(u128::MAX)),
        };

        let mut buffer = BytesMut::new();
        tx_deposit.rlp_encode_fields(&mut buffer);
        let decoded = TxDeposit::rlp_decode_fields(&mut &buffer[..]).expect("Failed to decode");

        assert_eq!(tx_deposit, decoded);
        assert_eq!(decoded.eth_value, U256::from(u128::MAX));
        assert_eq!(decoded.eth_tx_value, Some(U256::from(u128::MAX)));
    }

    #[test]
    fn test_eip2718_encode_decode_with_new_fields() {
        let tx_deposit = TxDeposit {
            source_hash: B256::with_last_byte(42),
            from: Address::with_last_byte(1),
            to: TxKind::Call(Address::with_last_byte(2)),
            mint: 1000,
            value: U256::from(5000),
            gas_limit: 100000,
            is_system_transaction: false,
            input: Bytes::from_static(&[1, 2, 3, 4]),
            eth_value: U256::from(200u128),
            eth_tx_value: Some(U256::from(300u128)),
        };

        // Test EIP-2718 encoding
        let mut encoded = BytesMut::new();
        tx_deposit.encode_2718(&mut encoded);

        // Test EIP-2718 decoding
        let mut encoded_slice = encoded.as_ref();
        let decoded = TxDeposit::decode_2718(&mut encoded_slice).expect("Failed to decode");

        assert_eq!(tx_deposit, decoded);
        assert_eq!(decoded.eth_value, U256::from(200u128));
        assert_eq!(decoded.eth_tx_value, Some(U256::from(300u128)));
    }

    #[test]
    fn test_eip2718_encode_decode_with_eth_tx_value_none() {
        let tx_deposit = TxDeposit {
            source_hash: B256::with_last_byte(42),
            from: Address::with_last_byte(1),
            to: TxKind::Call(Address::with_last_byte(2)),
            mint: 1000,
            value: U256::from(5000),
            gas_limit: 100000,
            is_system_transaction: false,
            input: Bytes::from_static(&[1, 2, 3, 4]),
            eth_value: U256::from(200u128),
            eth_tx_value: None, // Test None value
        };

        // Test EIP-2718 encoding
        let mut encoded = BytesMut::new();
        tx_deposit.encode_2718(&mut encoded);

        // Test EIP-2718 decoding
        let mut encoded_slice = encoded.as_ref();
        let decoded = TxDeposit::decode_2718(&mut encoded_slice).expect("Failed to decode");

        assert_eq!(tx_deposit, decoded);
        assert_eq!(decoded.eth_value, U256::from(200u128));
        assert_eq!(decoded.eth_tx_value, None);
    }

    #[test]
    fn test_decode_optional_u128_boundary_values() {
        // Values that encode to short-string headers (0x82 0xXX 0xXX). All valid u128
        // encodings start below 0xc0, so the strict decoder accepts them and yields
        // `Ok(Some(value))`. Sanity-checks that the decoder consumes the buffer exactly.
        let test_values = [
            0x8000u128, // Encodes to 0x82 0x80 0x00
            0xffffu128, // Encodes to 0x82 0xff 0xff
        ];

        for value in test_values {
            let mut encoded = BytesMut::new();
            value.encode(&mut encoded);
            let first_byte = encoded[0];

            // Only test if the encoding is valid for u128 (first byte < 0xc0)
            if first_byte < 0xc0 {
                let mut buf = encoded.as_ref();
                let result = TxDeposit::decode_optional_u256_from_rlp(&mut buf);
                assert_eq!(
                    result.unwrap(),
                    Some(U256::from(value)),
                    "Failed to decode value: {value}"
                );
                assert!(buf.is_empty(), "Buffer should be consumed after decoding");
            }
        }
    }

    // [MANTLE] Malformed-input tests for the optional `eth_tx_value` BVM_ETH field.
    // Port of mantle-xyz/op-alloy@498abec: "fix(consensus): make optional ethTxValue
    // RLP decode strict and add malformed-input tests". The companion implementation
    // change (return error rather than silently None on decode failure) is already
    // in place in `decode_optional_u256_from_rlp`.
    #[test]
    fn test_rlp_decode_fields_rejects_malformed_present_eth_tx_value() {
        let tx_deposit = TxDeposit {
            source_hash: B256::default(),
            from: Address::default(),
            to: TxKind::default(),
            mint: 100,
            value: U256::default(),
            gas_limit: 50000,
            is_system_transaction: true,
            input: Bytes::default(),
            eth_value: U256::from(100u128),
            eth_tx_value: None,
        };

        let mut buffer = BytesMut::new();
        tx_deposit.rlp_encode_fields(&mut buffer);
        // Simulate an explicitly present but malformed eth_tx_value field (list instead of
        // integer).
        buffer.extend_from_slice(&[0xc0]);

        let result = TxDeposit::rlp_decode_fields(&mut &buffer[..]);
        assert!(result.is_err());
    }

    /// `[MANTLE]` Builds a deposit carrying both `BVM_ETH` values, for the serde tests below.
    #[cfg(feature = "serde")]
    fn bvm_eth_deposit() -> TxDeposit {
        TxDeposit {
            source_hash: B256::with_last_byte(9),
            from: address!("1111111111111111111111111111111111111111"),
            to: TxKind::Call(address!("2222222222222222222222222222222222222222")),
            mint: 7,
            value: U256::from(11u64),
            gas_limit: 21_000,
            is_system_transaction: false,
            input: Bytes::from_static(&[0xAB, 0xCD]),
            eth_value: U256::from(123_456_000_000_000_000u128),
            eth_tx_value: Some(U256::from(42u128)),
        }
    }

    /// `[MANTLE]` The RLP path is covered above; this pins the **serde** path, which nothing else
    /// tested. It matters because `alloy_compat`'s `TryFrom<UnknownTypedTransaction> for
    /// TxDeposit` converts RPC responses via `fields.deserialize_into()` — serde is the only
    /// thing carrying `BVM_ETH` there.
    #[test]
    #[cfg(feature = "serde")]
    fn test_serde_json_roundtrip_preserves_bvm_eth() {
        let tx = bvm_eth_deposit();
        let json = serde_json::to_string(&tx).unwrap();
        let back: TxDeposit = serde_json::from_str(&json).unwrap();
        assert_eq!(back.eth_value, tx.eth_value, "eth_value did not survive JSON round-trip");
        assert_eq!(back.eth_tx_value, tx.eth_tx_value, "eth_tx_value did not survive round-trip");
        assert_eq!(back, tx);
    }

    /// `[MANTLE]` Pins the wire names and the quantity (hex) encoding. A rename or a switch away
    /// from `alloy_serde::quantity` would silently change what op-node/RPC peers must send, and
    /// `serde(default)` would then hand us `eth_value = 0` with no error.
    #[test]
    #[cfg(feature = "serde")]
    fn test_serde_json_field_names_are_eth_value_and_eth_tx_value() {
        let json = serde_json::to_value(bvm_eth_deposit()).unwrap();
        assert_eq!(
            json.get("ethValue").and_then(|v| v.as_str()),
            Some("0x1b69a93f1640000"), // 123_456_000_000_000_000
            "ethValue must serialise as a hex quantity under exactly this key"
        );
        assert_eq!(
            json.get("ethTxValue").and_then(|v| v.as_str()),
            Some("0x2a"),
            "ethTxValue must serialise as a hex quantity under exactly this key"
        );
    }

    /// `[MANTLE]` `skip_serializing_if = "Option::is_none"`: a deposit with no `BVM_ETH` transfer
    /// must not emit the key at all, and must read back as `None` (not `Some(0)`).
    #[test]
    #[cfg(feature = "serde")]
    fn test_serde_json_omits_eth_tx_value_when_none() {
        let tx = TxDeposit { eth_tx_value: None, ..bvm_eth_deposit() };
        let json = serde_json::to_value(&tx).unwrap();
        assert!(json.get("ethTxValue").is_none(), "ethTxValue must be omitted when None");
        let back: TxDeposit = serde_json::from_value(json).unwrap();
        assert_eq!(back.eth_tx_value, None);
        assert_eq!(back.eth_value, tx.eth_value, "omitting ethTxValue must not disturb ethValue");
    }

    /// `[MANTLE]` Documents the one genuinely lossy serde behaviour: `serde(default)` means a
    /// payload with no `ethValue` deserialises to `0` — i.e. "no `BVM_ETH` mint" — rather than
    /// failing. That is required for compatibility with non-Mantle payloads, but it also means a
    /// producer that forgets the field is indistinguishable from one that means zero. If this
    /// test ever has to change, re-read how `alloy_compat` feeds RPC data into `TxDeposit`.
    #[test]
    #[cfg(feature = "serde")]
    fn test_serde_json_missing_eth_value_defaults_to_zero_silently() {
        let mut json = serde_json::to_value(bvm_eth_deposit()).unwrap();
        json.as_object_mut().unwrap().remove("ethValue");
        let back: TxDeposit = serde_json::from_value(json).unwrap();
        assert_eq!(back.eth_value, 0, "a missing ethValue must default to 0, not error");
    }

    #[test]
    fn test_decode_2718_rejects_malformed_present_eth_tx_value() {
        let tx_deposit = TxDeposit {
            source_hash: B256::with_last_byte(42),
            from: Address::with_last_byte(1),
            to: TxKind::Call(Address::with_last_byte(2)),
            mint: 1000,
            value: U256::from(5000),
            gas_limit: 100000,
            is_system_transaction: false,
            input: Bytes::from_static(&[1, 2, 3, 4]),
            eth_value: U256::from(200u128),
            // Use a one-byte integer so we can mutate it in-place without changing length fields.
            eth_tx_value: Some(U256::from(1u128)),
        };

        let mut encoded = BytesMut::new();
        tx_deposit.encode_2718(&mut encoded);
        *encoded.last_mut().expect("encoded tx should not be empty") = 0xc0;

        let mut encoded_slice = encoded.as_ref();
        let result = TxDeposit::decode_2718(&mut encoded_slice);
        assert!(result.is_err());
    }
}

/// Bincode-compatible [`TxDeposit`] serde implementation.
#[cfg(all(feature = "serde", feature = "serde-bincode-compat"))]
pub(super) mod serde_bincode_compat {
    use alloc::borrow::Cow;
    use alloy_primitives::{Address, B256, Bytes, TxKind, U256};
    use serde::{Deserialize, Deserializer, Serialize, Serializer};
    use serde_with::{DeserializeAs, SerializeAs};

    /// Bincode-compatible [`super::TxDeposit`] serde implementation.
    ///
    /// Intended to use with the [`serde_with::serde_as`] macro in the following way:
    /// ```rust
    /// use op_alloy_consensus::{TxDeposit, serde_bincode_compat};
    /// use serde::{Deserialize, Serialize};
    /// use serde_with::serde_as;
    ///
    /// #[serde_as]
    /// #[derive(Serialize, Deserialize)]
    /// struct Data {
    ///     #[serde_as(as = "serde_bincode_compat::TxDeposit")]
    ///     transaction: TxDeposit,
    /// }
    /// ```
    #[derive(Debug, Serialize, Deserialize)]
    pub struct TxDeposit<'a> {
        source_hash: B256,
        from: Address,
        #[serde(default)]
        to: TxKind,
        #[serde(default)]
        mint: u128,
        value: U256,
        gas_limit: u64,
        is_system_transaction: bool,
        /// `[MANTLE]` `BVM_ETH` mint amount.
        ///
        /// `U256`, matching [`super::TxDeposit`] and op-node's `big.Int`. Widening this changed
        /// the bincode byte width (ruint writes a length-prefixed big-endian byte string, not a
        /// fixed 16-byte integer), so bincode blobs written by an older binary do not decode
        /// here. bincode-compat is an in-process/IPC shim, not a persisted format -- nothing in
        /// this workspace or in mantle-xyz/reth stores it across restarts.
        #[serde(default)]
        eth_value: U256,
        input: Cow<'a, Bytes>,
        /// `[MANTLE]` `BVM_ETH` transfer value (None when omitted).
        ///
        /// **No `skip_serializing_if` here, unlike the JSON-facing field on
        /// [`super::TxDeposit`].** bincode is not self-describing: skipping the field writes
        /// nothing, and the decoder — which reads positionally — then runs off the end with
        /// `UnexpectedEnd`. `serde(default)` cannot rescue it, because bincode never learns the
        /// field was absent. Every deposit that transfers no `BVM_ETH` (i.e. most of them)
        /// would
        /// fail to round-trip. Pinned by
        /// `test_tx_deposit_bincode_roundtrip_eth_tx_value_none_and_some`.
        #[serde(default)]
        eth_tx_value: Option<U256>,
    }

    impl<'a> From<&'a super::TxDeposit> for TxDeposit<'a> {
        fn from(value: &'a super::TxDeposit) -> Self {
            Self {
                source_hash: value.source_hash,
                from: value.from,
                to: value.to,
                mint: value.mint,
                value: value.value,
                gas_limit: value.gas_limit,
                is_system_transaction: value.is_system_transaction,
                eth_value: value.eth_value,
                input: Cow::Borrowed(&value.input),
                eth_tx_value: value.eth_tx_value,
            }
        }
    }

    impl<'a> From<TxDeposit<'a>> for super::TxDeposit {
        fn from(value: TxDeposit<'a>) -> Self {
            // [MANTLE] bincode_compat now carries BVM_ETH `eth_value` and
            // optional `eth_tx_value` through round-trip — fixes the prior
            // TODO where Mantle data was dropped on the floor.
            Self {
                source_hash: value.source_hash,
                from: value.from,
                to: value.to,
                mint: value.mint,
                value: value.value,
                gas_limit: value.gas_limit,
                is_system_transaction: value.is_system_transaction,
                eth_value: value.eth_value,
                input: value.input.into_owned(),
                eth_tx_value: value.eth_tx_value,
            }
        }
    }

    impl SerializeAs<super::TxDeposit> for TxDeposit<'_> {
        fn serialize_as<S>(source: &super::TxDeposit, serializer: S) -> Result<S::Ok, S::Error>
        where
            S: Serializer,
        {
            TxDeposit::from(source).serialize(serializer)
        }
    }

    impl<'de> DeserializeAs<'de, super::TxDeposit> for TxDeposit<'de> {
        fn deserialize_as<D>(deserializer: D) -> Result<super::TxDeposit, D::Error>
        where
            D: Deserializer<'de>,
        {
            TxDeposit::deserialize(deserializer).map(Into::into)
        }
    }

    #[cfg(test)]
    mod tests {
        use alloy_primitives::U256;
        use arbitrary::Arbitrary;
        use rand::Rng;
        use serde::{Deserialize, Serialize};
        use serde_with::serde_as;

        use super::super::{TxDeposit, serde_bincode_compat};

        /// `[MANTLE]` Deterministic counterpart to the randomised round-trip below.
        ///
        /// `eth_tx_value: None` is the common case (any deposit that transfers no `BVM_ETH`),
        /// and
        /// it is exactly the case the random test only hits by chance. Pin both polarities.
        #[test]
        fn test_tx_deposit_bincode_roundtrip_eth_tx_value_none_and_some() {
            #[serde_as]
            #[derive(Debug, PartialEq, Eq, Serialize, Deserialize)]
            struct Data {
                #[serde_as(as = "serde_bincode_compat::TxDeposit")]
                transaction: TxDeposit,
            }

            for eth_tx_value in [None, Some(U256::ZERO), Some(U256::from(7u128)), Some(U256::MAX)] {
                let data = Data {
                    transaction: TxDeposit {
                        source_hash: alloy_primitives::B256::with_last_byte(3),
                        eth_value: U256::from(5u128),
                        eth_tx_value,
                        ..Default::default()
                    },
                };
                let encoded =
                    bincode::serde::encode_to_vec(&data, bincode::config::legacy()).unwrap();
                let (decoded, _) = bincode::serde::decode_from_slice::<Data, _>(
                    &encoded,
                    bincode::config::legacy(),
                )
                .unwrap_or_else(|e| panic!("decode failed for eth_tx_value={eth_tx_value:?}: {e}"));
                assert_eq!(decoded, data, "round-trip differed for {eth_tx_value:?}");
            }
        }

        #[test]
        fn test_tx_deposit_bincode_roundtrip() {
            #[serde_as]
            #[derive(Debug, PartialEq, Eq, Serialize, Deserialize)]
            struct Data {
                #[serde_as(as = "serde_bincode_compat::TxDeposit")]
                transaction: TxDeposit,
            }

            let mut bytes = [0u8; 1024];
            rand::rng().fill(bytes.as_mut_slice());
            let data = Data {
                transaction: TxDeposit::arbitrary(&mut arbitrary::Unstructured::new(&bytes))
                    .unwrap(),
            };

            let encoded = bincode::serde::encode_to_vec(&data, bincode::config::legacy()).unwrap();
            let (decoded, _) =
                bincode::serde::decode_from_slice::<Data, _>(&encoded, bincode::config::legacy())
                    .unwrap();
            assert_eq!(decoded, data);
        }
    }
}
