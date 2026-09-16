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
    // [MANTLE] BVM_ETH fields. Stored as `Option<_>` (1 flag bit each) and placed BEFORE
    // `input` to reproduce the exact 2-byte Compact bitfield layout written by
    // reth v1.9.3-mantle-arsia (15 used bits -> 2 bytes). The conversion impls map
    // `0 <-> None` for `eth_value` (same pattern as `mint`).
    //
    // These are `Option<U256>`, widened from `Option<u128>` so they cover the full 32-byte
    // ABI word the portal packs. **The widening is byte-compatible**: reth's Compact gives an
    // `Option` one flag bit regardless of the inner type and length-prefixes the payload, so
    // the bitfield and the encoded bytes are unchanged for every value that fit in `u128` —
    // i.e. everything already on disk. Proven by `compact_layout_is_unchanged_by_widening`
    // below, which diffs real encoder output against a frozen `Option<u128>` mirror.
    //
    // DO NOT reorder these fields or change `Option<_>` to a bare value: either shifts the
    // bitfield and makes ALL existing on-disk deposit data unreadable (the original
    // op-reth-rpc41 sync failure). The frozen-mirror test is what makes that detectable.
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
            // [MANTLE] map `0` -> `None` (same convention as `mint`).
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
    /// mint Option=1, U256=6, u64=4, bool=1, eth_value Option=1, eth_tx_value Option=1,
    /// Bytes=0 => 15 used bits => 2 bytes). Changing field types/order shifts the layout and
    /// makes every existing on-disk deposit unreadable (the op-reth-rpc41 sync failure).
    #[test]
    fn compact_txdeposit_bitfield_is_2_bytes() {
        assert_eq!(CompactTxDeposit::bitflag_encoded_bytes(), 2);
    }

    /// `[MANTLE]` Frozen mirror of the **pre-widening** layout, when `eth_value` /
    /// `eth_tx_value` were `Option<u128>`. Only ever used by the test below.
    ///
    /// **DO NOT widen these fields to match [`CompactTxDeposit`].** They are `u128` on purpose:
    /// this struct is the *control* side of the comparison — the layout that produced every
    /// deposit already written to disk. Widening it would make the test compare the current
    /// struct against itself, which is trivially equal, and the guard would silently stop
    /// guarding anything.
    ///
    /// If [`CompactTxDeposit`] gains or loses a field, this mirror stays as it is and the test
    /// is expected to fail. That failure is the signal to decide whether the on-disk format
    /// really is changing, and to plan a migration if so — not a prompt to re-sync the mirror.
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

    /// `[MANTLE]` **On-disk compatibility guard for the `u128 -> U256` widening.**
    ///
    /// `compact_txdeposit_bitfield_is_2_bytes` above is necessary but not sufficient: the
    /// bitfield can stay two bytes while its *contents* shift, which is exactly the failure
    /// mode that made every stored deposit unreadable once before. This diffs real encoder
    /// output against a frozen copy of the old struct across a value matrix, so a layout shift
    /// shows up as differing bytes rather than as a surviving byte count.
    ///
    /// Every value here fits in `u128`, i.e. everything already written to disk. If this test
    /// passes, existing databases stay readable and no migration is required.
    /// `[MANTLE]` **The decode direction of the compatibility guard.**
    ///
    /// `compact_layout_is_unchanged_by_widening` proves the *encoder* still writes the old
    /// bytes. That is only half the question a live node asks: it mostly *reads*, and every
    /// deposit already in its database was written by the narrow (`Option<u128>`) layout.
    ///
    /// This encodes with the frozen old struct and decodes with the current one, asserting the
    /// values come back intact. Symmetric codecs make this follow from the encode test, but
    /// "follows from" is not a measurement, and the cost of being wrong here is an unreadable
    /// database.
    #[test]
    fn current_decoder_reads_bytes_written_by_the_old_layout() {
        let vals: [u128; 6] = [0, 1, 255, 256, u64::MAX as u128, u128::MAX];
        let mut checked = 0usize;

        for &mint in &vals {
            for &ev in &vals {
                for &etv in &[None, Some(1u128), Some(u128::MAX)] {
                    let frozen = FrozenCompactTxDeposit {
                        source_hash: B256::repeat_byte(7),
                        from: Address::repeat_byte(3),
                        to: TxKind::Call(Address::repeat_byte(9)),
                        mint: (mint != 0).then_some(mint),
                        value: U256::from(12_345u64),
                        gas_limit: 21_000,
                        is_system_transaction: true,
                        eth_value: (ev != 0).then_some(ev),
                        eth_tx_value: etv,
                        input: Bytes::from(vec![1u8, 2, 3]),
                    };

                    // Bytes exactly as the pre-widening node wrote them to disk.
                    let mut on_disk = Vec::new();
                    let _ = frozen.to_compact(&mut on_disk);

                    // Read them back with the current, widened decoder.
                    let (tx, rest) = TxDeposit::from_compact(&on_disk, on_disk.len());
                    assert!(rest.is_empty(), "decoder left trailing bytes at mint={mint} ev={ev}");

                    assert_eq!(tx.mint, mint, "mint changed");
                    assert_eq!(tx.eth_value, U256::from(ev), "eth_value changed at {ev}");
                    assert_eq!(
                        tx.eth_tx_value,
                        etv.map(U256::from),
                        "eth_tx_value changed at {etv:?}",
                    );
                    assert_eq!(tx.value, U256::from(12_345u64));
                    assert_eq!(tx.gas_limit, 21_000);
                    assert!(tx.is_system_transaction);
                    assert_eq!(tx.input, Bytes::from(vec![1u8, 2, 3]));
                    checked += 1;
                }
            }
        }

        assert_eq!(checked, 108, "matrix shrank; the guard is weaker than it reads");
    }

    #[test]
    fn compact_layout_is_unchanged_by_widening() {
        assert_eq!(
            CompactTxDeposit::bitflag_encoded_bytes(),
            FrozenCompactTxDeposit::bitflag_encoded_bytes(),
            "bitfield width changed",
        );

        let vals: [u128; 6] = [0, 1, 255, 256, u64::MAX as u128, u128::MAX];
        let mut compared = 0usize;

        for &mint in &vals {
            for &ev in &vals {
                for &etv in &[None, Some(0u128), Some(1u128), Some(u128::MAX)] {
                    let frozen = FrozenCompactTxDeposit {
                        source_hash: B256::repeat_byte(7),
                        from: Address::repeat_byte(3),
                        to: TxKind::Call(Address::repeat_byte(9)),
                        mint: (mint != 0).then_some(mint),
                        value: U256::from(12_345u64),
                        gas_limit: 21_000,
                        is_system_transaction: false,
                        eth_value: (ev != 0).then_some(ev),
                        eth_tx_value: etv,
                        input: Bytes::from(vec![1u8, 2, 3]),
                    };
                    let current = CompactTxDeposit {
                        source_hash: B256::repeat_byte(7),
                        from: Address::repeat_byte(3),
                        to: TxKind::Call(Address::repeat_byte(9)),
                        mint: (mint != 0).then_some(mint),
                        value: U256::from(12_345u64),
                        gas_limit: 21_000,
                        is_system_transaction: false,
                        eth_value: (ev != 0).then_some(U256::from(ev)),
                        eth_tx_value: etv.map(U256::from),
                        input: Bytes::from(vec![1u8, 2, 3]),
                    };

                    let mut a = Vec::new();
                    let mut b = Vec::new();
                    let _ = frozen.to_compact(&mut a);
                    let _ = current.to_compact(&mut b);
                    assert_eq!(
                        a, b,
                        "encoding diverged at mint={mint} eth_value={ev} eth_tx_value={etv:?}",
                    );
                    compared += 1;
                }
            }
        }

        assert_eq!(compared, 144, "matrix shrank; the guard is weaker than it reads");
    }

    #[test]
    fn roundtrip_zero_bvm_eth() {
        let tx =
            TxDeposit { eth_value: U256::from(0u128), eth_tx_value: None, ..Default::default() };
        assert_eq!(roundtrip(&tx), tx);
    }

    /// Regression for the elysium `CompactTxDeposit` field-drop: a BVM_ETH deposit must not
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
        let tx = TxDeposit { eth_tx_value: Some(U256::from(0u128)), ..Default::default() };
        assert_eq!(roundtrip(&tx).eth_tx_value, Some(U256::from(0u128)));
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
