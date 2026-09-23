//! Contains the `[BvmEth]` type and its implementation.
//!
//! BVM_ETH is an ERC20 token that represents ETH on the Mantle network.
//! Since MNT is the native token of Mantle, ETH needs to be wrapped as an ERC20 token (BVM_ETH) for
//! proper handling.
use crate::{
    api::exec::OpContextTr,
    transaction::{
        OpTxTr,
        error::{BvmEthError, OpTransactionError, db_error},
    },
};
use alloy_sol_types::SolValue;
use revm::{
    Database, Journal,
    context::{ContextTr, JournalTr, Transaction},
    primitives::{
        Address, Bytes, FixedBytes, Log, LogData, TxKind, U256, address, fixed_bytes, keccak256,
    },
};
use std::vec;

/// Extension on the concrete [`Journal`] to reset an address (and its currently-loaded
/// storage slots) back to EIP-2929 *cold*.
///
/// Used right after the BVM_ETH mint/transfer in [`BvmEth::process_eth_deposit`] so that
/// subsequent EVM execution observes BVM_ETH as cold — exactly like op-geth, whose
/// `StateDB.SetState()`-based mint never touches the EVM access list. Resetting the warm
/// flags does NOT affect the persisted balance/totalSupply changes (those are journaled
/// separately and stay committed); it only restores the EIP-2929 warm/cold accounting.
pub trait JournalColdExt {
    /// Mark `address` and all of its currently-loaded storage slots cold.
    fn mark_address_cold(&mut self, address: Address);
}

impl<DB: Database> JournalColdExt for Journal<DB> {
    fn mark_address_cold(&mut self, address: Address) {
        if let Some(account) = self.inner.state().get_mut(&address) {
            account.mark_cold();
            for slot in account.storage.values_mut() {
                slot.mark_cold();
            }
        }
    }
}

/// BVM_ETH ERC20 token implementation.
///
/// BVM_ETH is an ERC20 token that represents ETH on the Mantle network.
/// It allows users to hold and transfer ETH as an ERC20 token, since MNT is the native token of
/// Mantle.
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
#[cfg_attr(feature = "serde", derive(serde::Serialize, serde::Deserialize))]
pub struct BvmEth;

impl BvmEth {
    /// The contract address for BVM_ETH ERC20 token.
    /// BVM_ETH is an ERC20 token that represents ETH on the Mantle network.
    /// Since MNT is the native token of Mantle, ETH is wrapped as BVM_ETH (ERC20) for proper
    /// handling.
    pub const ADDRESS: Address = address!("dEAddEaDdeadDEadDEADDEAddEADDEAddead1111");

    /// keccak("Mint(address,uint256)")
    pub(crate) const MINT_SELECTOR: FixedBytes<32> =
        fixed_bytes!("0f6798a560793a54c3bcfe86a93cde1e73087d944c0ea20544137d4121396885");

    /// keccak("Transfer(address,address,uint256)")
    pub(crate) const TRANSFER_SELECTOR: FixedBytes<32> =
        fixed_bytes!("ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef");

    /// Get the storage key for a BVM_ETH balance in the ERC20 contract
    pub fn get_balance_slot(addr: Address) -> U256 {
        keccak256((addr, U256::ZERO).abi_encode()).into()
    }

    /// Get the storage key for the total supply of BVM_ETH ERC20 token
    pub fn get_total_supply_slot() -> U256 {
        U256::from_limbs([2u64, 0, 0, 0])
    }

    /// Mint BVM_ETH ERC20 tokens for a given context and amount.
    /// This is called when ETH is deposited and needs to be represented as BVM_ETH tokens.
    pub fn mint<CTX>(context: &mut CTX, eth_value: U256) -> Result<(), OpTransactionError>
    where
        CTX: OpContextTr,
    {
        let (_, tx, _, journal, _, _) = context.all_mut();

        journal.load_account(Self::ADDRESS).map_err(db_error)?;

        let from = tx.caller();
        Self::mint_inner(journal, tx, from, eth_value)?;

        journal.touch_account(Self::ADDRESS);
        Ok(())
    }

    /// Transfer BVM_ETH ERC20 tokens for a given context and amount.
    /// This is called when ETH needs to be transferred between accounts as BVM_ETH tokens.
    pub fn transfer<CTX>(context: &mut CTX, eth_value: U256) -> Result<(), OpTransactionError>
    where
        CTX: OpContextTr,
    {
        let (_, tx, _, journal, _, _) = context.all_mut();

        journal.load_account(Self::ADDRESS).map_err(db_error)?;

        Self::transfer_inner(journal, tx, eth_value)?;

        journal.touch_account(Self::ADDRESS);
        Ok(())
    }

    /// Process ETH deposit by minting and transferring BVM_ETH tokens.
    /// This handles the conversion of ETH deposits into BVM_ETH ERC20 tokens.
    pub fn process_eth_deposit<CTX>(
        context: &mut CTX,
        mint_only: bool,
    ) -> Result<(), OpTransactionError>
    where
        CTX: OpContextTr,
        <CTX as ContextTr>::Journal: JournalColdExt,
    {
        let (_, tx, _, journal, _, _) = context.all_mut();

        // Treat a zero amount the same as a missing one (None), mirroring op-geth, which gates the
        // BVM_ETH mint/transfer on `!= nil && != 0` (`core/state_transition.go`). Minting or
        // transferring 0 is not a no-op here — it would touch the BVM_ETH storage slots and emit a
        // zero-value Mint/Transfer log — so a stray `Some(0)` would diverge from op-geth. Filtering
        // it out at the execution layer keeps this correct regardless of how the deposit was
        // constructed (engine API, direct tx-env, or the conversion layer).
        let eth_value = tx.eth_value().filter(|v| !v.is_zero());
        let eth_tx_value = tx.eth_tx_value().filter(|v| !v.is_zero());

        // Only load and touch BVM_ETH account when there's actual work to do.
        // This avoids warming the contract address unnecessarily.
        let needs_mint = eth_value.is_some();
        let needs_transfer = !mint_only && eth_tx_value.is_some();

        if !needs_mint && !needs_transfer {
            return Ok(());
        }

        journal.load_account(Self::ADDRESS).map_err(db_error)?;

        // Handle mint if eth_value is present in the transaction
        if let Some(eth_value) = eth_value {
            let from = tx.caller();
            Self::mint_inner(journal, tx, from, eth_value)?;
        }

        // Handle transfer if eth_tx_value is present in the transaction
        if let Some(eth_tx_value) = eth_tx_value {
            if !mint_only {
                Self::transfer_inner(journal, tx, eth_tx_value)?;
            }
        }

        journal.touch_account(Self::ADDRESS);

        // Reset BVM_ETH (account + the storage slots just touched by mint/transfer) back to
        // cold so the subsequent EVM execution observes it cold, matching op-geth. This is the
        // single source of warm/cold parity with op-geth — no static gas compensation anywhere.
        journal.mark_address_cold(Self::ADDRESS);
        Ok(())
    }

    /// Add the value of ETH to the total supply of BVM_ETH ERC20 tokens.
    /// This increases the total supply when new BVM_ETH tokens are minted.
    fn add_total_supply<J>(journal: &mut J, eth_value: U256) -> Result<(), OpTransactionError>
    where
        J: JournalTr,
    {
        let total_supply_slot = Self::get_total_supply_slot();
        let value_supply = journal.sload(Self::ADDRESS, total_supply_slot).map_err(db_error)?.data;

        let new_value_supply = value_supply.saturating_add(eth_value);

        journal.sstore(Self::ADDRESS, total_supply_slot, new_value_supply).map_err(db_error)?;

        Ok(())
    }

    /// Generate a mint event for BVM_ETH ERC20 tokens.
    /// This emits an ERC20 Mint event when new BVM_ETH tokens are created.
    fn generate_mint_event(to: Address, eth_value: U256) -> Log {
        let topics = vec![Self::MINT_SELECTOR, to.into_word()];
        let data = Bytes::from(eth_value.to_be_bytes_vec());
        Log {
            address: Self::ADDRESS,
            data: LogData::new(topics, data).expect("LogData should have <=4 topics"),
        }
    }

    /// Generate a transfer event for BVM_ETH ERC20 tokens.
    /// This emits an ERC20 Transfer event when BVM_ETH tokens are transferred between accounts.
    fn generate_transfer_event(from: Address, to: Address, eth_value: U256) -> Log {
        let topics = vec![Self::TRANSFER_SELECTOR, from.into_word(), to.into_word()];
        let data = Bytes::from(eth_value.to_be_bytes_vec());
        Log {
            address: Self::ADDRESS,
            data: LogData::new(topics, data).expect("LogData should have <=4 topics"),
        }
    }

    /// Update account balance for BVM_ETH ERC20 tokens.
    /// This updates the balance of a specific account in the BVM_ETH contract.
    fn update_balance<J>(
        journal: &mut J,
        account: Address,
        amount: U256,
    ) -> Result<(), OpTransactionError>
    where
        J: JournalTr,
    {
        let slot = Self::get_balance_slot(account);
        journal.sstore(Self::ADDRESS, slot, amount).map_err(db_error)?;

        Ok(())
    }

    /// Get account balance for BVM_ETH ERC20 tokens.
    /// This retrieves the balance of a specific account from the BVM_ETH contract.
    fn get_balance<J>(journal: &mut J, account: Address) -> Result<U256, OpTransactionError>
    where
        J: JournalTr,
    {
        let slot = Self::get_balance_slot(account);
        let balance = journal.sload(Self::ADDRESS, slot).map_err(db_error)?.data;

        Ok(balance)
    }

    /// Inner implementation of mint for BVM_ETH ERC20 tokens.
    /// This handles the actual minting logic including balance updates, total supply increase, and
    /// event emission.
    fn mint_inner<J, T>(
        journal: &mut J,
        _tx: &T,
        to: Address,
        eth_value: U256,
    ) -> Result<(), OpTransactionError>
    where
        J: JournalTr,
        T: Transaction,
    {
        let current_balance = Self::get_balance(journal, to)?;
        let new_balance = current_balance.saturating_add(eth_value);

        Self::update_balance(journal, to, new_balance)?;

        Self::add_total_supply(journal, eth_value)?;

        let mint_log = Self::generate_mint_event(to, eth_value);
        journal.log(mint_log);

        Ok(())
    }

    /// Inner implementation of transfer for BVM_ETH ERC20 tokens.
    /// This handles the actual transfer logic including balance updates and event emission.
    fn transfer_inner<J, T>(
        journal: &mut J,
        tx: &T,
        eth_value: U256,
    ) -> Result<(), OpTransactionError>
    where
        J: JournalTr,
        T: Transaction,
    {
        let from = tx.caller();
        let to = match tx.kind() {
            TxKind::Call(address) => address,
            TxKind::Create => {
                let nonce = journal.load_account(from).map_err(db_error)?.data.info.nonce;
                from.create(nonce)
            }
        };

        if from == to {
            return Ok(());
        }

        let from_amount = Self::get_balance(journal, from)?;
        let to_amount = Self::get_balance(journal, to)?;

        if from_amount < eth_value {
            return Err(OpTransactionError::BvmEth(BvmEthError::InsufficientFunds));
        }

        let new_from_amount = from_amount.saturating_sub(eth_value);
        let new_to_amount = to_amount.saturating_add(eth_value);

        Self::update_balance(journal, from, new_from_amount)?;
        Self::update_balance(journal, to, new_to_amount)?;

        let transfer_log = Self::generate_transfer_event(from, to, eth_value);
        journal.log(transfer_log);

        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{
        L1BlockInfo, OpBuilder, OpHaltReason, OpSpecId, OpTransaction, api::default_ctx::DefaultOp,
        handler::OpHandler, transaction::deposit::DepositTransactionParts,
    };
    use alloy_sol_types::{SolEvent, sol};
    use revm::{
        context::{BlockEnv, Context, TxEnv},
        context_interface::result::{EVMError, ExecutionResult},
        database::{Cache, InMemoryDB},
        handler::{EthFrame, EvmTr, Handler},
        interpreter::interpreter::EthInterpreter,
        primitives::{Address, B256, Bytes, U256, hex},
        state::{AccountInfo, AccountStatus, Bytecode},
    };
    use rstest::rstest;
    use serde::{Deserialize, Serialize};
    use std::{
        collections::HashSet,
        fs,
        path::{Path, PathBuf},
        str::FromStr,
    };
    use tempfile::tempdir;

    sol! {
        interface ERC20Events {
            event Transfer(address indexed from, address indexed to, uint256 value);
            event Mint(address indexed to, uint256 value);
        }
    }

    #[test]
    fn test_selector_from_event() {
        let selector = ERC20Events::Transfer::SIGNATURE_HASH;
        assert_eq!(selector, BvmEth::TRANSFER_SELECTOR);

        let selector = ERC20Events::Mint::SIGNATURE_HASH;
        assert_eq!(selector, BvmEth::MINT_SELECTOR);
    }

    #[test]
    fn bvm_eth_balance_slot_test() {
        let addr = address!("667120e768cf024c2245dd6d9feece4b437c3518");
        let slot = BvmEth::get_balance_slot(addr);
        let expected =
            U256::from_str("0xfe0b4acb70bd1e455f00a22786aa76d07a905b7f77d9cbab254e4dddcbb681c9")
                .unwrap();
        assert_eq!(slot, expected);
    }

    #[test]
    fn bvm_eth_total_supply_slot_test() {
        assert_eq!(
            BvmEth::get_total_supply_slot(),
            U256::from_str("0x0000000000000000000000000000000000000000000000000000000000000002")
                .unwrap()
        );
    }

    #[test]
    fn test_process_eth_deposit_no_eth_value_no_eth_tx_value() {
        // When both eth_value and eth_tx_value are None, should return early
        // and not load BVM_ETH account
        let caller = address!("1234567890123456789012345678901234567890");
        let to = address!("abcdefabcdefabcdefabcdefabcdefabcdefabcd");

        let mut ctx = Context::op()
            .with_db(InMemoryDB::default())
            .with_chain(L1BlockInfo::default())
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ISTHMUS))
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.base.kind = revm::primitives::TxKind::Call(to);
                tx.base.gas_limit = 1_000_000;
                tx.deposit.eth_value = None;
                tx.deposit.eth_tx_value = None;
            });

        let result = BvmEth::process_eth_deposit(&mut ctx, false);
        assert!(result.is_ok());

        // BVM_ETH account should NOT be loaded
        assert!(
            !ctx.journaled_state.inner.state.contains_key(&BvmEth::ADDRESS),
            "BVM_ETH should not be loaded when eth_value and eth_tx_value are both None"
        );

        // No logs should be emitted
        assert!(ctx.journaled_state.inner.logs.is_empty());
    }

    #[test]
    fn test_process_eth_deposit_only_eth_value() {
        // When only eth_value is present, should mint BVM_ETH
        let eth_value = U256::from(1_000_000_000_000_000_000u128); // 1 ETH
        let caller = address!("1234567890123456789012345678901234567890");
        let to = address!("abcdefabcdefabcdefabcdefabcdefabcdefabcd");

        let mut ctx = Context::op()
            .with_db(InMemoryDB::default())
            .with_chain(L1BlockInfo::default())
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ISTHMUS))
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.base.kind = revm::primitives::TxKind::Call(to);
                tx.base.gas_limit = 1_000_000;
                tx.deposit.eth_value = Some(eth_value);
                tx.deposit.eth_tx_value = None;
            });

        let result = BvmEth::process_eth_deposit(&mut ctx, false);
        assert!(result.is_ok());

        // BVM_ETH account should be loaded
        assert!(
            ctx.journaled_state.inner.state.contains_key(&BvmEth::ADDRESS),
            "BVM_ETH should be loaded when eth_value is present"
        );

        // Should have Mint event
        let logs = &ctx.journaled_state.inner.logs;
        assert_eq!(logs.len(), 1, "Should have exactly 1 log (Mint event)");
        assert_eq!(logs[0].address, BvmEth::ADDRESS);
        assert_eq!(logs[0].topics()[0], BvmEth::MINT_SELECTOR);
    }

    #[test]
    fn test_process_eth_deposit_only_eth_tx_value() {
        // When only eth_tx_value is present, should transfer BVM_ETH
        let eth_tx_value = U256::from(500_000_000_000_000_000u128); // 0.5 ETH
        let caller = address!("1234567890123456789012345678901234567890");
        let to = address!("abcdefabcdefabcdefabcdefabcdefabcdefabcd");

        let mut ctx = Context::op()
            .with_db(InMemoryDB::default())
            .with_chain(L1BlockInfo::default())
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ISTHMUS))
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.base.kind = revm::primitives::TxKind::Call(to);
                tx.base.gas_limit = 1_000_000;
                tx.deposit.eth_value = None;
                tx.deposit.eth_tx_value = Some(eth_tx_value);
            });

        // First, give the caller some BVM_ETH balance for transfer
        use revm::context_interface::JournalTr;
        let balance_slot = BvmEth::get_balance_slot(caller);
        ctx.journaled_state.load_account(BvmEth::ADDRESS).expect("load account");
        ctx.journaled_state
            .sstore(BvmEth::ADDRESS, balance_slot, U256::from(eth_tx_value))
            .expect("sstore");
        // Clear logs from setup
        ctx.journaled_state.inner.logs.clear();

        let result = BvmEth::process_eth_deposit(&mut ctx, false);
        assert!(result.is_ok());

        // BVM_ETH account should be loaded
        assert!(
            ctx.journaled_state.inner.state.contains_key(&BvmEth::ADDRESS),
            "BVM_ETH should be loaded when eth_tx_value is present"
        );

        // Should have Transfer event
        let logs = &ctx.journaled_state.inner.logs;
        assert_eq!(logs.len(), 1, "Should have exactly 1 log (Transfer event)");
        assert_eq!(logs[0].address, BvmEth::ADDRESS);
        assert_eq!(logs[0].topics()[0], BvmEth::TRANSFER_SELECTOR);
    }

    #[test]
    fn test_process_eth_deposit_both_values() {
        // When both eth_value and eth_tx_value are present, should mint and transfer
        let eth_value = U256::from(1_000_000_000_000_000_000u128); // 1 ETH
        let eth_tx_value = U256::from(500_000_000_000_000_000u128); // 0.5 ETH
        let caller = address!("1234567890123456789012345678901234567890");
        let to = address!("abcdefabcdefabcdefabcdefabcdefabcdefabcd");

        let mut ctx = Context::op()
            .with_db(InMemoryDB::default())
            .with_chain(L1BlockInfo::default())
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ISTHMUS))
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.base.kind = revm::primitives::TxKind::Call(to);
                tx.base.gas_limit = 1_000_000;
                tx.deposit.eth_value = Some(eth_value);
                tx.deposit.eth_tx_value = Some(eth_tx_value);
            });

        let result = BvmEth::process_eth_deposit(&mut ctx, false);
        assert!(result.is_ok());

        // BVM_ETH account should be loaded
        assert!(
            ctx.journaled_state.inner.state.contains_key(&BvmEth::ADDRESS),
            "BVM_ETH should be loaded when eth_value or eth_tx_value is present"
        );

        // Should have Mint and Transfer events
        let logs = &ctx.journaled_state.inner.logs;
        assert_eq!(logs.len(), 2, "Should have 2 logs (Mint and Transfer events)");

        // First log should be Mint
        assert_eq!(logs[0].address, BvmEth::ADDRESS);
        assert_eq!(logs[0].topics()[0], BvmEth::MINT_SELECTOR);

        // Second log should be Transfer
        assert_eq!(logs[1].address, BvmEth::ADDRESS);
        assert_eq!(logs[1].topics()[0], BvmEth::TRANSFER_SELECTOR);
    }

    #[test]
    fn test_process_eth_deposit_mint_only_flag() {
        // When mint_only is true and eth_tx_value is present, should only mint
        let eth_value = U256::from(1_000_000_000_000_000_000u128);
        let eth_tx_value = U256::from(500_000_000_000_000_000u128);
        let caller = address!("1234567890123456789012345678901234567890");
        let to = address!("abcdefabcdefabcdefabcdefabcdefabcdefabcd");

        let mut ctx = Context::op()
            .with_db(InMemoryDB::default())
            .with_chain(L1BlockInfo::default())
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ISTHMUS))
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.base.kind = revm::primitives::TxKind::Call(to);
                tx.base.gas_limit = 1_000_000;
                tx.deposit.eth_value = Some(eth_value);
                tx.deposit.eth_tx_value = Some(eth_tx_value);
            });

        let result = BvmEth::process_eth_deposit(&mut ctx, true); // mint_only = true
        assert!(result.is_ok());

        // Should only have Mint event, no Transfer
        let logs = &ctx.journaled_state.inner.logs;
        assert_eq!(logs.len(), 1, "Should have exactly 1 log (Mint event only)");
        assert_eq!(logs[0].topics()[0], BvmEth::MINT_SELECTOR);
    }

    #[test]
    fn test_process_eth_deposit_mint_only_no_eth_value() {
        // When mint_only is true but only eth_tx_value is present,
        // should return early because needs_transfer is false when mint_only=true
        let eth_tx_value = U256::from(500_000_000_000_000_000u128);
        let caller = address!("1234567890123456789012345678901234567890");
        let to = address!("abcdefabcdefabcdefabcdefabcdefabcdefabcd");

        let mut ctx = Context::op()
            .with_db(InMemoryDB::default())
            .with_chain(L1BlockInfo::default())
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ISTHMUS))
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.base.kind = revm::primitives::TxKind::Call(to);
                tx.base.gas_limit = 1_000_000;
                tx.deposit.eth_value = None;
                tx.deposit.eth_tx_value = Some(eth_tx_value);
            });

        let result = BvmEth::process_eth_deposit(&mut ctx, true); // mint_only = true
        assert!(result.is_ok());

        // BVM_ETH account should NOT be loaded because:
        // - needs_mint = false (eth_value is None)
        // - needs_transfer = false (mint_only is true)
        assert!(
            !ctx.journaled_state.inner.state.contains_key(&BvmEth::ADDRESS),
            "BVM_ETH should not be loaded when mint_only=true and eth_value is None"
        );

        assert!(ctx.journaled_state.inner.logs.is_empty());
    }

    /// Test case data structure
    #[test]
    fn process_eth_deposit_leaves_bvm_eth_cold() {
        // After minting/transferring BVM_ETH, the account AND its touched storage slots must
        // be COLD for the subsequent EVM execution — matching op-geth, whose SetState-based
        // mint never warms the EVM access list. This is what makes the static gas
        // compensation unnecessary.
        let caller = Address::from([0x11; 20]);
        let eth_value = U256::from(1_000_000_000_000_000u128); // 0.001 ETH

        let mut ctx = Context::op().with_db(InMemoryDB::default()).modify_tx_chained(|tx| {
            tx.base.caller = caller;
            tx.base.kind = TxKind::Call(caller); // to = EOA, deliberately NOT BVM_ETH
            tx.deposit.source_hash = B256::from([1u8; 32]);
            tx.deposit.eth_value = Some(eth_value);
            tx.deposit.eth_tx_value = Some(eth_value);
        });

        BvmEth::process_eth_deposit(&mut ctx, false).expect("deposit processing should succeed");

        // First post-mint access of the BVM_ETH account must report cold.
        let acc = ctx.journaled_state.load_account(BvmEth::ADDRESS).expect("load BVM_ETH account");
        assert!(acc.is_cold, "BVM_ETH account must be cold after process_eth_deposit");

        // The caller's BVM_ETH balance slot (touched by mint + transfer) must also be cold.
        let slot = BvmEth::get_balance_slot(caller);
        let loaded =
            ctx.journaled_state.sload(BvmEth::ADDRESS, slot).expect("sload BVM_ETH balance slot");
        assert!(loaded.is_cold, "BVM_ETH balance slot must be cold after process_eth_deposit");
    }

    #[test]
    fn deposit_to_eoa_with_calldata_no_compensation_matches_geth() {
        // Deterministic replay of hoodi-qa2 block 59294 user deposit (the fork tx):
        //   to = caller (EOA, no code), value = 0, input = 0xdeadbeef01020304 (8 nonzero bytes),
        //   ethValue = ethTxValue = 0.001 ETH.
        // op-geth gasUsed = 21320 (= 21000 + EIP-7623 floor for 8 calldata tokens, 32*10).
        // The removed BVM_ETH_MINT_GAS_COMPENSATION (4500) previously inflated reth to 25628,
        // diverging from op-geth and forking the chain at this block.
        let caller = Address::from([0x74; 20]);
        let eth_value = U256::from(1_000_000_000_000_000u128); // 0.001 ETH

        let op_tx = OpTransaction {
            base: TxEnv {
                caller,
                kind: TxKind::Call(caller),
                gas_limit: 100_000,
                gas_price: 0,
                value: U256::ZERO,
                data: Bytes::from(hex::decode("deadbeef01020304").unwrap()),
                ..Default::default()
            },
            enveloped_tx: None,
            deposit: DepositTransactionParts {
                source_hash: B256::from([9u8; 32]),
                mint: Some(0),
                is_system_transaction: false,
                eth_value: Some(eth_value),
                eth_tx_value: Some(eth_value),
            },
        };

        let block_env = BlockEnv {
            number: U256::from(59294u64),
            gas_limit: 30_000_000,
            basefee: 1_000_000_000,
            ..Default::default()
        };
        let l1_block_info = L1BlockInfo {
            l2_block: Some(U256::from(59294u64)),
            token_ratio: U256::from(3040u64),
            ..Default::default()
        };

        let ctx = Context::op()
            .with_db(InMemoryDB::default())
            .with_chain(l1_block_info)
            .with_block(block_env)
            // Block 59294 — and every deployed Mantle deposit — executes under ARSIA
            // (eth_spec OSAKA). ISTHMUS is a different gas regime and leaves the
            // is_arsia branches uncovered, so this replay must run under ARSIA.
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ARSIA))
            .with_tx(op_tx);
        let mut evm = ctx.build_op();
        let mut handler = OpHandler::<
            _,
            EVMError<_, crate::transaction::error::OpTransactionError>,
            EthFrame<EthInterpreter>,
        >::new();
        let result = handler.run(&mut evm).expect("deposit must execute");

        let gas = result.tx_gas_used();
        assert_ne!(gas, 25_628, "spurious BVM_ETH_MINT_GAS_COMPENSATION (+4500) must be gone");
        assert_eq!(
            gas, 21_320,
            "deposit gasUsed must match op-geth EIP-7623 floor (hoodi-qa2 block 59294)"
        );
    }

    #[test]
    fn process_eth_deposit_all_slots_cold_after_mint_and_transfer() {
        // Verify ALL storage slots touched by mint + transfer are cold:
        // balance(caller), balance(to), totalSupply.
        // This ensures mark_address_cold covers every slot, not just one.
        let caller = Address::from([0x11; 20]);
        let recipient = Address::from([0x22; 20]); // different from caller
        let eth_value = U256::from(1_000_000_000_000_000u128);

        let mut ctx = Context::op().with_db(InMemoryDB::default()).modify_tx_chained(|tx| {
            tx.base.caller = caller;
            tx.base.kind = TxKind::Call(recipient);
            tx.deposit.source_hash = B256::from([1u8; 32]);
            tx.deposit.eth_value = Some(eth_value);
            tx.deposit.eth_tx_value = Some(eth_value);
        });

        BvmEth::process_eth_deposit(&mut ctx, false).expect("deposit should succeed");

        // Account must be cold
        let acc = ctx.journaled_state.load_account(BvmEth::ADDRESS).expect("load BVM_ETH");
        assert!(acc.is_cold, "BVM_ETH account must be cold");

        // balance(caller) — warmed by mint_inner + transfer_inner
        let slot_caller = BvmEth::get_balance_slot(caller);
        let loaded =
            ctx.journaled_state.sload(BvmEth::ADDRESS, slot_caller).expect("sload balance(caller)");
        assert!(loaded.is_cold, "balance(caller) must be cold");

        // balance(recipient) — warmed by transfer_inner
        let slot_recipient = BvmEth::get_balance_slot(recipient);
        let loaded = ctx
            .journaled_state
            .sload(BvmEth::ADDRESS, slot_recipient)
            .expect("sload balance(recipient)");
        assert!(loaded.is_cold, "balance(recipient) must be cold");

        // totalSupply — warmed by add_total_supply in mint_inner
        let slot_supply = BvmEth::get_total_supply_slot();
        let loaded =
            ctx.journaled_state.sload(BvmEth::ADDRESS, slot_supply).expect("sload totalSupply");
        assert!(loaded.is_cold, "totalSupply must be cold");
    }

    #[test]
    fn process_eth_deposit_mint_only_no_transfer() {
        // When eth_tx_value is None, only mint_inner runs (no transfer_inner).
        // Slots warmed: balance(caller) + totalSupply. Both must be cold after.
        let caller = Address::from([0x33; 20]);
        let recipient = Address::from([0x44; 20]);
        let eth_value = U256::from(2_000_000_000_000_000u128);

        let mut ctx = Context::op().with_db(InMemoryDB::default()).modify_tx_chained(|tx| {
            tx.base.caller = caller;
            tx.base.kind = TxKind::Call(recipient);
            tx.deposit.source_hash = B256::from([2u8; 32]);
            tx.deposit.eth_value = Some(eth_value);
            tx.deposit.eth_tx_value = None; // no transfer
        });

        BvmEth::process_eth_deposit(&mut ctx, false).expect("deposit should succeed");

        let acc = ctx.journaled_state.load_account(BvmEth::ADDRESS).expect("load BVM_ETH");
        assert!(acc.is_cold, "BVM_ETH account must be cold (mint-only)");

        let slot_caller = BvmEth::get_balance_slot(caller);
        let loaded =
            ctx.journaled_state.sload(BvmEth::ADDRESS, slot_caller).expect("sload balance(caller)");
        assert!(loaded.is_cold, "balance(caller) must be cold (mint-only)");

        let slot_supply = BvmEth::get_total_supply_slot();
        let loaded =
            ctx.journaled_state.sload(BvmEth::ADDRESS, slot_supply).expect("sload totalSupply");
        assert!(loaded.is_cold, "totalSupply must be cold (mint-only)");

        // balance(recipient) should NOT have been loaded at all (no transfer)
        // Accessing it now should also be cold (never touched by process_eth_deposit)
        let slot_recipient = BvmEth::get_balance_slot(recipient);
        let loaded = ctx
            .journaled_state
            .sload(BvmEth::ADDRESS, slot_recipient)
            .expect("sload balance(recipient)");
        assert!(loaded.is_cold, "balance(recipient) must be cold (never touched in mint-only)");
    }

    #[test]
    fn process_eth_deposit_from_eq_to_transfer_skipped() {
        // When from == to, transfer_inner returns early (no-op).
        // Only mint_inner runs. Verify cold state is still correct.
        let caller = Address::from([0x55; 20]);
        let eth_value = U256::from(500_000_000_000_000u128);

        let mut ctx = Context::op().with_db(InMemoryDB::default()).modify_tx_chained(|tx| {
            tx.base.caller = caller;
            tx.base.kind = TxKind::Call(caller); // to == from → transfer_inner skips
            tx.deposit.source_hash = B256::from([3u8; 32]);
            tx.deposit.eth_value = Some(eth_value);
            tx.deposit.eth_tx_value = Some(eth_value);
        });

        BvmEth::process_eth_deposit(&mut ctx, false).expect("deposit should succeed");

        let acc = ctx.journaled_state.load_account(BvmEth::ADDRESS).expect("load BVM_ETH");
        assert!(acc.is_cold, "BVM_ETH account must be cold (from==to)");

        // balance(caller) — warmed by mint_inner only (transfer skipped)
        let slot = BvmEth::get_balance_slot(caller);
        let loaded =
            ctx.journaled_state.sload(BvmEth::ADDRESS, slot).expect("sload balance(caller)");
        assert!(loaded.is_cold, "balance(caller) must be cold (from==to)");

        let slot_supply = BvmEth::get_total_supply_slot();
        let loaded =
            ctx.journaled_state.sload(BvmEth::ADDRESS, slot_supply).expect("sload totalSupply");
        assert!(loaded.is_cold, "totalSupply must be cold (from==to)");
    }

    #[test]
    fn process_eth_deposit_no_eth_value_no_warming() {
        // When neither eth_value nor eth_tx_value is set, process_eth_deposit
        // returns early without loading BVM_ETH at all. No warming occurs.
        let caller = Address::from([0x66; 20]);

        let mut ctx = Context::op().with_db(InMemoryDB::default()).modify_tx_chained(|tx| {
            tx.base.caller = caller;
            tx.base.kind = TxKind::Call(caller);
            tx.deposit.source_hash = B256::from([4u8; 32]);
            tx.deposit.eth_value = None;
            tx.deposit.eth_tx_value = None;
        });

        BvmEth::process_eth_deposit(&mut ctx, false).expect("deposit should succeed");

        // BVM_ETH account was never loaded, first access must be cold
        let acc = ctx.journaled_state.load_account(BvmEth::ADDRESS).expect("load BVM_ETH");
        assert!(acc.is_cold, "BVM_ETH must be cold when no eth_value/eth_tx_value");
    }

    #[test]
    fn process_eth_deposit_zero_eth_value_treated_as_none() {
        // A zero amount must behave exactly like a missing one (None): op-geth gates the
        // BVM_ETH mint/transfer on `!= nil && != 0`. With `Some(0)`, process_eth_deposit must
        // not load/warm BVM_ETH, must not touch its storage, and must not emit a zero-value
        // Mint/Transfer log. Without the zero-filter this would warm BVM_ETH and emit a
        // zero-value log, diverging from op-geth.
        let caller = Address::from([0x66; 20]);

        let mut ctx = Context::op().with_db(InMemoryDB::default()).modify_tx_chained(|tx| {
            tx.base.caller = caller;
            tx.base.kind = TxKind::Call(caller);
            tx.deposit.source_hash = B256::from([6u8; 32]);
            tx.deposit.eth_value = Some(U256::from(0u128));
            tx.deposit.eth_tx_value = Some(U256::from(0u128));
        });

        BvmEth::process_eth_deposit(&mut ctx, false).expect("deposit should succeed");

        // BVM_ETH account was never loaded (early return), so first access must be cold —
        // same as the no-value case.
        let acc = ctx.journaled_state.load_account(BvmEth::ADDRESS).expect("load BVM_ETH");
        assert!(
            acc.is_cold,
            "BVM_ETH must stay cold when eth_value/eth_tx_value are Some(0) (treated as None)"
        );
    }

    #[test]
    fn process_eth_deposit_state_changes_persist_after_cold_reset() {
        // mark_address_cold must NOT undo the balance/totalSupply state changes.
        // Only the warm/cold flags should be reset.
        let caller = Address::from([0x77; 20]);
        let recipient = Address::from([0x88; 20]);
        let eth_value = U256::from(3_000_000_000_000_000u128); // 0.003 ETH

        let mut ctx = Context::op().with_db(InMemoryDB::default()).modify_tx_chained(|tx| {
            tx.base.caller = caller;
            tx.base.kind = TxKind::Call(recipient);
            tx.deposit.source_hash = B256::from([5u8; 32]);
            tx.deposit.eth_value = Some(eth_value);
            tx.deposit.eth_tx_value = Some(eth_value);
        });

        BvmEth::process_eth_deposit(&mut ctx, false).expect("deposit should succeed");

        // balance(caller) should have been minted (mint_inner) then debited (transfer_inner)
        // mint: 0 + eth_value = eth_value, transfer: eth_value - eth_value = 0
        let slot_caller = BvmEth::get_balance_slot(caller);
        let loaded =
            ctx.journaled_state.sload(BvmEth::ADDRESS, slot_caller).expect("sload balance(caller)");
        assert_eq!(
            loaded.data,
            U256::ZERO,
            "balance(caller) should be 0 after mint+transfer of same amount"
        );

        // balance(recipient) should have received the transfer
        let slot_recipient = BvmEth::get_balance_slot(recipient);
        let loaded = ctx
            .journaled_state
            .sload(BvmEth::ADDRESS, slot_recipient)
            .expect("sload balance(recipient)");
        assert_eq!(
            loaded.data,
            U256::from(eth_value),
            "balance(recipient) should equal eth_value after transfer"
        );

        // totalSupply should have increased by eth_value (from mint)
        let slot_supply = BvmEth::get_total_supply_slot();
        let loaded =
            ctx.journaled_state.sload(BvmEth::ADDRESS, slot_supply).expect("sload totalSupply");
        assert_eq!(
            loaded.data,
            U256::from(eth_value),
            "totalSupply should equal eth_value after mint"
        );
    }

    #[derive(Debug, Serialize, Deserialize)]
    struct BvmEthDepositTestCase {
        block_number: u64,
        tx_hash: String,
        from: String,
        to: String,
        source_hash: String,
        eth_value: String,
        eth_tx_value: String,
        gas_limit: u64,
        tx_input: String,
        expected_gas_used: u64,
        expected_logs: Vec<ExpectedLog>,
        /// Whether the deposit is expected to succeed (status=1). Defaults to `true` so the
        /// existing successful-deposit fixtures need no change. A FAILED Mantle deposit
        /// (status=0) still persists its BVM_ETH mint (and any successful transfer) logs
        /// into the receipt — matching op-geth — surfaced via `ExecutionResult::Halt`'s logs.
        #[serde(default = "default_expected_status")]
        expected_status: bool,
    }

    fn default_expected_status() -> bool {
        true
    }

    #[derive(Debug, Serialize, Deserialize)]
    struct ExpectedLog {
        address: String,
        topics: Vec<String>,
        data: String,
    }

    /// Load cache_db data from exported JSON file and build InMemoryDB
    fn load_cache_db_from_file(fixture_dir: &Path, block_number: u64) -> InMemoryDB {
        let cache_db_file = fixture_dir.join(format!("block_{}.json", block_number));

        let json_content = fs::read_to_string(&cache_db_file).unwrap_or_else(|e| {
            panic!("Failed to read cache_db file from {}: {}", cache_db_file.display(), e)
        });

        let cache: Cache = serde_json::from_str(&json_content)
            .unwrap_or_else(|e| panic!("Failed to parse cache_db JSON: {}", e));

        let mut db = InMemoryDB::default();

        // Load contracts into cache (they are indexed by code_hash)
        // Since InMemoryDB is CacheDB<EmptyDB>, we can access cache directly
        for (code_hash, bytecode) in &cache.contracts {
            db.cache.contracts.insert(*code_hash, bytecode.clone());
        }

        // Load accounts from cache
        for (address, db_account) in &cache.accounts {
            let account_info = db_account.info.clone();
            db.insert_account_info(*address, account_info);

            // Load storage
            let account = db.load_account(*address).unwrap();
            for (key, value) in &db_account.storage {
                account.storage.insert(*key, *value);
            }
        }

        db
    }

    /// Load test case data from JSON file
    fn load_test_case(fixture_dir: &Path, test_file: &str) -> BvmEthDepositTestCase {
        let test_case_path = fixture_dir.join(test_file);
        let json_content = fs::read_to_string(&test_case_path).unwrap_or_else(|e| {
            panic!("Failed to read test case from {}: {}", test_case_path.display(), e)
        });

        serde_json::from_str(&json_content)
            .unwrap_or_else(|e| panic!("Failed to parse test case JSON: {}", e))
    }

    /// Run BVM_ETH deposit transaction test
    fn run_bvm_eth_deposit_test(fixture_dir: &Path, test_case: BvmEthDepositTestCase) {
        let from = Address::from_str(&test_case.from).unwrap();
        let to = Address::from_str(&test_case.to).unwrap();
        let source_hash = B256::from_str(&test_case.source_hash).unwrap();
        let eth_value =
            U256::from_str_radix(test_case.eth_value.trim_start_matches("0x"), 16).unwrap();
        let eth_tx_value =
            U256::from_str_radix(test_case.eth_tx_value.trim_start_matches("0x"), 16).unwrap();
        let tx_input = hex::decode(test_case.tx_input.trim_start_matches("0x")).unwrap();

        // Load cache_db data
        let db = load_cache_db_from_file(fixture_dir, test_case.block_number);

        // Create block_env
        let block_env = BlockEnv {
            number: U256::from(test_case.block_number),
            beneficiary: Address::from_str("0x4200000000000000000000000000000000000011").unwrap(),
            timestamp: U256::from(1735128000u64),
            gas_limit: 30_000_000u64,
            basefee: 1_000_000_000u64,
            ..Default::default()
        };

        // Create L1BlockInfo
        let l1_block_info = L1BlockInfo {
            l2_block: Some(U256::from(test_case.block_number)),
            token_ratio: U256::from(3040),
            l1_base_fee: U256::from(1_000_000_000),
            l1_fee_overhead: Some(U256::from(188)),
            l1_base_fee_scalar: U256::from(10000),
            ..Default::default()
        };

        // Build deposit transaction parts
        let deposit = DepositTransactionParts {
            source_hash,
            mint: Some(0),
            is_system_transaction: false,
            eth_value: Some(eth_value),
            eth_tx_value: Some(eth_tx_value),
        };

        // Build complete OpTransaction
        let op_tx = OpTransaction {
            base: TxEnv {
                caller: from,
                kind: revm::primitives::TxKind::Call(to),
                gas_limit: test_case.gas_limit,
                gas_price: 0,
                value: U256::ZERO,
                data: Bytes::from(tx_input),
                ..Default::default()
            },
            enveloped_tx: None,
            deposit,
        };

        let ctx = Context::op()
            .with_db(db)
            .with_chain(l1_block_info)
            .with_block(block_env)
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ISTHMUS))
            .with_tx(op_tx);

        let mut evm = ctx.build_op();
        let mut handler = OpHandler::<
            _,
            EVMError<_, crate::transaction::error::OpTransactionError>,
            EthFrame<EthInterpreter>,
        >::new();

        // handler.run() will internally call validate_against_state_and_deduct_caller
        // so we should not call it manually here to avoid duplicate process_eth_deposit
        let result = handler.run(&mut evm).unwrap();

        // Verify the result variant matches the expected status. A failed deposit halts
        // (status=0) but still carries its BVM_ETH logs; a successful deposit returns
        // Success.
        assert_eq!(
            result.is_success(),
            test_case.expected_status,
            "status mismatch: expected_status={}, got result={:?}",
            test_case.expected_status,
            result
        );
        match (&result, test_case.expected_status) {
            (ExecutionResult::Success { .. }, true) => {}
            (ExecutionResult::Halt { .. }, false) => {}
            (other, exp) => {
                panic!("unexpected result variant for expected_status={}: {:?}", exp, other)
            }
        }

        // 1. Verify gas used (most important check - do this first)
        let actual_gas_used = result.tx_gas_used();
        assert_eq!(
            actual_gas_used, test_case.expected_gas_used,
            "Gas used mismatch! Expected: {}, Actual: {}",
            test_case.expected_gas_used, actual_gas_used
        );

        // 2. Verify logs. ExecutionResult::logs() returns the persisted logs for a failed
        // deposit's Halt too, so this covers both status=1 and status=0 fixtures.
        let logs = result.logs();
        verify_logs(logs, &test_case.expected_logs);
    }

    /// Verify that expected logs exist in actual logs
    fn verify_logs(logs: &[revm::primitives::Log], expected: &[ExpectedLog]) {
        // First verify that the number of logs matches
        assert_eq!(
            logs.len(),
            expected.len(),
            "Log count mismatch. Expected: {}, Actual: {}",
            expected.len(),
            logs.len()
        );

        // Parse all expected logs first
        let expected_logs_parsed: Vec<_> = expected
            .iter()
            .map(|e| {
                let address = Address::from_str(&e.address).unwrap();
                let topics: Vec<B256> =
                    e.topics.iter().map(|t| B256::from_str(t).unwrap()).collect();
                let data = hex::decode(e.data.trim_start_matches("0x")).unwrap();
                (address, topics, data)
            })
            .collect();

        // Track which logs have been matched to avoid duplicate matches
        let mut matched_indices = HashSet::new();

        for (i, (expected_address, expected_topics, expected_data)) in
            expected_logs_parsed.iter().enumerate()
        {
            // Find matching log by address, all topics, and data
            let matching_log_idx = logs.iter().enumerate().find(|(idx, log)| {
                !matched_indices.contains(idx) &&
                    log.address == *expected_address &&
                    log.topics().len() == expected_topics.len() &&
                    log.topics() == expected_topics.as_slice() &&
                    log.data.data.as_ref() == expected_data.as_slice()
            });

            assert!(
                matching_log_idx.is_some(),
                "Expected log {} not found. Address: {:?}, Topics: {:?}, Data: {}",
                i,
                expected_address,
                expected_topics.iter().map(hex::encode).collect::<Vec<_>>(),
                hex::encode(expected_data)
            );

            matched_indices.insert(matching_log_idx.unwrap().0);
        }
    }

    /// Executes a BVM_ETH deposit test fixture stored at the passed `fixture_path` (tar.gz file)
    /// and asserts that the execution results match the expected values.
    async fn run_test_fixture(fixture_path: PathBuf) {
        // First, untar the fixture
        let fixture_dir = tempdir().expect("Failed to create temporary directory");
        let output = tokio::process::Command::new("tar")
            .arg("-xzf")
            .arg(fixture_path.as_path())
            .arg("-C")
            .arg(fixture_dir.path())
            .output()
            .await
            .expect("Failed to untar fixture");

        if !output.status.success() {
            panic!("Failed to untar fixture: {}", String::from_utf8_lossy(&output.stderr));
        }

        // Find all bvm_eth_deposit_*.json files in the fixture directory
        let test_files: Vec<_> = fs::read_dir(fixture_dir.path())
            .unwrap()
            .filter_map(|entry| {
                let entry = entry.ok()?;
                let path = entry.path();
                if path.is_file() {
                    let file_name = path.file_name()?.to_str()?;
                    if file_name.starts_with("bvm_eth_deposit_") && file_name.ends_with(".json") {
                        return Some(file_name.to_string());
                    }
                }
                None
            })
            .collect();

        assert!(
            !test_files.is_empty(),
            "No bvm_eth_deposit_*.json files found in fixture directory: {}",
            fixture_dir.path().display()
        );

        // Run test for each test file
        for test_file in test_files {
            let test_case = load_test_case(fixture_dir.path(), &test_file);
            run_bvm_eth_deposit_test(fixture_dir.path(), test_case);
        }
    }

    #[rstest]
    #[tokio::test(flavor = "multi_thread")]
    async fn test_bvm_eth_deposit(
        #[base_dir = "./src/test_data"]
        #[files("*.tar.gz")]
        path: PathBuf,
    ) {
        run_test_fixture(path).await;
    }

    /// Real-block execution replay of Mantle Sepolia block 286456's FAILED deposit
    /// (tx 0xbcdbae6a -> L2StandardBridge, calldata `0xdeadbeef` -> the proxy/impl reverts).
    /// op-geth keeps the pre-snapshot BVM_ETH `Mint` AND `Transfer` logs on the failed
    /// receipt (receiptsRoot 0x9f29d9b8…) because the transfer executed before the EVM call
    /// and a vm error does not rewind it (op-geth's Case B). This exercises the
    /// `execution_result` revert path, which now surfaces a `FailedDeposit` halt carrying
    /// both logs and the actual gas (26230). Before the fix op-revm returned
    /// `ExecutionResult::Revert` and reth/alloy-op-evm dropped the logs, forking the node.
    ///
    /// Kept in `src/test_data/failed/` (not the auto-globbed `src/test_data/`) so the
    /// failed-deposit fixture is run only by this explicitly-named test.
    #[tokio::test(flavor = "multi_thread")]
    async fn test_failed_deposit_replay_block_286456() {
        run_test_fixture(PathBuf::from("./src/test_data/failed/test_fixture_286456.tar.gz")).await;
    }

    /// Real-block execution replay of Mantle Sepolia block 286432's FAILED deposit
    /// (tx 0x00637e34 -> L2StandardBridge, 256-byte calldata, gas_used == gas_limit ==
    /// 30000). op-geth keeps exactly one BVM_ETH `Mint` log: the eth_tx_value transfer
    /// does not persist here (op-geth's Case A — the transfer's own balance check fails, so
    /// `RevertToSnapshot` undoes it leaving only the mint). Pairs with
    /// `test_failed_deposit_replay_block_286456` (Mint + Transfer) to lock both failure
    /// shapes against op-geth.
    #[tokio::test(flavor = "multi_thread")]
    async fn test_failed_deposit_replay_block_286432() {
        run_test_fixture(PathBuf::from("./src/test_data/failed/test_fixture_286432.tar.gz")).await;
    }

    // -----------------------------------------------------------------------
    // Additional coverage for JournalColdExt cooling.
    //
    // Groups:
    //   * Cold-state for remaining process_eth_deposit scenarios (transfer- only paths,
    //     mint_only=true with both values).
    //   * End-to-end gas regression for paths not covered above: contract target that doesn't touch
    //     BVM_ETH (the case any heuristic-on- calldata fix would have failed), direct BVM_ETH call
    //     (STOP and SLOAD synthetic bytecode), nested CALL into BVM_ETH.
    //   * Revert paths: EVM REVERT preserves pre-EVM mint; cooling is idempotent across a
    //     catch_error-style full revert + re-mint replay; cooling is restored after an inner frame
    //     warms-then- reverts a slot.
    //   * CREATE deposits (TxKind::Create): cooling reaches the create- derived destination
    //     address.
    // -----------------------------------------------------------------------

    /// Inspect cold state without warming the account/slots (which the
    /// journal API would do as a side-effect of load_account/sload).
    fn assert_bvm_eth_and_slots_cold(state: &revm::state::EvmState, expected_cold_slots: &[U256]) {
        let acc = state.get(&BvmEth::ADDRESS).expect("BVM_ETH must be loaded");
        assert!(acc.status.contains(AccountStatus::Cold), "BVM_ETH account must be cold");
        for slot in expected_cold_slots {
            let s = acc
                .storage
                .get(slot)
                .unwrap_or_else(|| panic!("slot {:?} must be in storage", slot));
            assert!(s.is_cold, "slot {:?} must be cold", slot);
        }
    }

    #[test]
    fn process_eth_deposit_transfer_only_self_no_storage_writes() {
        // eth_tx_value only with from == to. transfer_inner early-returns,
        // so no balance slot is written. BVM_ETH account itself was loaded
        // by process_eth_deposit and must be cold afterwards.
        let caller = Address::from([0x11; 20]);
        let eth_tx_value = U256::from(500_000_000_000_000u128);

        let mut ctx = Context::op().with_db(InMemoryDB::default()).modify_tx_chained(|tx| {
            tx.base.caller = caller;
            tx.base.kind = TxKind::Call(caller);
            tx.deposit.source_hash = B256::from([0x10; 32]);
            tx.deposit.eth_value = None;
            tx.deposit.eth_tx_value = Some(eth_tx_value);
        });

        BvmEth::process_eth_deposit(&mut ctx, false).expect("deposit should succeed");

        let bvm =
            ctx.journaled_state.inner.state.get(&BvmEth::ADDRESS).expect("BVM_ETH must be loaded");
        assert!(
            bvm.status.contains(AccountStatus::Cold),
            "BVM_ETH account must be cold after self-transfer-only deposit"
        );
        assert!(
            bvm.storage.is_empty(),
            "self-transfer must not touch any balance slot; got {:?}",
            bvm.storage.keys().collect::<Vec<_>>()
        );
    }

    #[test]
    fn process_eth_deposit_transfer_only_distinct_cools_balance_slots() {
        // eth_tx_value only with from != to. transfer_inner writes both
        // balance[from] and balance[to]. Both must end up cold.
        use revm::context_interface::JournalTr;

        let caller = Address::from([0x11; 20]);
        let recipient = Address::from([0x22; 20]);
        let eth_tx_value = U256::from(500_000_000_000_000u128);

        let mut ctx = Context::op().with_db(InMemoryDB::default()).modify_tx_chained(|tx| {
            tx.base.caller = caller;
            tx.base.kind = TxKind::Call(recipient);
            tx.deposit.source_hash = B256::from([0x11; 32]);
            tx.deposit.eth_value = None;
            tx.deposit.eth_tx_value = Some(eth_tx_value);
        });

        // Pre-fund caller BVM_ETH balance so transfer_inner doesn't fail.
        ctx.journaled_state.load_account(BvmEth::ADDRESS).expect("load BVM_ETH");
        ctx.journaled_state
            .sstore(
                BvmEth::ADDRESS,
                BvmEth::get_balance_slot(caller),
                U256::from(1_000_000_000_000_000u128),
            )
            .expect("seed caller balance");

        BvmEth::process_eth_deposit(&mut ctx, false).expect("deposit should succeed");

        assert_bvm_eth_and_slots_cold(
            &ctx.journaled_state.inner.state,
            &[BvmEth::get_balance_slot(caller), BvmEth::get_balance_slot(recipient)],
        );
    }

    #[test]
    fn process_eth_deposit_mint_only_flag_with_both_values_skips_transfer() {
        // mint_only=true with both eth_value and eth_tx_value: transfer is
        // skipped, so balance[to] must NOT be touched. Only mint slots
        // (total_supply, balance[caller]) end up cold.
        let caller = Address::from([0x11; 20]);
        let recipient = Address::from([0x22; 20]);
        let eth_value = U256::from(1_000_000_000_000_000_000u128);
        let eth_tx_value = U256::from(500_000_000_000_000u128);

        let mut ctx = Context::op().with_db(InMemoryDB::default()).modify_tx_chained(|tx| {
            tx.base.caller = caller;
            tx.base.kind = TxKind::Call(recipient);
            tx.deposit.source_hash = B256::from([0x12; 32]);
            tx.deposit.eth_value = Some(eth_value);
            tx.deposit.eth_tx_value = Some(eth_tx_value);
        });

        BvmEth::process_eth_deposit(&mut ctx, true).expect("mint_only deposit");

        let state = &ctx.journaled_state.inner.state;
        assert_bvm_eth_and_slots_cold(
            state,
            &[BvmEth::get_total_supply_slot(), BvmEth::get_balance_slot(caller)],
        );
        let bvm = state.get(&BvmEth::ADDRESS).unwrap();
        assert!(
            !bvm.storage.contains_key(&BvmEth::get_balance_slot(recipient)),
            "balance[recipient] must not be touched when mint_only=true"
        );
    }

    #[test]
    fn deposit_to_contract_no_bvm_eth_access_no_overcharge() {
        // Contract target whose bytecode is just STOP. EVM enters and halts
        // without touching BVM_ETH. The previous heuristic (4500 magic gated
        // on non-empty input + eth_value) would have over-charged this case.
        // With cooling-based fix: gas_used reflects only intrinsic + calldata.
        let caller = Address::from([0x74; 20]);
        let target = Address::from([0xc0; 20]);

        let stop_code = Bytecode::new_raw(Bytes::from(vec![0x00]));
        let code_hash = stop_code.hash_slow();
        let mut db = InMemoryDB::default();
        db.insert_account_info(
            target,
            AccountInfo {
                balance: U256::ZERO,
                nonce: 1,
                code_hash,
                code: Some(stop_code),
                ..Default::default()
            },
        );
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(u128::MAX), nonce: 0, ..Default::default() },
        );

        let l1_block_info = L1BlockInfo {
            l2_block: Some(U256::ZERO),
            token_ratio: U256::from(3040u64),
            ..Default::default()
        };

        let ctx = Context::op()
            .with_db(db)
            .with_chain(l1_block_info)
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ARSIA))
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.base.kind = TxKind::Call(target);
                tx.base.gas_limit = 0x186a0;
                tx.base.gas_price = 0;
                tx.base.data = Bytes::from(hex::decode("deadbeef01020304").unwrap());
                tx.deposit.source_hash = B256::from([0x20; 32]);
                tx.deposit.mint = Some(0);
                tx.deposit.eth_value = Some(U256::from(0x38d7ea4c68000u128));
                tx.deposit.eth_tx_value = Some(U256::from(0x38d7ea4c68000u128));
            });

        let mut evm = ctx.build_op();
        let mut handler = OpHandler::<
            _,
            EVMError<_, crate::transaction::error::OpTransactionError>,
            EthFrame<EthInterpreter>,
        >::new();
        let result = handler.run(&mut evm).expect("handler.run");
        let gas_used = result.tx_gas_used();

        // Old heuristic would land near 25820 (intrinsic + calldata + 4500).
        // With fix: just intrinsic + calldata + STOP (0) ≈ 21320 territory.
        assert!(
            gas_used < 22000,
            "contract-target deposit (target does not touch BVM_ETH) must not include 4500 BVM_ETH compensation; got gas_used={}",
            gas_used
        );
    }

    #[test]
    fn deposit_direct_to_bvm_eth_stop_body_no_overcharge() {
        // tx.to = BVM_ETH with a STOP bytecode stub. EVM enters BVM_ETH
        // (auto-warm via EIP-2929 tx.to pre-warm), halts immediately, no
        // storage access. Verifies cooling doesn't break tx.to pre-warm and
        // there is no stale 4500 compensation.
        let caller = Address::from([0x74; 20]);

        let stop_code = Bytecode::new_raw(Bytes::from(vec![0x00]));
        let code_hash = stop_code.hash_slow();
        let mut db = InMemoryDB::default();
        db.insert_account_info(
            BvmEth::ADDRESS,
            AccountInfo {
                balance: U256::ZERO,
                nonce: 1,
                code_hash,
                code: Some(stop_code),
                ..Default::default()
            },
        );
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(u128::MAX), nonce: 0, ..Default::default() },
        );

        let l1_block_info = L1BlockInfo {
            l2_block: Some(U256::ZERO),
            token_ratio: U256::from(3040u64),
            ..Default::default()
        };

        let ctx = Context::op()
            .with_db(db)
            .with_chain(l1_block_info)
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ARSIA))
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.base.kind = TxKind::Call(BvmEth::ADDRESS);
                tx.base.gas_limit = 0x186a0;
                tx.base.gas_price = 0;
                tx.base.data = Bytes::from(hex::decode("deadbeef").unwrap());
                tx.deposit.source_hash = B256::from([0x21; 32]);
                tx.deposit.mint = Some(0);
                tx.deposit.eth_value = Some(U256::from(0x38d7ea4c68000u128));
                tx.deposit.eth_tx_value = None;
            });

        let mut evm = ctx.build_op();
        let mut handler = OpHandler::<
            _,
            EVMError<_, crate::transaction::error::OpTransactionError>,
            EthFrame<EthInterpreter>,
        >::new();
        let result = handler.run(&mut evm).expect("handler.run");
        let gas_used = result.tx_gas_used();

        assert!(
            gas_used < 22000,
            "direct BVM_ETH call (STOP body) must not include 4500 compensation; got gas_used={}",
            gas_used
        );
    }

    #[test]
    fn deposit_direct_to_bvm_eth_sload_pays_cold_cost() {
        // tx.to = BVM_ETH with bytecode `PUSH1 2; SLOAD; POP; STOP`. SLOAD
        // hits total_supply (slot 2), which was warmed by pre-EVM mint and
        // then cooled. The EVM SLOAD must pay cold cost (~2100), giving the
        // op-geth-aligned gas. Without cooling the SLOAD would be warm
        // (~100) and gas would be ~2000 lower — caught by the lower bound.
        let caller = Address::from([0x74; 20]);

        let sload_code = Bytecode::new_raw(Bytes::from(vec![0x60, 0x02, 0x54, 0x50, 0x00]));
        let code_hash = sload_code.hash_slow();
        let mut db = InMemoryDB::default();
        db.insert_account_info(
            BvmEth::ADDRESS,
            AccountInfo {
                balance: U256::ZERO,
                nonce: 1,
                code_hash,
                code: Some(sload_code),
                ..Default::default()
            },
        );
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(u128::MAX), nonce: 0, ..Default::default() },
        );

        let l1_block_info = L1BlockInfo {
            l2_block: Some(U256::ZERO),
            token_ratio: U256::from(3040u64),
            ..Default::default()
        };

        let ctx = Context::op()
            .with_db(db)
            .with_chain(l1_block_info)
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ARSIA))
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.base.kind = TxKind::Call(BvmEth::ADDRESS);
                tx.base.gas_limit = 0x186a0;
                tx.base.gas_price = 0;
                tx.base.data = Bytes::from(hex::decode("deadbeef").unwrap());
                tx.deposit.source_hash = B256::from([0x22; 32]);
                tx.deposit.mint = Some(0);
                tx.deposit.eth_value = Some(U256::from(0x38d7ea4c68000u128));
                tx.deposit.eth_tx_value = None;
            });

        let mut evm = ctx.build_op();
        let mut handler = OpHandler::<
            _,
            EVMError<_, crate::transaction::error::OpTransactionError>,
            EthFrame<EthInterpreter>,
        >::new();
        let result = handler.run(&mut evm).expect("handler.run");
        let gas_used = result.tx_gas_used();

        assert!(
            gas_used > 22500,
            "direct BVM_ETH SLOAD must pay cold storage cost (~2100 gas) — slot must have been cooled before EVM; got gas_used={}",
            gas_used
        );
        assert!(
            gas_used < 25000,
            "direct BVM_ETH SLOAD must not double-charge with 4500 compensation; got gas_used={}",
            gas_used
        );
    }

    #[test]
    fn deposit_with_target_revert_no_overcharge() {
        // Target bytecode `PUSH1 0; PUSH1 0; REVERT`. EVM enters and reverts.
        // Verifies that the previous code path (4500 applied even on revert)
        // is gone.
        let caller = Address::from([0x74; 20]);
        let target = Address::from([0xfa; 20]);

        let revert_code = Bytecode::new_raw(Bytes::from(vec![0x60, 0x00, 0x60, 0x00, 0xfd]));
        let code_hash = revert_code.hash_slow();
        let mut db = InMemoryDB::default();
        db.insert_account_info(
            target,
            AccountInfo {
                balance: U256::ZERO,
                nonce: 1,
                code_hash,
                code: Some(revert_code),
                ..Default::default()
            },
        );
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(u128::MAX), nonce: 0, ..Default::default() },
        );

        let l1_block_info = L1BlockInfo {
            l2_block: Some(U256::ZERO),
            token_ratio: U256::from(3040u64),
            ..Default::default()
        };

        let ctx = Context::op()
            .with_db(db)
            .with_chain(l1_block_info)
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ARSIA))
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.base.kind = TxKind::Call(target);
                tx.base.gas_limit = 0x186a0;
                tx.base.gas_price = 0;
                tx.base.data = Bytes::from(hex::decode("deadbeef").unwrap());
                tx.deposit.source_hash = B256::from([0x30; 32]);
                tx.deposit.mint = Some(0);
                tx.deposit.eth_value = Some(U256::from(0x38d7ea4c68000u128));
                tx.deposit.eth_tx_value = None;
            });

        let mut evm = ctx.build_op();
        let mut handler = OpHandler::<
            _,
            EVMError<_, crate::transaction::error::OpTransactionError>,
            EthFrame<EthInterpreter>,
        >::new();
        let result = handler.run(&mut evm).expect("handler.run");

        // A reverted deposit is reported as a FailedDeposit halt that still
        // persists the BVM_ETH mint (op-geth keeps the mint log past its revert
        // snapshot). eth_tx_value is None here, so only the Mint log survives.
        assert!(
            matches!(result, ExecutionResult::Halt { reason: OpHaltReason::FailedDeposit, .. }),
            "expected FailedDeposit halt; got {:?}",
            result
        );

        // The mint log must be present (this is the op-geth-parity fix; previously
        // a reverted deposit returned Revert and dropped the mint log).
        let logs = result.logs();
        assert_eq!(logs.len(), 1, "reverted deposit keeps exactly the Mint log");
        assert_eq!(logs[0].address, BvmEth::ADDRESS);
        assert_eq!(logs[0].topics()[0], BvmEth::MINT_SELECTOR, "topic0 = Mint");
        assert_eq!(logs[0].topics()[1], caller.into_word(), "minter = caller");

        // A revert refunds gas: the reported gas is the actual gas used, NOT the
        // gas limit, and must not include the removed 4500 BVM_ETH compensation.
        let gas_used = result.tx_gas_used();
        assert!(
            gas_used < 22000,
            "deposit-with-revert must not include 4500 compensation; got gas_used={}",
            gas_used
        );
    }

    #[test]
    fn deposit_with_target_revert_pre_mint_persists() {
        // Same shape as the revert gas test, but verify pre-EVM BVM_ETH
        // mint persists in journal state per OP deposit spec.
        use revm::context_interface::JournalTr;

        let caller = Address::from([0x74; 20]);
        let target = Address::from([0xfa; 20]);
        let mint_amount = 0x38d7ea4c68000u128;

        let revert_code = Bytecode::new_raw(Bytes::from(vec![0x60, 0x00, 0x60, 0x00, 0xfd]));
        let code_hash = revert_code.hash_slow();
        let mut db = InMemoryDB::default();
        db.insert_account_info(
            target,
            AccountInfo {
                balance: U256::ZERO,
                nonce: 1,
                code_hash,
                code: Some(revert_code),
                ..Default::default()
            },
        );
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(u128::MAX), nonce: 0, ..Default::default() },
        );

        let l1_block_info = L1BlockInfo {
            l2_block: Some(U256::ZERO),
            token_ratio: U256::from(3040u64),
            ..Default::default()
        };

        let ctx = Context::op()
            .with_db(db)
            .with_chain(l1_block_info)
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ARSIA))
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.base.kind = TxKind::Call(target);
                tx.base.gas_limit = 0x186a0;
                tx.base.gas_price = 0;
                tx.base.data = Bytes::from(hex::decode("deadbeef").unwrap());
                tx.deposit.source_hash = B256::from([0x31; 32]);
                tx.deposit.mint = Some(0);
                tx.deposit.eth_value = Some(U256::from(mint_amount));
                tx.deposit.eth_tx_value = None;
            });

        let mut evm = ctx.build_op();
        let mut handler = OpHandler::<
            _,
            EVMError<_, crate::transaction::error::OpTransactionError>,
            EthFrame<EthInterpreter>,
        >::new();
        let _ = handler.run(&mut evm).expect("handler.run");

        let balance_slot = BvmEth::get_balance_slot(caller);
        let supply_slot = BvmEth::get_total_supply_slot();
        let ctx_after = evm.ctx_mut();
        let balance = ctx_after
            .journaled_state
            .sload(BvmEth::ADDRESS, balance_slot)
            .expect("sload balance")
            .data;
        let supply = ctx_after
            .journaled_state
            .sload(BvmEth::ADDRESS, supply_slot)
            .expect("sload supply")
            .data;
        assert_eq!(
            balance,
            U256::from(mint_amount),
            "BVM_ETH balance[caller] must reflect pre-EVM mint despite EVM REVERT"
        );
        assert_eq!(
            supply,
            U256::from(mint_amount),
            "BVM_ETH total_supply must reflect pre-EVM mint despite EVM REVERT"
        );
    }

    #[test]
    fn catch_error_full_revert_then_remint_cooling_idempotent() {
        // Simulate the catch_error path in handler::catch_error:
        //   1. process_eth_deposit(false) — initial mint+transfer + cool
        //   2. journal.checkpoint_revert(default) — full revert
        //   3. process_eth_deposit(true) — re-mint (mint_only)
        // After this sequence BVM_ETH must again be cold with mint slots
        // cooled, exactly as on a fresh happy-path deposit.
        use revm::{context::journaled_state::JournalCheckpoint, context_interface::JournalTr};

        let caller = Address::from([0x11; 20]);
        let recipient = Address::from([0x22; 20]);
        let eth_value = U256::from(1_000_000_000_000_000_000u128);
        let eth_tx_value = U256::from(500_000_000_000_000u128);

        let mut ctx = Context::op().with_db(InMemoryDB::default()).modify_tx_chained(|tx| {
            tx.base.caller = caller;
            tx.base.kind = TxKind::Call(recipient);
            tx.deposit.source_hash = B256::from([0x40; 32]);
            tx.deposit.eth_value = Some(eth_value);
            tx.deposit.eth_tx_value = Some(eth_tx_value);
        });

        BvmEth::process_eth_deposit(&mut ctx, false).expect("first process_eth_deposit");
        assert_bvm_eth_and_slots_cold(
            &ctx.journaled_state.inner.state,
            &[
                BvmEth::get_total_supply_slot(),
                BvmEth::get_balance_slot(caller),
                BvmEth::get_balance_slot(recipient),
            ],
        );

        // Full revert (catch_error step 1).
        ctx.journaled_state.checkpoint_revert(JournalCheckpoint::default());

        // After full revert BVM_ETH stays in state but is cold (AccountWarmed
        // revert calls mark_cold).
        let bvm = ctx
            .journaled_state
            .inner
            .state
            .get(&BvmEth::ADDRESS)
            .expect("BVM_ETH still in state after revert");
        assert!(
            bvm.status.contains(AccountStatus::Cold),
            "BVM_ETH must be cold after full journal revert"
        );

        // Re-mint via mint_only (catch_error step 2).
        BvmEth::process_eth_deposit(&mut ctx, true).expect("re-mint after revert");

        assert_bvm_eth_and_slots_cold(
            &ctx.journaled_state.inner.state,
            &[BvmEth::get_total_supply_slot(), BvmEth::get_balance_slot(caller)],
        );

        // And the re-minted balance must be present at the journal level.
        let balance = ctx
            .journaled_state
            .sload(BvmEth::ADDRESS, BvmEth::get_balance_slot(caller))
            .expect("sload balance after re-mint")
            .data;
        assert_eq!(
            balance,
            U256::from(eth_value),
            "balance[caller] must reflect the re-mint amount"
        );
    }

    #[test]
    fn cooling_restored_after_inner_frame_warms_then_reverts_slot() {
        // After process_eth_deposit cools BVM_ETH, an inner EVM frame
        // performs an sload (warming the slot via mark_warm_with_transaction_id)
        // and then reverts. The journal's StorageWarmed::revert calls
        // slot.mark_cold, so post-revert the slot is cold again.
        use revm::context_interface::JournalTr;

        let caller = Address::from([0x11; 20]);
        let recipient = Address::from([0x22; 20]);
        let mut ctx = Context::op().with_db(InMemoryDB::default()).modify_tx_chained(|tx| {
            tx.base.caller = caller;
            tx.base.kind = TxKind::Call(recipient);
            tx.deposit.source_hash = B256::from([0x41; 32]);
            tx.deposit.eth_value = Some(U256::from(1_000_000_000_000_000_000u128));
            tx.deposit.eth_tx_value = None;
        });

        BvmEth::process_eth_deposit(&mut ctx, false).expect("process_eth_deposit");

        let supply_slot = BvmEth::get_total_supply_slot();
        {
            let slot = ctx
                .journaled_state
                .inner
                .state
                .get(&BvmEth::ADDRESS)
                .unwrap()
                .storage
                .get(&supply_slot)
                .unwrap();
            assert!(slot.is_cold, "supply slot must be cold after process_eth_deposit");
        }

        // Inner frame: checkpoint, sload (warms), then revert.
        let checkpoint = ctx.journaled_state.checkpoint();
        let _ = ctx.journaled_state.sload(BvmEth::ADDRESS, supply_slot).expect("inner-frame sload");
        {
            let slot = ctx
                .journaled_state
                .inner
                .state
                .get(&BvmEth::ADDRESS)
                .unwrap()
                .storage
                .get(&supply_slot)
                .unwrap();
            assert!(!slot.is_cold, "supply slot must be warm after inner sload");
        }
        ctx.journaled_state.checkpoint_revert(checkpoint);

        let slot = ctx
            .journaled_state
            .inner
            .state
            .get(&BvmEth::ADDRESS)
            .unwrap()
            .storage
            .get(&supply_slot)
            .unwrap();
        assert!(
            slot.is_cold,
            "supply slot must be cold after revert undoes the inner-frame warming"
        );
    }

    #[test]
    fn process_eth_deposit_create_transfer_cools_create_address_slot() {
        // CREATE deposit with eth_tx_value only. transfer_inner derives the
        // destination as caller.create(nonce). Verify cooling covers both
        // balance[caller] and balance[create_addr].
        use revm::context_interface::JournalTr;

        let caller = Address::from([0x11; 20]);
        let mut ctx = Context::op().with_db(InMemoryDB::default()).modify_tx_chained(|tx| {
            tx.base.caller = caller;
            tx.base.kind = TxKind::Create;
            tx.deposit.source_hash = B256::from([0x50; 32]);
            tx.deposit.eth_value = None;
            tx.deposit.eth_tx_value = Some(U256::from(500_000_000_000_000_000u128));
        });

        // Pre-seed caller balance for transfer to succeed.
        ctx.journaled_state.load_account(BvmEth::ADDRESS).expect("load BVM_ETH");
        ctx.journaled_state
            .sstore(
                BvmEth::ADDRESS,
                BvmEth::get_balance_slot(caller),
                U256::from(1_000_000_000_000_000_000u128),
            )
            .expect("seed caller balance");

        let caller_nonce =
            ctx.journaled_state.load_account(caller).expect("load caller").data.info.nonce;
        let create_addr = caller.create(caller_nonce);

        BvmEth::process_eth_deposit(&mut ctx, false).expect("process_eth_deposit");

        assert_bvm_eth_and_slots_cold(
            &ctx.journaled_state.inner.state,
            &[BvmEth::get_balance_slot(caller), BvmEth::get_balance_slot(create_addr)],
        );
    }

    #[test]
    fn process_eth_deposit_create_mint_and_transfer_cools_all_slots() {
        // CREATE deposit with both eth_value and eth_tx_value. Cooling must
        // cover total_supply, balance[caller], balance[create_addr].
        use revm::context_interface::JournalTr;

        let caller = Address::from([0x11; 20]);
        let mut ctx = Context::op().with_db(InMemoryDB::default()).modify_tx_chained(|tx| {
            tx.base.caller = caller;
            tx.base.kind = TxKind::Create;
            tx.deposit.source_hash = B256::from([0x51; 32]);
            tx.deposit.eth_value = Some(U256::from(1_000_000_000_000_000_000u128));
            tx.deposit.eth_tx_value = Some(U256::from(500_000_000_000_000_000u128));
        });

        let caller_nonce =
            ctx.journaled_state.load_account(caller).expect("load caller").data.info.nonce;
        let create_addr = caller.create(caller_nonce);

        BvmEth::process_eth_deposit(&mut ctx, false).expect("process_eth_deposit");

        assert_bvm_eth_and_slots_cold(
            &ctx.journaled_state.inner.state,
            &[
                BvmEth::get_total_supply_slot(),
                BvmEth::get_balance_slot(caller),
                BvmEth::get_balance_slot(create_addr),
            ],
        );
    }

    #[test]
    fn deposit_nested_call_to_bvm_eth_pays_cold_cost() {
        // tx.to = outer contract whose code is:
        //   PUSH1 0 PUSH1 0 PUSH1 0 PUSH1 0 PUSH1 0 PUSH20 <BVM_ETH> GAS CALL STOP
        // i.e. CALL(BVM_ETH, 0, 0, 0, 0, 0); STOP.
        //
        // BVM_ETH stub: PUSH1 2; SLOAD; POP; STOP.
        //
        // The first CALL to BVM_ETH from EVM pays cold account cost (~2600),
        // and the inner SLOAD on total_supply pays cold storage cost
        // (~2100) — both made cold by the JournalColdExt reset.
        let caller = Address::from([0x74; 20]);
        let outer = Address::from([0xc0; 20]);

        let mut outer_code: Vec<u8> = Vec::new();
        for _ in 0..5 {
            outer_code.push(0x60);
            outer_code.push(0x00);
        }
        outer_code.push(0x73);
        outer_code.extend_from_slice(BvmEth::ADDRESS.as_slice());
        outer_code.push(0x5a);
        outer_code.push(0xf1);
        outer_code.push(0x00);

        let outer_bc = Bytecode::new_raw(Bytes::from(outer_code));
        let outer_hash = outer_bc.hash_slow();

        let bvm_stub = Bytecode::new_raw(Bytes::from(vec![0x60, 0x02, 0x54, 0x50, 0x00]));
        let bvm_hash = bvm_stub.hash_slow();

        let mut db = InMemoryDB::default();
        db.insert_account_info(
            outer,
            AccountInfo {
                balance: U256::ZERO,
                nonce: 1,
                code_hash: outer_hash,
                code: Some(outer_bc),
                ..Default::default()
            },
        );
        db.insert_account_info(
            BvmEth::ADDRESS,
            AccountInfo {
                balance: U256::ZERO,
                nonce: 1,
                code_hash: bvm_hash,
                code: Some(bvm_stub),
                ..Default::default()
            },
        );
        db.insert_account_info(
            caller,
            AccountInfo { balance: U256::from(u128::MAX), nonce: 0, ..Default::default() },
        );

        let l1_block_info = L1BlockInfo {
            l2_block: Some(U256::ZERO),
            token_ratio: U256::from(3040u64),
            ..Default::default()
        };

        let ctx = Context::op()
            .with_db(db)
            .with_chain(l1_block_info)
            .modify_cfg_chained(|cfg| cfg.set_spec_and_mainnet_gas_params(OpSpecId::ARSIA))
            .modify_tx_chained(|tx| {
                tx.base.caller = caller;
                tx.base.kind = TxKind::Call(outer);
                tx.base.gas_limit = 0x186a0;
                tx.base.gas_price = 0;
                tx.base.data = Bytes::new();
                tx.deposit.source_hash = B256::from([0x60; 32]);
                tx.deposit.mint = Some(0);
                tx.deposit.eth_value = Some(U256::from(0x38d7ea4c68000u128));
                tx.deposit.eth_tx_value = None;
            });

        let mut evm = ctx.build_op();
        let mut handler = OpHandler::<
            _,
            EVMError<_, crate::transaction::error::OpTransactionError>,
            EthFrame<EthInterpreter>,
        >::new();
        let result = handler.run(&mut evm).expect("handler.run");
        let gas_used = result.tx_gas_used();

        // Cold CALL (~2600) + cold SLOAD (~2100) must both be paid.
        assert!(
            gas_used > 25000,
            "nested CALL to BVM_ETH must pay cold account + cold storage cost; got gas_used={}",
            gas_used
        );
        // No stale 4500 compensation on top.
        assert!(
            gas_used < 30000,
            "nested CALL to BVM_ETH must not include stale 4500 compensation; got gas_used={}",
            gas_used
        );
    }
}
