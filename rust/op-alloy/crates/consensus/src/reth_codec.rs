//! Compact codec implementations for OP Stack consensus types.
//!
//! Ported from reth v1.11.3 (`d6324d63e`), where they lived behind the `op` feature:
//! - Transaction codecs: `crates/storage/codecs/src/alloy/transaction/optimism.rs`
//! - Receipt codecs: `crates/storage/codecs/src/alloy/optimism.rs`
//!
//! Differences from upstream:
//! - `CompactOpReceipt` uses `Vec<Log>` instead of `Cow<'a, Vec<Log>>` because the crates.io
//!   `reth-codecs-derive` macro doesn't support lifetime parameters. The wire format is identical;
//!   only serialization performance differs (clone vs borrow).
//! - `Compress`/`Decompress` impls for `OpTxEnvelope` and `OpReceipt` are added here since they
//!   were previously provided by reth's in-tree codecs crate.

use crate::{
    OpReceipt, OpTxEnvelope, OpTxType, OpTypedTransaction, POST_EXEC_TX_TYPE_ID, TxDeposit,
    TxPostExec,
};
use alloc::vec::Vec;
use alloy_consensus::{Receipt, Signed, Transaction};
use alloy_primitives::{Address, B256, Bytes, Log, Signature, TxKind, U256};
use alloy_rlp::Decodable;
use reth_codecs::{
    Compact,
    alloy::transaction::{CompactEnvelope, Envelope, FromTxCompact, ToTxCompact},
    txtype::*,
};

// --- OpTxType ---

impl Compact for OpTxType {
    fn to_compact<B>(&self, buf: &mut B) -> usize
    where
        B: bytes::BufMut + AsMut<[u8]>,
    {
        match self {
            Self::Legacy => COMPACT_IDENTIFIER_LEGACY,
            Self::Eip2930 => COMPACT_IDENTIFIER_EIP2930,
            Self::Eip1559 => COMPACT_IDENTIFIER_EIP1559,
            Self::Eip7702 => {
                buf.put_u8(alloy_consensus::constants::EIP7702_TX_TYPE_ID);
                COMPACT_EXTENDED_IDENTIFIER_FLAG
            }
            Self::Deposit => {
                buf.put_u8(crate::DEPOSIT_TX_TYPE_ID);
                COMPACT_EXTENDED_IDENTIFIER_FLAG
            }
            Self::PostExec => {
                buf.put_u8(POST_EXEC_TX_TYPE_ID);
                COMPACT_EXTENDED_IDENTIFIER_FLAG
            }
        }
    }

    fn from_compact(mut buf: &[u8], identifier: usize) -> (Self, &[u8]) {
        use bytes::Buf;
        match identifier {
            COMPACT_IDENTIFIER_LEGACY => (Self::Legacy, buf),
            COMPACT_IDENTIFIER_EIP2930 => (Self::Eip2930, buf),
            COMPACT_IDENTIFIER_EIP1559 => (Self::Eip1559, buf),
            COMPACT_EXTENDED_IDENTIFIER_FLAG => {
                let extended_identifier = buf.get_u8();
                let ty = match extended_identifier {
                    alloy_consensus::constants::EIP7702_TX_TYPE_ID => Self::Eip7702,
                    crate::DEPOSIT_TX_TYPE_ID => Self::Deposit,
                    POST_EXEC_TX_TYPE_ID => Self::PostExec,
                    _ => panic!("Unsupported OpTxType identifier: {extended_identifier}"),
                };
                (ty, buf)
            }
            _ => panic!("Unknown identifier for OpTxType: {identifier}"),
        }
    }
}

// --- TxDeposit ---

/// Mirror struct for compact encoding of [`TxDeposit`].
#[derive(reth_codecs_derive::Compact)]
#[reth_codecs(crate = "reth_codecs")]
struct CompactTxDeposit {
    source_hash: B256,
    from: Address,
    to: TxKind,
    mint: Option<u128>,
    value: U256,
    gas_limit: u64,
    is_system_transaction: bool,
    // [MANTLE] BVM_ETH fields, placed BEFORE `input` to reproduce the exact Compact bitfield
    // layout written by reth v1.9.3-mantle-arsia. The op-alloy `TxDeposit` uses `U256` for
    // these; the conversion impls map `0 <-> None` for `eth_value` (same pattern as `mint`).
    //
    // These were `Option<u128>` until `eth_value`/`eth_tx_value` were widened to `U256` to
    // match op-node's `big.Int`. `Option<U256>` is NOT assumed to be layout-compatible with
    // `Option<u128>` -- that is proven byte-for-byte by `mantle_compact_layout_tests` below,
    // which keeps a frozen mirror of the pre-widening shape and asserts identical output.
    //
    // DO NOT reorder these fields or move them after `input`: that shifts the bitfield and
    // makes ALL existing on-disk deposit data unreadable (the original op-reth-rpc41 sync
    // failure).
    eth_value: Option<U256>,
    eth_tx_value: Option<U256>,
    input: Bytes,
}

impl From<&TxDeposit> for CompactTxDeposit {
    fn from(tx: &TxDeposit) -> Self {
        Self {
            source_hash: tx.source_hash,
            from: tx.from,
            to: tx.to,
            mint: match tx.mint {
                0 => None,
                v => Some(v),
            },
            value: tx.value,
            gas_limit: tx.gas_limit,
            is_system_transaction: tx.is_system_transaction,
            // [MANTLE] map `U256::ZERO` -> `None` (same convention as `mint`).
            eth_value: (!tx.eth_value.is_zero()).then_some(tx.eth_value),
            eth_tx_value: tx.eth_tx_value,
            input: tx.input.clone(),
        }
    }
}

impl From<CompactTxDeposit> for TxDeposit {
    fn from(tx: CompactTxDeposit) -> Self {
        // [MANTLE] BVM_ETH fields are preserved across the Compact round-trip
        // (`eth_value` via `None -> 0`, same as `mint`).
        Self {
            source_hash: tx.source_hash,
            from: tx.from,
            to: tx.to,
            mint: tx.mint.unwrap_or_default(),
            value: tx.value,
            gas_limit: tx.gas_limit,
            is_system_transaction: tx.is_system_transaction,
            eth_value: tx.eth_value.unwrap_or_default(),
            input: tx.input,
            eth_tx_value: tx.eth_tx_value,
        }
    }
}

impl Compact for TxDeposit {
    fn to_compact<B>(&self, buf: &mut B) -> usize
    where
        B: bytes::BufMut + AsMut<[u8]>,
    {
        CompactTxDeposit::from(self).to_compact(buf)
    }

    fn from_compact(buf: &[u8], len: usize) -> (Self, &[u8]) {
        let (compact, buf) = CompactTxDeposit::from_compact(buf, len);
        (compact.into(), buf)
    }
}

#[cfg(test)]
mod mantle_txdeposit_compact_tests {
    use super::*;
    use alloc::vec;

    fn roundtrip(tx: &TxDeposit) -> TxDeposit {
        let mut buf = Vec::new();
        let _ = Compact::to_compact(tx, &mut buf);
        let (decoded, _) = TxDeposit::from_compact(&buf, buf.len());
        decoded
    }

    /// The Compact flag bitfield MUST stay 2 bytes to remain byte-for-byte compatible with
    /// the reth v1.9.3-mantle-arsia on-disk format (B256=0, Address=0, TxKind=1,
    /// mint Option=1, U256=6, u64=4, bool=1, `eth_value` Option=1, `eth_tx_value` Option=1,
    /// Bytes=0 => 15 used bits => 2 bytes). Changing field types/order shifts the layout and
    /// makes every existing on-disk deposit unreadable (the op-reth-rpc41 sync failure).
    #[test]
    fn compact_txdeposit_bitfield_is_2_bytes() {
        assert_eq!(CompactTxDeposit::bitflag_encoded_bytes(), 2);
    }

    #[test]
    fn roundtrip_zero_bvm_eth() {
        let tx = TxDeposit { eth_value: U256::ZERO, eth_tx_value: None, ..Default::default() };
        assert_eq!(roundtrip(&tx), tx);
    }

    /// Regression for the elysium `CompactTxDeposit` field-drop: a `BVM_ETH` deposit must not
    /// read back with `eth_value = 0` / `eth_tx_value = None`, and `input` (the last,
    /// length-suffixed field) must survive the two extra bitfield bits intact.
    #[test]
    fn roundtrip_nonzero_bvm_eth_preserves_fields() {
        let tx = TxDeposit {
            source_hash: B256::repeat_byte(0xab),
            from: Address::repeat_byte(0x11),
            to: TxKind::Call(Address::repeat_byte(0x22)),
            mint: 7,
            value: U256::from(9u64),
            gas_limit: 300_000,
            is_system_transaction: false,
            eth_value: U256::from(123_456_000_000_000_000u128),
            input: Bytes::from(vec![0xde, 0xad, 0xbe, 0xef]),
            eth_tx_value: Some(U256::from(123_456_000_000_000_000u128)),
        };
        let rt = roundtrip(&tx);
        assert_eq!(rt.eth_value, U256::from(123_456_000_000_000_000u128), "eth_value was dropped");
        assert_eq!(
            rt.eth_tx_value,
            Some(U256::from(123_456_000_000_000_000u128)),
            "eth_tx_value was dropped"
        );
        assert_eq!(rt.input, tx.input, "input corrupted (bitfield shift)");
        assert_eq!(rt, tx);
    }

    /// `Some(0)` must round-trip as `Some(0)` (not `None`): the Option flag is independent of
    /// the value being zero.
    #[test]
    fn roundtrip_eth_tx_value_some_zero() {
        let tx = TxDeposit { eth_tx_value: Some(U256::ZERO), ..Default::default() };
        assert_eq!(roundtrip(&tx).eth_tx_value, Some(U256::ZERO));
    }

    /// A value at or above 2^128 -- unreachable while the fields were `u128` -- must survive.
    /// `OptimismPortal.depositTransaction` does not bound `_ethTxValue`, so any EOA can put one
    /// on chain; op-node holds it as a `big.Int` and would disagree with a truncating node.
    #[test]
    fn roundtrip_bvm_eth_above_u128_max() {
        let wide = U256::from(1u128) << 200;
        let tx = TxDeposit { eth_value: wide, eth_tx_value: Some(U256::MAX), ..Default::default() };
        let rt = roundtrip(&tx);
        assert_eq!(rt.eth_value, wide);
        assert_eq!(rt.eth_tx_value, Some(U256::MAX));
        assert_eq!(rt, tx);
    }
}

/// `[MANTLE]` Proof that widening `eth_value` / `eth_tx_value` from `u128` to `U256` did not
/// change the on-disk Compact layout for any value reth v1.9.3-mantle-arsia could have written.
///
/// `FrozenCompactTxDeposit` is a byte-frozen copy of `CompactTxDeposit` as it existed *before*
/// the widening. It is never used in production -- it exists only so these tests can compare
/// real encoder output against the historical shape instead of asserting a hand-computed
/// bitfield width. If a future change to `CompactTxDeposit` shifts the layout, `encodings_match`
/// fails with the two byte strings side by side.
///
/// Do not "clean this up" by deleting the frozen struct: the whole point is that it does not
/// track `CompactTxDeposit`.
#[cfg(test)]
mod mantle_compact_layout_tests {
    use super::*;
    use alloc::{vec, vec::Vec};

    /// Frozen pre-widening mirror. Field order and types must never change.
    #[derive(reth_codecs_derive::Compact)]
    #[reth_codecs(crate = "reth_codecs")]
    struct FrozenCompactTxDeposit {
        source_hash: B256,
        from: Address,
        to: TxKind,
        mint: Option<u128>,
        value: U256,
        gas_limit: u64,
        is_system_transaction: bool,
        eth_value: Option<u128>,
        eth_tx_value: Option<u128>,
        input: Bytes,
    }

    /// The flag bitfield must stay the same width as the frozen layout. (It is 2 bytes:
    /// `B256`=0, `Address`=0, `TxKind`=1, `mint` Option=1, `U256`=6, `u64`=4, `bool`=1,
    /// `eth_value`=1, `eth_tx_value`=1, `Bytes`=0 => 15 used bits. Asserted against the frozen
    /// struct rather
    /// than the literal 2 so the two can never drift apart silently.)
    #[test]
    fn bitfield_width_matches_frozen_layout() {
        assert_eq!(
            CompactTxDeposit::bitflag_encoded_bytes(),
            FrozenCompactTxDeposit::bitflag_encoded_bytes(),
        );
        assert_eq!(CompactTxDeposit::bitflag_encoded_bytes(), 2);
    }

    fn encode_pair(
        eth_value: Option<u128>,
        eth_tx_value: Option<u128>,
        input: &[u8],
    ) -> (Vec<u8>, Vec<u8>) {
        let (source_hash, from, to) = (
            B256::repeat_byte(0xab),
            Address::repeat_byte(0x11),
            TxKind::Call(Address::repeat_byte(0x22)),
        );
        let (mint, value, gas_limit, is_system_transaction) =
            (Some(7u128), U256::from(9u64), 300_000u64, true);

        let frozen = FrozenCompactTxDeposit {
            source_hash,
            from,
            to,
            mint,
            value,
            gas_limit,
            is_system_transaction,
            eth_value,
            eth_tx_value,
            input: Bytes::copy_from_slice(input),
        };
        let widened = CompactTxDeposit {
            source_hash,
            from,
            to,
            mint,
            value,
            gas_limit,
            is_system_transaction,
            eth_value: eth_value.map(U256::from),
            eth_tx_value: eth_tx_value.map(U256::from),
            input: Bytes::copy_from_slice(input),
        };

        let (mut a, mut b) = (Vec::new(), Vec::new());
        let _ = frozen.to_compact(&mut a);
        let _ = widened.to_compact(&mut b);
        (a, b)
    }

    /// Every `u128`-representable `BVM_ETH` value -- i.e. everything reth could already have on
    /// disk -- must encode to exactly the same bytes after the widening.
    #[test]
    fn encodings_match_for_all_u128_representable_values() {
        let interesting = [
            None,
            Some(0u128),
            Some(1),
            Some(0xff),
            Some(0x100),
            Some(123_456_000_000_000_000),
            Some(u64::MAX as u128),
            Some(u64::MAX as u128 + 1),
            Some(u128::MAX),
        ];
        for ev in interesting {
            for etv in interesting {
                for input in [&[][..], &[0xde, 0xad, 0xbe, 0xef][..]] {
                    let (frozen, widened) = encode_pair(ev, etv, input);
                    assert_eq!(
                        frozen,
                        widened,
                        "Compact layout changed for eth_value={ev:?} eth_tx_value={etv:?} \
                         input_len={}: on-disk data written by reth v1.9.3-mantle-arsia is no \
                         longer readable",
                        input.len(),
                    );
                }
            }
        }
    }

    /// The widened decoder must read bytes produced by the frozen (pre-widening) encoder.
    /// This is the actual failure mode for an existing node: old bytes, new binary.
    #[test]
    fn widened_decoder_reads_frozen_encoder_output() {
        let (frozen_bytes, _) = encode_pair(Some(123_456_000_000_000_000), Some(42), &[0xde, 0xad]);
        let (decoded, rest) = CompactTxDeposit::from_compact(&frozen_bytes, frozen_bytes.len());
        assert!(rest.is_empty(), "trailing bytes: bitfield is misaligned");
        assert_eq!(decoded.eth_value, Some(U256::from(123_456_000_000_000_000u128)));
        assert_eq!(decoded.eth_tx_value, Some(U256::from(42u128)));
        assert_eq!(decoded.input, Bytes::from(vec![0xde, 0xad]));
        assert_eq!(decoded.mint, Some(7));
        assert_eq!(decoded.gas_limit, 300_000);
    }
}

impl Compact for TxPostExec {
    fn to_compact<B>(&self, buf: &mut B) -> usize
    where
        B: bytes::BufMut + AsMut<[u8]>,
    {
        self.input().to_compact(buf)
    }

    fn from_compact(buf: &[u8], len: usize) -> (Self, &[u8]) {
        let (input, buf) = Bytes::from_compact(buf, len);
        let mut slice = input.as_ref();
        let tx = Self::decode(&mut slice).expect("valid compact post-exec tx");
        (tx, buf)
    }
}

// --- OpTypedTransaction ---

impl Compact for OpTypedTransaction {
    fn to_compact<B>(&self, buf: &mut B) -> usize
    where
        B: bytes::BufMut + AsMut<[u8]>,
    {
        let tx_type = self.tx_type();
        let identifier = tx_type.to_compact(buf);
        match self {
            Self::Legacy(tx) => {
                tx.to_compact(buf);
            }
            Self::Eip2930(tx) => {
                tx.to_compact(buf);
            }
            Self::Eip1559(tx) => {
                tx.to_compact(buf);
            }
            Self::Eip7702(tx) => {
                tx.to_compact(buf);
            }
            Self::Deposit(tx) => {
                tx.to_compact(buf);
            }
            Self::PostExec(tx) => {
                tx.to_compact(buf);
            }
        }
        identifier
    }

    fn from_compact(buf: &[u8], identifier: usize) -> (Self, &[u8]) {
        let (tx_type, buf) = OpTxType::from_compact(buf, identifier);
        match tx_type {
            OpTxType::Legacy => {
                let (tx, buf) = alloy_consensus::TxLegacy::from_compact(buf, buf.len());
                (Self::Legacy(tx), buf)
            }
            OpTxType::Eip2930 => {
                let (tx, buf) = alloy_consensus::TxEip2930::from_compact(buf, buf.len());
                (Self::Eip2930(tx), buf)
            }
            OpTxType::Eip1559 => {
                let (tx, buf) = alloy_consensus::TxEip1559::from_compact(buf, buf.len());
                (Self::Eip1559(tx), buf)
            }
            OpTxType::Eip7702 => {
                let (tx, buf) = alloy_consensus::TxEip7702::from_compact(buf, buf.len());
                (Self::Eip7702(tx), buf)
            }
            OpTxType::Deposit => {
                let (tx, buf) = TxDeposit::from_compact(buf, buf.len());
                (Self::Deposit(tx), buf)
            }
            OpTxType::PostExec => {
                let (tx, buf) = TxPostExec::from_compact(buf, buf.len());
                (Self::PostExec(tx), buf)
            }
        }
    }
}

// --- OpTxEnvelope ---

impl Envelope for OpTxEnvelope {
    fn signature(&self) -> &Signature {
        match self {
            Self::Legacy(tx) => tx.signature(),
            Self::Eip2930(tx) => tx.signature(),
            Self::Eip1559(tx) => tx.signature(),
            Self::Eip7702(tx) => tx.signature(),
            Self::Deposit(_) | Self::PostExec(_) => {
                const DEPOSIT_SIG: Signature = Signature::new(U256::ZERO, U256::ZERO, false);
                &DEPOSIT_SIG
            }
        }
    }

    fn tx_type(&self) -> Self::TxType {
        alloy_consensus::Typed2718::ty(self).try_into().expect("valid op tx type")
    }
}

impl ToTxCompact for OpTxEnvelope {
    fn to_tx_compact(&self, buf: &mut (impl bytes::BufMut + AsMut<[u8]>)) {
        // Only write the tx body without the type prefix. The type is serialized separately
        // by CompactEnvelope.
        match self {
            Self::Legacy(tx) => {
                tx.tx().to_compact(buf);
            }
            Self::Eip2930(tx) => {
                tx.tx().to_compact(buf);
            }
            Self::Eip1559(tx) => {
                tx.tx().to_compact(buf);
            }
            Self::Eip7702(tx) => {
                tx.tx().to_compact(buf);
            }
            Self::Deposit(tx) => {
                tx.inner().to_compact(buf);
            }
            Self::PostExec(tx) => {
                tx.inner().to_compact(buf);
            }
        };
    }
}

impl FromTxCompact for OpTxEnvelope {
    type TxType = OpTxType;

    fn from_tx_compact(buf: &[u8], tx_type: Self::TxType, signature: Signature) -> (Self, &[u8])
    where
        Self: Sized,
    {
        // Deserialize the tx body directly based on tx_type. The type prefix was already
        // consumed by CompactEnvelope.
        match tx_type {
            OpTxType::Legacy => {
                let (tx, buf) = alloy_consensus::TxLegacy::from_compact(buf, buf.len());
                (Self::Legacy(Signed::new_unhashed(tx, signature)), buf)
            }
            OpTxType::Eip2930 => {
                let (tx, buf) = alloy_consensus::TxEip2930::from_compact(buf, buf.len());
                (Self::Eip2930(Signed::new_unhashed(tx, signature)), buf)
            }
            OpTxType::Eip1559 => {
                let (tx, buf) = alloy_consensus::TxEip1559::from_compact(buf, buf.len());
                (Self::Eip1559(Signed::new_unhashed(tx, signature)), buf)
            }
            OpTxType::Eip7702 => {
                let (tx, buf) = alloy_consensus::TxEip7702::from_compact(buf, buf.len());
                (Self::Eip7702(Signed::new_unhashed(tx, signature)), buf)
            }
            OpTxType::Deposit => {
                let (tx, buf) = TxDeposit::from_compact(buf, buf.len());
                (Self::Deposit(alloy_consensus::Sealed::new(tx)), buf)
            }
            OpTxType::PostExec => {
                let (tx, buf) = TxPostExec::from_compact(buf, buf.len());
                (Self::PostExec(alloy_consensus::Sealed::new(tx)), buf)
            }
        }
    }
}

impl Compact for OpTxEnvelope {
    fn to_compact<B>(&self, buf: &mut B) -> usize
    where
        B: bytes::BufMut + AsMut<[u8]>,
    {
        <Self as CompactEnvelope>::to_compact(self, buf)
    }

    fn from_compact(buf: &[u8], len: usize) -> (Self, &[u8]) {
        <Self as CompactEnvelope>::from_compact(buf, len)
    }
}

impl reth_codecs::Compress for OpTxEnvelope {
    type Compressed = Vec<u8>;

    fn compress_to_buf<B: bytes::BufMut + AsMut<[u8]>>(&self, buf: &mut B) {
        let _ = Compact::to_compact(self, buf);
    }
}

impl reth_codecs::Decompress for OpTxEnvelope {
    fn decompress(value: &[u8]) -> Result<Self, reth_codecs::DecompressError> {
        let (obj, _) = Compact::from_compact(value, value.len());
        Ok(obj)
    }
}

// --- OpReceipt ---

/// Mirror struct for compact encoding of [`crate::OpDepositReceipt`].
#[derive(reth_codecs_derive::CompactZstd)]
#[reth_codecs(crate = "reth_codecs")]
#[reth_zstd(
    compressor = reth_zstd_compressors::with_receipt_compressor,
    decompressor = reth_zstd_compressors::with_receipt_decompressor
)]
struct CompactOpReceipt {
    tx_type: OpTxType,
    success: bool,
    cumulative_gas_used: u64,
    logs: Vec<Log>,
    deposit_nonce: Option<u64>,
    deposit_receipt_version: Option<u64>,
}

impl From<&OpReceipt> for CompactOpReceipt {
    fn from(receipt: &OpReceipt) -> Self {
        use alloy_consensus::TxReceipt;
        let (deposit_nonce, deposit_receipt_version) = match receipt {
            OpReceipt::Deposit(deposit) => (deposit.deposit_nonce, deposit.deposit_receipt_version),
            _ => (None, None),
        };
        Self {
            tx_type: receipt.tx_type(),
            success: receipt.status(),
            cumulative_gas_used: receipt.cumulative_gas_used(),
            logs: receipt.as_receipt().logs.clone(),
            deposit_nonce,
            deposit_receipt_version,
        }
    }
}

impl From<CompactOpReceipt> for OpReceipt {
    fn from(compact: CompactOpReceipt) -> Self {
        let receipt = Receipt {
            status: compact.success.into(),
            cumulative_gas_used: compact.cumulative_gas_used,
            logs: compact.logs,
        };
        match compact.tx_type {
            OpTxType::Legacy => Self::Legacy(receipt),
            OpTxType::Eip2930 => Self::Eip2930(receipt),
            OpTxType::Eip1559 => Self::Eip1559(receipt),
            OpTxType::Eip7702 => Self::Eip7702(receipt),
            OpTxType::PostExec => Self::PostExec(receipt),
            OpTxType::Deposit => Self::Deposit(crate::OpDepositReceipt {
                inner: receipt,
                deposit_nonce: compact.deposit_nonce,
                deposit_receipt_version: compact.deposit_receipt_version,
            }),
        }
    }
}

impl Compact for OpReceipt {
    fn to_compact<B>(&self, buf: &mut B) -> usize
    where
        B: bytes::BufMut + AsMut<[u8]>,
    {
        CompactOpReceipt::from(self).to_compact(buf)
    }

    fn from_compact(buf: &[u8], len: usize) -> (Self, &[u8]) {
        let (compact, buf) = CompactOpReceipt::from_compact(buf, len);
        (compact.into(), buf)
    }
}

impl reth_codecs::Compress for OpReceipt {
    type Compressed = Vec<u8>;

    fn compress_to_buf<B: bytes::BufMut + AsMut<[u8]>>(&self, buf: &mut B) {
        let _ = Compact::to_compact(self, buf);
    }
}

impl reth_codecs::Decompress for OpReceipt {
    fn decompress(value: &[u8]) -> Result<Self, reth_codecs::DecompressError> {
        let (obj, _) = Compact::from_compact(value, value.len());
        Ok(obj)
    }
}
