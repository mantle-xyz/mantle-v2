//! Contains the `[L1BlockInfo]` type and its implementation.
use crate::{
    OpSpecId,
    constants::{
        BASE_FEE_SCALAR_OFFSET, BLOB_BASE_FEE_SCALAR_OFFSET, DA_FOOTPRINT_GAS_SCALAR_OFFSET,
        DA_FOOTPRINT_GAS_SCALAR_SLOT, ECOTONE_L1_BLOB_BASE_FEE_SLOT, ECOTONE_L1_FEE_SCALARS_SLOT,
        EMPTY_SCALARS, GAS_ORACLE_CONTRACT, L1_BASE_FEE_SLOT, L1_BLOCK_CONTRACT, L1_OVERHEAD_SLOT,
        L1_SCALAR_SLOT, NON_ZERO_BYTE_COST, OPERATOR_FEE_CONSTANT_OFFSET,
        OPERATOR_FEE_JOVIAN_MULTIPLIER, OPERATOR_FEE_SCALAR_OFFSET, OPERATOR_FEE_SCALARS_SLOT,
        TOKEN_RATIO_SLOT,
    },
    transaction::{OpTxTr, estimate_tx_compressed_size, estimate_tx_compressed_size_with_delta},
};
use revm::{
    context_interface::cfg::gas::{NON_ZERO_BYTE_MULTIPLIER_ISTANBUL, STANDARD_TOKEN_COST},
    database_interface::Database,
    interpreter::{Gas, gas::get_tokens_in_calldata_istanbul},
    primitives::U256,
};

/// L1 block info
///
/// We can extract L1 epoch data from each L2 block, by looking at the `setL1BlockValues`
/// transaction data. This data is then used to calculate the L1 cost of a transaction.
///
/// Here is the format of the `setL1BlockValues` transaction data:
///
/// setL1BlockValues(uint64 _number, uint64 _timestamp, uint256 _basefee, bytes32 _hash,
/// uint64 _sequenceNumber, bytes32 _batcherHash, uint256 _l1FeeOverhead, uint256 _l1FeeScalar)
///
/// For now, we only care about the fields necessary for L1 cost calculation.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct L1BlockInfo {
    /// The L2 block number. If not same as the one in the context,
    /// `L1BlockInfo` is not valid and will be reloaded from the database.
    pub l2_block: Option<U256>,
    /// The base fee of the L1 origin block.
    pub l1_base_fee: U256,
    /// The current L1 fee overhead. None if Ecotone is activated.
    pub l1_fee_overhead: Option<U256>,
    /// The current L1 fee scalar.
    pub l1_base_fee_scalar: U256,
    /// The current token ratio.
    pub token_ratio: U256,
    /// The current L1 blob base fee. None if Ecotone is not activated, except if
    /// `empty_ecotone_scalars` is `true`.
    pub l1_blob_base_fee: Option<U256>,
    /// The current L1 blob base fee scalar. None if Ecotone is not activated.
    pub l1_blob_base_fee_scalar: Option<U256>,
    /// The operator fee scalar. None if Isthmus is not activated.
    pub operator_fee_scalar: Option<U256>,
    /// The operator fee constant. None if Isthmus is not activated.
    pub operator_fee_constant: Option<U256>,
    /// Da footprint gas scalar. Used to set the DA footprint block limit on the L2. Always null
    /// prior to the Jovian hardfork.
    pub da_footprint_gas_scalar: Option<u16>,
    /// True if Ecotone is activated, but the L1 fee scalars have not yet been set.
    pub empty_ecotone_scalars: bool,
    /// Last calculated l1 fee cost. Uses as a cache between validation and pre execution stages.
    pub tx_l1_cost: Option<U256>,
}

impl L1BlockInfo {
    /// Fetch the DA footprint gas scalar from the database.
    pub fn fetch_da_footprint_gas_scalar<DB: Database>(db: &mut DB) -> Result<u16, DB::Error> {
        let da_footprint_gas_scalar_slot =
            db.storage(L1_BLOCK_CONTRACT, DA_FOOTPRINT_GAS_SCALAR_SLOT)?.to_be_bytes::<32>();

        // Extract the first 2 bytes directly as a u16 in big-endian format
        let bytes = [
            da_footprint_gas_scalar_slot[DA_FOOTPRINT_GAS_SCALAR_OFFSET],
            da_footprint_gas_scalar_slot[DA_FOOTPRINT_GAS_SCALAR_OFFSET + 1],
        ];
        Ok(u16::from_be_bytes(bytes))
    }

    /// Try to fetch the L1 block info from the database, post-Jovian.
    fn try_fetch_jovian<DB: Database>(&mut self, db: &mut DB) -> Result<(), DB::Error> {
        self.da_footprint_gas_scalar = Some(Self::fetch_da_footprint_gas_scalar(db)?);

        Ok(())
    }

    /// Try to fetch the L1 block info from the database, post-Isthmus.
    fn try_fetch_isthmus<DB: Database>(&mut self, db: &mut DB) -> Result<(), DB::Error> {
        // Post-isthmus L1 block info
        let operator_fee_scalars =
            db.storage(L1_BLOCK_CONTRACT, OPERATOR_FEE_SCALARS_SLOT)?.to_be_bytes::<32>();

        // The `operator_fee_scalar` is stored as a big endian u32 at
        // OPERATOR_FEE_SCALAR_OFFSET.
        self.operator_fee_scalar = Some(U256::from_be_slice(
            operator_fee_scalars[OPERATOR_FEE_SCALAR_OFFSET..OPERATOR_FEE_SCALAR_OFFSET + 4]
                .as_ref(),
        ));
        // The `operator_fee_constant` is stored as a big endian u64 at
        // OPERATOR_FEE_CONSTANT_OFFSET.
        self.operator_fee_constant = Some(U256::from_be_slice(
            operator_fee_scalars[OPERATOR_FEE_CONSTANT_OFFSET..OPERATOR_FEE_CONSTANT_OFFSET + 8]
                .as_ref(),
        ));

        Ok(())
    }

    /// Try to fetch the L1 block info from the database, post-Ecotone.
    fn try_fetch_ecotone<DB: Database>(&mut self, db: &mut DB) -> Result<(), DB::Error> {
        self.l1_blob_base_fee = Some(db.storage(L1_BLOCK_CONTRACT, ECOTONE_L1_BLOB_BASE_FEE_SLOT)?);

        let l1_fee_scalars =
            db.storage(L1_BLOCK_CONTRACT, ECOTONE_L1_FEE_SCALARS_SLOT)?.to_be_bytes::<32>();

        self.l1_base_fee_scalar = U256::from_be_slice(
            l1_fee_scalars[BASE_FEE_SCALAR_OFFSET..BASE_FEE_SCALAR_OFFSET + 4].as_ref(),
        );

        let l1_blob_base_fee = U256::from_be_slice(
            l1_fee_scalars[BLOB_BASE_FEE_SCALAR_OFFSET..BLOB_BASE_FEE_SCALAR_OFFSET + 4].as_ref(),
        );
        self.l1_blob_base_fee_scalar = Some(l1_blob_base_fee);

        // Check if the L1 fee scalars are empty. If so, we use the Bedrock cost function.
        // The L1 fee overhead is only necessary if `empty_ecotone_scalars` is true, as it was
        // deprecated in Ecotone.
        self.empty_ecotone_scalars = l1_blob_base_fee.is_zero() &&
            l1_fee_scalars[BASE_FEE_SCALAR_OFFSET..BLOB_BASE_FEE_SCALAR_OFFSET + 4] ==
                EMPTY_SCALARS;
        self.l1_fee_overhead = self
            .empty_ecotone_scalars
            .then(|| db.storage(L1_BLOCK_CONTRACT, L1_OVERHEAD_SLOT))
            .transpose()?;

        Ok(())
    }

    /// Try to fetch the L1 block info from the database.
    pub fn try_fetch<DB: Database>(
        db: &mut DB,
        l2_block: U256,
        spec_id: OpSpecId,
    ) -> Result<Self, DB::Error> {
        // Ensure the L1 Block account is loaded into the cache.
        let _ = db.basic(L1_BLOCK_CONTRACT)?;
        let _ = db.basic(GAS_ORACLE_CONTRACT)?;

        // [MANTLE] - GAS_ORACLE + token_ratio
        let l1_base_fee = db.storage(L1_BLOCK_CONTRACT, L1_BASE_FEE_SLOT)?;
        let token_ratio = db.storage(GAS_ORACLE_CONTRACT, TOKEN_RATIO_SLOT)?;

        let l1_fee_overhead = db.storage(L1_BLOCK_CONTRACT, L1_OVERHEAD_SLOT)?;
        let l1_fee_scalar = db.storage(L1_BLOCK_CONTRACT, L1_SCALAR_SLOT)?;

        let mut out = Self {
            l2_block: Some(l2_block),
            l1_base_fee,
            token_ratio,
            l1_fee_overhead: Some(l1_fee_overhead),
            l1_base_fee_scalar: l1_fee_scalar,
            ..Default::default()
        };

        // Post-Arsia L1 block info
        // Mantle uses the same L1 block info as Ecotone, Isthmus, and Jovian.
        if spec_id.is_enabled_in(OpSpecId::ARSIA) {
            out.try_fetch_ecotone(db)?;
            out.try_fetch_isthmus(db)?;
            out.try_fetch_jovian(db)?;
        }

        Ok(out)
    }

    /// Calculate the operator fee for executing this transaction.
    ///
    /// Introduced in isthmus. Prior to isthmus, the operator fee is always zero.
    pub fn operator_fee_charge(&self, input: &[u8], gas_limit: U256) -> U256 {
        // If the input is a deposit transaction or empty, the default value is zero.
        if input.is_empty() || input.first() == Some(&0x7E) {
            return U256::ZERO;
        }

        self.operator_fee_charge_inner(gas_limit)
    }

    /// `[MANTLE]` spec_id not used in operator_fee_charge_inner
    /// Calculate the operator fee for the given `gas`.
    fn operator_fee_charge_inner(&self, gas: U256) -> U256 {
        let operator_fee_scalar =
            self.operator_fee_scalar.expect("Missing operator fee scalar for isthmus L1 Block");
        let operator_fee_constant =
            self.operator_fee_constant.expect("Missing operator fee constant for isthmus L1 Block");

        let product = gas
            .saturating_mul(operator_fee_scalar)
            .saturating_mul(U256::from(OPERATOR_FEE_JOVIAN_MULTIPLIER));

        product.saturating_add(operator_fee_constant)
    }

    /// Calculate the operator fee for executing this transaction.
    ///
    /// Introduced in isthmus. Prior to isthmus, the operator fee is always zero.
    pub fn operator_fee_refund(&self, gas: &Gas, spec_id: OpSpecId) -> U256 {
        if !spec_id.is_enabled_in(OpSpecId::ARSIA) {
            return U256::ZERO;
        }

        // [MANTLE] spec_id not used in operator_fee_charge_inner
        let operator_cost_gas_limit = self.operator_fee_charge_inner(U256::from(gas.limit()));
        // Exclude reservoir gas (EIP-8037) from used gas — reservoir is unused and reimbursed.
        let operator_cost_gas_used = self.operator_fee_charge_inner(U256::from(
            gas.limit() - (gas.remaining() + gas.reservoir() + gas.refunded() as u64),
        ));

        operator_cost_gas_limit.saturating_sub(operator_cost_gas_used)
    }

    /// Calculate the data gas for posting the transaction on L1. Calldata costs 16 gas per byte
    /// after compression.
    ///
    /// Prior to regolith, an extra 68 non-zero bytes were included in the rollup data costs to
    /// account for the empty signature.
    pub fn data_gas(&self, input: &[u8], spec_id: OpSpecId) -> U256 {
        if spec_id.is_enabled_in(OpSpecId::ARSIA) {
            let estimated_size = self.tx_estimated_size_fjord(input);

            return estimated_size
                .saturating_mul(U256::from(NON_ZERO_BYTE_COST))
                .wrapping_div(U256::from(1_000_000));
        };

        // tokens in calldata where non-zero bytes are priced 4 times higher than zero bytes (Same
        // as in Istanbul).
        let mut tokens_in_transaction_data = get_tokens_in_calldata_istanbul(input);

        // Prior to regolith, an extra 68 non zero bytes were included in the rollup data costs.
        if !spec_id.is_enabled_in(OpSpecId::REGOLITH) {
            tokens_in_transaction_data += 68 * NON_ZERO_BYTE_MULTIPLIER_ISTANBUL;
        }

        U256::from(tokens_in_transaction_data.saturating_mul(STANDARD_TOKEN_COST))
    }

    // Calculate the estimated compressed transaction size in bytes, scaled by 1e6.
    // This value is computed based on the following formula:
    // max(minTransactionSize, intercept + fastlzCoef*fastlzSize)
    fn tx_estimated_size_fjord(&self, input: &[u8]) -> U256 {
        U256::from(estimate_tx_compressed_size(input))
    }

    /// Clears the cached L1 cost of the transaction.
    pub const fn clear_tx_l1_cost(&mut self) {
        self.tx_l1_cost = None;
    }

    /// Calculate additional transaction cost with `OpTxTr`.
    ///
    /// Internally calls [`L1BlockInfo::tx_cost`].
    pub fn tx_cost_with_tx(&mut self, tx: impl OpTxTr, spec: OpSpecId) -> Option<U256> {
        // account for additional cost of l1 fee and operator fee
        let enveloped_tx = tx.enveloped_tx()?;
        let gas_limit = U256::from(tx.gas_limit());
        Some(self.tx_cost(enveloped_tx, gas_limit, spec))
    }

    /// Calculate additional transaction cost.
    #[inline]
    pub fn tx_cost(&mut self, enveloped_tx: &[u8], gas_limit: U256, spec: OpSpecId) -> U256 {
        // compute L1 cost
        let mut additional_cost = self.calculate_tx_l1_cost(enveloped_tx, spec);

        // compute operator fee
        if spec.is_enabled_in(OpSpecId::ARSIA) {
            let operator_fee_charge = self.operator_fee_charge(enveloped_tx, gas_limit);
            additional_cost = additional_cost.saturating_add(operator_fee_charge);
        }

        additional_cost
    }

    /// Calculate the gas cost of a transaction based on L1 block data posted on L2, depending on
    /// the [`OpSpecId`] passed.
    pub fn calculate_tx_l1_cost(&mut self, input: &[u8], spec_id: OpSpecId) -> U256 {
        if let Some(tx_l1_cost) = self.tx_l1_cost {
            return tx_l1_cost;
        }
        // If the input is a deposit transaction or empty, the default value is zero.
        let tx_l1_cost = if input.is_empty() || input.first() == Some(&0x7E) {
            return U256::ZERO;
        } else if spec_id.is_enabled_in(OpSpecId::ARSIA) {
            self.calculate_tx_l1_cost_arsia(input)
        } else {
            self.calculate_tx_l1_cost_before_arsia(input, spec_id)
        };

        self.tx_l1_cost = Some(tx_l1_cost);
        tx_l1_cost
    }

    /// Calculate the gas cost of a transaction based on L1 block data posted on L2, pre-Arsia.
    fn calculate_tx_l1_cost_before_arsia(&self, input: &[u8], spec_id: OpSpecId) -> U256 {
        let rollup_data_gas_cost = self.data_gas(input, spec_id);
        rollup_data_gas_cost
            .saturating_add(self.l1_fee_overhead.unwrap_or_default())
            .saturating_mul(self.l1_base_fee)
            .saturating_mul(self.l1_base_fee_scalar)
            .saturating_mul(self.token_ratio)
            .wrapping_div(U256::from(1_000_000))
    }

    /// Calculate the gas cost of a transaction based on L1 block data posted on L2, post-Arsia.
    ///
    /// [OpSpecId::ARSIA] L1 cost function:
    /// `estimatedSize*(baseFeeScalar*l1BaseFee*16 + blobFeeScalar*l1BlobBaseFee)/1e12`
    fn calculate_tx_l1_cost_arsia(&self, input: &[u8]) -> U256 {
        self.calculate_tx_l1_cost_arsia_with_delta(input, 0)
    }

    /// `[MANTLE]` - for reth calculate_tx_l1_cost_for_estimate
    ///
    /// Like [`Self::calculate_tx_l1_cost_arsia`] but adds `fastlz_delta` to compressed size (e.g.
    /// +80 for geth alignment). Uses geth Fjord formula: estimatedDASizeScaled =
    /// max(MinTxSizeScaled, intercept + coef*fastLzSize).
    /// [`estimate_tx_compressed_size_with_delta`] already returns that scaled value; use it
    /// directly.
    fn calculate_tx_l1_cost_arsia_with_delta(&self, input: &[u8], fastlz_delta: u64) -> U256 {
        let l1_fee_scaled = self.calculate_l1_fee_scaled_ecotone();
        if l1_fee_scaled.is_zero() {
            return U256::ZERO;
        }

        let estimated_size_scaled = estimate_tx_compressed_size_with_delta(input, fastlz_delta);

        U256::from(estimated_size_scaled)
            .saturating_mul(l1_fee_scaled)
            .wrapping_div(U256::from(1_000_000_000_000u64))
            .saturating_mul(self.token_ratio)
    }

    /// L1 cost for RPC estimate (no cache). Uses `fastlz_delta` in Arsia formula to align with geth
    /// (+80).
    pub fn calculate_tx_l1_cost_for_estimate(
        &self,
        input: &[u8],
        spec_id: OpSpecId,
        fastlz_delta: u64,
    ) -> U256 {
        if input.is_empty() || input.first() == Some(&0x7E) {
            return U256::ZERO;
        }
        if spec_id.is_enabled_in(OpSpecId::ARSIA) {
            self.calculate_tx_l1_cost_arsia_with_delta(input, fastlz_delta)
        } else {
            self.calculate_tx_l1_cost_before_arsia(input, spec_id)
        }
    }

    // l1BaseFee*16*l1BaseFeeScalar + l1BlobBaseFee*l1BlobBaseFeeScalar
    fn calculate_l1_fee_scaled_ecotone(&self) -> U256 {
        let calldata_cost_per_byte = self
            .l1_base_fee
            .saturating_mul(U256::from(NON_ZERO_BYTE_COST))
            .saturating_mul(self.l1_base_fee_scalar);
        let blob_cost_per_byte = self
            .l1_blob_base_fee
            .unwrap_or_default()
            .saturating_mul(self.l1_blob_base_fee_scalar.unwrap_or_default());

        calldata_cost_per_byte.saturating_add(blob_cost_per_byte)
    }

    /// Reset the l2_block to None.
    pub fn reset_l2_block(&mut self) {
        self.l2_block = None
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use revm::primitives::{bytes, hex};
    use std::{
        collections::hash_map::DefaultHasher,
        hash::{Hash, Hasher},
    };

    #[test]
    fn test_data_gas_non_zero_bytes() {
        let l1_block_info = L1BlockInfo {
            l1_base_fee: U256::from(1_000_000),
            l1_fee_overhead: Some(U256::from(1_000_000)),
            l1_base_fee_scalar: U256::from(1_000_000),
            ..Default::default()
        };

        // 0xFACADE = 6 nibbles = 3 bytes
        // 0xFACADE = 1111 1010 . 1100 1010 . 1101 1110

        // Pre-regolith (ie bedrock) has an extra 68 non-zero bytes
        // gas cost = 3 non-zero bytes * NON_ZERO_BYTE_COST + NON_ZERO_BYTE_COST * 68
        // gas cost = 3 * 16 + 68 * 16 = 1136
        let input = bytes!("FACADE");
        let bedrock_data_gas = l1_block_info.data_gas(&input, OpSpecId::BEDROCK);
        assert_eq!(bedrock_data_gas, U256::from(1136));

        // Regolith has no added 68 non zero bytes
        // gas cost = 3 * 16 = 48
        let regolith_data_gas = l1_block_info.data_gas(&input, OpSpecId::REGOLITH);
        assert_eq!(regolith_data_gas, U256::from(48));

        // Fjord has a minimum compressed size of 100 bytes
        // gas cost = 100 * 16 = 1600
        let fjord_data_gas = l1_block_info.data_gas(&input, OpSpecId::ARSIA);
        assert_eq!(fjord_data_gas, U256::from(1600));
    }

    #[test]
    fn test_data_gas_zero_bytes() {
        let l1_block_info = L1BlockInfo {
            l1_base_fee: U256::from(1_000_000),
            l1_fee_overhead: Some(U256::from(1_000_000)),
            l1_base_fee_scalar: U256::from(1_000_000),
            ..Default::default()
        };

        // 0xFA00CA00DE = 10 nibbles = 5 bytes
        // 0xFA00CA00DE = 1111 1010 . 0000 0000 . 1100 1010 . 0000 0000 . 1101 1110

        // Pre-regolith (ie bedrock) has an extra 68 non-zero bytes
        // gas cost = 3 non-zero * NON_ZERO_BYTE_COST + 2 * ZERO_BYTE_COST + NON_ZERO_BYTE_COST * 68
        // gas cost = 3 * 16 + 2 * 4 + 68 * 16 = 1144
        let input = bytes!("FA00CA00DE");
        let bedrock_data_gas = l1_block_info.data_gas(&input, OpSpecId::BEDROCK);
        assert_eq!(bedrock_data_gas, U256::from(1144));

        // Regolith has no added 68 non zero bytes
        // gas cost = 3 * 16 + 2 * 4 = 56
        let regolith_data_gas = l1_block_info.data_gas(&input, OpSpecId::REGOLITH);
        assert_eq!(regolith_data_gas, U256::from(56));

        // Fjord has a minimum compressed size of 100 bytes
        // gas cost = 100 * 16 = 1600
        let fjord_data_gas = l1_block_info.data_gas(&input, OpSpecId::ARSIA);
        assert_eq!(fjord_data_gas, U256::from(1600));
    }

    #[test]
    fn test_calculate_tx_l1_cost() {
        // Generate a random token_ratio for testing (using a simple hash-based approach)
        // This ensures we test with different values while avoiding rand API compatibility issues
        let mut hasher = DefaultHasher::new();
        std::thread::current().id().hash(&mut hasher);
        let token_ratio = (hasher.finish() % 10_000) + 1;

        let mut l1_block_info = L1BlockInfo {
            l1_base_fee: U256::from(1_000),
            l1_fee_overhead: Some(U256::from(1_000)),
            l1_base_fee_scalar: U256::from(1_000),
            token_ratio: U256::from(token_ratio),
            ..Default::default()
        };

        let input = bytes!("FACADE");
        let gas_cost = l1_block_info.calculate_tx_l1_cost(&input, OpSpecId::REGOLITH);
        let expected_cost = U256::from(1048).saturating_mul(U256::from(token_ratio));
        assert_eq!(gas_cost, expected_cost);
        l1_block_info.clear_tx_l1_cost();

        // Zero rollup data gas cost should result in zero
        let input = bytes!("");
        let gas_cost = l1_block_info.calculate_tx_l1_cost(&input, OpSpecId::REGOLITH);
        assert_eq!(gas_cost, U256::ZERO);
        l1_block_info.clear_tx_l1_cost();

        // Deposit transactions with the EIP-2718 type of 0x7E should result in zero
        let input = bytes!("7EFACADE");
        let gas_cost = l1_block_info.calculate_tx_l1_cost(&input, OpSpecId::REGOLITH);
        assert_eq!(gas_cost, U256::ZERO);
    }

    #[test]
    fn test_calculate_tx_l1_cost_with_token_ratio_fjord() {
        // Test that token_ratio affects the result in Fjord
        // Generate a random token_ratio using a simple hash-based approach
        use std::{
            collections::hash_map::DefaultHasher,
            hash::{Hash, Hasher},
        };
        let mut hasher = DefaultHasher::new();
        std::thread::current().id().hash(&mut hasher);
        std::time::SystemTime::now().hash(&mut hasher);
        let token_ratio = (hasher.finish() % 10_000) + 1;

        let mut l1_block_info = L1BlockInfo {
            l1_base_fee: U256::from(1_000),
            l1_base_fee_scalar: U256::from(1_000),
            l1_blob_base_fee: Some(U256::from(1_000)),
            l1_blob_base_fee_scalar: Some(U256::from(1_000)),
            token_ratio: U256::from(token_ratio),
            ..Default::default()
        };

        let input = bytes!("FACADE");

        // Calculate cost with the random token_ratio
        let gas_cost_with_ratio = l1_block_info.calculate_tx_l1_cost(&input, OpSpecId::ARSIA);

        // Calculate expected cost: estimatedSize * l1FeeScaled / 1e12 * token_ratio
        // l1FeeScaled = baseFeeScalar*l1BaseFee*16 + blobFeeScalar*l1BlobBaseFee
        //             = 1000 * 1000 * 16 + 1000 * 1000 = 17_000_000
        // estimatedSize = 100_000_000 (minTransactionSize for small input like "FACADE")
        // gas_cost = 100_000_000 * 17_000_000 / 1_000_000_000_000 * token_ratio
        //         = 1_700_000_000_000 / 1_000_000_000_000 * token_ratio
        //         = 1_700 * token_ratio
        let base_cost = U256::from(1_700u64);
        let expected_cost = base_cost.saturating_mul(U256::from(token_ratio));

        assert_eq!(
            gas_cost_with_ratio, expected_cost,
            "Gas cost with token_ratio={} should be {}",
            token_ratio, expected_cost
        );

        // Verify that different token_ratio values produce proportional results
        l1_block_info.clear_tx_l1_cost();
        l1_block_info.token_ratio = U256::from(1);
        let gas_cost_ratio_1 = l1_block_info.calculate_tx_l1_cost(&input, OpSpecId::ARSIA);

        l1_block_info.clear_tx_l1_cost();
        l1_block_info.token_ratio = U256::from(2);
        let gas_cost_ratio_2 = l1_block_info.calculate_tx_l1_cost(&input, OpSpecId::ARSIA);

        // Cost with ratio=2 should be exactly double of cost with ratio=1
        assert_eq!(
            gas_cost_ratio_2,
            gas_cost_ratio_1.saturating_mul(U256::from(2)),
            "Gas cost should scale linearly with token_ratio"
        );
    }

    #[test]
    fn test_reset_l2_block() {
        let mut l1_block_info = L1BlockInfo { l2_block: Some(U256::from(1)), ..Default::default() };
        l1_block_info.reset_l2_block();
        assert_eq!(l1_block_info.l2_block, None);
    }

    #[test]
    // after arsia
    fn test_calculate_tx_l1_cost_fjord() {
        // l1FeeScaled = baseFeeScalar*l1BaseFee*16 + blobFeeScalar*l1BlobBaseFee
        //             = 1000 * 1000 * 16 + 1000 * 1000
        //             = 17e6
        let mut l1_block_info = L1BlockInfo {
            l1_base_fee: U256::from(1_000),
            l1_base_fee_scalar: U256::from(1_000),
            l1_blob_base_fee: Some(U256::from(1_000)),
            l1_blob_base_fee_scalar: Some(U256::from(1_000)),
            token_ratio: U256::from(1),
            ..Default::default()
        };

        // fastLzSize = 4
        // estimatedSize = max(minTransactionSize, intercept + fastlzCoef*fastlzSize)
        //               = max(100e6, 836500*4 - 42585600)
        //               = 100e6
        let input = bytes!("FACADE");
        // l1Cost = estimatedSize * l1FeeScaled / 1e12
        //        = 100e6 * 17 / 1e6
        //        = 1700
        let gas_cost = l1_block_info.calculate_tx_l1_cost(&input, OpSpecId::ARSIA);
        assert_eq!(gas_cost, U256::from(1700));
        l1_block_info.clear_tx_l1_cost();

        // fastLzSize = 202
        // estimatedSize = max(minTransactionSize, intercept + fastlzCoef*fastlzSize)
        //               = max(100e6, 836500*202 - 42585600)
        //               = 126387400
        let input = bytes!(
            "02f901550a758302df1483be21b88304743f94f80e51afb613d764fa61751affd3313c190a86bb870151bd62fd12adb8e41ef24f3f000000000000000000000000000000000000000000000000000000000000006e000000000000000000000000af88d065e77c8cc2239327c5edb3a432268e5831000000000000000000000000000000000000000000000000000000000003c1e5000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000a000000000000000000000000000000000000000000000000000000000000000148c89ed219d02f1a5be012c689b4f5b731827bebe000000000000000000000000c001a033fd89cb37c31b2cba46b6466e040c61fc9b2a3675a7f5f493ebd5ad77c497f8a07cdf65680e238392693019b4092f610222e71b7cec06449cb922b93b6a12744e"
        );
        // l1Cost = estimatedSize * l1FeeScaled / 1e12
        //        = 126387400 * 17 / 1e6
        //        = 2148
        let gas_cost = l1_block_info.calculate_tx_l1_cost(&input, OpSpecId::ARSIA);
        assert_eq!(gas_cost, U256::from(2148));
        l1_block_info.clear_tx_l1_cost();

        // Zero rollup data gas cost should result in zero
        let input = bytes!("");
        let gas_cost = l1_block_info.calculate_tx_l1_cost(&input, OpSpecId::ARSIA);
        assert_eq!(gas_cost, U256::ZERO);
        l1_block_info.clear_tx_l1_cost();

        // Deposit transactions with the EIP-2718 type of 0x7E should result in zero
        let input = bytes!("7EFACADE");
        let gas_cost = l1_block_info.calculate_tx_l1_cost(&input, OpSpecId::ARSIA);
        assert_eq!(gas_cost, U256::ZERO);
    }

    #[test]
    // after arsia
    fn calculate_tx_l1_cost_fjord() {
        // rig

        // L1 block info for OP mainnet fjord block 124665056
        // <https://optimistic.etherscan.io/block/124665056>
        let l1_block_info = L1BlockInfo {
            l1_base_fee: U256::from(1055991687),
            l1_base_fee_scalar: U256::from(5227),
            l1_blob_base_fee_scalar: Some(U256::from(1014213)),
            l1_blob_base_fee: Some(U256::from(1)),
            token_ratio: U256::from(1),
            ..Default::default() // L1 fee overhead (l1 gas used) deprecated since Fjord
        };

        // Second tx in OP mainnet Fjord block 124665056
        // <https://optimistic.etherscan.io/tx/0x1059e8004daff32caa1f1b1ef97fe3a07a8cf40508f5b835b66d9420d87c4a4a>
        const TX: &[u8] = &hex!(
            "02f904940a8303fba78401d6d2798401db2b6d830493e0943e6f4f7866654c18f536170780344aa8772950b680b904246a761202000000000000000000000000087000a300de7200382b55d40045000000e5d60e0000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000014000000000000000000000000000000000000000000000000000000000000000010000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000003a0000000000000000000000000000000000000000000000000000000000000022482ad56cb0000000000000000000000000000000000000000000000000000000000000020000000000000000000000000000000000000000000000000000000000000000200000000000000000000000000000000000000000000000000000000000000400000000000000000000000000000000000000000000000000000000000000120000000000000000000000000dc6ff44d5d932cbd77b52e5612ba0529dc6226f1000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000044095ea7b300000000000000000000000021c4928109acb0659a88ae5329b5374a3024694c0000000000000000000000000000000000000000000000049b9ca9a6943400000000000000000000000000000000000000000000000000000000000000000000000000000000000021c4928109acb0659a88ae5329b5374a3024694c000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000024b6b55f250000000000000000000000000000000000000000000000049b9ca9a694340000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000415ec214a3950bea839a7e6fbb0ba1540ac2076acd50820e2d5ef83d0902cdffb24a47aff7de5190290769c4f0a9c6fabf63012986a0d590b1b571547a8c7050ea1b00000000000000000000000000000000000000000000000000000000000000c080a06db770e6e25a617fe9652f0958bd9bd6e49281a53036906386ed39ec48eadf63a07f47cf51a4a40b4494cf26efc686709a9b03939e20ee27e59682f5faa536667e"
        );

        // L1 gas used for tx and L1 fee for tx, from OP mainnet block scanner
        // https://optimistic.etherscan.io/tx/0x1059e8004daff32caa1f1b1ef97fe3a07a8cf40508f5b835b66d9420d87c4a4a
        let expected_data_gas = U256::from(4471);
        let expected_l1_fee = U256::from_be_bytes(hex!(
            "00000000000000000000000000000000000000000000000000000005bf1ab43d"
        ));

        // test

        let data_gas = l1_block_info.data_gas(TX, OpSpecId::ARSIA);

        assert_eq!(data_gas, expected_data_gas);

        let l1_fee = l1_block_info.calculate_tx_l1_cost_arsia(TX);

        assert_eq!(l1_fee, expected_l1_fee)
    }

    #[test]
    fn calculate_tx_l1_cost_mantle_eip1559() {
        // rig
        //
        // <https://mantlescan.xyz/block/70683492>
        //
        // The token ratio changed at:
        // 70683076
        // 70683686 (70683492 is in between)
        // <https://mantlescan.xyz/tx/0xe1c72a781f15b0c23104101d52cc7562b520f7c62c9fa2a2269d9cadc8718c0e#eventlog>
        //
        // decoded from
        let mut l1_block_info = L1BlockInfo {
            l1_base_fee: U256::from_be_bytes(hex!(
                "00000000000000000000000000000000000000000000000000000001d04db9ad"
            )), // 7,789,722,029
            l1_fee_overhead: Some(U256::from_be_bytes(hex!(
                "00000000000000000000000000000000000000000000000000000000000000bc"
            ))), // 188
            l1_base_fee_scalar: U256::from_be_bytes(hex!(
                "0000000000000000000000000000000000000000000000000000000000002710"
            )), // 10,000
            token_ratio: U256::from(4368),
            ..Default::default()
        };

        // second tx in Mantle block 70683492
        // <https://mantlescan.xyz/tx/0xa061114290fbe3c06550e61d5c9cb39c575bad277f3c6a2459446b90b2b02577>
        const TX: &[u8] = &hex!(
            "02f901bf8213888202a584015752a084015752a084b48675bd94d9f4e85489adcd0baf0cd63b4231c6af58c2674589056bc75e2d63100000b9014483bd37f900000001cda86a272531e8640cd7f1a92c01839911b90bb009056bc75e2d63100000074dee7563cd80200147ae0001ac041df48df9791b0654f1dbbf2cc8450c5f2e9d0000000199550aaf158915c17ee0e0f81db48e4c7454b10400000001070202080004010103b24db100060000010200020600000302000006010004050102060001060700ff000000000000000000000000000000000000000000000000262255f4770aebe2d0c8b97a46287dcecc2a0aff78c1b0c915c4faa5fffa6cabf0219da63d7f4cb81bae52e2b8e401de1429b7ca94bb0abbf133ae34a125af1a4704044501fe12ca9567ef1550e430e8201eba5cc46d216ce6dc03f6a759e8e766e956ae8a6a1ed01989ff1c5ac6361c34cad9d7d0015ab4deaddeaddeaddeaddeaddeaddeaddeaddead11110000000000000000000000000000000000000000c001a008570dac13b3b52af488672a168d0f0ed4fd6da12d431c30a7326fd6f03dbe81a051ebad75b430e05b4d33a84c581c3cfd28820abdef2004234be4d8b7abe06f74"
        );

        // l1 gas used for tx and l1 fee for tx, from Mantle block scanner
        // <https://mantlescan.xyz/tx/0xa061114290fbe3c06550e61d5c9cb39c575bad277f3c6a2459446b90b2b02577>
        //
        let expected_l1_gas_used = U256::from(6564);
        let expected_l1_fee = U256::from_be_bytes(hex!(
            "0000000000000000000000000000000000000000000000000007ef4bec40587e" /* 223343420220019 wei */
        ));

        // test
        // TIPS: the Bedrock's l1GasUsed added the overhead, so we need to add it
        // <https://github.com/ethereum-optimism/op-geth/blob/v1.101411.0/core/types/rollup_cost.go#L206>
        let gas_used = l1_block_info.data_gas(TX, OpSpecId::REGOLITH) +
            l1_block_info.l1_fee_overhead.unwrap_or_default();
        assert_eq!(gas_used, expected_l1_gas_used);

        let l1_fee = l1_block_info.calculate_tx_l1_cost(TX, OpSpecId::REGOLITH);
        assert_eq!(l1_fee, expected_l1_fee)
    }

    #[test]
    fn calculate_tx_l1_cost_mantle_legacy() {
        // rig
        //
        // <https://mantlescan.xyz/block/70718078>
        //
        // decoded from
        let mut l1_block_info = L1BlockInfo {
            l1_base_fee: U256::from_be_bytes(hex!(
                "0000000000000000000000000000000000000000000000000000000168d7ab30"
            )),
            l1_fee_overhead: Some(U256::from_be_bytes(hex!(
                "00000000000000000000000000000000000000000000000000000000000000bc"
            ))), // 188
            l1_base_fee_scalar: U256::from_be_bytes(hex!(
                "0000000000000000000000000000000000000000000000000000000000002710"
            )), // 10,000
            token_ratio: U256::from(4359),
            ..Default::default()
        };

        // seventh tx in Mantle block 70718078
        // <https://mantlescan.xyz/tx/0x27e8441109b10bc4fa9ceceda6ffbebea47e8d38e3972939435c23dfa70df820>
        const TX: &[u8] = &hex!(
            "f8718301b78e8401312d008410d91858946b80e191f678a8378e1a3009eaf027b7515e88eb87282b83459182d080822733a0f820683c02811f2950e72c1a4c82c41bc31fcb9348c58a14bedbc4025e41bc5fa054896a3ee0dd03f8d29109c5ae6192f19338030ea045e208aeaf24cb11af6a6b"
        );

        // l1 gas used for tx and l1 fee for tx, from Mantle block scanner
        // <https://mantlescan.xyz/tx/0x27e8441109b10bc4fa9ceceda6ffbebea47e8d38e3972939435c23dfa70df820>
        let expected_l1_gas_used = U256::from(2016);
        let expected_l1_fee = U256::from_be_bytes(hex!(
            "0000000000000000000000000000000000000000000000000001e3dad743bf42" /* 00053200403062765 wei */
        ));

        // test
        let gas_used = l1_block_info.data_gas(TX, OpSpecId::REGOLITH) +
            l1_block_info.l1_fee_overhead.unwrap_or_default();
        assert_eq!(gas_used, expected_l1_gas_used);

        let l1_fee = l1_block_info.calculate_tx_l1_cost(TX, OpSpecId::REGOLITH);
        assert_eq!(l1_fee, expected_l1_fee)
    }

    #[test]
    fn test_operator_fee_charge_formulas() {
        let l1_block_info = L1BlockInfo {
            operator_fee_scalar: Some(U256::from(1_000u64)),
            operator_fee_constant: Some(U256::from(10u64)),
            ..Default::default()
        };

        let input = [0x01u8];

        let operator_fee = l1_block_info.operator_fee_charge(&input, U256::from(1_000u64));
        assert_eq!(operator_fee, U256::from(100_000_010u64));
    }

    #[test]
    fn test_operator_fee_refund() {
        let gas = Gas::new(50000);

        let l1_block_info = L1BlockInfo {
            l1_base_fee: U256::from(1055991687),
            l1_base_fee_scalar: U256::from(5227),
            operator_fee_scalar: Some(U256::from(2000)),
            operator_fee_constant: Some(U256::from(5)),
            ..Default::default()
        };

        let isthmus_refunded = l1_block_info.operator_fee_refund(&gas, OpSpecId::ISTHMUS);
        assert_eq!(isthmus_refunded, U256::ZERO);

        let arsia_refunded = l1_block_info.operator_fee_refund(&gas, OpSpecId::ARSIA);
        assert_eq!(arsia_refunded, U256::from(10_000_000_000u64))
    }
}
