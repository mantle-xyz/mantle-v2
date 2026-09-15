// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { VmSafe } from "forge-std/Vm.sol";
import { Messenger_Initializer } from "./CommonTest.t.sol";
import { Burn } from "src/libraries/Burn.sol";
import { Encoding } from "src/libraries/Encoding.sol";
import { Hashing } from "src/libraries/Hashing.sol";
import { Predeploys } from "src/libraries/Predeploys.sol";

contract RelayGasConsumer {
    fallback() external payable {
        Burn.gas(500_000);
    }
}

contract L1CrossDomainMessengerGas_Test is Messenger_Initializer {
    function _authenticatePortal() internal {
        vm.store(address(op), bytes32(uint256(50)), bytes32(uint256(uint160(Predeploys.L2_CROSS_DOMAIN_MESSENGER))));
    }

    /// @dev Also run with --isolate so each relay has a transaction-sized state-gas budget.
    function test_relayMessage_targetOutOfGasRetry_succeeds() external {
        address target = address(new RelayGasConsumer());
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

        gasLimit = L1Messenger.baseGas(message, 600_000);
        vm.expectCallMinGas(target, 1, 500_000, message);
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
        address target = address(new RelayGasConsumer());
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
}
