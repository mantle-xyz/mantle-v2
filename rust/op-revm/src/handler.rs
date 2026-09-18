//!Handler related to Optimism chain
use crate::{
    BvmEth, L1BlockInfo, OpHaltReason, OpSpecId,
    api::exec::OpContextTr,
    constants::{
        BASE_FEE_RECIPIENT, GAS_ORACLE_CONTRACT, L1_FEE_RECIPIENT, OPERATOR_FEE_RECIPIENT,
    },
    transaction::{
        OpTransactionError, OpTxTr, bvm_eth::JournalColdExt, deposit::DEPOSIT_TRANSACTION_TYPE,
    },
};
use revm::{
    context::{
        LocalContextTr,
        journaled_state::{JournalCheckpoint, account::JournaledAccountTr},
        result::InvalidTransaction,
    },
    context_interface::{
        Block, Cfg, ContextTr, JournalTr, Transaction,
        context::take_error,
        result::{EVMError, ExecutionResult, FromStringError, ResultGas},
    },
    handler::{
        EthFrame, EvmTr, FrameResult, Handler, MainnetHandler,
        evm::FrameTr,
        handler::EvmTrError,
        post_execution::{self, reimburse_caller},
        pre_execution::{calculate_caller_fee, validate_account_nonce_and_code_with_components},
        validation::validate_tx_env,
    },
    inspector::{Inspector, InspectorEvmTr, InspectorHandler},
    interpreter::{
        Gas, InitialAndFloorGas, interpreter::EthInterpreter, interpreter_action::FrameInit,
    },
    primitives::{U256, hardfork::SpecId},
};
use std::{boxed::Box, vec};

/// Optimism handler extends the [`Handler`] with Optimism specific logic.
#[derive(Debug, Clone)]
pub struct OpHandler<EVM, ERROR, FRAME> {
    /// Mainnet handler allows us to use functions from the mainnet handler inside optimism
    /// handler. So we dont duplicate the logic
    pub mainnet: MainnetHandler<EVM, ERROR, FRAME>,
}

impl<EVM, ERROR, FRAME> OpHandler<EVM, ERROR, FRAME> {
    /// Create a new Optimism handler.
    pub fn new() -> Self {
        Self { mainnet: MainnetHandler::default() }
    }
}

impl<EVM, ERROR, FRAME> Default for OpHandler<EVM, ERROR, FRAME> {
    fn default() -> Self {
        Self::new()
    }
}

/// Scales refund counter by token ratio using geth-like `uint64` arithmetic first,
/// then maps into revm's signed refund representation.
fn scale_refund_with_token_ratio(refund: i64, token_ratio_u64: u64) -> i64 {
    let refund_u64 = u64::try_from(refund).unwrap_or_default();
    let scaled_u64 = refund_u64.wrapping_mul(token_ratio_u64);
    i64::try_from(scaled_u64).unwrap_or(i64::MAX)
}

/// Trait to check if the error is a transaction error.
///
/// Used in `cache_error` handler to catch deposit transaction that was halted.
pub trait IsTxError {
    /// Check if the error is a transaction error.
    fn is_tx_error(&self) -> bool;
}

impl<DB, TX> IsTxError for EVMError<DB, TX> {
    fn is_tx_error(&self) -> bool {
        matches!(self, Self::Transaction(_))
    }
}

impl<EVM, ERROR, FRAME> Handler for OpHandler<EVM, ERROR, FRAME>
where
    EVM: EvmTr<Context: OpContextTr, Frame = FRAME>,
    <<EVM as EvmTr>::Context as ContextTr>::Journal: JournalColdExt,
    ERROR: EvmTrError<EVM> + From<OpTransactionError> + FromStringError + IsTxError,
    // TODO `FrameResult` should be a generic trait.
    // TODO `FrameInit` should be a generic.
    FRAME: FrameTr<FrameResult = FrameResult, FrameInit = FrameInit>,
{
    type Evm = EVM;
    type Error = ERROR;
    type HaltReason = OpHaltReason;

    fn validate_env(&self, evm: &mut Self::Evm) -> Result<(), Self::Error> {
        // Do not perform any extra validation for deposit transactions, they are pre-verified on
        // L1.
        let ctx = evm.ctx();
        let tx = ctx.tx();
        let tx_type = tx.tx_type();
        if tx_type == DEPOSIT_TRANSACTION_TYPE {
            // Do not allow for a system transaction to be processed if Regolith is enabled.
            if tx.is_system_transaction() &&
                evm.ctx().cfg().spec().is_enabled_in(OpSpecId::REGOLITH)
            {
                return Err(OpTransactionError::DepositSystemTxPostRegolith.into());
            }
            return Ok(());
        }

        // Check that non-deposit transactions have enveloped_tx set
        if tx.enveloped_tx().is_none() {
            return Err(OpTransactionError::MissingEnvelopedTx.into());
        }

        let spec = ctx.cfg().spec();
        if spec.is_enabled_in(OpSpecId::ARSIA) {
            self.mainnet.validate_env(evm)
        } else {
            validate_tx_env(ctx, spec.into_eth_spec()).map_err(Into::into)
        }
    }

    /**
     * Validates and calculates the initial gas requirements for a transaction.
     *
     * For deposit transactions, this simply returns the base gas calculation from the mainnet
     * handler. For non-deposit transactions, this method:
     * 1. Updates L1 block info in the following scenarios:
     *    - When the current transaction belongs to a different block than previously cached
     *    - When the transaction target is the gas oracle contract, indicating a token ratio
     *      update that needs to be reloaded for the next transaction
     * 2. Adjusts both initial_gas and floor_gas by multiplying with the token ratio
     * 3. If ARSIA is enabled, we don't need to multiply the initial gas and floor gas by the
     *    token ratio
     *
     * The token ratio adjustment is essential for properly accounting for the price difference
     * between ETH and MNT.
     */
    fn validate_initial_tx_gas(
        &self,
        evm: &mut Self::Evm,
    ) -> Result<InitialAndFloorGas, Self::Error> {
        let mut initial_gas = self.mainnet.validate_initial_tx_gas(evm)?;

        let (block, tx, cfg, journal, chain, _) = evm.ctx().all_mut();
        let spec = cfg.spec();
        if tx.tx_type() == DEPOSIT_TRANSACTION_TYPE {
            Ok(initial_gas)
        } else {
            // L1 block info is stored in the context for later use.
            // and it will be reloaded from the database if it is not for the current block.
            if chain.l2_block != Some(block.number()) {
                *chain = L1BlockInfo::try_fetch(journal.db_mut(), block.number(), spec)?;
            }

            // Reset the l2_block if the tx is set token ratio, we need reload token ratio from the
            // database in next transaction
            if tx.kind().to() == Some(&GAS_ORACLE_CONTRACT) {
                chain.reset_l2_block();
            }

            if !cfg.spec().is_enabled_in(OpSpecId::ARSIA) {
                // if the tx is not a deposit transaction and ARSIA is not enabled, we need to
                // multiply the initial gas by the token ratio Keep behavior aligned
                // with op-geth: Uint256 token ratio is truncated to low 64 bits.
                let token_ratio = chain.token_ratio.as_limbs()[0].max(1);
                // [MANTLE] revm 41 removed the `initial_total_gas` field; it is now a
                // derived value, `initial_regular_gas + initial_state_gas - state_refund`.
                // The original semantics were "scale the total by token_ratio", so the
                // `checked_mul` is still applied to the total (preserving the original
                // overflow check) and the result is then written entirely into the regular
                // component with the state component zeroed, so that `initial_total_gas()`
                // still equals the scaled total. Mantle does not enable EIP-8037, so the
                // state component is always zero anyway.
                let scaled_total = initial_gas
                    .initial_total_gas()
                    .checked_mul(token_ratio)
                    .ok_or(InvalidTransaction::CallerGasLimitMoreThanBlock)?;
                initial_gas.initial_regular_gas = scaled_total;
                initial_gas.initial_state_gas = 0;
                initial_gas.state_refund = 0;

                initial_gas.floor_gas = initial_gas
                    .floor_gas
                    .checked_mul(token_ratio)
                    .ok_or(InvalidTransaction::CallerGasLimitMoreThanBlock)?;
            }

            Ok(initial_gas)
        }
    }

    fn validate_against_state_and_deduct_caller(
        &self,
        evm: &mut Self::Evm,
        _init_and_floor_gas: &mut InitialAndFloorGas,
    ) -> Result<(), Self::Error> {
        let ctx = evm.ctx();
        let is_deposit = ctx.tx().tx_type() == DEPOSIT_TRANSACTION_TYPE;

        if is_deposit {
            // Process ETH deposit by minting and transferring BVM_ETH tokens.
            // process_eth_deposit resets BVM_ETH (account + touched storage slots) back to
            // cold afterwards, so subsequent EVM execution observes it as cold — matching
            // op-geth's StateDB.SetState() mint which never touches the EVM access list.
            BvmEth::process_eth_deposit(ctx, false).map_err(ERROR::from)?;
        }

        let (block, tx, cfg, journal, chain, _) = evm.ctx().all_mut();
        let spec = cfg.spec();

        if is_deposit {
            let basefee = block.basefee() as u128;
            let blob_price = block.blob_gasprice().unwrap_or_default();
            // deposit skips max fee check and just deducts the effective balance spending.

            let mut caller = journal.load_account_with_code_mut(tx.caller())?.data;

            let effective_balance_spending = tx
                .effective_balance_spending(basefee, blob_price)
                .expect("Deposit transaction effective balance spending overflow") -
                tx.value();

            // Mind value should be added first before subtracting the effective balance spending.
            let mut new_balance = caller
                .balance()
                .saturating_add(U256::from(tx.mint().unwrap_or_default()))
                .saturating_sub(effective_balance_spending);

            if cfg.is_balance_check_disabled() {
                // Make sure the caller's balance is at least the value of the transaction.
                // this is not consensus critical, and it is used in testing.
                new_balance = new_balance.max(tx.value());
            }

            // set the new balance and bump the nonce if it is a call
            caller.set_balance(new_balance);
            if tx.kind().is_call() {
                caller.bump_nonce();
            }

            // Pre-call gates that op-geth applies inside innerExecute() and routes to the
            // mint-only failed-deposit path (RevertToSnapshot + gasUsed=GasLimit). op-revm
            // skips env validation for deposits ("pre-verified on L1"), so without these the
            // condition leaks into the EVM frame and takes the wrong (success/keep-transfer)
            // route, diverging from op-geth. See state_transition.go innerExecute().

            // (#5) EIP-3860 init code size limit for CREATE deposits (state_transition.go L786).
            if tx.kind().is_create() &&
                spec.into_eth_spec().is_enabled_in(SpecId::SHANGHAI) &&
                tx.input().len() > cfg.max_initcode_size()
            {
                return Err(InvalidTransaction::CreateInitCodeSizeLimit.into());
            }
            // (#4) Native (MNT) value transfer must be affordable (CanTransfer, L781), checked
            // against the post-mint balance, mirroring op-geth (mint is applied pre-snapshot).
            if !cfg.is_balance_check_disabled() && *caller.balance() < tx.value() {
                return Err(InvalidTransaction::LackOfFundForMaxFee {
                    fee: Box::new(tx.value()),
                    balance: Box::new(*caller.balance()),
                }
                .into());
            }

            return Ok(());
        }

        // NOTE: L1BlockInfo refresh is handled by validate_initial_tx_gas (which runs
        // before this function). That function calls try_fetch when l2_block is stale and
        // reset_l2_block when the tx targets GAS_ORACLE_CONTRACT. Do NOT re-fetch here
        // via journal.db_mut() — it bypasses the journal and reads stale storage, missing
        // intra-block setTokenRatio updates that are only visible through journal.sload().

        let mut caller_account = journal.load_account_with_code_mut(tx.caller())?.data;

        // validates account nonce and code
        validate_account_nonce_and_code_with_components(&caller_account.account().info, tx, cfg)?;

        // check additional cost and deduct it from the caller's balances
        let mut balance = caller_account.account().info.balance;

        // [MANTLE] - if ARSIA is enabled, we need to calculate the additional cost and deduct it
        // from the caller's balances
        if !cfg.is_fee_charge_disabled() && cfg.spec().is_enabled_in(OpSpecId::ARSIA) {
            let Some(additional_cost) = chain.tx_cost_with_tx(tx, spec) else {
                return Err(OpTransactionError::MissingEnvelopedTx.into());
            };
            let Some(new_balance) = balance.checked_sub(additional_cost) else {
                return Err(InvalidTransaction::LackOfFundForMaxFee {
                    fee: Box::new(additional_cost),
                    balance: Box::new(balance),
                }
                .into());
            };
            balance = new_balance
        }

        let balance = calculate_caller_fee(balance, tx, block, cfg)?;

        // make changes to the account
        caller_account.set_balance(balance);
        if tx.kind().is_call() {
            caller_account.bump_nonce();
        }

        Ok(())
    }

    fn last_frame_result(
        &mut self,
        evm: &mut Self::Evm,
        _original_reservoir: u64,
        frame_result: &mut <<Self::Evm as EvmTr>::Frame as FrameTr>::FrameResult,
    ) -> Result<(), Self::Error> {
        let ctx = evm.ctx();
        let tx = ctx.tx();
        let is_deposit = tx.tx_type() == DEPOSIT_TRANSACTION_TYPE;
        let tx_gas_limit = tx.gas_limit();
        let is_regolith = ctx.cfg().spec().is_enabled_in(OpSpecId::REGOLITH);

        let instruction_result = frame_result.interpreter_result().result;
        // Detect a failed top-level CREATE so the intrinsic `create_state_gas`
        // charged at tx entry can be unwound below. Mirrors the `create_failed`
        // condition used in `EthFrame::return_result` for nested creates.
        let create_failed =
            matches!(frame_result, FrameResult::Create(_)) && !instruction_result.is_ok();
        let gas = frame_result.gas_mut();
        let remaining = gas.remaining();
        let refunded = gas.refunded();
        let reservoir = gas.reservoir();
        let state_gas_spent = gas.state_gas_spent();

        // Spend the gas limit. Gas is reimbursed when the tx returns successfully.
        *gas = Gas::new_spent_with_reservoir(tx_gas_limit, reservoir);

        if instruction_result.is_ok() {
            // On Optimism, deposit transactions report gas usage uniquely to other
            // transactions due to them being pre-paid on L1.
            //
            // Hardfork Behavior:
            // - Bedrock (success path):
            //   - Deposit transactions (non-system) report their gas limit as the usage. No
            //     refunds.
            //   - Deposit transactions (system) report 0 gas used. No refunds.
            //   - Regular transactions report gas usage as normal.
            // - Regolith (success path):
            //   - Deposit transactions (all) report their gas used as normal. Refunds enabled.
            //   - Regular transactions report their gas used as normal.
            if !is_deposit || is_regolith {
                // Return unused regular gas and unused reservoir gas.
                gas.erase_cost(remaining);
                gas.record_refund(refunded);
            } else if is_deposit && tx.is_system_transaction() {
                // System transactions were a special type of deposit transaction in
                // the Bedrock hardfork that did not incur any gas costs.
                gas.erase_cost(tx_gas_limit);
            }
        } else if instruction_result.is_revert() {
            // On Optimism, deposit transactions report gas usage uniquely to other
            // transactions due to them being pre-paid on L1.
            //
            // Hardfork Behavior:
            // - Bedrock (revert path):
            //   - Deposit transactions (all) report the gas limit as the amount of gas used on
            //     failure. No refunds.
            //   - Regular transactions receive a refund on remaining gas as normal.
            // - Regolith (revert path):
            //   - Deposit transactions (all) report the actual gas used as the amount of gas used
            //     on failure. Refunds on remaining gas enabled.
            //   - Regular transactions receive a refund on remaining gas as normal.
            if !is_deposit || is_regolith {
                // Return unused regular gas.
                gas.erase_cost(remaining);
            }
        }

        if instruction_result.is_ok() {
            // Restore state_gas_spent on successful paths (lost by the Gas overwrite above;
            // the reservoir is carried over by the constructor).
            gas.set_state_gas_spent(state_gas_spent);
        } else {
            // On failure - zero execution state gas: [bal-devnet notes](<https://notes.ethereum.org/@ethpandaops/bal-devnet-4#Changes-vs-bal-devnet-3>)
            // and [specs](<https://github.com/ethereum/EIPs/pull/11476>)
            //
            // State changes rolled back, so recover the pre-tx reservoir value: signed
            // `reservoir + state_gas_spent` (state_gas_spent can be negative when 0→x→0
            // restoration refilled more than this tx charged).
            gas.set_state_gas_spent(0);
            gas.set_reservoir(reservoir.saturating_add_signed(state_gas_spent));
        }

        // EIP-8037: for a failed top-level CREATE (or one that self-destructs in init
        // code, see EIP-6780), refund the intrinsic `create_state_gas` to the reservoir.
        // At the top level the charge is deducted in `initial_gas_and_reservoir` rather
        // than via `record_state_cost`, so it would otherwise stay consumed when the
        // deployment is rolled back or erased.
        if create_failed && evm.ctx().cfg().is_amsterdam_eip8037_enabled() {
            let state_gas_charged = evm.ctx().cfg().gas_params().create_state_gas();
            gas.refill_reservoir(state_gas_charged);
        }

        Ok(())
    }

    fn reimburse_caller(
        &self,
        evm: &mut Self::Evm,
        frame_result: &mut <<Self::Evm as EvmTr>::Frame as FrameTr>::FrameResult,
    ) -> Result<(), Self::Error> {
        let additional_refund = if evm.ctx().tx().tx_type() != DEPOSIT_TRANSACTION_TYPE &&
            !evm.ctx().cfg().is_fee_charge_disabled()
        {
            let spec = evm.ctx().cfg().spec();
            evm.ctx().chain().operator_fee_refund(frame_result.gas(), spec)
        } else {
            U256::ZERO
        };

        reimburse_caller(evm.ctx(), frame_result.gas(), additional_refund).map_err(From::from)
    }

    fn execution(
        &mut self,
        evm: &mut Self::Evm,
        init_and_floor_gas: &InitialAndFloorGas,
    ) -> Result<FrameResult, Self::Error> {
        let (block, tx, cfg, _, chain, _) = evm.ctx().all_mut();

        if cfg.spec().is_enabled_in(OpSpecId::ARSIA) {
            return self.mainnet.execution(evm, init_and_floor_gas);
        }

        let is_deposit = tx.tx_type() == DEPOSIT_TRANSACTION_TYPE;
        let mut gas_limit = tx.gas_limit() - init_and_floor_gas.initial_total_gas();

        // l1cost = l1cost / effective_gas_price
        // gas_limit = gas_limit - l1cost
        // gas_limit = gas_limit / token_ratio
        if !is_deposit {
            let spec = cfg.spec();

            let enveloped_tx =
                tx.enveloped_tx().expect("all not deposit tx have enveloped tx").clone();
            let basefee = block.basefee() as u128;
            let mut tx_l1_cost = chain.calculate_tx_l1_cost(&enveloped_tx, spec);
            let effective_gas_price = tx.effective_gas_price(basefee);

            if effective_gas_price > 0 {
                tx_l1_cost = tx_l1_cost.wrapping_div(U256::from(effective_gas_price));
            }

            if tx_l1_cost.gt(&U256::from(gas_limit)) {
                return Err(ERROR::from(OpTransactionError::Base(
                    InvalidTransaction::CallGasCostMoreThanGasLimit {
                        initial_gas: init_and_floor_gas.initial_total_gas(),
                        gas_limit,
                    },
                )));
            }

            // Edge case: if token ratio is zero, set it to 1.
            // This is only possible if the token ratio is not set at all.
            let token_ratio = chain.token_ratio.max(U256::from(1));
            // Keep behavior aligned with op-geth: truncate to low 64 bits and ensure non-zero
            // divisor.
            let token_ratio_u64 = token_ratio.as_limbs()[0].max(1);
            let tx_l1_cost_u64: u64 =
                tx_l1_cost.try_into().map_err(|_| OpTransactionError::TxL1CostOutOfRange)?;
            gas_limit = gas_limit.wrapping_sub(tx_l1_cost_u64).wrapping_div(token_ratio_u64);
        }

        // Create first frame action
        let first_frame_input = self.first_frame_input(evm, gas_limit, 0)?;

        // Run execution loop
        let mut frame_result = self.run_exec_loop(evm, first_frame_input)?;

        // Handle last frame result
        // [MANTLE] reservoir is 0: EIP-8037 state gas is not enabled on Mantle (same as the
        // `first_frame_input` call above).
        self.last_frame_result(evm, 0, &mut frame_result)?;
        Ok(frame_result)
    }

    fn refund(
        &self,
        evm: &mut Self::Evm,
        frame_result: &mut <<Self::Evm as EvmTr>::Frame as FrameTr>::FrameResult,
        eip7702_refund: i64,
    ) {
        frame_result.gas_mut().record_refund(eip7702_refund);
        let (_, tx, cfg, _, chain, _) = evm.ctx().all_mut();
        let is_deposit = tx.tx_type() == DEPOSIT_TRANSACTION_TYPE;
        let is_london = cfg.spec().into_eth_spec().is_enabled_in(SpecId::LONDON);
        let gas = frame_result.gas_mut();
        let is_system = tx.is_system_transaction();

        // BVM_ETH warm/cold is reset to cold inside process_eth_deposit (see JournalColdExt),
        // so subsequent EVM execution observes it cold exactly like op-geth. No static gas
        // compensation is needed here (the previous mint-gas-compensation hack was removed —
        // it over/under-compensated depending on the EVM access path).

        let limit = gas.limit();
        // Keep behavior aligned with op-geth: Uint256 token ratio is truncated to low 64 bits.
        let token_ratio_u64 = chain.token_ratio.as_limbs()[0].max(1);

        let is_arsia = cfg.spec().is_enabled_in(OpSpecId::ARSIA);

        if !is_system && !is_deposit {
            if !is_arsia {
                //  adjust limit temporarily for refund calculation
                if token_ratio_u64 > 0 {
                    gas.set_limit(gas.limit().saturating_div(token_ratio_u64));
                }
            }

            // Calculate final refund based on (possibly adjusted) limit
            gas.set_final_refund(is_london);

            if !is_arsia {
                // scale refund and remaining by token_ratio, restore limit
                gas.set_refund(scale_refund_with_token_ratio(gas.refunded(), token_ratio_u64));
                gas.set_remaining(gas.remaining().saturating_mul(token_ratio_u64));
                gas.set_limit(limit);
            }
        } else {
            // Deposit and system transactions: no refunds
            gas.set_refund(0);
            gas.set_final_refund(is_london);
        }
    }

    fn reward_beneficiary(
        &self,
        evm: &mut Self::Evm,
        frame_result: &mut <<Self::Evm as EvmTr>::Frame as FrameTr>::FrameResult,
    ) -> Result<(), Self::Error> {
        let is_deposit = evm.ctx().tx().tx_type() == DEPOSIT_TRANSACTION_TYPE;

        // Transfer fee to coinbase/beneficiary.
        if is_deposit {
            return Ok(());
        }

        self.mainnet.reward_beneficiary(evm, frame_result)?;
        let basefee = evm.ctx().block().basefee() as u128;

        // If the transaction is not a deposit transaction, fees are paid out
        // to both the Base Fee Vault as well as the L1 Fee Vault.
        // Use all_mut() to simultaneously borrow tx (immutable) and chain (mutable),
        // avoiding an unnecessary clone of the enveloped transaction bytes.
        let (_, tx, cfg, journal, l1_block_info, _) = evm.ctx().all_mut();
        let spec = cfg.spec();

        let Some(enveloped_tx) = tx.enveloped_tx() else {
            return Err(OpTransactionError::MissingEnvelopedTx.into());
        };

        let l1_cost = l1_block_info.calculate_tx_l1_cost(enveloped_tx, spec);
        // Exclude reservoir gas (EIP-8037) from used gas — reservoir is unused and reimbursed.
        let effective_used =
            frame_result.gas().used().saturating_sub(frame_result.gas().reservoir());
        // [MANTLE] operator fee is enabled from Arsia, not Isthmus. Upstream op-revm 20 also
        // added a `spec` parameter to operator_fee_charge / _inner to branch on the pre-Jovian
        // formula; Mantle deliberately has no such branch (it always uses the Jovian form), so
        // the 2-arg shape is kept. See MANTLE_CHANGES.md §5.1 "the 2-arg operator_fee_charge
        // call sites".
        let operator_fee_cost = if spec.is_enabled_in(OpSpecId::ARSIA) {
            l1_block_info.operator_fee_charge(enveloped_tx, U256::from(effective_used))
        } else {
            U256::ZERO
        };
        let base_fee_amount = U256::from(basefee.saturating_mul(effective_used as u128));

        // [MANTLE] - Send fees to their respective recipients
        let mut recipients = vec![(BASE_FEE_RECIPIENT, base_fee_amount)];
        if spec.is_enabled_in(OpSpecId::ARSIA) {
            recipients
                .extend([(L1_FEE_RECIPIENT, l1_cost), (OPERATOR_FEE_RECIPIENT, operator_fee_cost)]);
        }
        for (recipient, amount) in recipients {
            journal.balance_incr(recipient, amount)?;
        }

        Ok(())
    }

    fn execution_result(
        &mut self,
        evm: &mut Self::Evm,
        frame_result: <<Self::Evm as EvmTr>::Frame as FrameTr>::FrameResult,
        result_gas: ResultGas,
    ) -> Result<ExecutionResult<Self::HaltReason>, Self::Error> {
        take_error::<Self::Error, _>(evm.ctx().error())?;

        let exec_result = post_execution::output(evm.ctx(), frame_result, result_gas)
            .map_haltreason(OpHaltReason::Base);

        // [MANTLE] - failed deposit that ENTERED EVM execution (op-geth's "Case B").
        //
        // A deposit whose EVM frame fails by REVERT or HALT (out-of-gas / exceptional)
        // is, in op-geth, a "vm error" stored in `result.Err` with the Go `err == nil`
        // ("vm errors do not effect consensus and are therefore not assigned to err"), so
        // op-geth does NOT rewind: the BVM_ETH mint + transfer applied before the EVM call
        // (mintBVMETH before the revert snapshot, transferBVMETH at the top of innerExecute)
        // — and their Mint/Transfer logs — survive into the receipt, with the ACTUAL gas
        // used (a revert refunds, a halt consumes all). op-geth treats REVERT and OOG
        // identically here, so both surface as a status-0 failed deposit.
        //
        // On revm those mint/transfer state changes and logs are emitted in
        // `validate_against_state_and_deduct_caller` BEFORE the top-level frame checkpoint,
        // so the frame's revert/halt (which truncates the journal entries + logs back to
        // that checkpoint) leaves them intact, and `post_execution::output` already
        // collected the surviving logs into `exec_result` via `take_logs()`. We therefore
        // only (1) keep the surviving logs + actual gas and (2) re-label the result as a
        // `FailedDeposit` halt — the consistent receipt shape op-geth produces for both
        // failure modes. No checkpoint_revert and no re-mint: the surviving state is already
        // correct (including the CREATE transfer recipient `create(caller, pre-bump nonce)`
        // and the caller nonce bump), so rebuilding it would only risk divergence.
        //
        // Deposits that fail BEFORE reaching execution (insufficient BVM_ETH transfer
        // balance, intrinsic/gas/validation) are op-geth's "Case A" (`innerExecute` returns
        // a Go error -> RevertToSnapshot), handled in `catch_error` (mint only).
        let is_deposit = evm.ctx().tx().tx_type() == DEPOSIT_TRANSACTION_TYPE;
        let is_regolith = evm.ctx().cfg().spec().is_enabled_in(OpSpecId::REGOLITH);
        let exec_result = if is_deposit &&
            is_regolith &&
            matches!(exec_result, ExecutionResult::Revert { .. } | ExecutionResult::Halt { .. })
        {
            // Carry the full ResultGas unchanged (floor/state-gas aware) — collapsing it to
            // total_gas_spent only would diverge on EIP-7623 / refunded-gas deposits.
            let gas = *exec_result.gas();
            let logs = exec_result.into_logs();
            ExecutionResult::Halt { reason: OpHaltReason::FailedDeposit, gas, logs }
        } else {
            exec_result
        };

        // Single cleanup for both the failed-deposit re-label and the normal path.
        evm.ctx().journal_mut().commit_tx();
        evm.ctx().chain_mut().clear_tx_l1_cost();
        evm.ctx().local_mut().clear();
        evm.frame_stack().clear();

        Ok(exec_result)
    }

    fn catch_error(
        &self,
        evm: &mut Self::Evm,
        error: Self::Error,
    ) -> Result<ExecutionResult<Self::HaltReason>, Self::Error> {
        let is_deposit = evm.ctx().tx().tx_type() == DEPOSIT_TRANSACTION_TYPE;
        let is_tx_error = error.is_tx_error();

        // Deposit transaction can't fail so we manually handle it here.
        let output = if is_tx_error && is_deposit {
            let ctx = evm.ctx();
            let spec = ctx.cfg().spec();
            let tx = ctx.tx();
            let caller = tx.caller();
            let mint = tx.mint();
            let is_system_tx = tx.is_system_transaction();
            let gas_limit = tx.gas_limit();

            // Revert all transaction changes first.
            // Default JournalCheckpoint is the first checkpoint and will wipe all changes.
            evm.ctx().journal_mut().checkpoint_revert(JournalCheckpoint::default());

            // [MANTLE] - BVM_ETH
            // If the transaction is a deposit transaction and it failed
            // for any reason, the caller nonce must be bumped, and the
            // gas reported must be altered depending on the Hardfork. This is
            // also returned as a special Halt variant so that consumers can more
            // easily distinguish between a failed deposit and a failed
            // normal transaction.
            //
            // Increment sender nonce and account balance for the mint amount. Deposits
            // always persist the mint amount, even if the transaction fails.
            //
            // All fallible operations use match (not ?) so errors flow to the
            // unified cleanup at the bottom instead of bypassing it.
            let nonce_mint_result = {
                let journal = evm.ctx().journal_mut();
                journal.load_account_mut(caller).map(|mut acc| {
                    acc.bump_nonce();
                    acc.incr_balance(U256::from(mint.unwrap_or_default()));
                })
            };

            match nonce_mint_result {
                Ok(()) => {
                    // Commit nonce bump and mint balance immediately.
                    // Per the OP deposit spec, these MUST persist even when
                    // subsequent operations (e.g. BVM_ETH minting) fail.
                    // Committing here moves them out of the journal so a later
                    // discard_tx() cannot revert them.
                    evm.ctx().journal_mut().commit_tx();

                    // Mint BVM_ETH tokens for the failed deposit (no transfer).
                    //
                    // This is op-geth's Case A (innerExecute returns a Go error, e.g. the
                    // BVM_ETH transfer's ErrEthTxValueTooLarge, or intrinsic/validation):
                    // execute() RevertToSnapshot(snap) undoes the transfer but the snapshot
                    // is taken AFTER mintBVMETH, so only the Mint (and its log) persists.
                    match BvmEth::process_eth_deposit(evm.ctx(), true).map_err(ERROR::from) {
                        Ok(()) => {
                            // Capture the BVM_ETH Mint log BEFORE commit_tx clears the journal
                            // logs (see JournalInner::commit_tx). op-geth emits the mint before
                            // its revert snapshot, so a failed deposit still persists the Mint
                            // log into the receipt; it must survive here to match op-geth's
                            // receipts root. Mint only (no transfer) on this path.
                            let logs = evm.ctx().journal_mut().take_logs();
                            evm.ctx().journal_mut().commit_tx();

                            // The gas used of a failed deposit post-regolith is the gas
                            // limit of the transaction. pre-regolith, it is the gas limit
                            // of the transaction for non system transactions and 0 for
                            // system transactions.
                            let gas_used =
                                if spec.is_enabled_in(OpSpecId::REGOLITH) || !is_system_tx {
                                    gas_limit
                                } else {
                                    0
                                };
                            Ok(ExecutionResult::Halt {
                                reason: OpHaltReason::FailedDeposit,
                                gas: ResultGas::default().with_total_gas_spent(gas_used),
                                logs,
                            })
                        }
                        Err(e) => Err(e),
                    }
                }
                Err(e) => Err(e.into()),
            }
        } else {
            Err(error)
        };

        // Unified cleanup for all exit paths.
        // discard_tx() on error ensures no partial state (nonce bump, balance credit,
        // BVM_ETH mint) leaks into the next transaction.
        if output.is_err() {
            evm.ctx().journal_mut().discard_tx();
        }
        evm.ctx().chain_mut().clear_tx_l1_cost();
        evm.ctx().local_mut().clear();
        evm.frame_stack().clear();

        output
    }
}

impl<EVM, ERROR> InspectorHandler for OpHandler<EVM, ERROR, EthFrame<EthInterpreter>>
where
    EVM: InspectorEvmTr<
            Context: OpContextTr,
            Frame = EthFrame<EthInterpreter>,
            Inspector: Inspector<<<Self as Handler>::Evm as EvmTr>::Context, EthInterpreter>,
        >,
    ERROR: EvmTrError<EVM> + From<OpTransactionError> + FromStringError + IsTxError,
{
    type IT = EthInterpreter;
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{
        DefaultOp, OpBuilder, OpTransaction,
        api::default_ctx::OpContext,
        constants::{
            BASE_FEE_SCALAR_OFFSET, BLOB_BASE_FEE_SCALAR_OFFSET, ECOTONE_L1_BLOB_BASE_FEE_SLOT,
            ECOTONE_L1_FEE_SCALARS_SLOT, GAS_ORACLE_CONTRACT, L1_BASE_FEE_SLOT, L1_BLOCK_CONTRACT,
            L1_OVERHEAD_SLOT, L1_SCALAR_SLOT, OPERATOR_FEE_SCALARS_SLOT, TOKEN_RATIO_SLOT,
        },
    };
    use alloy_primitives::uint;
    use revm::{
        context::{BlockEnv, CfgEnv, Context, TxEnv},
        context_interface::{cfg::GasParams, result::InvalidTransaction},
        database::InMemoryDB,
        database_interface::EmptyDB,
        handler::EthFrame,
        interpreter::{CallOutcome, CreateOutcome, InstructionResult, InterpreterResult},
        primitives::{Address, B256, Bytes, bytes, hex},
        state::AccountInfo,
    };
    use rstest::rstest;
    use std::{boxed::Box, str::FromStr};

    /// Creates frame result.
    fn call_last_frame_return(
        ctx: OpContext<EmptyDB>,
        instruction_result: InstructionResult,
        gas: Gas,
    ) -> Gas {
        let mut evm = ctx.build_op();

        let mut exec_result = FrameResult::Call(CallOutcome::new(
            InterpreterResult { result: instruction_result, output: Bytes::new(), gas },
            0..0,
        ));

        let mut handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        handler.last_frame_result(&mut evm, 0, &mut exec_result).unwrap();
        handler.refund(&mut evm, &mut exec_result, 0);
        *exec_result.gas()
    }

    /// Like [`call_last_frame_return`], but wraps the result in a top-level CREATE frame.
    fn create_last_frame_return(
        ctx: OpContext<EmptyDB>,
        instruction_result: InstructionResult,
        gas: Gas,
    ) -> Gas {
        let mut evm = ctx.build_op();

        let mut exec_result = FrameResult::Create(CreateOutcome::new(
            InterpreterResult { result: instruction_result, output: Bytes::new(), gas },
            None,
        ));

        let mut handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        handler.last_frame_result(&mut evm, 0, &mut exec_result).unwrap();
        handler.refund(&mut evm, &mut exec_result, 0);
        *exec_result.gas()
    }

    fn ecotone_fee_scalars(base_fee_scalar: u32, blob_fee_scalar: u32) -> U256 {
        let mut scalars = [0u8; 32];
        scalars[BASE_FEE_SCALAR_OFFSET..BASE_FEE_SCALAR_OFFSET + 4]
            .copy_from_slice(&base_fee_scalar.to_be_bytes());
        scalars[BLOB_BASE_FEE_SCALAR_OFFSET..BLOB_BASE_FEE_SCALAR_OFFSET + 4]
            .copy_from_slice(&blob_fee_scalar.to_be_bytes());
        U256::from_be_bytes(scalars)
    }

    fn update_fee_params_in_db(
        db: &mut InMemoryDB,
        spec: OpSpecId,
        l1_base_fee: u64,
        l1_fee_overhead: u64,
        l1_base_fee_scalar: u64,
        l1_blob_base_fee: u64,
        l1_blob_base_fee_scalar: u64,
    ) {
        let l1_block_contract = db.load_account(L1_BLOCK_CONTRACT).unwrap();
        l1_block_contract.storage.insert(L1_BASE_FEE_SLOT, U256::from(l1_base_fee));

        if spec.is_enabled_in(OpSpecId::ARSIA) {
            l1_block_contract
                .storage
                .insert(ECOTONE_L1_BLOB_BASE_FEE_SLOT, U256::from(l1_blob_base_fee));
            l1_block_contract.storage.insert(
                ECOTONE_L1_FEE_SCALARS_SLOT,
                ecotone_fee_scalars(l1_base_fee_scalar as u32, l1_blob_base_fee_scalar as u32),
            );
        } else {
            l1_block_contract.storage.insert(L1_OVERHEAD_SLOT, U256::from(l1_fee_overhead));
            l1_block_contract.storage.insert(L1_SCALAR_SLOT, U256::from(l1_base_fee_scalar));
        }
    }

    #[allow(clippy::too_many_arguments)]
    fn build_chain(
        spec: OpSpecId,
        l2_block: U256,
        token_ratio: u64,
        l1_base_fee: u64,
        l1_fee_overhead: u64,
        l1_base_fee_scalar: u64,
        l1_blob_base_fee: u64,
        l1_blob_base_fee_scalar: u64,
    ) -> L1BlockInfo {
        let mut chain = L1BlockInfo {
            l2_block: Some(l2_block),
            l1_base_fee: U256::from(l1_base_fee),
            l1_base_fee_scalar: U256::from(l1_base_fee_scalar),
            token_ratio: U256::from(token_ratio),
            ..Default::default()
        };

        if spec.is_enabled_in(OpSpecId::ARSIA) {
            chain.l1_blob_base_fee = Some(U256::from(l1_blob_base_fee));
            chain.l1_blob_base_fee_scalar = Some(U256::from(l1_blob_base_fee_scalar));
            chain.operator_fee_scalar = Some(U256::ZERO);
            chain.operator_fee_constant = Some(U256::ZERO);
        } else {
            chain.l1_fee_overhead = Some(U256::from(l1_fee_overhead));
        }

        chain
    }

    #[allow(clippy::too_many_arguments)]
    fn l1_cost_for(
        spec: OpSpecId,
        input: &[u8],
        token_ratio: u64,
        l1_base_fee: u64,
        l1_fee_overhead: u64,
        l1_base_fee_scalar: u64,
        l1_blob_base_fee: u64,
        l1_blob_base_fee_scalar: u64,
    ) -> U256 {
        let mut info = build_chain(
            spec,
            U256::ZERO,
            token_ratio,
            l1_base_fee,
            l1_fee_overhead,
            l1_base_fee_scalar,
            l1_blob_base_fee,
            l1_blob_base_fee_scalar,
        );
        info.calculate_tx_l1_cost(input, spec)
    }

    fn regular_tx(
        caller: Address,
        to: Address,
        gas_limit: u64,
        input: &[u8],
    ) -> OpTransaction<TxEnv> {
        OpTransaction::builder()
            .base(
                TxEnv::builder()
                    .caller(caller)
                    .to(to)
                    .gas_limit(gas_limit)
                    .data(Bytes::copy_from_slice(input)),
            )
            .enveloped_tx(Some(Bytes::copy_from_slice(input)))
            .build_fill()
    }

    /// Cfg on the newest OP fork with EIP-8037 (Amsterdam) state gas enabled and the
    /// matching gas params — an Amsterdam analog would activate on top of an upcoming fork.
    fn amsterdam_cfg() -> CfgEnv<OpSpecId> {
        let mut cfg = CfgEnv::new_with_spec(OpSpecId::KARST).with_enable_amsterdam_eip8037(true);
        cfg.set_gas_params(GasParams::new_spec(SpecId::AMSTERDAM));
        cfg
    }

    /// Gas as a frame might leave it: limit 100 with 10 regular gas spent, and 30 state
    /// gas charged against an initial reservoir of 50.
    fn gas_with_state_usage() -> Gas {
        let mut gas = Gas::new_with_regular_gas_and_reservoir(100, 50);
        assert!(gas.record_regular_cost(10));
        assert!(gas.record_state_cost(30));
        assert_eq!(gas.reservoir(), 20);
        assert_eq!(gas.state_gas_spent(), 30);
        gas
    }

    #[test]
    fn test_failed_create_refills_reservoir_with_create_state_gas() {
        let cfg = amsterdam_cfg();
        let create_state_gas = cfg.gas_params().create_state_gas();
        assert!(create_state_gas > 0, "create_state_gas must be nonzero for this test to bite");

        let ctx = Context::op()
            .with_tx(OpTransaction::builder().base(TxEnv::builder().gas_limit(100)).build_fill())
            .with_cfg(cfg);

        let gas = create_last_frame_return(ctx, InstructionResult::Revert, gas_with_state_usage());
        // Unused regular gas is returned on revert.
        assert_eq!(gas.remaining(), 90);
        assert_eq!(gas.total_gas_spent(), 10);
        // State changes rolled back: the pre-tx reservoir (50 = 20 + 30 spent state gas) is
        // recovered, and the intrinsic `create_state_gas` charged at tx entry is refunded on
        // top because the top-level CREATE failed (EIP-8037).
        assert_eq!(gas.reservoir(), 50 + create_state_gas);
        // `refill_reservoir` books the refund as negative state gas spent; the post-execution
        // accounting reconciles it against the intrinsic charge.
        assert_eq!(gas.state_gas_spent(), -(create_state_gas as i64));
    }

    #[test]
    fn test_failed_create_without_eip8037_only_recovers_reservoir() {
        let ctx = Context::op()
            .with_tx(OpTransaction::builder().base(TxEnv::builder().gas_limit(100)).build_fill())
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::KARST));

        let gas = create_last_frame_return(ctx, InstructionResult::Revert, gas_with_state_usage());
        assert_eq!(gas.remaining(), 90);
        assert_eq!(gas.total_gas_spent(), 10);
        // No EIP-8037: the pre-tx reservoir is recovered, but no create_state_gas refund.
        assert_eq!(gas.reservoir(), 50);
        assert_eq!(gas.state_gas_spent(), 0);
    }

    #[test]
    fn test_successful_create_keeps_state_gas_spent() {
        let ctx = Context::op()
            .with_tx(OpTransaction::builder().base(TxEnv::builder().gas_limit(100)).build_fill())
            .with_cfg(amsterdam_cfg());

        let gas = create_last_frame_return(ctx, InstructionResult::Return, gas_with_state_usage());
        assert_eq!(gas.remaining(), 90);
        assert_eq!(gas.total_gas_spent(), 10);
        // Success: state gas was genuinely consumed — no recovery, no refill.
        assert_eq!(gas.reservoir(), 20);
        assert_eq!(gas.state_gas_spent(), 30);
    }

    #[test]
    fn test_revert_recovers_reservoir_from_negative_state_gas() {
        let ctx = Context::op()
            .with_tx(OpTransaction::builder().base(TxEnv::builder().gas_limit(100)).build_fill())
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::KARST));

        let mut ret_gas = Gas::new_with_regular_gas_and_reservoir(100, 50);
        assert!(ret_gas.record_regular_cost(10));
        // 0→x→0 storage restoration refilled more than this frame charged: net-negative
        // state gas spent (reservoir 75, state_gas_spent -25).
        ret_gas.refill_reservoir(25);

        let gas = call_last_frame_return(ctx, InstructionResult::Revert, ret_gas);
        assert_eq!(gas.remaining(), 90);
        // Signed recovery: 75 + (-25) = 50, the pre-tx reservoir. An unsigned addition
        // would have inflated the reservoir to 100 here.
        assert_eq!(gas.reservoir(), 50);
        assert_eq!(gas.state_gas_spent(), 0);
    }

    #[test]
    fn test_revert_gas() {
        let ctx = Context::op()
            .with_tx(OpTransaction::builder().base(TxEnv::builder().gas_limit(100)).build_fill())
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::BEDROCK));

        let gas = call_last_frame_return(ctx, InstructionResult::Revert, Gas::new(90));
        assert_eq!(gas.remaining(), 90);
        assert_eq!(gas.total_gas_spent(), 10);
        assert_eq!(gas.refunded(), 0);
    }

    #[test]
    fn test_revert_gas_arsia() {
        let ctx = Context::op()
            .with_tx(OpTransaction::builder().base(TxEnv::builder().gas_limit(100)).build_fill())
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ARSIA));
        let gas = call_last_frame_return(ctx, InstructionResult::Revert, Gas::new(90));
        assert_eq!(gas.remaining(), 90);
        assert_eq!(gas.total_gas_spent(), 10);
        assert_eq!(gas.refunded(), 0);
    }

    #[test]
    fn test_consume_gas() {
        let ctx = Context::op()
            .with_tx(OpTransaction::builder().base(TxEnv::builder().gas_limit(100)).build_fill())
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::ARSIA));

        let gas = call_last_frame_return(ctx, InstructionResult::Stop, Gas::new(90));
        assert_eq!(gas.remaining(), 90);
        assert_eq!(gas.total_gas_spent(), 10);
        assert_eq!(gas.refunded(), 0);
    }

    #[test]
    fn test_consume_gas_with_refund() {
        // Use non-deposit transaction to test refund logic
        let ctx = Context::op()
            .with_tx(
                OpTransaction::builder()
                    .base(TxEnv::builder().gas_limit(100))
                    .source_hash(B256::ZERO)
                    .enveloped_tx(Some(bytes!("FACADE")))
                    .build()
                    .unwrap(),
            )
            .with_chain(L1BlockInfo { token_ratio: U256::from(1), ..Default::default() })
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::REGOLITH));

        let mut ret_gas = Gas::new(90);
        ret_gas.record_refund(20);

        let gas = call_last_frame_return(ctx.clone(), InstructionResult::Stop, ret_gas);
        assert_eq!(gas.remaining(), 90);
        assert_eq!(gas.total_gas_spent(), 10);
        assert_eq!(gas.refunded(), 2); // min(20, 10/5)

        let gas = call_last_frame_return(ctx, InstructionResult::Revert, ret_gas);
        assert_eq!(gas.remaining(), 90);
        assert_eq!(gas.total_gas_spent(), 10);
        assert_eq!(gas.refunded(), 0);
    }

    #[test]
    fn test_validate_initial_tx_gas_truncates_large_token_ratio_like_geth() {
        let huge_ratio = (U256::from(1) << 64) + U256::from(3);
        let truncated_ratio = U256::from(3);
        let caller = Address::with_last_byte(0x11);
        let recipient = Address::with_last_byte(0x22);
        let tx_input = bytes!("FACADE");

        let mk_ctx = |token_ratio: U256| {
            Context::op()
                .with_chain(L1BlockInfo {
                    l2_block: Some(U256::from(1)),
                    l1_base_fee: U256::from(1),
                    l1_fee_overhead: Some(U256::ZERO),
                    l1_base_fee_scalar: U256::from(1),
                    token_ratio,
                    ..Default::default()
                })
                .with_block(BlockEnv { number: U256::from(1), ..Default::default() })
                .with_tx(regular_tx(caller, recipient, 500_000, &tx_input))
                .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::REGOLITH))
        };

        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        let mut evm_huge = mk_ctx(huge_ratio).build_op();
        let huge_gas = handler.validate_initial_tx_gas(&mut evm_huge).unwrap();

        let mut evm_truncated = mk_ctx(truncated_ratio).build_op();
        let truncated_gas = handler.validate_initial_tx_gas(&mut evm_truncated).unwrap();

        assert_eq!(huge_gas.initial_total_gas(), truncated_gas.initial_total_gas());
        assert_eq!(huge_gas.floor_gas, truncated_gas.floor_gas);
    }

    #[test]
    fn test_refund_path_truncates_large_token_ratio_like_geth() {
        let huge_ratio = (U256::from(1) << 64) + U256::from(3);
        let truncated_ratio = U256::from(3);

        let mk_ctx = |token_ratio: U256| {
            Context::op()
                .with_tx(
                    OpTransaction::builder()
                        .base(TxEnv::builder().gas_limit(100))
                        .source_hash(B256::ZERO)
                        .enveloped_tx(Some(bytes!("FACADE")))
                        .build()
                        .unwrap(),
                )
                .with_chain(L1BlockInfo { token_ratio, ..Default::default() })
                .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::REGOLITH))
        };

        let mut ret_gas = Gas::new(10);
        ret_gas.record_refund(20);

        let gas_huge = call_last_frame_return(mk_ctx(huge_ratio), InstructionResult::Stop, ret_gas);
        let gas_truncated =
            call_last_frame_return(mk_ctx(truncated_ratio), InstructionResult::Stop, ret_gas);

        assert_eq!(gas_huge.remaining(), gas_truncated.remaining());
        assert_eq!(gas_huge.refunded(), gas_truncated.refunded());
        assert_eq!(gas_huge.total_gas_spent(), gas_truncated.total_gas_spent());
    }

    #[test]
    fn test_scale_refund_with_token_ratio_uses_u64_wrapping_semantics() {
        let token_ratio = (i64::MAX as u64) + 1;
        // 2 * 2^63 wraps to 0 in uint64 arithmetic.
        assert_eq!(scale_refund_with_token_ratio(2, token_ratio), 0);
    }

    #[test]
    fn test_scale_refund_with_token_ratio_clamps_non_representable_to_i64_max() {
        let token_ratio = (i64::MAX as u64) + 1;
        // 1 * (i64::MAX + 1) is > i64::MAX, so we clamp at the i64 boundary.
        assert_eq!(scale_refund_with_token_ratio(1, token_ratio), i64::MAX);
    }

    #[test]
    fn test_execution_uses_divisor_one_when_token_ratio_low64_is_zero() {
        let caller = Address::with_last_byte(0x31);
        let recipient = Address::with_last_byte(0x32);
        let tx_input = bytes!("FACADE");

        let run = |token_ratio: U256| {
            let ctx = Context::op()
                .with_chain(L1BlockInfo {
                    l2_block: Some(U256::from(1)),
                    l1_base_fee: U256::from(1),
                    l1_fee_overhead: Some(U256::ZERO),
                    l1_base_fee_scalar: U256::from(1),
                    token_ratio,
                    ..Default::default()
                })
                .with_block(BlockEnv { number: U256::from(1), basefee: 1, ..Default::default() })
                .with_tx(regular_tx(caller, recipient, 1_000_000, &tx_input))
                .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::REGOLITH));

            let mut evm = ctx.build_op();
            let mut handler =
                OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();
            let init = handler.validate_initial_tx_gas(&mut evm)?;
            handler.execution(&mut evm, &init)
        };

        // low 64 bits are 0, so execution path should clamp divisor to 1.
        let low64_zero_ratio: U256 = U256::from(1u64) << 64u32;
        assert_eq!(low64_zero_ratio.as_limbs()[0], 0);
        assert_eq!(low64_zero_ratio.as_limbs()[0].max(1), 1);

        // The main assertion here is "no panic" in execution path with low64 == 0.
        let zero_low64_result = run(low64_zero_ratio)
            .map(|frame| frame.interpreter_result().result)
            .map_err(|err| err.to_string());
        if let Err(msg) = zero_low64_result {
            assert!(
                !msg.to_ascii_lowercase().contains("divide"),
                "unexpected divide-by-zero style error: {msg}"
            );
        }
    }

    #[test]
    fn test_consume_gas_deposit_tx() {
        let ctx = Context::op()
            .with_tx(
                OpTransaction::builder()
                    .base(TxEnv::builder().gas_limit(100))
                    .source_hash(B256::from([1u8; 32]))
                    .build_fill(),
            )
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::BEDROCK));
        let gas = call_last_frame_return(ctx, InstructionResult::Stop, Gas::new(90));
        assert_eq!(gas.remaining(), 0);
        assert_eq!(gas.total_gas_spent(), 100);
        assert_eq!(gas.refunded(), 0);
    }

    #[test]
    fn test_consume_gas_sys_deposit_tx() {
        let ctx = Context::op()
            .with_tx(
                OpTransaction::builder()
                    .base(TxEnv::builder().gas_limit(100))
                    .source_hash(B256::from([1u8; 32]))
                    .is_system_transaction()
                    .build_fill(),
            )
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::BEDROCK));
        let gas = call_last_frame_return(ctx, InstructionResult::Stop, Gas::new(90));
        assert_eq!(gas.remaining(), 100);
        assert_eq!(gas.total_gas_spent(), 0);
        assert_eq!(gas.refunded(), 0);
    }

    #[test]
    fn test_commit_mint_value() {
        let caller = Address::ZERO;
        let mut db = InMemoryDB::default();
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(1000), ..Default::default() },
        );

        let mut ctx = Context::op()
            .with_db(db)
            .with_chain(L1BlockInfo {
                l1_base_fee: U256::from(1_000),
                l1_fee_overhead: Some(U256::from(1_000)),
                l1_base_fee_scalar: U256::from(1_000),
                ..Default::default()
            })
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::REGOLITH));
        ctx.modify_tx(|tx| {
            tx.deposit.source_hash = B256::from([1u8; 32]);
            tx.deposit.mint = Some(10);
        });

        let mut evm = ctx.build_op();

        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();
        handler
            .validate_against_state_and_deduct_caller(&mut evm, &mut Default::default())
            .unwrap();

        // Check the account balance is updated.
        let account = evm.ctx().journal_mut().load_account(caller).unwrap();
        assert_eq!(account.info.balance, U256::from(1010));
    }

    #[test]
    fn test_remove_l1_cost_non_deposit() {
        let caller = Address::ZERO;
        let mut db = InMemoryDB::default();
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(1610), ..Default::default() },
        );
        // Set up L1 block contract storage for ARSIA
        let l1_block_contract = db.load_account(L1_BLOCK_CONTRACT).unwrap();
        l1_block_contract.storage.insert(L1_BASE_FEE_SLOT, U256::from(1_000));
        l1_block_contract.storage.insert(ECOTONE_L1_BLOB_BASE_FEE_SLOT, U256::ZERO);
        l1_block_contract.storage.insert(ECOTONE_L1_FEE_SCALARS_SLOT, U256::from(1_000) << 128); // base_fee_scalar = 1000
        let gas_oracle_contract = db.load_account(GAS_ORACLE_CONTRACT).unwrap();
        gas_oracle_contract.storage.insert(TOKEN_RATIO_SLOT, U256::from(1)); // token_ratio = 1

        let ctx = Context::op()
            .with_db(db)
            .with_chain(L1BlockInfo {
                l1_base_fee: U256::from(1_000),
                l1_base_fee_scalar: U256::from(1_000),
                l1_blob_base_fee: Some(U256::ZERO),
                l1_blob_base_fee_scalar: Some(U256::ZERO),
                l2_block: Some(U256::from(0)),
                operator_fee_scalar: Some(U256::ZERO),
                operator_fee_constant: Some(U256::ZERO),
                token_ratio: U256::from(1),
                ..Default::default()
            })
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::ARSIA))
            .with_tx(
                OpTransaction::builder()
                    .base(TxEnv::builder().gas_limit(100))
                    .enveloped_tx(Some(bytes!("FACADE")))
                    .source_hash(B256::ZERO)
                    .build()
                    .unwrap(),
            );

        let mut evm = ctx.build_op();

        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();
        handler
            .validate_against_state_and_deduct_caller(&mut evm, &mut Default::default())
            .unwrap();

        // Check the account balance is updated.
        let account = evm.ctx().journal_mut().load_account(caller).unwrap();
        assert_eq!(account.info.balance, U256::from(10)); // 1610 - 1600 = 10
    }

    #[test]
    fn test_reload_l1_block_info_isthmus() {
        const BLOCK_NUM: U256 = uint!(100_U256);
        const L1_BASE_FEE: U256 = uint!(1_U256);

        let mut db = InMemoryDB::default();
        let l1_block_contract = db.load_account(L1_BLOCK_CONTRACT).unwrap();
        l1_block_contract.storage.insert(L1_BASE_FEE_SLOT, L1_BASE_FEE);
        db.insert_account_info(
            Address::ZERO,
            AccountInfo { balance: U256::from(10_000_000_000_000u64), ..Default::default() },
        );

        let ctx = Context::op()
            .with_db(db)
            .with_chain(L1BlockInfo {
                l2_block: Some(BLOCK_NUM + U256::from(1)),
                operator_fee_scalar: Some(U256::ZERO),
                operator_fee_constant: Some(U256::ZERO),
                ..Default::default()
            })
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::ISTHMUS));

        let mut evm = ctx.build_op();

        assert_ne!(evm.ctx().chain().l2_block, Some(BLOCK_NUM));

        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();
        // Call validate_initial_tx_gas first to load L1BlockInfo
        handler.validate_initial_tx_gas(&mut evm).unwrap();
        handler
            .validate_against_state_and_deduct_caller(&mut evm, &mut Default::default())
            .unwrap();

        assert_eq!(
            *evm.ctx().chain(),
            L1BlockInfo {
                l2_block: Some(BLOCK_NUM),
                l1_base_fee: L1_BASE_FEE,
                l1_base_fee_scalar: U256::ZERO,
                l1_fee_overhead: Some(U256::ZERO),
                token_ratio: U256::ZERO,
                ..Default::default()
            }
        );
    }

    #[test]
    fn test_parse_da_footprint_gas_scalar_jovian() {
        const BLOCK_NUM: U256 = uint!(100_U256);
        const L1_BASE_FEE: U256 = uint!(1_U256);
        const L1_BLOB_BASE_FEE: U256 = uint!(2_U256);
        const L1_BASE_FEE_SCALAR: u64 = 3;
        const L1_BLOB_BASE_FEE_SCALAR: u64 = 4;
        const L1_FEE_SCALARS: U256 = U256::from_limbs([
            0,
            (L1_BASE_FEE_SCALAR << (64 - BASE_FEE_SCALAR_OFFSET * 2)) | L1_BLOB_BASE_FEE_SCALAR,
            0,
            0,
        ]);
        const OPERATOR_FEE_SCALAR: u8 = 5;
        const OPERATOR_FEE_CONST: u8 = 6;
        const DA_FOOTPRINT_GAS_SCALAR: u8 = 7;
        let mut operator_fee_and_da_footprint = [0u8; 32];
        operator_fee_and_da_footprint[31] = OPERATOR_FEE_CONST;
        operator_fee_and_da_footprint[23] = OPERATOR_FEE_SCALAR;
        operator_fee_and_da_footprint[19] = DA_FOOTPRINT_GAS_SCALAR;
        let operator_fee_and_da_footprint_u256 = U256::from_be_bytes(operator_fee_and_da_footprint);

        let mut db = InMemoryDB::default();
        let l1_block_contract = db.load_account(L1_BLOCK_CONTRACT).unwrap();
        l1_block_contract.storage.insert(L1_BASE_FEE_SLOT, L1_BASE_FEE);
        l1_block_contract.storage.insert(ECOTONE_L1_BLOB_BASE_FEE_SLOT, L1_BLOB_BASE_FEE);
        l1_block_contract.storage.insert(ECOTONE_L1_FEE_SCALARS_SLOT, L1_FEE_SCALARS);
        l1_block_contract
            .storage
            .insert(OPERATOR_FEE_SCALARS_SLOT, operator_fee_and_da_footprint_u256);
        db.insert_account_info(
            Address::ZERO,
            AccountInfo { balance: U256::from(20_000_000), ..Default::default() },
        );

        let ctx = Context::op()
            .with_db(db)
            .with_chain(L1BlockInfo {
                l2_block: Some(BLOCK_NUM + U256::from(1)), // ahead by one block
                operator_fee_scalar: Some(U256::from(2)),
                operator_fee_constant: Some(U256::from(50)),
                ..Default::default()
            })
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::ARSIA))
            // set the operator fee to a low value
            .with_tx(
                OpTransaction::builder()
                    .base(TxEnv::builder().gas_limit(30_000))
                    .enveloped_tx(Some(bytes!("FACADE")))
                    .build_fill(),
            );

        let mut evm = ctx.build_op();

        assert_ne!(evm.ctx().chain().l2_block, Some(BLOCK_NUM));

        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();
        // Call validate_initial_tx_gas first to load L1BlockInfo
        handler.validate_initial_tx_gas(&mut evm).unwrap();
        handler
            .validate_against_state_and_deduct_caller(&mut evm, &mut Default::default())
            .unwrap();

        assert_eq!(
            *evm.ctx().chain(),
            L1BlockInfo {
                l2_block: Some(BLOCK_NUM),
                l1_base_fee: L1_BASE_FEE,
                l1_base_fee_scalar: U256::from(L1_BASE_FEE_SCALAR),
                l1_blob_base_fee: Some(L1_BLOB_BASE_FEE),
                l1_blob_base_fee_scalar: Some(U256::from(L1_BLOB_BASE_FEE_SCALAR)),
                empty_ecotone_scalars: false,
                l1_fee_overhead: None,
                operator_fee_scalar: Some(U256::from(OPERATOR_FEE_SCALAR)),
                operator_fee_constant: Some(U256::from(OPERATOR_FEE_CONST)),
                tx_l1_cost: Some(U256::ZERO),
                da_footprint_gas_scalar: Some(DA_FOOTPRINT_GAS_SCALAR as u16),
                token_ratio: U256::ZERO,
            }
        );
    }

    #[test]
    fn test_reload_l1_block_info_regolith() {
        const BLOCK_NUM: U256 = uint!(200_U256);
        const L1_BASE_FEE: U256 = uint!(7_U256);
        const L1_FEE_OVERHEAD: U256 = uint!(9_U256);
        const L1_BASE_FEE_SCALAR: u64 = 11;

        let mut db = InMemoryDB::default();
        let l1_block_contract = db.load_account(L1_BLOCK_CONTRACT).unwrap();
        l1_block_contract.storage.insert(L1_BASE_FEE_SLOT, L1_BASE_FEE);
        // Pre-ecotone bedrock/regolith slots
        use crate::constants::{L1_OVERHEAD_SLOT, L1_SCALAR_SLOT};
        l1_block_contract.storage.insert(L1_OVERHEAD_SLOT, L1_FEE_OVERHEAD);
        l1_block_contract.storage.insert(L1_SCALAR_SLOT, U256::from(L1_BASE_FEE_SCALAR));

        db.insert_account_info(
            Address::ZERO,
            AccountInfo { balance: U256::from(1000), ..Default::default() },
        );

        let ctx = Context::op()
            .with_db(db)
            .with_chain(L1BlockInfo {
                l2_block: Some(BLOCK_NUM + U256::from(1)),
                ..Default::default()
            })
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::REGOLITH));

        let mut evm = ctx.build_op();
        assert_ne!(evm.ctx().chain().l2_block, Some(BLOCK_NUM));

        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();
        // Call validate_initial_tx_gas first to load L1BlockInfo
        handler.validate_initial_tx_gas(&mut evm).unwrap();
        handler
            .validate_against_state_and_deduct_caller(&mut evm, &mut Default::default())
            .unwrap();

        assert_eq!(
            *evm.ctx().chain(),
            L1BlockInfo {
                l2_block: Some(BLOCK_NUM),
                l1_base_fee: L1_BASE_FEE,
                l1_fee_overhead: Some(L1_FEE_OVERHEAD),
                l1_base_fee_scalar: U256::from(L1_BASE_FEE_SCALAR),
                token_ratio: U256::ZERO,
                tx_l1_cost: None,
                ..Default::default()
            }
        );
    }

    #[test]
    fn test_reload_l1_block_info_ecotone_pre_isthmus() {
        const BLOCK_NUM: U256 = uint!(300_U256);
        const L1_BASE_FEE: U256 = uint!(13_U256);
        const L1_BLOB_BASE_FEE: U256 = uint!(17_U256);
        const L1_BASE_FEE_SCALAR: u64 = 19;
        const L1_BLOB_BASE_FEE_SCALAR: u64 = 23;
        const L1_FEE_SCALARS: U256 = U256::from_limbs([
            0,
            (L1_BASE_FEE_SCALAR << (64 - BASE_FEE_SCALAR_OFFSET * 2)) | L1_BLOB_BASE_FEE_SCALAR,
            0,
            0,
        ]);

        let mut db = InMemoryDB::default();
        let l1_block_contract = db.load_account(L1_BLOCK_CONTRACT).unwrap();
        l1_block_contract.storage.insert(L1_BASE_FEE_SLOT, L1_BASE_FEE);
        l1_block_contract.storage.insert(ECOTONE_L1_BLOB_BASE_FEE_SLOT, L1_BLOB_BASE_FEE);
        l1_block_contract.storage.insert(ECOTONE_L1_FEE_SCALARS_SLOT, L1_FEE_SCALARS);
        db.insert_account_info(
            Address::ZERO,
            AccountInfo { balance: U256::from(1000), ..Default::default() },
        );

        let ctx = Context::op()
            .with_db(db)
            .with_chain(L1BlockInfo {
                l2_block: Some(BLOCK_NUM + U256::from(1)),
                ..Default::default()
            })
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::ECOTONE));

        let mut evm = ctx.build_op();
        assert_ne!(evm.ctx().chain().l2_block, Some(BLOCK_NUM));

        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();
        // Call validate_initial_tx_gas first to load L1BlockInfo
        handler.validate_initial_tx_gas(&mut evm).unwrap();
        handler
            .validate_against_state_and_deduct_caller(&mut evm, &mut Default::default())
            .unwrap();

        assert_eq!(
            *evm.ctx().chain(),
            L1BlockInfo {
                l2_block: Some(BLOCK_NUM),
                l1_base_fee: L1_BASE_FEE,
                l1_base_fee_scalar: U256::ZERO,
                l1_fee_overhead: Some(U256::ZERO),
                token_ratio: U256::ZERO,
                tx_l1_cost: None,
                ..Default::default()
            }
        );
    }

    #[test]
    fn test_load_l1_block_info_isthmus_none() {
        const BLOCK_NUM: U256 = uint!(100_U256);
        const L1_BASE_FEE: U256 = uint!(1_U256);

        let mut db = InMemoryDB::default();
        let l1_block_contract = db.load_account(L1_BLOCK_CONTRACT).unwrap();
        l1_block_contract.storage.insert(L1_BASE_FEE_SLOT, L1_BASE_FEE);
        db.insert_account_info(
            Address::ZERO,
            AccountInfo { balance: U256::from(10_000_000_000_000u64), ..Default::default() },
        );

        let ctx = Context::op()
            .with_db(db)
            .with_chain(L1BlockInfo {
                operator_fee_scalar: Some(U256::ZERO),
                operator_fee_constant: Some(U256::ZERO),
                ..Default::default()
            })
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::ISTHMUS));

        let mut evm = ctx.build_op();

        assert_ne!(evm.ctx().chain().l2_block, Some(BLOCK_NUM));

        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();
        // Call validate_initial_tx_gas first to load L1BlockInfo
        handler.validate_initial_tx_gas(&mut evm).unwrap();
        handler
            .validate_against_state_and_deduct_caller(&mut evm, &mut Default::default())
            .unwrap();

        assert_eq!(
            *evm.ctx().chain(),
            L1BlockInfo {
                l2_block: Some(BLOCK_NUM),
                l1_base_fee: L1_BASE_FEE,
                l1_base_fee_scalar: U256::ZERO,
                l1_fee_overhead: Some(U256::ZERO),
                tx_l1_cost: None,
                token_ratio: U256::ZERO,
                ..Default::default()
            }
        );
    }

    #[test]
    fn test_remove_l1_cost() {
        let caller = Address::ZERO;
        let mut db = InMemoryDB::default();
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(1601), ..Default::default() },
        );
        // Set up L1 block contract storage for ARSIA
        let l1_block_contract = db.load_account(L1_BLOCK_CONTRACT).unwrap();
        l1_block_contract.storage.insert(L1_BASE_FEE_SLOT, U256::from(1_000));
        l1_block_contract.storage.insert(ECOTONE_L1_BLOB_BASE_FEE_SLOT, U256::ZERO);
        l1_block_contract.storage.insert(ECOTONE_L1_FEE_SCALARS_SLOT, U256::from(1_000) << 128); // base_fee_scalar = 1000
        let gas_oracle_contract = db.load_account(GAS_ORACLE_CONTRACT).unwrap();
        gas_oracle_contract.storage.insert(TOKEN_RATIO_SLOT, U256::from(1)); // token_ratio = 1

        let ctx = Context::op()
            .with_db(db)
            .with_chain(L1BlockInfo {
                l1_base_fee: U256::from(1_000),
                l1_base_fee_scalar: U256::from(1_000),
                l1_blob_base_fee: Some(U256::ZERO),
                l1_blob_base_fee_scalar: Some(U256::ZERO),
                l2_block: Some(U256::from(0)),
                operator_fee_scalar: Some(U256::ZERO),
                operator_fee_constant: Some(U256::ZERO),
                token_ratio: U256::from(1),
                ..Default::default()
            })
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::ARSIA))
            .with_tx(
                OpTransaction::builder()
                    .base(TxEnv::builder().gas_limit(100))
                    .source_hash(B256::ZERO)
                    .enveloped_tx(Some(bytes!("FACADE")))
                    .build()
                    .unwrap(),
            );

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        // l1block cost is 1600 fee.
        handler
            .validate_against_state_and_deduct_caller(&mut evm, &mut Default::default())
            .unwrap();

        // Check the account balance is updated.
        let account = evm.ctx().journal_mut().load_account(caller).unwrap();
        assert_eq!(account.info.balance, U256::from(1)); // 1601 - 1600 = 1
    }

    #[test]
    fn test_remove_operator_cost_isthmus() {
        let caller = Address::ZERO;
        let mut db = InMemoryDB::default();
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(151), ..Default::default() },
        );
        let ctx = Context::op()
            .with_db(db)
            .with_chain(L1BlockInfo {
                operator_fee_scalar: Some(U256::from(10_000_000)),
                operator_fee_constant: Some(U256::from(50)),
                l2_block: Some(U256::from(0)),
                ..Default::default()
            })
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::ISTHMUS))
            .with_tx(
                OpTransaction::builder()
                    .base(TxEnv::builder().gas_limit(10))
                    .enveloped_tx(Some(bytes!("FACADE")))
                    .build_fill(),
            );

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        // Under Isthmus the operator fee cost is operator_fee_scalar * gas_limit / 1e6 +
        // operator_fee_constant 10_000_000 * 10 / 1_000_000 + 50 = 150
        handler
            .validate_against_state_and_deduct_caller(&mut evm, &mut Default::default())
            .unwrap();

        // Check the account balance is updated.
        let account = evm.ctx().journal_mut().load_account(caller).unwrap();
        assert_eq!(account.info.balance, U256::from(151));
    }

    #[test]
    fn test_remove_operator_cost_jovian() {
        let caller = Address::ZERO;
        let mut db = InMemoryDB::default();
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(2_051), ..Default::default() },
        );
        let ctx = Context::op()
            .with_db(db)
            .with_chain(L1BlockInfo {
                operator_fee_scalar: Some(U256::from(2)),
                operator_fee_constant: Some(U256::from(50)),
                l2_block: Some(U256::from(0)),
                ..Default::default()
            })
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::ARSIA))
            .with_tx(
                OpTransaction::builder()
                    .base(TxEnv::builder().gas_limit(10))
                    .enveloped_tx(Some(bytes!("FACADE")))
                    .build_fill(),
            );

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        // Under Jovian the operator fee cost is operator_fee_scalar * gas_limit * 100 +
        // operator_fee_constant 2 * 10 * 100 + 50 = 2_050
        handler
            .validate_against_state_and_deduct_caller(&mut evm, &mut Default::default())
            .unwrap();

        let account = evm.ctx().journal_mut().load_account(caller).unwrap();
        assert_eq!(account.info.balance, U256::from(1));
    }

    #[test]
    fn test_remove_l1_cost_lack_of_funds() {
        let caller = Address::ZERO;
        let mut db = InMemoryDB::default();
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(48), ..Default::default() },
        );

        let l1_block_contract = db.load_account(L1_BLOCK_CONTRACT).unwrap();
        l1_block_contract.storage.insert(L1_BASE_FEE_SLOT, U256::from(1_000));
        l1_block_contract.storage.insert(ECOTONE_L1_BLOB_BASE_FEE_SLOT, U256::ZERO);
        l1_block_contract.storage.insert(ECOTONE_L1_FEE_SCALARS_SLOT, U256::from(1_000) << 128); // base_fee_scalar = 1000
        let gas_oracle_contract = db.load_account(GAS_ORACLE_CONTRACT).unwrap();
        gas_oracle_contract.storage.insert(TOKEN_RATIO_SLOT, U256::from(1)); // token_ratio = 1

        let ctx = Context::op()
            .with_db(db)
            .with_chain(L1BlockInfo {
                l1_base_fee: U256::from(1_000),
                l1_base_fee_scalar: U256::from(1_000),
                l1_blob_base_fee: Some(U256::ZERO),
                l1_blob_base_fee_scalar: Some(U256::ZERO),
                l2_block: Some(U256::from(0)),
                operator_fee_scalar: Some(U256::ZERO),
                operator_fee_constant: Some(U256::ZERO),
                token_ratio: U256::from(1),
                ..Default::default()
            })
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::ARSIA))
            .modify_tx_chained(|tx| {
                tx.enveloped_tx = Some(bytes!("FACADE"));
            });

        // l1block cost is 1048 fee.
        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        // l1block cost is 1600 fee.
        assert_eq!(
            handler.validate_against_state_and_deduct_caller(&mut evm, &mut Default::default()),
            Err(EVMError::Transaction(
                InvalidTransaction::LackOfFundForMaxFee {
                    fee: Box::new(U256::from(1600)),
                    balance: Box::new(U256::from(48)),
                }
                .into(),
            ))
        );
    }

    #[test]
    fn test_validate_sys_tx() {
        // mark the tx as a system transaction.
        let ctx = Context::op()
            .modify_tx_chained(|tx| {
                tx.deposit.source_hash = B256::from([1u8; 32]);
                tx.deposit.is_system_transaction = true;
            })
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::REGOLITH));

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        assert_eq!(
            handler.validate_env(&mut evm),
            Err(EVMError::Transaction(OpTransactionError::DepositSystemTxPostRegolith))
        );

        // With BEDROCK spec.
        let ctx = evm.into_context();
        let mut evm = ctx.with_cfg(CfgEnv::new_with_spec(OpSpecId::BEDROCK)).build_op();

        // Pre-regolith system transactions should be allowed.
        assert!(handler.validate_env(&mut evm).is_ok());
    }

    #[test]
    fn test_validate_deposit_tx() {
        // Set source hash.
        let ctx = Context::op()
            .modify_tx_chained(|tx| {
                tx.deposit.source_hash = B256::from([1u8; 32]);
            })
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::REGOLITH));

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        assert!(handler.validate_env(&mut evm).is_ok());
    }

    #[test]
    fn test_validate_tx_against_state_deposit_tx() {
        // Set source hash.
        let ctx = Context::op()
            .modify_tx_chained(|tx| {
                tx.deposit.source_hash = B256::from([1u8; 32]);
            })
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::REGOLITH));

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        // Nonce and balance checks should be skipped for deposit transactions.
        assert!(handler.validate_env(&mut evm).is_ok());
    }

    #[test]
    fn test_halted_deposit_tx_post_regolith() {
        // A deposit that REACHED execution and then halted (here: OOG) is now included
        // post-regolith as a `FailedDeposit` halt directly in `execution_result`
        // (mirroring op-geth's `err == nil` vm-error path), rather than bubbling to the
        // global error handler. No eth_value is set, so there are no BVM_ETH logs.
        let ctx = Context::op()
            .modify_tx_chained(|tx| {
                // Set up as deposit transaction by having a deposit with source_hash
                tx.deposit.source_hash = B256::from([1u8; 32]);
            })
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::REGOLITH));

        let mut evm = ctx.build_op();
        let mut handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        let result = handler
            .execution_result(
                &mut evm,
                FrameResult::Call(CallOutcome::new(
                    InterpreterResult {
                        result: InstructionResult::OutOfGas,
                        output: Default::default(),
                        gas: Default::default(),
                    },
                    Default::default(),
                )),
                ResultGas::default(),
            )
            .unwrap();

        match result {
            ExecutionResult::Halt { reason, logs, .. } => {
                assert_eq!(reason, OpHaltReason::FailedDeposit);
                assert!(logs.is_empty(), "no eth_value -> no BVM_ETH logs");
            }
            other => panic!("expected FailedDeposit halt, got {other:?}"),
        }
    }

    #[test]
    fn test_tx_zero_value_touch_caller() {
        let ctx = Context::op();

        let mut evm = ctx.build_op();

        assert!(!evm.0.ctx.journal_mut().load_account(Address::ZERO).unwrap().is_touched());

        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        handler
            .validate_against_state_and_deduct_caller(&mut evm, &mut Default::default())
            .unwrap();

        assert!(evm.0.ctx.journal_mut().load_account(Address::ZERO).unwrap().is_touched());
    }

    #[rstest]
    #[case::deposit(true)]
    #[case::dyn_fee(false)]
    fn test_operator_fee_refund(#[case] is_deposit: bool) {
        const SENDER: Address = Address::ZERO;
        const GAS_PRICE: u128 = 0xFF;
        const OP_FEE_MOCK_PARAM: u128 = 0xFFFF;

        let ctx = Context::op()
            .with_tx(
                OpTransaction::builder()
                    .base(
                        TxEnv::builder().gas_price(GAS_PRICE).gas_priority_fee(None).caller(SENDER),
                    )
                    .enveloped_tx(if is_deposit { None } else { Some(bytes!("FACADE")) })
                    .source_hash(if is_deposit { B256::from([1u8; 32]) } else { B256::ZERO })
                    .build_fill(),
            )
            .with_cfg(CfgEnv::new_with_spec(OpSpecId::ARSIA));

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        // Set the operator fee scalar & constant to non-zero values in the L1 block info.
        evm.ctx().chain.operator_fee_scalar = Some(U256::from(OP_FEE_MOCK_PARAM));
        evm.ctx().chain.operator_fee_constant = Some(U256::from(OP_FEE_MOCK_PARAM));

        let mut gas = Gas::new(100);
        gas.set_spent(10);
        let mut exec_result = FrameResult::Call(CallOutcome::new(
            InterpreterResult {
                result: InstructionResult::Return,
                output: Default::default(),
                gas,
            },
            0..0,
        ));

        // Reimburse the caller for the unspent portion of the fees.
        handler.reimburse_caller(&mut evm, &mut exec_result).unwrap();

        // Compute the expected refund amount. If the transaction is a deposit, the operator fee
        // refund never applies. If the transaction is not a deposit, the operator fee
        // refund is added to the refund amount.
        let mut expected_refund =
            U256::from(GAS_PRICE * (gas.remaining() + gas.refunded() as u64) as u128);
        let op_fee_refund = evm.ctx().chain().operator_fee_refund(&gas, OpSpecId::ARSIA);
        assert!(op_fee_refund > U256::ZERO);

        if !is_deposit {
            expected_refund += op_fee_refund;
        }

        // Check that the caller was reimbursed the correct amount of ETH.
        let account = evm.ctx().journal_mut().load_account(SENDER).unwrap();
        assert_eq!(account.info.balance, expected_refund);
    }

    #[rstest]
    #[case::pre_arsia(OpSpecId::REGOLITH)]
    #[case::arsia(OpSpecId::ARSIA)]
    fn test_fee_params_update_applies_to_next_regular_tx(#[case] spec: OpSpecId) {
        const BLOCK_NUM: U256 = uint!(500_U256);
        const TOKEN_RATIO: u64 = 3000;

        const OLD_L1_BASE_FEE: u64 = 11;
        const OLD_L1_OVERHEAD: u64 = 22;
        const OLD_L1_BASE_FEE_SCALAR: u64 = 33;
        const OLD_L1_BLOB_BASE_FEE: u64 = 44;
        const OLD_L1_BLOB_BASE_FEE_SCALAR: u64 = 55;

        const NEW_L1_BASE_FEE: u64 = 101;
        const NEW_L1_OVERHEAD: u64 = 202;
        const NEW_L1_BASE_FEE_SCALAR: u64 = 303;
        const NEW_L1_BLOB_BASE_FEE: u64 = 404;
        const NEW_L1_BLOB_BASE_FEE_SCALAR: u64 = 505;

        let caller = Address::from_str("0x1000000000000000000000000000000000000001").unwrap();
        let recipient = Address::from_str("0x2000000000000000000000000000000000000002").unwrap();
        let regular_input = bytes!("faca01");
        let deposit_caller =
            Address::from_str("0x9000000000000000000000000000000000000009").unwrap();

        let mut db = InMemoryDB::default();
        update_fee_params_in_db(
            &mut db,
            spec,
            OLD_L1_BASE_FEE,
            OLD_L1_OVERHEAD,
            OLD_L1_BASE_FEE_SCALAR,
            OLD_L1_BLOB_BASE_FEE,
            OLD_L1_BLOB_BASE_FEE_SCALAR,
        );
        db.load_account(GAS_ORACLE_CONTRACT)
            .unwrap()
            .storage
            .insert(TOKEN_RATIO_SLOT, U256::from(TOKEN_RATIO));

        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        // Tx1 (deposit): update fee params through transaction execution.
        let mut evm1 = Context::op()
            .with_db(db)
            .with_chain(build_chain(
                spec,
                BLOCK_NUM - U256::from(1),
                TOKEN_RATIO,
                OLD_L1_BASE_FEE,
                OLD_L1_OVERHEAD,
                OLD_L1_BASE_FEE_SCALAR,
                OLD_L1_BLOB_BASE_FEE,
                OLD_L1_BLOB_BASE_FEE_SCALAR,
            ))
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(spec))
            .with_tx(
                OpTransaction::builder()
                    .base(TxEnv::builder().caller(deposit_caller).gas_limit(500_000))
                    .source_hash(B256::from([1u8; 32]))
                    .build_fill(),
            )
            .build_op();
        let initial_gas1 = handler.validate_initial_tx_gas(&mut evm1).unwrap();
        assert_eq!(initial_gas1.initial_total_gas(), 21000);

        if spec.is_enabled_in(OpSpecId::ARSIA) {
            let l1_block_contract =
                evm1.ctx().journal_mut().db_mut().load_account(L1_BLOCK_CONTRACT).unwrap();
            l1_block_contract.storage.insert(L1_BASE_FEE_SLOT, U256::from(NEW_L1_BASE_FEE));
            l1_block_contract
                .storage
                .insert(ECOTONE_L1_BLOB_BASE_FEE_SLOT, U256::from(NEW_L1_BLOB_BASE_FEE));
            l1_block_contract.storage.insert(
                ECOTONE_L1_FEE_SCALARS_SLOT,
                ecotone_fee_scalars(
                    NEW_L1_BASE_FEE_SCALAR as u32,
                    NEW_L1_BLOB_BASE_FEE_SCALAR as u32,
                ),
            );
        } else {
            let l1_block_contract =
                evm1.ctx().journal_mut().db_mut().load_account(L1_BLOCK_CONTRACT).unwrap();
            l1_block_contract.storage.insert(L1_BASE_FEE_SLOT, U256::from(NEW_L1_BASE_FEE));
            l1_block_contract.storage.insert(L1_OVERHEAD_SLOT, U256::from(NEW_L1_OVERHEAD));
            l1_block_contract.storage.insert(L1_SCALAR_SLOT, U256::from(NEW_L1_BASE_FEE_SCALAR));
        }
        let updated_db = evm1.ctx().journal().db().clone();

        // Tx2 (regular): should reload and use new fee params.
        let mut evm2 = Context::op()
            .with_db(updated_db)
            .with_chain(evm1.ctx().chain().clone())
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(spec))
            .with_tx(regular_tx(caller, recipient, 500_000, &regular_input))
            .build_op();
        handler.validate_initial_tx_gas(&mut evm2).unwrap();

        let mut chain = evm2.ctx().chain().clone();
        assert_eq!(chain.token_ratio, U256::from(TOKEN_RATIO));
        assert_eq!(chain.l1_base_fee, U256::from(NEW_L1_BASE_FEE));
        if spec.is_enabled_in(OpSpecId::ARSIA) {
            assert_eq!(chain.l1_blob_base_fee, Some(U256::from(NEW_L1_BLOB_BASE_FEE)));
            assert_eq!(
                chain.l1_blob_base_fee_scalar,
                Some(U256::from(NEW_L1_BLOB_BASE_FEE_SCALAR))
            );
        } else {
            assert_eq!(chain.l1_fee_overhead, Some(U256::from(NEW_L1_OVERHEAD)));
            assert_eq!(chain.l1_base_fee_scalar, U256::from(NEW_L1_BASE_FEE_SCALAR));
        }

        chain.clear_tx_l1_cost();
        let actual = chain.calculate_tx_l1_cost(&regular_input, spec);
        let expected_new = l1_cost_for(
            spec,
            &regular_input,
            TOKEN_RATIO,
            NEW_L1_BASE_FEE,
            NEW_L1_OVERHEAD,
            NEW_L1_BASE_FEE_SCALAR,
            NEW_L1_BLOB_BASE_FEE,
            NEW_L1_BLOB_BASE_FEE_SCALAR,
        );
        let expected_old = l1_cost_for(
            spec,
            &regular_input,
            TOKEN_RATIO,
            OLD_L1_BASE_FEE,
            OLD_L1_OVERHEAD,
            OLD_L1_BASE_FEE_SCALAR,
            OLD_L1_BLOB_BASE_FEE,
            OLD_L1_BLOB_BASE_FEE_SCALAR,
        );
        assert_eq!(actual, expected_new);
        assert_ne!(actual, expected_old);
    }

    #[rstest]
    #[case::pre_arsia(OpSpecId::REGOLITH)]
    #[case::arsia(OpSpecId::ARSIA)]
    fn test_token_ratio_update_uses_old_on_setter_and_new_on_next_tx(#[case] spec: OpSpecId) {
        const BLOCK_NUM: U256 = uint!(600_U256);
        const OLD_TOKEN_RATIO: u64 = 3045;
        const NEW_TOKEN_RATIO: u64 = 3040;
        const L1_BASE_FEE: u64 = 17;
        const L1_OVERHEAD: u64 = 19;
        const L1_BASE_FEE_SCALAR: u64 = 23;
        const L1_BLOB_BASE_FEE: u64 = 29;
        const L1_BLOB_BASE_FEE_SCALAR: u64 = 31;

        let caller = Address::from_str("0x3000000000000000000000000000000000000003").unwrap();
        let recipient = Address::from_str("0x4000000000000000000000000000000000000004").unwrap();
        let regular_input = bytes!("faca02");
        let set_token_ratio_input =
            bytes!("e38e91f90000000000000000000000000000000000000000000000000000000000000be0");

        let mut db = InMemoryDB::default();
        update_fee_params_in_db(
            &mut db,
            spec,
            L1_BASE_FEE,
            L1_OVERHEAD,
            L1_BASE_FEE_SCALAR,
            L1_BLOB_BASE_FEE,
            L1_BLOB_BASE_FEE_SCALAR,
        );
        db.load_account(GAS_ORACLE_CONTRACT)
            .unwrap()
            .storage
            .insert(TOKEN_RATIO_SLOT, U256::from(OLD_TOKEN_RATIO));

        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        // Tx1: regular tx with old token ratio.
        let mut evm1 = Context::op()
            .with_db(db)
            .with_chain(build_chain(
                spec,
                BLOCK_NUM,
                OLD_TOKEN_RATIO,
                L1_BASE_FEE,
                L1_OVERHEAD,
                L1_BASE_FEE_SCALAR,
                L1_BLOB_BASE_FEE,
                L1_BLOB_BASE_FEE_SCALAR,
            ))
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(spec))
            .with_tx(regular_tx(caller, recipient, 600_000, &regular_input))
            .build_op();
        handler.validate_initial_tx_gas(&mut evm1).unwrap();
        assert_eq!(evm1.ctx().chain().token_ratio, U256::from(OLD_TOKEN_RATIO));

        // Tx2: set token ratio tx still uses old token ratio.
        let mut evm2 = Context::op()
            .with_db(evm1.ctx().journal().db().clone())
            .with_chain(evm1.ctx().chain().clone())
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(spec))
            .with_tx(regular_tx(caller, GAS_ORACLE_CONTRACT, 600_000, &set_token_ratio_input))
            .build_op();
        let initial_gas2 = handler.validate_initial_tx_gas(&mut evm2).unwrap();
        assert_eq!(evm2.ctx().chain().token_ratio, U256::from(OLD_TOKEN_RATIO));
        assert_eq!(evm2.ctx().chain().l2_block, None);
        if !spec.is_enabled_in(OpSpecId::ARSIA) {
            assert_eq!(initial_gas2.initial_total_gas() % OLD_TOKEN_RATIO, 0);
        }
        evm2.ctx()
            .journal_mut()
            .db_mut()
            .load_account(GAS_ORACLE_CONTRACT)
            .unwrap()
            .storage
            .insert(TOKEN_RATIO_SLOT, U256::from(NEW_TOKEN_RATIO));
        let updated_db = evm2.ctx().journal().db().clone();

        let mut tx2_chain = evm2.ctx().chain().clone();
        tx2_chain.clear_tx_l1_cost();
        let tx2_cost = tx2_chain.calculate_tx_l1_cost(&set_token_ratio_input, spec);
        let tx2_expected = l1_cost_for(
            spec,
            &set_token_ratio_input,
            OLD_TOKEN_RATIO,
            L1_BASE_FEE,
            L1_OVERHEAD,
            L1_BASE_FEE_SCALAR,
            L1_BLOB_BASE_FEE,
            L1_BLOB_BASE_FEE_SCALAR,
        );
        assert_eq!(tx2_cost, tx2_expected);

        // Tx3: regular tx reloads and uses new token ratio.
        let mut evm3 = Context::op()
            .with_db(updated_db)
            .with_chain(evm2.ctx().chain().clone())
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(spec))
            .with_tx(regular_tx(caller, recipient, 600_000, &regular_input))
            .build_op();
        let initial_gas3 = handler.validate_initial_tx_gas(&mut evm3).unwrap();
        assert_eq!(evm3.ctx().chain().token_ratio, U256::from(NEW_TOKEN_RATIO));
        assert_eq!(evm3.ctx().chain().l2_block, Some(BLOCK_NUM));
        if !spec.is_enabled_in(OpSpecId::ARSIA) {
            assert_eq!(initial_gas3.initial_total_gas() % NEW_TOKEN_RATIO, 0);
        }

        let mut tx3_chain = evm3.ctx().chain().clone();
        tx3_chain.clear_tx_l1_cost();
        let tx3_cost = tx3_chain.calculate_tx_l1_cost(&regular_input, spec);
        let tx3_expected = l1_cost_for(
            spec,
            &regular_input,
            NEW_TOKEN_RATIO,
            L1_BASE_FEE,
            L1_OVERHEAD,
            L1_BASE_FEE_SCALAR,
            L1_BLOB_BASE_FEE,
            L1_BLOB_BASE_FEE_SCALAR,
        );
        assert_eq!(tx3_cost, tx3_expected);
    }

    #[rstest]
    #[case::pre_arsia(OpSpecId::REGOLITH)]
    #[case::arsia(OpSpecId::ARSIA)]
    fn test_fee_params_and_token_ratio_combined_timing(#[case] spec: OpSpecId) {
        const BLOCK_NUM: U256 = uint!(700_U256);
        const OLD_TOKEN_RATIO: u64 = 4500;
        const NEW_TOKEN_RATIO: u64 = 4300;

        const OLD_L1_BASE_FEE: u64 = 41;
        const OLD_L1_OVERHEAD: u64 = 43;
        const OLD_L1_BASE_FEE_SCALAR: u64 = 47;
        const OLD_L1_BLOB_BASE_FEE: u64 = 53;
        const OLD_L1_BLOB_BASE_FEE_SCALAR: u64 = 59;

        const NEW_L1_BASE_FEE: u64 = 61;
        const NEW_L1_OVERHEAD: u64 = 67;
        const NEW_L1_BASE_FEE_SCALAR: u64 = 71;
        const NEW_L1_BLOB_BASE_FEE: u64 = 73;
        const NEW_L1_BLOB_BASE_FEE_SCALAR: u64 = 79;

        let caller = Address::from_str("0x5000000000000000000000000000000000000005").unwrap();
        let recipient = Address::from_str("0x6000000000000000000000000000000000000006").unwrap();
        let deposit_caller =
            Address::from_str("0x7000000000000000000000000000000000000007").unwrap();
        let regular_input = bytes!("faca03");
        let set_token_ratio_input =
            bytes!("e38e91f900000000000000000000000000000000000000000000000000000000000010cc");

        let mut db = InMemoryDB::default();
        update_fee_params_in_db(
            &mut db,
            spec,
            OLD_L1_BASE_FEE,
            OLD_L1_OVERHEAD,
            OLD_L1_BASE_FEE_SCALAR,
            OLD_L1_BLOB_BASE_FEE,
            OLD_L1_BLOB_BASE_FEE_SCALAR,
        );
        db.load_account(GAS_ORACLE_CONTRACT)
            .unwrap()
            .storage
            .insert(TOKEN_RATIO_SLOT, U256::from(OLD_TOKEN_RATIO));

        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        // Tx1 (deposit): update fee params through transaction execution.
        let mut evm1 = Context::op()
            .with_db(db)
            .with_chain(build_chain(
                spec,
                BLOCK_NUM - U256::from(1),
                OLD_TOKEN_RATIO,
                OLD_L1_BASE_FEE,
                OLD_L1_OVERHEAD,
                OLD_L1_BASE_FEE_SCALAR,
                OLD_L1_BLOB_BASE_FEE,
                OLD_L1_BLOB_BASE_FEE_SCALAR,
            ))
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(spec))
            .with_tx(
                OpTransaction::builder()
                    .base(TxEnv::builder().caller(deposit_caller).gas_limit(700_000))
                    .source_hash(B256::from([1u8; 32]))
                    .build_fill(),
            )
            .build_op();
        let initial_gas1 = handler.validate_initial_tx_gas(&mut evm1).unwrap();
        assert_eq!(initial_gas1.initial_total_gas(), 21000);

        if spec.is_enabled_in(OpSpecId::ARSIA) {
            let l1_block_contract =
                evm1.ctx().journal_mut().db_mut().load_account(L1_BLOCK_CONTRACT).unwrap();
            l1_block_contract.storage.insert(L1_BASE_FEE_SLOT, U256::from(NEW_L1_BASE_FEE));
            l1_block_contract
                .storage
                .insert(ECOTONE_L1_BLOB_BASE_FEE_SLOT, U256::from(NEW_L1_BLOB_BASE_FEE));
            l1_block_contract.storage.insert(
                ECOTONE_L1_FEE_SCALARS_SLOT,
                ecotone_fee_scalars(
                    NEW_L1_BASE_FEE_SCALAR as u32,
                    NEW_L1_BLOB_BASE_FEE_SCALAR as u32,
                ),
            );
        } else {
            let l1_block_contract =
                evm1.ctx().journal_mut().db_mut().load_account(L1_BLOCK_CONTRACT).unwrap();
            l1_block_contract.storage.insert(L1_BASE_FEE_SLOT, U256::from(NEW_L1_BASE_FEE));
            l1_block_contract.storage.insert(L1_OVERHEAD_SLOT, U256::from(NEW_L1_OVERHEAD));
            l1_block_contract.storage.insert(L1_SCALAR_SLOT, U256::from(NEW_L1_BASE_FEE_SCALAR));
        }

        // Tx2: regular tx uses new fee params + old token ratio.
        let mut evm2 = Context::op()
            .with_db(evm1.ctx().journal().db().clone())
            .with_chain(evm1.ctx().chain().clone())
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(spec))
            .with_tx(regular_tx(caller, recipient, 700_000, &regular_input))
            .build_op();
        handler.validate_initial_tx_gas(&mut evm2).unwrap();
        assert_eq!(evm2.ctx().chain().token_ratio, U256::from(OLD_TOKEN_RATIO));
        assert_eq!(evm2.ctx().chain().l1_base_fee, U256::from(NEW_L1_BASE_FEE));

        let mut tx2_chain = evm2.ctx().chain().clone();
        tx2_chain.clear_tx_l1_cost();
        let tx2_cost = tx2_chain.calculate_tx_l1_cost(&regular_input, spec);
        let tx2_expected = l1_cost_for(
            spec,
            &regular_input,
            OLD_TOKEN_RATIO,
            NEW_L1_BASE_FEE,
            NEW_L1_OVERHEAD,
            NEW_L1_BASE_FEE_SCALAR,
            NEW_L1_BLOB_BASE_FEE,
            NEW_L1_BLOB_BASE_FEE_SCALAR,
        );
        assert_eq!(tx2_cost, tx2_expected);

        let mut evm3 = Context::op()
            .with_db(evm2.ctx().journal().db().clone())
            .with_chain(evm2.ctx().chain().clone())
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(spec))
            .with_tx(regular_tx(caller, GAS_ORACLE_CONTRACT, 700_000, &set_token_ratio_input))
            .build_op();
        let initial_gas3 = handler.validate_initial_tx_gas(&mut evm3).unwrap();
        assert_eq!(evm3.ctx().chain().token_ratio, U256::from(OLD_TOKEN_RATIO));
        assert_eq!(evm3.ctx().chain().l2_block, None);
        assert_eq!(evm3.ctx().chain().l1_base_fee, U256::from(NEW_L1_BASE_FEE));
        if !spec.is_enabled_in(OpSpecId::ARSIA) {
            assert_eq!(initial_gas3.initial_total_gas() % OLD_TOKEN_RATIO, 0);
        }
        evm3.ctx()
            .journal_mut()
            .db_mut()
            .load_account(GAS_ORACLE_CONTRACT)
            .unwrap()
            .storage
            .insert(TOKEN_RATIO_SLOT, U256::from(NEW_TOKEN_RATIO));
        let updated_db = evm3.ctx().journal().db().clone();

        let mut tx3_chain = evm3.ctx().chain().clone();
        tx3_chain.clear_tx_l1_cost();
        let tx3_cost = tx3_chain.calculate_tx_l1_cost(&set_token_ratio_input, spec);
        let tx3_expected = l1_cost_for(
            spec,
            &set_token_ratio_input,
            OLD_TOKEN_RATIO,
            NEW_L1_BASE_FEE,
            NEW_L1_OVERHEAD,
            NEW_L1_BASE_FEE_SCALAR,
            NEW_L1_BLOB_BASE_FEE,
            NEW_L1_BLOB_BASE_FEE_SCALAR,
        );
        assert_eq!(tx3_cost, tx3_expected);

        // Tx4: regular tx reloads and uses new fee params + new token ratio.
        let mut evm4 = Context::op()
            .with_db(updated_db)
            .with_chain(evm3.ctx().chain().clone())
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(spec))
            .with_tx(regular_tx(caller, recipient, 700_000, &regular_input))
            .build_op();
        let initial_gas4 = handler.validate_initial_tx_gas(&mut evm4).unwrap();
        assert_eq!(evm4.ctx().chain().token_ratio, U256::from(NEW_TOKEN_RATIO));
        assert_eq!(evm4.ctx().chain().l2_block, Some(BLOCK_NUM));
        assert_eq!(evm4.ctx().chain().l1_base_fee, U256::from(NEW_L1_BASE_FEE));
        if !spec.is_enabled_in(OpSpecId::ARSIA) {
            assert_eq!(initial_gas4.initial_total_gas() % NEW_TOKEN_RATIO, 0);
        }

        let mut tx4_chain = evm4.ctx().chain().clone();
        tx4_chain.clear_tx_l1_cost();
        let tx4_cost = tx4_chain.calculate_tx_l1_cost(&regular_input, spec);
        let tx4_expected = l1_cost_for(
            spec,
            &regular_input,
            NEW_TOKEN_RATIO,
            NEW_L1_BASE_FEE,
            NEW_L1_OVERHEAD,
            NEW_L1_BASE_FEE_SCALAR,
            NEW_L1_BLOB_BASE_FEE,
            NEW_L1_BLOB_BASE_FEE_SCALAR,
        );
        assert_eq!(tx4_cost, tx4_expected);
    }

    #[test]
    fn test_tx_low_balance_nonce_unchanged() {
        let ctx = Context::op().with_tx(
            OpTransaction::builder().base(TxEnv::builder().value(U256::from(1000))).build_fill(),
        );

        let mut evm = ctx.build_op();

        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        let result =
            handler.validate_against_state_and_deduct_caller(&mut evm, &mut Default::default());

        assert!(matches!(
            result.err().unwrap(),
            EVMError::Transaction(OpTransactionError::Base(
                InvalidTransaction::LackOfFundForMaxFee { .. }
            ))
        ));
        assert_eq!(evm.0.ctx.journal_mut().load_account(Address::ZERO).unwrap().info.nonce, 0);
    }

    #[test]
    fn test_validate_missing_enveloped_tx() {
        use crate::transaction::deposit::DepositTransactionParts;

        // Create a non-deposit transaction without enveloped_tx
        let ctx = Context::op().with_tx(OpTransaction {
            base: TxEnv::builder().build_fill(),
            enveloped_tx: None, // Missing enveloped_tx for non-deposit transaction
            deposit: DepositTransactionParts::default(), // No source_hash means non-deposit
        });

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        assert_eq!(
            handler.validate_env(&mut evm),
            Err(EVMError::Transaction(OpTransactionError::MissingEnvelopedTx))
        );
    }

    #[test]
    fn test_halted_deposit_bvm_eth_mint_only() {
        let caller = Address::from([0x01; 20]);
        let mint_amount = 100u64;
        // Transfer exceeds the minted balance, so the BVM_ETH transfer fails and is
        // skipped during deduct_caller (op-geth's Case A -> RevertToSnapshot): only the
        // mint and its Mint log persist. This is the block-96442768 shape (eth_tx_value
        // > mint).
        let transfer_amount = 150u64;

        let ctx = Context::op()
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.deposit.source_hash = B256::from([1u8; 32]);
                tx.deposit.mint = Some(mint_amount.into());
                tx.deposit.eth_value = Some(U256::from(mint_amount));
                tx.deposit.eth_tx_value = Some(U256::from(transfer_amount));
                tx.base.value = U256::from(transfer_amount);
            })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::REGOLITH));

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        // Simulate the error that happens when a deposit transaction halts.
        let error = EVMError::Transaction(OpTransactionError::HaltedDepositPostRegolith);

        // catch_error handles the cleanup and BVM_ETH minting logic for failed deposits.
        let result = handler.catch_error(&mut evm, error).unwrap();

        match &result {
            ExecutionResult::Halt { reason, .. } => {
                assert_eq!(*reason, OpHaltReason::FailedDeposit);
            }
            _ => panic!("Expected Halt result"),
        }

        // The failed deposit must still surface the BVM_ETH `Mint` log into the receipt
        // (op-geth emits it before its revert snapshot). Assert the log matches op-geth's
        // format exactly: BVM_ETH address, Mint selector, minter = caller (left-padded),
        // data = eth_value as 32-byte big-endian.
        let logs = result.logs();
        assert_eq!(logs.len(), 1, "failed deposit with eth_value must keep 1 Mint log");
        let mint = &logs[0];
        assert_eq!(mint.address, BvmEth::ADDRESS, "log address must be BVM_ETH");
        assert_eq!(mint.topics().len(), 2, "Mint event has 2 topics");
        assert_eq!(mint.topics()[0], BvmEth::MINT_SELECTOR, "topic0 = Mint selector");
        assert_eq!(
            mint.topics()[1],
            caller.into_word(),
            "topic1 = minter (caller), left-padded to 32 bytes"
        );
        assert_eq!(
            mint.data.data,
            Bytes::from(U256::from(mint_amount).to_be_bytes_vec()),
            "data = eth_value as 32-byte big-endian"
        );

        // Verify BVM_ETH was minted.
        // We calculate the storage slot for the caller's balance in the BvmEth contract
        let slot = BvmEth::get_balance_slot(caller);
        let bvm_balance = evm.ctx().journal_mut().sload(BvmEth::ADDRESS, slot).unwrap().data;

        assert_eq!(bvm_balance, U256::from(mint_amount));

        // Verify caller nonce was bumped and mint balance was credited.
        // Per the OP deposit spec, these must persist even on deposit failure.
        let caller_acc = evm.ctx().journal_mut().load_account(caller).unwrap();
        assert_eq!(caller_acc.info.nonce, 1, "Caller nonce must be bumped for failed deposits");
        assert_eq!(
            caller_acc.info.balance,
            U256::from(mint_amount),
            "Caller balance must include mint for failed deposits"
        );
    }

    #[test]
    fn test_mantle_block_89689906_token_ratio_update() {
        // rig
        //
        // Test case based on Mantle block 89689906
        // <https://mantlescan.xyz/block/89689906>
        //
        // This block contains 6 transactions:
        // 1. Deposit transaction - should not multiply by token ratio <https://mantlescan.xyz/tx/0x9334f65ee1716e7f374b3a499ff82899a0f0e168fd61d2c316bf0eda5c3ea212>
        // 4. Set token ratio transaction - calls gas oracle contract, resets l2_block <https://mantlescan.xyz/tx/0x0ca2cfb8a879f0688913c19cf153969edbb9fb587503b12cef5e920d8aa70283>
        // 5. Regular transaction - should reload and use new token ratio <https://mantlescan.xyz/tx/0xd398169756466a2d1b3fb4c377972d6ecefb9c26ae30ee84aeaa215f2cf77780>
        //
        // The token ratio changed at block 89689906:
        // - Previous block (89689905): 3045 (0xbe5)
        // - After tx4: 3040 (0xbe0)
        //
        // All transactions use ISTHMUS spec

        const BLOCK_NUM: U256 = uint!(89689906_U256);

        // Values extracted from block 89689906
        // L1_BLOCK_CONTRACT storage slots and GAS_ORACLE_CONTRACT storage slots
        // Token ratio at block start (from previous block 89689905): 3045 (0xbe5)
        const INITIAL_TOKEN_RATIO: u64 = 3045;
        // Token ratio after tx4 (set token ratio transaction): 3040 (0xbe0)
        const NEW_TOKEN_RATIO: u64 = 3040;
        const L1_BASE_FEE: U256 = uint!(54869431_U256);
        const L1_FEE_OVERHEAD: U256 = uint!(188_U256);
        const L1_BASE_FEE_SCALAR: u64 = 10000;

        let mut db = InMemoryDB::default();

        // Set up L1 block contract storage
        let l1_block_contract = db.load_account(L1_BLOCK_CONTRACT).unwrap();
        l1_block_contract.storage.insert(L1_BASE_FEE_SLOT, L1_BASE_FEE);
        l1_block_contract.storage.insert(L1_OVERHEAD_SLOT, L1_FEE_OVERHEAD);
        l1_block_contract.storage.insert(L1_SCALAR_SLOT, U256::from(L1_BASE_FEE_SCALAR));

        // Set initial token ratio in gas oracle contract
        let gas_oracle_contract = db.load_account(GAS_ORACLE_CONTRACT).unwrap();
        gas_oracle_contract.storage.insert(TOKEN_RATIO_SLOT, U256::from(INITIAL_TOKEN_RATIO));

        // Set up caller accounts with sufficient balance
        // Addresses from block 89689906 transactions
        let deposit_sender =
            Address::from_str("0xdeaddeaddeaddeaddeaddeaddeaddeaddead0001").unwrap();
        let token_ratio_sender =
            Address::from_str("0xe8bf1c5750354694ed75f97b549cf570fa516725").unwrap();
        let regular_tx_sender =
            Address::from_str("0xd99ac0681b904991169a4f398b9043781adbe0c3").unwrap();

        db.insert_account_info(
            deposit_sender,
            AccountInfo { balance: U256::from(100_000_000), ..Default::default() },
        );
        db.insert_account_info(
            token_ratio_sender,
            AccountInfo { balance: U256::from(100_000_000), ..Default::default() },
        );
        db.insert_account_info(
            regular_tx_sender,
            AccountInfo { balance: U256::from(100_000_000), ..Default::default() },
        );

        let ctx = Context::op()
            .with_db(db)
            .with_chain(L1BlockInfo {
                l2_block: Some(BLOCK_NUM),
                token_ratio: U256::from(INITIAL_TOKEN_RATIO),
                l1_base_fee: L1_BASE_FEE,
                l1_fee_overhead: Some(L1_FEE_OVERHEAD),
                l1_base_fee_scalar: U256::from(L1_BASE_FEE_SCALAR),
                ..Default::default()
            })
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ISTHMUS));

        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        // Transaction 1: Deposit transaction
        // Transaction hash: 0x9334f65ee1716e7f374b3a499ff82899a0f0e168fd61d2c316bf0eda5c3ea212
        const DEPOSIT_TX_RLP: &[u8] = &hex!(
            "015d8eb9000000000000000000000000000000000000000000000000000000000170a5b400000000000000000000000000000000000000000000000000000000695a10370000000000000000000000000000000000000000000000000000000003453db78236bc1a30d9f0a2c237de8235d815dab3258124d87a238d6559ddbb208f012a00000000000000000000000000000000000000000000000000000000000000010000000000000000000000002f40d796917ffb642bd2e2bdd2c762a5e40fd74900000000000000000000000000000000000000000000000000000000000000bc0000000000000000000000000000000000000000000000000000000000002710"
        );
        let deposit_source_hash =
            B256::from_str("0x35e1ede43236480e80925f8a1f4d91c209ab586d058cf7af2b48c754915282c7")
                .unwrap();
        let mut evm1 = ctx
            .clone()
            .with_tx(
                OpTransaction::builder()
                    .base(TxEnv::builder().gas_limit(1_000_000))
                    .source_hash(deposit_source_hash)
                    .enveloped_tx(Some(Bytes::from(DEPOSIT_TX_RLP)))
                    .build_fill(),
            )
            .build_op();

        // Deposit transactions don't multiply by token ratio
        let initial_gas1 = handler.validate_initial_tx_gas(&mut evm1).unwrap();
        // Deposit tx should not multiply by token ratio
        assert_eq!(
            initial_gas1.initial_total_gas(),
            21000,
            "Deposit transaction should not multiply by token ratio"
        );

        // Verify token ratio is still initial value
        assert_eq!(
            evm1.ctx().chain().token_ratio,
            U256::from(INITIAL_TOKEN_RATIO),
            "Token ratio should remain unchanged after deposit transaction"
        );

        // Continue the same block with tx sequence state.
        let ctx_after_tx1 = Context::op()
            .with_db(evm1.ctx().journal().db().clone())
            .with_chain(L1BlockInfo {
                l2_block: Some(BLOCK_NUM),
                token_ratio: U256::from(INITIAL_TOKEN_RATIO),
                l1_base_fee: L1_BASE_FEE,
                l1_fee_overhead: Some(L1_FEE_OVERHEAD),
                l1_base_fee_scalar: U256::from(L1_BASE_FEE_SCALAR),
                ..Default::default()
            })
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ISTHMUS));

        // Transaction 4: Set token ratio transaction
        // Transaction hash: 0x0ca2cfb8a879f0688913c19cf153969edbb9fb587503b12cef5e920d8aa70283
        // Calls gas oracle contract to set token ratio
        const SET_TOKEN_RATIO_TX_RLP: &[u8] =
            &hex!("e38e91f90000000000000000000000000000000000000000000000000000000000000be0");
        let mut evm2 = ctx_after_tx1
            .clone()
            .with_tx(
                OpTransaction::builder()
                    .base(
                        TxEnv::builder()
                            .gas_limit(129_500_000)
                            .caller(token_ratio_sender)
                            .to(GAS_ORACLE_CONTRACT),
                    )
                    .enveloped_tx(Some(Bytes::from(SET_TOKEN_RATIO_TX_RLP)))
                    .build()
                    .unwrap(),
            )
            .build_op();

        // This transaction calls gas oracle contract, which should reset l2_block
        // It should use old token ratio (INITIAL_TOKEN_RATIO) because the chain still has the old
        // value cached
        let initial_gas2 = handler.validate_initial_tx_gas(&mut evm2).unwrap();

        // Calculate expected gas with old token ratio (3045)
        // The base gas (before multiplying by token_ratio) should be consistent
        let base_gas_before = initial_gas2.initial_total_gas() / INITIAL_TOKEN_RATIO;
        let expected_gas_before = base_gas_before * INITIAL_TOKEN_RATIO;

        // Should use old token ratio (3045) before the reset
        assert_eq!(
            initial_gas2.initial_total_gas(),
            expected_gas_before,
            "Transaction 4 should use old token ratio (3045) before reset. Gas: {}, Base: {}, Token ratio: {}",
            initial_gas2.initial_total_gas(),
            base_gas_before,
            INITIAL_TOKEN_RATIO
        );

        // Verify the gas calculation uses INITIAL_TOKEN_RATIO
        assert_eq!(
            initial_gas2.initial_total_gas() % INITIAL_TOKEN_RATIO,
            0,
            "Gas should be divisible by INITIAL_TOKEN_RATIO"
        );

        // Verify that l2_block was reset
        assert_eq!(
            evm2.ctx().chain().l2_block,
            None,
            "l2_block should be reset after calling gas oracle contract"
        );

        // Verify token ratio is still old value (not reloaded yet)
        assert_eq!(
            evm2.ctx().chain().token_ratio,
            U256::from(INITIAL_TOKEN_RATIO),
            "Token ratio should still be old value (3045) before reload"
        );

        // Apply tx4 side effect: token ratio storage is updated by this tx execution.
        evm2.ctx()
            .journal_mut()
            .db_mut()
            .load_account(GAS_ORACLE_CONTRACT)
            .unwrap()
            .storage
            .insert(TOKEN_RATIO_SLOT, U256::from(NEW_TOKEN_RATIO));
        let ctx_after_tx4 = Context::op()
            .with_db(evm2.ctx().journal().db().clone())
            .with_chain(evm2.ctx().chain().clone())
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ISTHMUS));

        // Transaction 5: Regular transaction - should use new token ratio
        // Transaction hash: 0xd398169756466a2d1b3fb4c377972d6ecefb9c26ae30ee84aeaa215f2cf77780
        // Create a new context with l2_block reset to None so it will reload from database
        const REGULAR_TX_RLP: &[u8] = &hex!(
            "38899935000000000000000000000000428ef0f8209be073ae7ccf4c2586942e2d21820f000000000000000000000000000000000000000000000000000000000000004000000000000000000000000000000000000000000000000000000000000000412d3b8b4ed1764ae8529350b87648aca1fbf2f4c8e22fcaed899a050292caf9ba4e68c61e5baa496e7ab64b3fbca12a65584142a17228ac9e5457055d4412fa051b00000000000000000000000000000000000000000000000000000000000000"
        );
        let regular_tx_to =
            Address::from_str("0x5523985926aa12ba58dc5ad00ddca99678d7227e").unwrap();
        let mut evm3 = ctx_after_tx4
            .with_chain(L1BlockInfo {
                l2_block: None, // Reset to None so it will reload from database
                // This will be overwritten when reloaded
                token_ratio: U256::from(INITIAL_TOKEN_RATIO),
                l1_base_fee: L1_BASE_FEE,
                l1_fee_overhead: Some(L1_FEE_OVERHEAD),
                l1_base_fee_scalar: U256::from(L1_BASE_FEE_SCALAR),
                ..Default::default()
            })
            .with_tx(
                OpTransaction::builder()
                    .base(
                        TxEnv::builder()
                            .gas_limit(585_000_000)
                            .caller(regular_tx_sender)
                            .to(regular_tx_to),
                    )
                    .enveloped_tx(Some(Bytes::from(REGULAR_TX_RLP)))
                    .build_fill(),
            )
            .build_op();

        // This transaction should reload L1BlockInfo and use new token ratio
        let initial_gas3 = handler.validate_initial_tx_gas(&mut evm3).unwrap();

        // Calculate expected gas with new token ratio (3040)
        // The base gas (before multiplying by token_ratio) should be the same as before
        // Only the token_ratio multiplier changes from 3045 to 3040
        let base_gas_after = initial_gas3.initial_total_gas() / NEW_TOKEN_RATIO;
        let expected_gas_after = base_gas_after * NEW_TOKEN_RATIO;

        // Should use new token ratio (3040) after reload
        assert_eq!(
            initial_gas3.initial_total_gas(),
            expected_gas_after,
            "Transaction 5 should use new token ratio (3040) after reload. Gas: {}, Base: {}, Token ratio: {}",
            initial_gas3.initial_total_gas(),
            base_gas_after,
            NEW_TOKEN_RATIO
        );

        // Verify that gas calculation changed due to token ratio change
        // Since token_ratio decreased from 3045 to 3040, gas should decrease
        assert!(
            initial_gas3.initial_total_gas() < initial_gas2.initial_total_gas(),
            "Gas should decrease after token ratio change: {} (with ratio {}) -> {} (with ratio {})",
            initial_gas2.initial_total_gas(),
            INITIAL_TOKEN_RATIO,
            initial_gas3.initial_total_gas(),
            NEW_TOKEN_RATIO
        );

        // Verify the base gas calculation and token ratio multiplier
        let base_gas_before = initial_gas2.initial_total_gas() / INITIAL_TOKEN_RATIO;
        let base_gas_after = initial_gas3.initial_total_gas() / NEW_TOKEN_RATIO;

        // Calculate the actual difference due to token ratio change
        let gas_diff = initial_gas2.initial_total_gas() - initial_gas3.initial_total_gas();
        let ratio_diff = INITIAL_TOKEN_RATIO - NEW_TOKEN_RATIO;

        // Verify that the gas difference is consistent with token ratio difference
        // If base gas were the same, the difference would be: base_gas * (3045 - 3040) = base_gas *
        // 5
        let expected_diff_if_same_base = base_gas_before * ratio_diff;

        // Verify the gas difference matches the expected difference when base gas is the same
        if base_gas_before == base_gas_after {
            assert_eq!(
                gas_diff, expected_diff_if_same_base,
                "Gas difference should match token ratio difference when base gas is the same"
            );
        }

        // Verify token ratio multiplier is correctly applied
        assert_eq!(
            initial_gas2.initial_total_gas() % INITIAL_TOKEN_RATIO,
            0,
            "Gas should be divisible by INITIAL_TOKEN_RATIO"
        );
        assert_eq!(
            initial_gas3.initial_total_gas() % NEW_TOKEN_RATIO,
            0,
            "Gas should be divisible by NEW_TOKEN_RATIO"
        );

        // Verify that L1BlockInfo was reloaded with new token ratio
        assert_eq!(
            evm3.ctx().chain().token_ratio,
            U256::from(NEW_TOKEN_RATIO),
            "Token ratio should be reloaded from database (changed from 3045 to 3040)"
        );
        assert_eq!(
            evm3.ctx().chain().l2_block,
            Some(BLOCK_NUM),
            "l2_block should be set after reload"
        );

        // Verify that ISTHMUS spec doesn't load ecotone/isthmus/jovian fields
        assert_eq!(
            evm3.ctx().chain().l1_blob_base_fee,
            None,
            "ISTHMUS should not load l1_blob_base_fee"
        );
        assert_eq!(
            evm3.ctx().chain().operator_fee_scalar,
            None,
            "ISTHMUS should not load operator_fee_scalar"
        );
        assert_eq!(
            evm3.ctx().chain().da_footprint_gas_scalar,
            None,
            "ISTHMUS should not load da_footprint_gas_scalar"
        );

        // Verify L1 cost calculation uses the correct token ratio
        // For ISTHMUS spec, L1 cost calculation should use the reloaded token ratio
        let mut chain_info = evm3.ctx().chain().clone();
        chain_info.clear_tx_l1_cost();
        let l1_cost_with_new_ratio =
            chain_info.calculate_tx_l1_cost(REGULAR_TX_RLP, OpSpecId::ISTHMUS);

        // Verify that L1 cost calculation uses NEW_TOKEN_RATIO
        // L1 cost formula for ISTHMUS: (data_gas + overhead) * l1_base_fee * l1_base_fee_scalar *
        // token_ratio / 1_000_000
        let data_gas = chain_info.data_gas(REGULAR_TX_RLP, OpSpecId::ISTHMUS);
        let expected_l1_cost = (data_gas.saturating_add(L1_FEE_OVERHEAD))
            .saturating_mul(L1_BASE_FEE)
            .saturating_mul(U256::from(L1_BASE_FEE_SCALAR))
            .saturating_mul(U256::from(NEW_TOKEN_RATIO))
            .wrapping_div(U256::from(1_000_000));

        assert_eq!(
            l1_cost_with_new_ratio, expected_l1_cost,
            "L1 cost should be calculated with NEW_TOKEN_RATIO (3040)"
        );
    }

    #[test]
    fn test_catch_error_deposit_cleanup_always_runs() {
        // Verify that catch_error always performs cleanup (clear_tx_l1_cost,
        // clear local, clear frame_stack) regardless of the error path taken.
        let caller = Address::from([0x02; 20]);
        let mint_amount = 200u64;

        let ctx = Context::op()
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.deposit.source_hash = B256::from([1u8; 32]);
                tx.deposit.mint = Some(mint_amount.into());
                tx.deposit.eth_value = Some(U256::from(mint_amount));
            })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::REGOLITH));

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        // Set a cached L1 cost to verify it gets cleared by catch_error.
        evm.ctx().chain.tx_l1_cost = Some(U256::from(12345));

        let error = EVMError::Transaction(OpTransactionError::HaltedDepositPostRegolith);
        let result = handler.catch_error(&mut evm, error);

        assert!(result.is_ok(), "Failed deposit should return Ok(FailedDeposit)");

        // Verify cleanup was performed.
        assert_eq!(
            evm.ctx().chain.tx_l1_cost,
            None,
            "clear_tx_l1_cost must be called after catch_error"
        );
    }

    #[test]
    fn test_catch_error_non_deposit_reverts_all_state() {
        // For non-deposit transaction errors, catch_error must discard_tx()
        // to revert all uncommitted state changes.
        let caller = Address::from([0x03; 20]);
        let initial_balance = U256::from(1_000_000u64);

        let ctx = Context::op()
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                // No source_hash → not a deposit transaction
            })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::REGOLITH));

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        // Pre-load caller with balance and touch the account (simulating
        // validate_against_state_and_deduct_caller partial execution).
        {
            let journal = evm.ctx().journal_mut();
            journal.balance_incr(caller, initial_balance).unwrap();
            let mut acc = journal.load_account_mut(caller).unwrap();
            acc.bump_nonce();
        }

        // Verify state was modified before catch_error.
        let nonce_before = evm.ctx().journal_mut().load_account(caller).unwrap().info.nonce;
        assert_eq!(nonce_before, 1, "Nonce should be 1 before catch_error");

        // catch_error for non-deposit should return Err and call discard_tx().
        let error =
            EVMError::Transaction(OpTransactionError::Base(InvalidTransaction::NonceTooLow {
                tx: 0,
                state: 1,
            }));
        let result = handler.catch_error(&mut evm, error);

        assert!(result.is_err(), "Non-deposit error should propagate as Err");

        // After discard_tx(), the nonce bump should be reverted.
        let nonce_after = evm.ctx().journal_mut().load_account(caller).unwrap().info.nonce;
        assert_eq!(nonce_after, 0, "discard_tx must revert nonce bump for non-deposit errors");
    }

    #[test]
    fn test_catch_error_deposit_nonce_mint_persist_across_next_tx() {
        // Verify that after catch_error processes a failed deposit, the committed
        // nonce/mint are not reverted by a subsequent discard_tx() (simulating
        // a follow-up transaction that also fails).
        let caller = Address::from([0x04; 20]);
        let mint_amount = 500u64;

        let ctx = Context::op()
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.deposit.source_hash = B256::from([1u8; 32]);
                tx.deposit.mint = Some(mint_amount.into());
                tx.deposit.eth_value = Some(U256::from(mint_amount));
            })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::REGOLITH));

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        // Process the failed deposit.
        let error = EVMError::Transaction(OpTransactionError::HaltedDepositPostRegolith);
        let result = handler.catch_error(&mut evm, error);
        assert!(result.is_ok());

        // Now simulate a subsequent transaction failure by calling discard_tx().
        // This must NOT revert the previously committed nonce/mint.
        evm.ctx().journal_mut().discard_tx();

        let caller_acc = evm.ctx().journal_mut().load_account(caller).unwrap();
        assert_eq!(caller_acc.info.nonce, 1, "Committed nonce must survive subsequent discard_tx");
        assert_eq!(
            caller_acc.info.balance,
            U256::from(mint_amount),
            "Committed mint balance must survive subsequent discard_tx"
        );

        // BVM_ETH state should also survive.
        let slot = BvmEth::get_balance_slot(caller);
        let bvm_balance = evm.ctx().journal_mut().sload(BvmEth::ADDRESS, slot).unwrap().data;
        assert_eq!(
            bvm_balance,
            U256::from(mint_amount),
            "Committed BVM_ETH balance must survive subsequent discard_tx"
        );
    }

    // ==================== Intra-block token_ratio regression tests ====================
    //
    // op-gas-oracle calls setTokenRatio on GasOracle (0x42..000F) mid-block.
    // op-geth reads tokenRatio from statedb on every L1 cost call (rollup_cost.go:316),
    // so subsequent txs see the updated value. op-revm caches L1BlockInfo at block start.
    // The v107 merge added a redundant try_fetch(journal.db_mut()) guard in
    // validate_against_state_and_deduct_caller; journal.db_mut() bypasses journal state
    // → reads stale underlying-DB values → silently reverts the ratio → stateRoot mismatch.
    //
    // Fix: remove the try_fetch guard from validate_against_state_and_deduct_caller.
    // validate_initial_tx_gas already handles L1BlockInfo reload via reset_l2_block().

    /// Test 1: validate_against_state_and_deduct_caller must preserve token_ratio
    /// even when l2_block is None (which would have triggered the removed guard).
    #[test]
    fn test_no_stale_refetch_preserves_token_ratio() {
        const OLD_TOKEN_RATIO: u64 = 3000;
        const NEW_TOKEN_RATIO: u64 = 2500;

        let caller = Address::ZERO;
        let mut db = InMemoryDB::default();
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(10_000_000u64), ..Default::default() },
        );
        let l1_block = db.load_account(L1_BLOCK_CONTRACT).unwrap();
        l1_block.storage.insert(L1_BASE_FEE_SLOT, U256::from(1_000));
        l1_block.storage.insert(ECOTONE_L1_BLOB_BASE_FEE_SLOT, U256::ZERO);
        l1_block.storage.insert(ECOTONE_L1_FEE_SCALARS_SLOT, U256::from(1_000) << 128);
        let gas_oracle = db.load_account(GAS_ORACLE_CONTRACT).unwrap();
        gas_oracle.storage.insert(TOKEN_RATIO_SLOT, U256::from(OLD_TOKEN_RATIO));

        let ctx = Context::op()
            .with_db(db)
            .with_chain(L1BlockInfo {
                l1_base_fee: U256::from(1_000),
                l1_base_fee_scalar: U256::from(1_000),
                l1_blob_base_fee: Some(U256::ZERO),
                l1_blob_base_fee_scalar: Some(U256::ZERO),
                l2_block: None,
                operator_fee_scalar: Some(U256::ZERO),
                operator_fee_constant: Some(U256::ZERO),
                token_ratio: U256::from(NEW_TOKEN_RATIO),
                ..Default::default()
            })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ARSIA))
            .with_tx(
                OpTransaction::builder()
                    .base(TxEnv::builder().gas_limit(100))
                    .enveloped_tx(Some(bytes!("FACADE")))
                    .source_hash(B256::ZERO)
                    .build()
                    .unwrap(),
            );

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();
        handler
            .validate_against_state_and_deduct_caller(&mut evm, &mut Default::default())
            .unwrap();

        assert_eq!(
            evm.ctx().chain().token_ratio,
            U256::from(NEW_TOKEN_RATIO),
            "token_ratio must not be overwritten by stale db_mut() read"
        );
    }

    /// Test 2: Cached tx_l1_cost must reflect the live token_ratio, not the stale one.
    #[test]
    fn test_l1_cost_uses_live_token_ratio() {
        const OLD_TOKEN_RATIO: u64 = 3000;
        const NEW_TOKEN_RATIO: u64 = 2000;

        let caller = Address::ZERO;
        let mut db = InMemoryDB::default();
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(10_000_000_000u64), ..Default::default() },
        );
        let l1_block = db.load_account(L1_BLOCK_CONTRACT).unwrap();
        l1_block.storage.insert(L1_BASE_FEE_SLOT, U256::from(1_000));
        l1_block.storage.insert(ECOTONE_L1_BLOB_BASE_FEE_SLOT, U256::ZERO);
        l1_block.storage.insert(ECOTONE_L1_FEE_SCALARS_SLOT, U256::from(1_000) << 128);
        let gas_oracle = db.load_account(GAS_ORACLE_CONTRACT).unwrap();
        gas_oracle.storage.insert(TOKEN_RATIO_SLOT, U256::from(OLD_TOKEN_RATIO));

        let mut info_new = L1BlockInfo {
            l1_base_fee: U256::from(1_000),
            l1_base_fee_scalar: U256::from(1_000),
            l1_blob_base_fee: Some(U256::ZERO),
            l1_blob_base_fee_scalar: Some(U256::ZERO),
            token_ratio: U256::from(NEW_TOKEN_RATIO),
            ..Default::default()
        };
        let cost_new = info_new.calculate_tx_l1_cost(&bytes!("FACADE"), OpSpecId::ARSIA);
        let mut info_old =
            L1BlockInfo { token_ratio: U256::from(OLD_TOKEN_RATIO), ..info_new.clone() };
        info_old.tx_l1_cost = None;
        let cost_old = info_old.calculate_tx_l1_cost(&bytes!("FACADE"), OpSpecId::ARSIA);
        assert_ne!(cost_new, cost_old, "precondition: different ratios produce different costs");

        let ctx = Context::op()
            .with_db(db)
            .with_chain(L1BlockInfo {
                l1_base_fee: U256::from(1_000),
                l1_base_fee_scalar: U256::from(1_000),
                l1_blob_base_fee: Some(U256::ZERO),
                l1_blob_base_fee_scalar: Some(U256::ZERO),
                l2_block: None,
                operator_fee_scalar: Some(U256::ZERO),
                operator_fee_constant: Some(U256::ZERO),
                token_ratio: U256::from(NEW_TOKEN_RATIO),
                ..Default::default()
            })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ARSIA))
            .with_tx(
                OpTransaction::builder()
                    .base(TxEnv::builder().gas_limit(100))
                    .enveloped_tx(Some(bytes!("FACADE")))
                    .source_hash(B256::ZERO)
                    .build()
                    .unwrap(),
            );

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();
        handler
            .validate_against_state_and_deduct_caller(&mut evm, &mut Default::default())
            .unwrap();

        assert_eq!(
            evm.ctx().chain().tx_l1_cost,
            Some(cost_new),
            "L1 cost must use NEW ratio ({NEW_TOKEN_RATIO}), not stale OLD ({OLD_TOKEN_RATIO})"
        );
    }

    /// Test 3: Sender balance after deduction must match L1 cost with live token_ratio.
    #[test]
    fn test_sender_balance_reflects_live_token_ratio() {
        const OLD_TOKEN_RATIO: u64 = 3000;
        const NEW_TOKEN_RATIO: u64 = 2000;
        const INITIAL_BALANCE: u64 = 10_000_000_000;

        let caller = Address::ZERO;
        let mut db = InMemoryDB::default();
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(INITIAL_BALANCE), ..Default::default() },
        );
        let l1_block = db.load_account(L1_BLOCK_CONTRACT).unwrap();
        l1_block.storage.insert(L1_BASE_FEE_SLOT, U256::from(1_000));
        l1_block.storage.insert(ECOTONE_L1_BLOB_BASE_FEE_SLOT, U256::ZERO);
        l1_block.storage.insert(ECOTONE_L1_FEE_SCALARS_SLOT, U256::from(1_000) << 128);
        let gas_oracle = db.load_account(GAS_ORACLE_CONTRACT).unwrap();
        gas_oracle.storage.insert(TOKEN_RATIO_SLOT, U256::from(OLD_TOKEN_RATIO));

        let mut info = L1BlockInfo {
            l1_base_fee: U256::from(1_000),
            l1_base_fee_scalar: U256::from(1_000),
            l1_blob_base_fee: Some(U256::ZERO),
            l1_blob_base_fee_scalar: Some(U256::ZERO),
            token_ratio: U256::from(NEW_TOKEN_RATIO),
            ..Default::default()
        };
        let expected_l1_cost = info.calculate_tx_l1_cost(&bytes!("FACADE"), OpSpecId::ARSIA);

        let ctx = Context::op()
            .with_db(db)
            .with_chain(L1BlockInfo {
                l1_base_fee: U256::from(1_000),
                l1_base_fee_scalar: U256::from(1_000),
                l1_blob_base_fee: Some(U256::ZERO),
                l1_blob_base_fee_scalar: Some(U256::ZERO),
                l2_block: None,
                operator_fee_scalar: Some(U256::ZERO),
                operator_fee_constant: Some(U256::ZERO),
                token_ratio: U256::from(NEW_TOKEN_RATIO),
                ..Default::default()
            })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ARSIA))
            .with_tx(
                OpTransaction::builder()
                    .base(TxEnv::builder().gas_limit(100))
                    .enveloped_tx(Some(bytes!("FACADE")))
                    .source_hash(B256::ZERO)
                    .build()
                    .unwrap(),
            );

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();
        handler
            .validate_against_state_and_deduct_caller(&mut evm, &mut Default::default())
            .unwrap();

        let account = evm.ctx().journal_mut().load_account(caller).unwrap();
        let expected_balance = U256::from(INITIAL_BALANCE) - expected_l1_cost;
        assert_eq!(
            account.info.balance, expected_balance,
            "balance must reflect L1 cost with NEW ratio. l1_cost={expected_l1_cost}"
        );
    }

    /// Test 4: Normal reload via validate_initial_tx_gas still works for new blocks.
    #[test]
    fn test_l1_block_info_still_reloads_on_new_block() {
        const BLOCK_NUM: U256 = uint!(200_U256);
        const TOKEN_RATIO: u64 = 4000;

        let caller = Address::ZERO;
        let mut db = InMemoryDB::default();
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(10_000_000_000u64), ..Default::default() },
        );
        let l1_block = db.load_account(L1_BLOCK_CONTRACT).unwrap();
        l1_block.storage.insert(L1_BASE_FEE_SLOT, U256::from(7));
        l1_block.storage.insert(ECOTONE_L1_BLOB_BASE_FEE_SLOT, U256::from(1));
        l1_block.storage.insert(
            ECOTONE_L1_FEE_SCALARS_SLOT,
            U256::from(1368u64) << 128 | U256::from(810949u64) << 64,
        );
        l1_block.storage.insert(L1_OVERHEAD_SLOT, U256::ZERO);
        l1_block.storage.insert(L1_SCALAR_SLOT, U256::ZERO);
        l1_block.storage.insert(OPERATOR_FEE_SCALARS_SLOT, U256::ZERO);
        let gas_oracle = db.load_account(GAS_ORACLE_CONTRACT).unwrap();
        gas_oracle.storage.insert(TOKEN_RATIO_SLOT, U256::from(TOKEN_RATIO));

        let ctx = Context::op()
            .with_db(db)
            .with_chain(L1BlockInfo {
                l2_block: Some(BLOCK_NUM + U256::from(1)),
                token_ratio: U256::from(9999),
                ..Default::default()
            })
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ARSIA))
            .with_tx(
                OpTransaction::builder()
                    .base(TxEnv::builder().gas_limit(100_000))
                    .enveloped_tx(Some(bytes!("FACADE")))
                    .source_hash(B256::ZERO)
                    .build()
                    .unwrap(),
            );

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();
        handler.validate_initial_tx_gas(&mut evm).unwrap();

        assert_eq!(evm.ctx().chain().l2_block, Some(BLOCK_NUM));
        assert_eq!(
            evm.ctx().chain().token_ratio,
            U256::from(TOKEN_RATIO),
            "validate_initial_tx_gas must reload from DB on new block"
        );

        handler
            .validate_against_state_and_deduct_caller(&mut evm, &mut Default::default())
            .unwrap();
        assert_eq!(
            evm.ctx().chain().token_ratio,
            U256::from(TOKEN_RATIO),
            "validate_against_state must not alter token_ratio"
        );
    }

    /// Test 5: Two-tx flow — setTokenRatio then normal tx in the same block.
    #[test]
    fn test_two_tx_set_token_ratio_then_normal_tx() {
        const BLOCK_NUM: U256 = uint!(300_U256);
        const OLD_TOKEN_RATIO: u64 = 2984;
        const NEW_TOKEN_RATIO: u64 = 2980;

        let caller = Address::ZERO;
        let mut db = InMemoryDB::default();
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(10_000_000_000u64), ..Default::default() },
        );
        let l1_block = db.load_account(L1_BLOCK_CONTRACT).unwrap();
        l1_block.storage.insert(L1_BASE_FEE_SLOT, U256::from(7));
        l1_block.storage.insert(ECOTONE_L1_BLOB_BASE_FEE_SLOT, U256::from(1));
        l1_block.storage.insert(
            ECOTONE_L1_FEE_SCALARS_SLOT,
            U256::from(1368u64) << 128 | U256::from(810949u64) << 64,
        );
        l1_block.storage.insert(L1_OVERHEAD_SLOT, U256::ZERO);
        l1_block.storage.insert(L1_SCALAR_SLOT, U256::ZERO);
        l1_block.storage.insert(OPERATOR_FEE_SCALARS_SLOT, U256::ZERO);
        let gas_oracle = db.load_account(GAS_ORACLE_CONTRACT).unwrap();
        gas_oracle.storage.insert(TOKEN_RATIO_SLOT, U256::from(OLD_TOKEN_RATIO));

        let ctx = Context::op()
            .with_db(db)
            .with_chain(L1BlockInfo {
                l2_block: Some(BLOCK_NUM),
                token_ratio: U256::from(OLD_TOKEN_RATIO),
                l1_base_fee: U256::from(7),
                l1_base_fee_scalar: U256::from(1368),
                l1_blob_base_fee: Some(U256::from(1)),
                l1_blob_base_fee_scalar: Some(U256::from(810949)),
                operator_fee_scalar: Some(U256::ZERO),
                operator_fee_constant: Some(U256::ZERO),
                ..Default::default()
            })
            .with_block(BlockEnv { number: BLOCK_NUM, ..Default::default() })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ARSIA));

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        // --- tx1: targets GAS_ORACLE_CONTRACT ---
        evm.ctx().modify_tx(|tx| {
            tx.base = TxEnv::builder().gas_limit(100_000).to(GAS_ORACLE_CONTRACT).build().unwrap();
            tx.enveloped_tx = Some(bytes!("FACADE"));
        });
        handler.validate_initial_tx_gas(&mut evm).unwrap();
        assert_eq!(
            evm.ctx().chain().l2_block,
            None,
            "reset_l2_block must fire for GAS_ORACLE_CONTRACT tx"
        );

        // Simulate tx1 SSTORE: update token_ratio in journal
        evm.ctx().journal_mut().load_account(GAS_ORACLE_CONTRACT).unwrap();
        evm.ctx()
            .journal_mut()
            .sstore(GAS_ORACLE_CONTRACT, TOKEN_RATIO_SLOT, U256::from(NEW_TOKEN_RATIO))
            .unwrap();
        evm.ctx().journal_mut().commit_tx();
        evm.ctx().chain_mut().clear_tx_l1_cost();

        // --- tx2: normal transfer ---
        evm.ctx().modify_tx(|tx| {
            tx.base =
                TxEnv::builder().gas_limit(100_000).to(Address::from([0x42; 20])).build().unwrap();
            tx.enveloped_tx = Some(bytes!("FACADE"));
        });
        handler.validate_initial_tx_gas(&mut evm).unwrap();
        assert_eq!(evm.ctx().chain().l2_block, Some(BLOCK_NUM));

        let ratio_before = evm.ctx().chain().token_ratio;
        handler
            .validate_against_state_and_deduct_caller(&mut evm, &mut Default::default())
            .unwrap();
        let ratio_after = evm.ctx().chain().token_ratio;

        assert_eq!(ratio_before, ratio_after, "validate_against_state must not change token_ratio");
    }

    /// A failed deposit that takes the `catch_error` path (op-geth's Case A: the BVM_ETH
    /// transfer fails its balance check, so `innerExecute` returns a Go error and op-geth
    /// `RevertToSnapshot`s) persists only the BVM_ETH Mint — NOT the transfer — even when
    /// eth_tx_value would otherwise be transferable. The transfer only survives op-geth's
    /// Case B (vm error after the transfer executed), handled in `execution_result` — see
    /// `test_failed_deposit_executed_then_revert_keeps_mint_and_transfer`.
    #[test]
    fn test_failed_deposit_halt_persists_mint_only_not_transfer() {
        let caller = Address::from([0x11; 20]);
        let recipient = Address::from([0x42; 20]);
        // transfer == mint: the transfer WOULD succeed on balance, yet the catch_error
        // path must still drop it (only the mint persists).
        let amount = 1_000_000_000_000_000u128; // 0.001 ETH, like block 286432

        let ctx = Context::op()
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.base.kind = revm::primitives::TxKind::Call(recipient);
                tx.deposit.source_hash = B256::from([1u8; 32]);
                tx.deposit.eth_value = Some(U256::from(amount)); // mint
                tx.deposit.eth_tx_value = Some(U256::from(amount)); // transfer (dropped on this path)
            })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::REGOLITH));

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        let error = EVMError::Transaction(OpTransactionError::HaltedDepositPostRegolith);
        let result = handler.catch_error(&mut evm, error).unwrap();

        assert!(
            matches!(&result, ExecutionResult::Halt { reason: OpHaltReason::FailedDeposit, .. }),
            "failed deposit must Halt with FailedDeposit"
        );

        // catch_error keeps exactly the Mint log — the transfer does not persist.
        let logs = result.logs();
        assert_eq!(
            logs.len(),
            1,
            "catch_error failed deposit persists only the Mint log (transfer dropped)"
        );
        assert_eq!(logs[0].address, BvmEth::ADDRESS);
        assert_eq!(logs[0].topics()[0], BvmEth::MINT_SELECTOR, "topic0 = Mint");
        assert_eq!(logs[0].topics()[1], caller.into_word(), "minter = caller");

        // Balances: minted to caller; nothing transferred out (transfer dropped).
        let caller_bal = evm
            .ctx()
            .journal_mut()
            .sload(BvmEth::ADDRESS, BvmEth::get_balance_slot(caller))
            .unwrap()
            .data;
        let recip_bal = evm
            .ctx()
            .journal_mut()
            .sload(BvmEth::ADDRESS, BvmEth::get_balance_slot(recipient))
            .unwrap()
            .data;
        assert_eq!(
            caller_bal,
            U256::from(amount),
            "caller keeps the minted BVM_ETH (no transfer on this path)"
        );
        assert_eq!(
            recip_bal,
            U256::ZERO,
            "recipient receives nothing on a catch_error failed deposit"
        );
    }

    /// Reproduces Mantle mainnet block 96442768, tx index 1
    /// (0x6b7cbdddeae0c93405abb79fd4f18eb162377efc3a39d50a3412dbe58e427482):
    /// a failed deposit (status=0) to an EOA with empty calldata that still mints
    /// `eth_value` of BVM_ETH. op-geth emits the ERC20 `Mint` event before its revert
    /// snapshot, so the mint log survives the failure and appears in the receipt (1 log,
    /// non-zero logsBloom). op-revm previously dropped it (`logs: Vec::new()`), producing
    /// a different receipts root and forking the node.
    ///
    /// Asserts op-revm now surfaces a Mint log byte-for-byte identical to the canonical
    /// (op-geth) log observed on-chain for this transaction.
    #[test]
    fn test_failed_deposit_persists_mint_log_like_geth_block_96442768() {
        // Exact on-chain values for the failing deposit.
        let caller = Address::from_str("0xc214b42e093c7739179833496791fbd50ec68de4").unwrap();
        // eth_value = 0.001 ETH = 1_000_000_000_000_000 wei = 0x38d7ea4c68000.
        let eth_value: u128 = 1_000_000_000_000_000;

        let ctx = Context::op()
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.deposit.source_hash = B256::from([1u8; 32]);
                tx.deposit.mint = Some(eth_value);
                tx.deposit.eth_value = Some(U256::from(eth_value));
            })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::REGOLITH));

        let mut evm = ctx.build_op();
        let handler =
            OpHandler::<_, EVMError<_, OpTransactionError>, EthFrame<EthInterpreter>>::new();

        let error = EVMError::Transaction(OpTransactionError::HaltedDepositPostRegolith);
        let result = handler.catch_error(&mut evm, error).unwrap();

        // status = 0 (Halt) but the receipt still carries the Mint log.
        assert!(
            matches!(&result, ExecutionResult::Halt { reason: OpHaltReason::FailedDeposit, .. }),
            "failed deposit must Halt with FailedDeposit"
        );

        let logs = result.logs();
        assert_eq!(logs.len(), 1, "exactly one Mint log, matching op-geth");
        let mint = &logs[0];

        // Byte-for-byte equality with the canonical op-geth log.
        assert_eq!(
            mint.address,
            Address::from_str("0xdeaddeaddeaddeaddeaddeaddeaddeaddead1111").unwrap(),
            "address = BVM_ETH"
        );
        assert_eq!(
            mint.topics()[0],
            B256::from_str("0x0f6798a560793a54c3bcfe86a93cde1e73087d944c0ea20544137d4121396885")
                .unwrap(),
            "topic0 = keccak(Mint(address,uint256))"
        );
        assert_eq!(
            mint.topics()[1],
            B256::from_str("0x000000000000000000000000c214b42e093c7739179833496791fbd50ec68de4")
                .unwrap(),
            "topic1 = minter, left-padded"
        );
        assert_eq!(
            mint.data.data,
            Bytes::from_str("0x00000000000000000000000000000000000000000000000000038d7ea4c68000")
                .unwrap(),
            "data = 0.001 ETH as 32-byte big-endian"
        );
    }

    // ---- Executed-then-failed deposit keeps mint + transfer (revert AND OOG) ----

    fn run_deposit_to_target(
        target: Address,
        code: std::vec::Vec<u8>,
        amount: u128,
        gas_limit: u64,
    ) -> (ExecutionResult<OpHaltReason>, U256) {
        use revm::{ExecuteEvm, bytecode::Bytecode, primitives::TxKind};

        let caller = Address::from([0x11; 20]);
        let mut db = InMemoryDB::default();
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(10_000_000_000_000_000u128), ..Default::default() },
        );
        let target_code = Bytecode::new_raw(Bytes::from(code));
        let code_hash = target_code.hash_slow();
        db.insert_account_info(
            target,
            AccountInfo { code_hash, code: Some(target_code), ..Default::default() },
        );

        let ctx = Context::op()
            .with_db(db)
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.base.kind = TxKind::Call(target);
                tx.base.gas_limit = gas_limit;
                tx.deposit.source_hash = B256::from([1u8; 32]);
                tx.deposit.eth_value = Some(U256::from(amount));
                tx.deposit.eth_tx_value = Some(U256::from(amount));
            })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::REGOLITH));

        let mut evm = ctx.build_op();
        let out = evm.replay().unwrap();
        let slot = BvmEth::get_balance_slot(target);
        let to_balance = out
            .state
            .get(&BvmEth::ADDRESS)
            .and_then(|acc| acc.storage.get(&slot))
            .map(|s| s.present_value)
            .unwrap_or_default();
        (out.result, to_balance)
    }

    fn assert_failed_deposit_mint_and_transfer(
        result: &ExecutionResult<OpHaltReason>,
        to_balance: U256,
        target: Address,
        amount: u128,
    ) {
        let caller = Address::from([0x11; 20]);
        match result {
            ExecutionResult::Halt { reason, .. } => {
                assert_eq!(*reason, OpHaltReason::FailedDeposit)
            }
            other => panic!("expected FailedDeposit halt, got {other:?}"),
        }
        let logs = result.logs();
        assert_eq!(logs.len(), 2, "must keep Mint + Transfer; got {logs:?}");
        assert_eq!(logs[0].topics()[0], BvmEth::MINT_SELECTOR, "log[0] = Mint");
        assert_eq!(logs[0].topics()[1], caller.into_word());
        assert_eq!(logs[1].topics()[0], BvmEth::TRANSFER_SELECTOR, "log[1] = Transfer");
        assert_eq!(logs[1].topics()[1], caller.into_word());
        assert_eq!(logs[1].topics()[2], target.into_word());
        assert_eq!(to_balance, U256::from(amount), "transfer must persist in state");
    }

    /// Block-286456 shape: deposit ENTERS the EVM call (transfer succeeds) and the target
    /// then reverts. Keeps Mint + Transfer (logs and state).
    #[test]
    fn test_failed_deposit_executed_then_revert_keeps_mint_and_transfer() {
        let target = Address::from([0x42; 20]);
        let amount = 1_000_000_000_000_000u128; // 1e15, like block 286456
        // PUSH1 0; PUSH1 0; REVERT
        let (result, to_balance) =
            run_deposit_to_target(target, vec![0x60, 0x00, 0x60, 0x00, 0xfd], amount, 200_000);
        assert_failed_deposit_mint_and_transfer(&result, to_balance, target, amount);
    }

    /// Same but the target OUT-OF-GAS halts (infinite loop). op-geth treats an EVM-level
    /// halt like a revert for deposits (vm error -> err == nil), so it also keeps Mint +
    /// Transfer. (The case the prior Halt-bubbles-to-catch_error handling dropped.)
    #[test]
    fn test_failed_deposit_executed_then_oog_keeps_mint_and_transfer() {
        let target = Address::from([0x43; 20]);
        let amount = 1_000_000_000_000_000u128;
        // JUMPDEST; PUSH1 0; JUMP -> infinite loop -> out of gas
        let (result, to_balance) =
            run_deposit_to_target(target, vec![0x5b, 0x60, 0x00, 0x56], amount, 200_000);
        assert_failed_deposit_mint_and_transfer(&result, to_balance, target, amount);
    }

    /// A CREATE-kind deposit (`to == null`) that fails must transfer BVM_ETH to the SAME
    /// created address as a successful one — the address derived from the caller's nonce
    /// BEFORE it is bumped, matching op-geth's `transferBVMETH` (which runs before the EVM
    /// call increments the nonce). On v38 the transfer is applied pre-frame at the un-bumped
    /// nonce on BOTH the success and the failure path, so the recipients coincide.
    #[test]
    fn test_failed_create_deposit_transfer_recipient_matches_success() {
        use revm::{ExecuteEvm, primitives::TxKind};

        fn transfer_recipient(init_code: std::vec::Vec<u8>) -> Option<Address> {
            let caller = Address::from([0x11; 20]);
            let amount = 1_000_000_000_000_000u128;
            let mut db = InMemoryDB::default();
            db.insert_account_info(
                caller,
                AccountInfo {
                    balance: U256::from(10_000_000_000_000_000u128),
                    ..Default::default()
                },
            );
            let ctx = Context::op()
                .with_db(db)
                .modify_tx_chained(|tx| {
                    tx.base.caller = caller;
                    tx.base.kind = TxKind::Create;
                    tx.base.data = Bytes::from(init_code);
                    tx.base.gas_limit = 200_000;
                    tx.deposit.source_hash = B256::from([1u8; 32]);
                    tx.deposit.eth_value = Some(U256::from(amount));
                    tx.deposit.eth_tx_value = Some(U256::from(amount));
                })
                .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::REGOLITH));
            let mut evm = ctx.build_op();
            evm.replay()
                .unwrap()
                .result
                .into_logs()
                .into_iter()
                .find(|l| l.topics()[0] == BvmEth::TRANSFER_SELECTOR)
                .map(|l| Address::from_word(l.topics()[2]))
        }

        let caller = Address::from([0x11; 20]);
        let to_success = transfer_recipient(vec![0x00]); // STOP -> deploys, succeeds
        let to_revert = transfer_recipient(vec![0x60, 0x00, 0x60, 0x00, 0xfd]); // REVERT

        assert_eq!(
            to_revert,
            Some(caller.create(0)),
            "failed create deposit must transfer to create(caller, pre-bump nonce)"
        );
        assert_eq!(
            to_success, to_revert,
            "create deposit transfer recipient must match between success and failure"
        );
    }

    /// CREATE-kind deposit that OOG-halts inside its init code: composes BOTH behaviors.
    /// The OOG path must keep Mint + Transfer, AND the transfer recipient must be
    /// `create(caller, pre-bump nonce N)`.
    #[test]
    fn test_failed_create_deposit_oog_keeps_mint_and_transfer_correct_recipient() {
        use revm::{ExecuteEvm, primitives::TxKind};

        let caller = Address::from([0x11; 20]);
        let amount = 1_000_000_000_000_000u128;
        // Recipient op-geth's transferBVMETH targets: create(caller, nonce) with the nonce
        // taken BEFORE the EVM call bumps it (here the caller's initial nonce 0).
        let created = caller.create(0);

        let mut db = InMemoryDB::default();
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(10_000_000_000_000_000u128), ..Default::default() },
        );
        let ctx = Context::op()
            .with_db(db)
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.base.kind = TxKind::Create;
                // JUMPDEST; PUSH1 0; JUMP -> infinite loop in init code -> out of gas
                tx.base.data = Bytes::from(vec![0x5b, 0x60, 0x00, 0x56]);
                tx.base.gas_limit = 200_000;
                tx.deposit.source_hash = B256::from([1u8; 32]);
                tx.deposit.eth_value = Some(U256::from(amount));
                tx.deposit.eth_tx_value = Some(U256::from(amount));
            })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::REGOLITH));
        let mut evm = ctx.build_op();
        let out = evm.replay().unwrap();

        // BVM_ETH balance must have moved to the SAME created address as a success.
        let slot = BvmEth::get_balance_slot(created);
        let to_balance = out
            .state
            .get(&BvmEth::ADDRESS)
            .and_then(|acc| acc.storage.get(&slot))
            .map(|s| s.present_value)
            .unwrap_or_default();

        // FailedDeposit halt + Mint + Transfer(caller -> created) in logs AND state.
        assert_failed_deposit_mint_and_transfer(&out.result, to_balance, created, amount);
    }

    /// (#4) Native (MNT) value transfer larger than the caller can afford after the mint.
    /// op-geth gates this with CanTransfer inside innerExecute() -> RevertToSnapshot ->
    /// mint-only (1 BVM_ETH Mint log, transfer + native value rolled back, native mint kept).
    /// Without the pre-call CanTransfer gate revm would route this through the EVM frame and
    /// keep the BVM_ETH transfer, diverging from op-geth.
    #[test]
    fn test_failed_deposit_native_value_too_large_keeps_eth_mint_only() {
        use revm::{ExecuteEvm, bytecode::Bytecode, primitives::TxKind};

        let caller = Address::from([0x11; 20]);
        let target = Address::from([0x42; 20]);
        let native_mint = 1_000_000_000_000_000u128; // 1e15
        let native_value = 2_000_000_000_000_000u128; // 2e15 > mint, caller can't afford
        let eth_amount = 3_000_000_000_000_000u128; // BVM_ETH mint == transfer (would succeed)

        let mut db = InMemoryDB::default();
        // caller starts at ZERO native balance, so its only MNT is the mint (1e15) < value (2e15).
        db.insert_account_info(caller, AccountInfo::default());
        let tcode = Bytecode::new_raw(Bytes::from(std::vec![0x00u8])); // STOP (would succeed if reached)
        let code_hash = tcode.hash_slow();
        db.insert_account_info(
            target,
            AccountInfo { code_hash, code: Some(tcode), ..Default::default() },
        );
        let ctx = Context::op()
            .with_db(db)
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.base.kind = TxKind::Call(target);
                tx.base.value = U256::from(native_value);
                tx.base.gas_limit = 200_000;
                tx.deposit.source_hash = B256::from([1u8; 32]);
                tx.deposit.mint = Some(native_mint);
                tx.deposit.eth_value = Some(U256::from(eth_amount));
                tx.deposit.eth_tx_value = Some(U256::from(eth_amount));
            })
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::REGOLITH));
        let mut evm = ctx.build_op();
        let out = evm.replay().unwrap();

        let logs = out.result.logs();
        let target_eth = out
            .state
            .get(&BvmEth::ADDRESS)
            .and_then(|a| a.storage.get(&BvmEth::get_balance_slot(target)))
            .map(|s| s.present_value)
            .unwrap_or_default();
        let caller_native = out.state.get(&caller).map(|a| a.info.balance).unwrap_or_default();
        let target_native = out.state.get(&target).map(|a| a.info.balance).unwrap_or_default();

        // op-geth expectation: exactly ONE log = BVM_ETH Mint (transfer rolled back).
        assert_eq!(logs.len(), 1, "expected only the BVM_ETH Mint log; got {logs:?}");
        assert_eq!(logs[0].topics()[0], BvmEth::MINT_SELECTOR, "the one log must be Mint");
        // ETH transfer rolled back -> target holds no BVM_ETH.
        assert_eq!(target_eth, U256::ZERO, "BVM_ETH transfer must be rolled back");
        // Native MNT value transfer failed/rolled back -> target got no MNT.
        assert_eq!(target_native, U256::ZERO, "native MNT value must not transfer");
        // Native MNT mint persists -> caller keeps exactly the mint.
        assert_eq!(caller_native, U256::from(native_mint), "native MNT mint must persist");
    }

    /// (#5) CREATE deposit whose init code exceeds EIP-3860 MaxInitCodeSize (49152 bytes),
    /// with BVM_ETH mint+transfer set and the native value affordable (so the ONLY failure
    /// is the init-code-size limit). op-geth treats this as a pre-call Go error
    /// (ErrMaxInitCodeSizeExceeded) -> RevertToSnapshot -> mint-only (1 Mint log, transfer
    /// rolled back). Without the pre-call EIP-3860 gate revm keeps the transfer.
    #[test]
    fn test_failed_create_deposit_initcode_too_large_keeps_eth_mint_only() {
        use revm::{ExecuteEvm, primitives::TxKind};

        let caller = Address::from([0x11; 20]);
        let amount = 1_000_000_000_000_000u128; // BVM_ETH mint == transfer
        let created = caller.create(0);

        let mut db = InMemoryDB::default();
        db.insert_account_info(
            caller,
            AccountInfo {
                balance: U256::from(10_000_000_000_000_000_000u128), // plenty (value affordable)
                ..Default::default()
            },
        );
        // 49153 bytes > MaxInitCodeSize (49152). Zero bytes (cheap calldata gas).
        let init_code = std::vec![0x00u8; 49153];
        let ctx = Context::op()
            .with_db(db)
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.base.kind = TxKind::Create;
                tx.base.data = Bytes::from(init_code);
                tx.base.gas_limit = 5_000_000; // covers intrinsic+calldata, isolates size limit
                tx.deposit.source_hash = B256::from([1u8; 32]);
                tx.deposit.eth_value = Some(U256::from(amount));
                tx.deposit.eth_tx_value = Some(U256::from(amount));
            })
            // ARSIA -> SHANGHAI+ enabled, so EIP-3860 init code size limit is active.
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ARSIA));
        let mut evm = ctx.build_op();
        let out = evm.replay().unwrap();

        let logs = out.result.logs();
        let created_eth = out
            .state
            .get(&BvmEth::ADDRESS)
            .and_then(|a| a.storage.get(&BvmEth::get_balance_slot(created)))
            .map(|s| s.present_value)
            .unwrap_or_default();
        // op-geth expectation: mint-only (1 Mint log, transfer rolled back).
        assert_eq!(logs.len(), 1, "op-geth keeps only Mint; got {} logs", logs.len());
        assert_eq!(logs[0].topics()[0], BvmEth::MINT_SELECTOR, "the one log must be Mint");
        assert_eq!(created_eth, U256::ZERO, "BVM_ETH transfer must be rolled back");
    }
}
