// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { VmSafe } from "forge-std/Vm.sol";
import { Bridge_Initializer } from "./CommonTest.t.sol";
import { Burn } from "src/libraries/Burn.sol";
import { Encoding } from "src/libraries/Encoding.sol";
import { Hashing } from "src/libraries/Hashing.sol";
import { Predeploys } from "src/libraries/Predeploys.sol";
import { Types } from "src/libraries/Types.sol";
import { OptimismPortal } from "src/L1/OptimismPortal.sol";
import { L1CrossDomainMessenger } from "src/L1/L1CrossDomainMessenger.sol";
import { L1StandardBridge } from "src/L1/L1StandardBridge.sol";
import { L1MantleToken } from "./mocks/TestMantleToken.sol";
import { TransparentUpgradeableProxy } from "@openzeppelin/contracts/proxy/transparent/TransparentUpgradeableProxy.sol";

contract RelayGasConsumer {
    uint256 internal immutable gasToBurn;

    constructor(uint256 _gasToBurn) {
        gasToBurn = _gasToBurn;
    }

    fallback() external payable {
        Burn.gas(gasToBurn);
    }
}

contract L1CrossDomainMessengerGas_Test is Bridge_Initializer {
    function setUp() public override {
        super.setUp();
        // Include the implementation/admin reads and DELEGATECALL used by the L1 MNT proxy.
        // Keep the token address and existing fixture storage used by Portal and Messenger.
        address implementation = address(new L1MantleToken());
        address proxy = address(new TransparentUpgradeableProxy(implementation, multisig, hex""));
        vm.etch(address(l1MNT), proxy.code);
        vm.store(
            address(l1MNT),
            bytes32(uint256(keccak256("eip1967.proxy.implementation")) - 1),
            bytes32(uint256(uint160(implementation)))
        );
        vm.store(
            address(l1MNT), bytes32(uint256(keccak256("eip1967.proxy.admin")) - 1), bytes32(uint256(uint160(multisig)))
        );
    }

    function _authenticatePortal() internal {
        vm.store(address(op), bytes32(uint256(50)), bytes32(uint256(uint160(Predeploys.L2_CROSS_DOMAIN_MESSENGER))));
    }

    /// @dev Also run with --isolate so each relay has a transaction-sized state-gas budget.
    function test_relayMessage_targetOutOfGasRetry_succeeds() external {
        address target = address(new RelayGasConsumer(1_000_000));
        uint256 nonce = Encoding.encodeVersionedNonce(0, 1);
        bytes memory message = hex"1122";
        bytes32 messageHash = Hashing.hashCrossDomainMessageV1(nonce, alice, target, 0, 1, 25_000, message);
        uint64 gasLimit = L1Messenger.baseGas(message, 25_000);
        _authenticatePortal();
        vm.deal(address(op), 1);

        vm.expectCallMinGas(target, 1, 25_000, message);
        vm.expectEmit(true, false, false, true, address(L1Messenger));
        emit FailedRelayedMessage(messageHash);
        vm.prank(address(op));
        L1Messenger.relayMessage{ gas: gasLimit, value: 1 }(nonce, alice, target, 0, 1, 25_000, message);

        assertTrue(L1Messenger.failedMessages(messageHash));
        assertFalse(L1Messenger.successfulMessages(messageHash));
        assertEq(target.balance, 0);
        assertEq(address(L1Messenger).balance, 1);
        vm.expectRevert("CrossDomainMessenger: xDomainMessageSender is not set");
        L1Messenger.xDomainMessageSender();

        gasLimit = L1Messenger.baseGas(message, 1_100_000);
        vm.expectCallMinGas(target, 1, 1_000_000, message);
        vm.expectEmit(true, false, false, true, address(L1Messenger));
        emit RelayedMessage(messageHash);
        L1Messenger.relayMessage{ gas: gasLimit }(nonce, alice, target, 0, 1, 25_000, message);

        assertTrue(L1Messenger.successfulMessages(messageHash));
        assertTrue(L1Messenger.failedMessages(messageHash));
        assertEq(target.balance, 1);
        assertEq(address(L1Messenger).balance, 0);
        vm.expectRevert("CrossDomainMessenger: message has already been relayed");
        L1Messenger.relayMessage(nonce, alice, target, 0, 1, 25_000, message);
    }

    function test_relayMessage_freshMNTAllowanceInsufficientGasRetry_succeeds() external {
        address target = address(new RelayGasConsumer(500_000));
        uint256 nonce = Encoding.encodeVersionedNonce(0, 1);
        bytes memory message = hex"1122";
        bytes32 messageHash = Hashing.hashCrossDomainMessageV1(nonce, alice, target, 100, 0, 200_000, message);
        dealL1MNT(address(L1Messenger), 100);
        _authenticatePortal();

        // Without budgeting for the fresh allowance, this passes the check but forwards
        // less than the requested minimum gas to the target under Amsterdam pricing.
        vm.startStateDiffRecording();
        vm.prank(address(op));
        L1Messenger.relayMessage{ gas: 505_000 }(nonce, alice, target, 100, 0, 200_000, message);
        VmSafe.AccountAccess[] memory accesses = vm.stopAndReturnStateDiff();
        for (uint256 i; i < accesses.length; ++i) {
            assertFalse(accesses[i].kind == VmSafe.AccountAccessKind.Call && accesses[i].account == target);
        }
        assertTrue(L1Messenger.failedMessages(messageHash));
        assertFalse(L1Messenger.successfulMessages(messageHash));
        assertEq(l1MNT.allowance(address(L1Messenger), target), 0);

        vm.expectCallMinGas(target, 0, 200_000, message);
        L1Messenger.relayMessage{ gas: 1_000_000 }(nonce, alice, target, 100, 0, 200_000, message);
        assertTrue(L1Messenger.successfulMessages(messageHash));
        assertEq(l1MNT.allowance(address(L1Messenger), target), 0);
    }

    function _relayToEmptyAccount(uint256 _gas, uint256 _nonce) internal returns (bool successful) {
        address target = address(uint160(uint256(keccak256(abi.encode("empty L1 recipient", _nonce)))));
        assertEq(target.code.length, 0);
        assertEq(target.balance, 0);
        assertEq(vm.getNonce(target), 0);
        uint256 nonce = Encoding.encodeVersionedNonce(uint240(_nonce), 1);
        bytes32 messageHash = Hashing.hashCrossDomainMessageV1(nonce, alice, target, 1, 1, 0, hex"");
        _authenticatePortal();
        vm.deal(address(op), 1);

        // Both asset values exercise approval, account creation, approval cleanup and result recording.
        vm.prank(address(op));
        (bool completed,) = address(L1Messenger).call{ gas: _gas, value: 1 }(
            abi.encodeCall(L1CrossDomainMessenger.relayMessage, (nonce, alice, target, 1, 1, 0, hex""))
        );
        assertTrue(completed, "relay must preserve a success or failure record");
        successful = L1Messenger.successfulMessages(messageHash);
        assertTrue(successful != L1Messenger.failedMessages(messageHash));
        assertEq(target.balance, successful ? 1 : 0);
        assertEq(l1MNT.allowance(address(L1Messenger), target), 0);
        if (!successful) {
            L1Messenger.relayMessage{ gas: 1_000_000 }(nonce, alice, target, 1, 1, 0, hex"");
            assertTrue(L1Messenger.successfulMessages(messageHash));
            assertEq(target.balance, 1);
            assertEq(l1MNT.allowance(address(L1Messenger), target), 0);
        }
        vm.expectRevert();
        L1Messenger.relayMessage(nonce, alice, target, 1, 1, 0, hex"");
        assertEq(target.balance, 1);
    }

    function test_relayMessage_emptyAccountGasBoundary_succeeds() external {
        // Scan the former gap between passing hasMinGas and completing the final SSTORE.
        for (uint256 gasLimit = 370_000; gasLimit <= 420_000; gasLimit += 1_000) {
            _relayToEmptyAccount(gasLimit, gasLimit);
        }
    }

    function testFuzz_relayMessage_emptyAccount_succeeds(uint32 _gas) external {
        _relayToEmptyAccount(bound(_gas, 350_000, 900_000), 0);
    }

    function test_relayMessage_emptyAccountNewGasBoundary_succeeds() external {
        // This is the gas required at hasMinGas, not at relayMessage entry. Scan past the
        // authentication/hash overhead and require coverage on both sides of the transition.
        uint256 checkGas = L1Messenger.RELAY_CALL_OVERHEAD() + L1Messenger.RELAY_RESERVED_GAS()
            + L1Messenger.RELAY_GAS_CHECK_BUFFER() + L1Messenger.RELAY_NEW_ACCOUNT_OVERHEAD();
        bool sawFailure;
        bool sawSuccess;
        for (uint256 gasLimit = checkGas - 5_000; gasLimit <= checkGas + 75_000; gasLimit += 500) {
            if (_relayToEmptyAccount(gasLimit, gasLimit)) {
                sawSuccess = true;
            } else {
                assertFalse(sawSuccess, "more gas must not turn success into failure");
                sawFailure = true;
            }
        }
        assertTrue(sawFailure && sawSuccess, "scan must straddle the actual relay boundary");
    }

    function test_relayMessage_emptyAccountLegacyBudgets_succeeds() external {
        // In-flight messages retain budgets emitted by older implementations. First-attempt
        // failure is not a protocol requirement: either succeed or remain replayable, and pay once.
        _relayToEmptyAccount(385_800, 0);
        _relayToEmptyAccount(540_800, 1);
    }

    function test_relayMessage_emptyAccountNewBudget_succeeds() external {
        assertTrue(_relayToEmptyAccount(L1Messenger.baseGas(hex"", 0), 0));
    }

    function _prepareEmptyAccountWithdrawal(uint64 _messageGas)
        internal
        returns (Types.WithdrawalTransaction memory withdrawal, bytes32 messageHash, address target)
    {
        target = makeAddr("empty Portal recipient");
        uint256 nonce = Encoding.encodeVersionedNonce(0, 1);
        bytes memory data = abi.encodeCall(L1CrossDomainMessenger.relayMessage, (nonce, alice, target, 1, 1, 0, hex""));
        messageHash = Hashing.hashCrossDomainMessageV1(nonce, alice, target, 1, 1, 0, hex"");
        withdrawal = Types.WithdrawalTransaction({
            nonce: 0,
            sender: Predeploys.L2_CROSS_DOMAIN_MESSENGER,
            target: address(L1Messenger),
            mntValue: 1,
            ethValue: 1,
            gasLimit: _messageGas,
            data: data
        });
        _proveWithdrawal(withdrawal);
        dealL1MNT(address(op), 1);
        vm.deal(address(op), 1);
    }

    function _assertFinalizedAndRecover(
        Types.WithdrawalTransaction memory _withdrawal,
        bytes32 _messageHash,
        address _recipient
    )
        internal
        returns (bool successful)
    {
        bytes32 withdrawalHash = Hashing.hashWithdrawal(_withdrawal);
        assertTrue(op.finalizedWithdrawals(withdrawalHash));
        successful = L1Messenger.successfulMessages(_messageHash);
        assertTrue(successful != L1Messenger.failedMessages(_messageHash));
        assertEq(_recipient.balance, successful ? _withdrawal.ethValue : 0);
        if (!successful) {
            (bool completed,) = address(L1Messenger).call{ gas: 1_000_000 }(_withdrawal.data);
            assertTrue(completed, "recorded failure must be replayable");
        }
        assertTrue(L1Messenger.successfulMessages(_messageHash));
        assertEq(_recipient.balance, _withdrawal.ethValue);
        assertEq(l1MNT.allowance(address(L1Messenger), _recipient), 0);
        vm.expectRevert("OptimismPortal: withdrawal has already been finalized");
        op.finalizeWithdrawalTransaction(_withdrawal);
        (bool replayed,) = address(L1Messenger).call(_withdrawal.data);
        assertFalse(replayed, "successful message must not be replayable");
        assertEq(_recipient.balance, _withdrawal.ethValue);
    }

    function _finalizeToEmptyAccount(uint64 _messageGas, uint256 _finalizeGas) internal returns (bool) {
        (Types.WithdrawalTransaction memory withdrawal, bytes32 messageHash, address target) =
            _prepareEmptyAccountWithdrawal(_messageGas);
        op.finalizeWithdrawalTransaction{ gas: _finalizeGas }(withdrawal);
        return _assertFinalizedAndRecover(withdrawal, messageHash, target);
    }

    function _findFinalizeBoundary(Types.WithdrawalTransaction memory _withdrawal) internal returns (uint256) {
        bytes memory data = abi.encodeCall(OptimismPortal.finalizeWithdrawalTransaction, (_withdrawal));
        uint256 snapshot = vm.snapshotState();
        uint256 low = 100_000;
        uint256 high = 1_500_000;
        while (high - low > 1) {
            uint256 middle = (low + high) / 2;
            (bool completed,) = address(op).call{ gas: middle }(data);
            if (completed) high = middle;
            else low = middle;
            assertTrue(vm.revertToState(snapshot));
        }
        assertTrue(vm.revertToStateAndDelete(snapshot));
        return high;
    }

    function _testFinalizeBoundary(uint64 _messageGas) internal {
        (Types.WithdrawalTransaction memory withdrawal, bytes32 messageHash, address target) =
            _prepareEmptyAccountWithdrawal(_messageGas);
        // Locate the outer Portal call budget. The ~432k check for a 385,800-gas message
        // applies inside SafeCall, after Portal proof checks and writes, not at Portal entry.
        uint256 boundary = _findFinalizeBoundary(withdrawal);
        bytes memory data = abi.encodeCall(OptimismPortal.finalizeWithdrawalTransaction, (withdrawal));
        uint256 snapshot = vm.snapshotState();
        (bool completed,) = address(op).call{ gas: boundary - 1 }(data);
        assertFalse(completed);
        assertFalse(op.finalizedWithdrawals(Hashing.hashWithdrawal(withdrawal)));
        assertFalse(L1Messenger.successfulMessages(messageHash));
        assertFalse(L1Messenger.failedMessages(messageHash));
        assertEq(target.balance, 0);
        assertEq(address(op).balance, 1);
        assertTrue(vm.revertToStateAndDelete(snapshot));

        (completed,) = address(op).call{ gas: boundary }(data);
        assertTrue(completed);
        _assertFinalizedAndRecover(withdrawal, messageHash, target);
    }

    function test_finalizeWithdrawal_emptyAccountLegacy385kBoundary_succeeds() external {
        _testFinalizeBoundary(385_800);
    }

    function test_finalizeWithdrawal_emptyAccountLegacy540kBoundary_succeeds() external {
        _testFinalizeBoundary(540_800);
    }

    function test_finalizeWithdrawal_standardBridgeETHLegacyBudget_succeeds() external {
        address recipient = makeAddr("legacy ETH withdrawal recipient");
        uint256 nonce = Encoding.encodeVersionedNonce(0, 1);
        bytes memory message = abi.encodeCall(L1StandardBridge.finalizeBridgeETH, (alice, recipient, 1, hex""));
        bytes32 messageHash =
            Hashing.hashCrossDomainMessageV1(nonce, address(L2Bridge), address(L1Bridge), 0, 1, 200_000, message);
        Types.WithdrawalTransaction memory withdrawal = Types.WithdrawalTransaction({
            nonce: 0,
            sender: Predeploys.L2_CROSS_DOMAIN_MESSENGER,
            target: address(L1Messenger),
            mntValue: 0,
            ethValue: 1,
            // Budget emitted before the upgrade for this message with minGas=200,000.
            gasLimit: 591_926,
            data: abi.encodeCall(
                L1CrossDomainMessenger.relayMessage,
                (nonce, address(L2Bridge), address(L1Bridge), 0, 1, 200_000, message)
            )
        });
        _proveWithdrawal(withdrawal);
        vm.deal(address(op), 1);
        uint256 boundary = _findFinalizeBoundary(withdrawal);
        op.finalizeWithdrawalTransaction{ gas: boundary }(withdrawal);
        _assertFinalizedAndRecover(withdrawal, messageHash, recipient);
    }

    function _proveWithdrawal(Types.WithdrawalTransaction memory _withdrawal) internal returns (bytes32) {
        (bytes32 stateRoot, bytes32 storageRoot, bytes32 outputRoot, bytes32 withdrawalHash, bytes[] memory proof) =
            ffi.getProveWithdrawalTransactionInputs(_withdrawal);
        uint256 outputIndex = oracle.nextOutputIndex();
        uint256 outputBlock = oracle.nextBlockNumber();
        warpToProposeTime(outputBlock);
        vm.prank(proposer);
        oracle.proposeL2Output(outputRoot, outputBlock, 0, 0);
        op.proveWithdrawalTransaction(
            _withdrawal, outputIndex, Types.OutputRootProof(bytes32(0), stateRoot, storageRoot, bytes32(0)), proof
        );
        vm.warp(block.timestamp + oracle.FINALIZATION_PERIOD_SECONDS() + 1);
        return withdrawalHash;
    }

    function test_finalizeWithdrawal_emptyAccountOldBudget_succeeds() external {
        _finalizeToEmptyAccount(385_800, 800_000);
    }

    function test_finalizeWithdrawal_emptyAccountNewBudget_succeeds() external {
        assertTrue(_finalizeToEmptyAccount(L1Messenger.baseGas(hex"", 0), 1_300_000));
    }
}
