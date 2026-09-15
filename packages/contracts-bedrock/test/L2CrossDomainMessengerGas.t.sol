// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { VmSafe } from "forge-std/Vm.sol";
import { Messenger_Initializer } from "./CommonTest.t.sol";
import { Burn } from "src/libraries/Burn.sol";
import { Encoding } from "src/libraries/Encoding.sol";
import { Hashing } from "src/libraries/Hashing.sol";
import { AddressAliasHelper } from "src/vendor/AddressAliasHelper.sol";

contract L2RelayGasConsumer {
    fallback() external payable {
        Burn.gas(500_000);
    }
}

contract L2CrossDomainMessengerGas_Test is Messenger_Initializer {
    /// @dev Also run with --isolate so the relay has a transaction-sized state-gas budget.
    function test_relayMessage_targetOutOfGasRetry_succeeds() external {
        address target = address(new L2RelayGasConsumer());
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

        gasLimit = L1Messenger.baseGas(message, 600_000);
        vm.expectCallMinGas(target, 1, 500_000, message);
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
        address target = address(new L2RelayGasConsumer());
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
}
