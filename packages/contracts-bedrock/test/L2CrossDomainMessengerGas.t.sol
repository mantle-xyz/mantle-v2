// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { VmSafe } from "forge-std/Vm.sol";
import { Messenger_Initializer } from "./CommonTest.t.sol";
import { Burn } from "src/libraries/Burn.sol";
import { Encoding } from "src/libraries/Encoding.sol";
import { Hashing } from "src/libraries/Hashing.sol";
import { AddressAliasHelper } from "src/vendor/AddressAliasHelper.sol";
import { L2CrossDomainMessenger } from "src/L2/L2CrossDomainMessenger.sol";

contract L2RelayGasConsumer {
    uint256 internal immutable gasToBurn;

    constructor(uint256 _gasToBurn) {
        gasToBurn = _gasToBurn;
    }

    fallback() external payable {
        Burn.gas(gasToBurn);
    }
}

contract L2CrossDomainMessengerGas_Test is Messenger_Initializer {
    /// @dev Also run with --isolate so the relay has a transaction-sized state-gas budget.
    function test_relayMessage_targetOutOfGasRetry_succeeds() external {
        address target = address(new L2RelayGasConsumer(1_000_000));
        address caller = AddressAliasHelper.applyL1ToL2Alias(address(L1Messenger));
        uint256 nonce = Encoding.encodeVersionedNonce(0, 1);
        bytes memory message = hex"1122";
        bytes32 messageHash = Hashing.hashCrossDomainMessageV1(nonce, alice, target, 1, 0, 25_000, message);
        uint64 gasLimit = L1Messenger.baseGas(message, 25_000);
        vm.deal(caller, 1);

        vm.expectCallMinGas(target, 1, 25_000, message);
        vm.expectEmit(true, false, false, true, address(L2Messenger));
        emit FailedRelayedMessage(messageHash);
        vm.prank(caller);
        L2Messenger.relayMessage{ gas: gasLimit, value: 1 }(nonce, alice, target, 1, 0, 25_000, message);

        assertTrue(L2Messenger.failedMessages(messageHash));
        assertFalse(L2Messenger.successfulMessages(messageHash));
        assertEq(target.balance, 0);
        assertEq(address(L2Messenger).balance, 1);
        vm.expectRevert("CrossDomainMessenger: xDomainMessageSender is not set");
        L2Messenger.xDomainMessageSender();

        gasLimit = L1Messenger.baseGas(message, 1_100_000);
        vm.expectCallMinGas(target, 1, 1_000_000, message);
        vm.expectEmit(true, false, false, true, address(L2Messenger));
        emit RelayedMessage(messageHash);
        L2Messenger.relayMessage{ gas: gasLimit }(nonce, alice, target, 1, 0, 25_000, message);

        assertTrue(L2Messenger.successfulMessages(messageHash));
        assertTrue(L2Messenger.failedMessages(messageHash));
        assertEq(target.balance, 1);
        assertEq(address(L2Messenger).balance, 0);
        vm.expectRevert("CrossDomainMessenger: message has already been relayed");
        L2Messenger.relayMessage(nonce, alice, target, 1, 0, 25_000, message);
    }

    function test_relayMessage_freshETHAllowanceInsufficientGasRetry_succeeds() external {
        address target = address(new L2RelayGasConsumer(500_000));
        address caller = AddressAliasHelper.applyL1ToL2Alias(address(L1Messenger));
        uint256 nonce = Encoding.encodeVersionedNonce(0, 1);
        bytes memory message = hex"1122";
        bytes32 messageHash = Hashing.hashCrossDomainMessageV1(nonce, alice, target, 0, 100, 200_000, message);
        deal(address(l2ETH), address(L2Messenger), 100);

        // Budget for creating the BVM_ETH allowance before forwarding the requested minimum gas.
        vm.startStateDiffRecording();
        vm.prank(caller);
        L2Messenger.relayMessage{ gas: 480_000 }(nonce, alice, target, 0, 100, 200_000, message);
        VmSafe.AccountAccess[] memory accesses = vm.stopAndReturnStateDiff();
        for (uint256 i; i < accesses.length; ++i) {
            assertFalse(accesses[i].kind == VmSafe.AccountAccessKind.Call && accesses[i].account == target);
        }
        assertTrue(L2Messenger.failedMessages(messageHash));
        assertFalse(L2Messenger.successfulMessages(messageHash));
        assertEq(l2ETH.allowance(address(L2Messenger), target), 0);

        vm.expectCallMinGas(target, 0, 200_000, message);
        L2Messenger.relayMessage{ gas: 1_000_000 }(nonce, alice, target, 0, 100, 200_000, message);
        assertTrue(L2Messenger.successfulMessages(messageHash));
        assertEq(l2ETH.allowance(address(L2Messenger), target), 0);
    }

    function _relayToEmptyAccount(uint256 _gas, uint256 _nonce) internal returns (bool successful) {
        address target = address(uint160(uint256(keccak256(abi.encode("empty L2 recipient", _nonce)))));
        assertEq(target.code.length, 0);
        assertEq(target.balance, 0);
        assertEq(vm.getNonce(target), 0);
        address caller = AddressAliasHelper.applyL1ToL2Alias(address(L1Messenger));
        uint256 nonce = Encoding.encodeVersionedNonce(uint240(_nonce), 1);
        bytes32 messageHash = Hashing.hashCrossDomainMessageV1(nonce, alice, target, 1, 1, 0, hex"");
        vm.deal(caller, 1);

        // BVM_ETH approval plus native MNT account creation are the L2 counterparts.
        vm.prank(caller);
        (bool completed,) = address(L2Messenger).call{ gas: _gas, value: 1 }(
            abi.encodeCall(L2CrossDomainMessenger.relayMessage, (nonce, alice, target, 1, 1, 0, hex""))
        );
        assertTrue(completed, "relay must preserve a success or failure record");
        successful = L2Messenger.successfulMessages(messageHash);
        assertTrue(successful != L2Messenger.failedMessages(messageHash));
        assertEq(target.balance, successful ? 1 : 0);
        assertEq(l2ETH.allowance(address(L2Messenger), target), 0);
        if (!successful) {
            L2Messenger.relayMessage{ gas: 1_000_000 }(nonce, alice, target, 1, 1, 0, hex"");
            assertTrue(L2Messenger.successfulMessages(messageHash));
            assertEq(target.balance, 1);
            assertEq(l2ETH.allowance(address(L2Messenger), target), 0);
        }
        vm.expectRevert();
        L2Messenger.relayMessage(nonce, alice, target, 1, 1, 0, hex"");
        assertEq(target.balance, 1);
    }

    function testFuzz_relayMessage_emptyAccount_succeeds(uint32 _gas) external {
        _relayToEmptyAccount(bound(_gas, 350_000, 900_000), 0);
    }

    function test_relayMessage_emptyAccountNewGasBoundary_succeeds() external {
        uint256 checkGas = L2Messenger.RELAY_CALL_OVERHEAD() + L2Messenger.RELAY_RESERVED_GAS()
            + L2Messenger.RELAY_GAS_CHECK_BUFFER() + L2Messenger.RELAY_NEW_ACCOUNT_OVERHEAD();
        bool sawFailure;
        bool sawSuccess;
        // Include the code before hasMinGas; the check's 525k is not the relay entry budget.
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
        // Legacy in-flight messages may succeed directly or need replay after the upgrade.
        _relayToEmptyAccount(385_800, 0);
        // Unlike the L1 MNT proxy path, this fixture has enough gas for first-attempt success.
        assertTrue(_relayToEmptyAccount(540_800, 1));
    }

    function test_relayMessage_emptyAccountNewBudget_succeeds() external {
        assertTrue(_relayToEmptyAccount(L1Messenger.baseGas(hex"", 0), 0));
    }
}
