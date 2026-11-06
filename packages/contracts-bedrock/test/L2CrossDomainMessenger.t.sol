// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { Messenger_Initializer, Reverter, ConfigurableCaller } from "./CommonTest.t.sol";

import { AddressAliasHelper } from "src/vendor/AddressAliasHelper.sol";
import { L2ToL1MessagePasser } from "src/L2/L2ToL1MessagePasser.sol";
import { L2OutputOracle } from "src/L1/L2OutputOracle.sol";
import { L2CrossDomainMessenger } from "src/L2/L2CrossDomainMessenger.sol";
import { L1CrossDomainMessenger } from "src/L1/L1CrossDomainMessenger.sol";
import { Hashing } from "src/libraries/Hashing.sol";
import { Encoding } from "src/libraries/Encoding.sol";
import { Types } from "src/libraries/Types.sol";
import { Predeploys } from "src/libraries/Predeploys.sol";

contract L2CrossDomainMessenger_WithdrawalLimits_Test is Messenger_Initializer {
    address internal constant RECIPIENT = address(0xabbaacdc);

    function _maxMinGas(bytes memory _message) internal view returns (uint32) {
        uint256 dataLength = 260 + ((_message.length + 31) / 32) * 32;
        uint256 available = 15_000_000 - 16 * dataLength - L2Messenger.baseGas(_message, 0);
        // Largest m for which floor(64 * m / 63) <= available.
        return uint32(((available + 1) * 63 - 1) / 64);
    }

    function _send(bytes memory _message, uint32 _minGas, bool _withEthArgument) internal {
        vm.prank(alice);
        if (_withEthArgument) {
            L2Messenger.sendMessage(0, RECIPIENT, _message, _minGas);
        } else {
            L2Messenger.sendMessage(RECIPIENT, _message, _minGas);
        }
    }

    function test_sendMessage_withdrawalLimits_boundary() external {
        assertEq(L2Messenger.MAX_WITHDRAWAL_GAS_LIMIT(), 15_000_000);
        assertEq(L2Messenger.MAX_WITHDRAWAL_DATA_LENGTH(), 90_000);
        uint256 messengerNonce = L2Messenger.messageNonce();
        uint256 passerNonce = messagePasser.messageNonce();
        uint256[2] memory sizes = [uint256(0), 89_728];
        for (uint256 i; i < sizes.length; ++i) {
            bytes memory message = new bytes(sizes[i]);
            uint32 minGas = _maxMinGas(message);
            for (uint256 j; j < 2; ++j) {
                _send(message, minGas, j == 1);
                vm.expectRevert("L2CrossDomainMessenger: gas limit too high");
                _send(message, minGas + 1, j == 1);
            }
        }
        assertEq(L2Messenger.messageNonce(), messengerNonce + 4);
        assertEq(messagePasser.messageNonce(), passerNonce + 4);
    }

    function test_sendMessage_gasLimitTooHigh_reverts() external {
        uint32[2] memory limits = [uint32(15_300_000), type(uint32).max];
        for (uint256 i; i < limits.length; ++i) {
            vm.expectRevert("L2CrossDomainMessenger: gas limit too high");
            _send(hex"", limits[i], false);
            vm.expectRevert("L2CrossDomainMessenger: gas limit too high");
            _send(hex"", limits[i], true);
        }
    }

    function test_sendMessage_messageTooLarge_reverts() external {
        uint256[2] memory sizes = [uint256(89_729), 300_000];
        for (uint256 i; i < sizes.length; ++i) {
            bytes memory message = new bytes(sizes[i]);
            vm.expectRevert("L2CrossDomainMessenger: message too large");
            _send(message, 0, false);
            vm.expectRevert("L2CrossDomainMessenger: message too large");
            _send(message, 0, true);
        }
    }

    function test_sendMessage_rejectedWithdrawal_rollsBackAssets() external {
        deal(address(l2ETH), alice, 100, true);
        vm.prank(alice);
        l2ETH.approve(address(L2Messenger), 100);
        uint256 supply = l2ETH.totalSupply();
        uint256 balance = alice.balance;
        uint256 messengerNonce = L2Messenger.messageNonce();
        uint256 passerNonce = messagePasser.messageNonce();
        for (uint256 i; i < 2; ++i) {
            bytes memory message = new bytes(i == 0 ? 0 : 89_729);
            bytes memory reason = bytes(
                i == 0 ? "L2CrossDomainMessenger: gas limit too high" : "L2CrossDomainMessenger: message too large"
            );
            vm.expectRevert(reason);
            vm.prank(alice);
            L2Messenger.sendMessage{ value: 100 }(100, RECIPIENT, message, 15_300_000);
            assertEq(l2ETH.balanceOf(alice), 100);
            assertEq(l2ETH.allowance(alice, address(L2Messenger)), 100);
            assertEq(l2ETH.totalSupply(), supply);
            assertEq(alice.balance, balance);
        }
        assertEq(L2Messenger.messageNonce(), messengerNonce);
        assertEq(messagePasser.messageNonce(), passerNonce);
    }
}

contract L2CrossDomainMessenger_Test is Messenger_Initializer {
    // Receiver address for testing
    address recipient = address(0xabbaacdc);

    function test_messageVersion_succeeds() external view {
        (, uint16 version) = Encoding.decodeVersionedNonce(L2Messenger.messageNonce());
        assertEq(version, L2Messenger.MESSAGE_VERSION());
    }

    function test_l1CrossDomainMessenger_succeeds() external view {
        assertEq(address(L1Messenger), L2Messenger.l1CrossDomainMessenger());
    }

    function test_sendMessage_succeeds() external {
        bytes memory xDomainCallData =
            Encoding.encodeCrossDomainMessage(L2Messenger.messageNonce(), alice, recipient, 0, 0, 100, hex"ff");
        vm.expectCall(
            address(messagePasser),
            abi.encodeCall(
                L2ToL1MessagePasser.initiateWithdrawal,
                (0, address(L1Messenger), L2Messenger.baseGas(hex"ff", 100), xDomainCallData)
            )
        );

        // MessagePassed event
        vm.expectEmit(true, true, true, true);
        emit MessagePassed(
            messagePasser.messageNonce(),
            address(L2Messenger),
            address(L1Messenger),
            0,
            0,
            L2Messenger.baseGas(hex"ff", 100),
            xDomainCallData,
            Hashing.hashWithdrawal(
                Types.WithdrawalTransaction({
                    nonce: messagePasser.messageNonce(),
                    sender: address(L2Messenger),
                    target: address(L1Messenger),
                    mntValue: 0,
                    ethValue: 0,
                    gasLimit: L2Messenger.baseGas(hex"ff", 100),
                    data: xDomainCallData
                })
            )
        );

        vm.prank(alice);
        L2Messenger.sendMessage(0, recipient, hex"ff", uint32(100));
    }

    function test_sendMessage_with_eth_value_succeeds() external {
        deal(address(l2ETH), alice, 100);
        vm.store(address(l2ETH), bytes32(uint256(0x2)), bytes32(uint256(100))); //set total supply

        vm.prank(alice);
        l2ETH.approve(address(messagePasser), 100);
        vm.prank(alice);
        l2ETH.approve(address(L2Messenger), 100);

        bytes memory xDomainCallData =
            Encoding.encodeCrossDomainMessage(L2Messenger.messageNonce(), alice, recipient, 0, 100, 100, hex"ff");
        vm.expectCall(
            address(messagePasser),
            abi.encodeCall(
                L2ToL1MessagePasser.initiateWithdrawal,
                (100, address(L1Messenger), L2Messenger.baseGas(hex"ff", 100), xDomainCallData)
            )
        );

        // MessagePassed event
        vm.expectEmit(true, true, true, true);
        emit MessagePassed(
            messagePasser.messageNonce(),
            address(L2Messenger),
            address(L1Messenger),
            0,
            100,
            L2Messenger.baseGas(hex"ff", 100),
            xDomainCallData,
            Hashing.hashWithdrawal(
                Types.WithdrawalTransaction({
                    nonce: messagePasser.messageNonce(),
                    sender: address(L2Messenger),
                    target: address(L1Messenger),
                    mntValue: 0,
                    ethValue: 100,
                    gasLimit: L2Messenger.baseGas(hex"ff", 100),
                    data: xDomainCallData
                })
            )
        );

        vm.prank(alice);
        L2Messenger.sendMessage(100, recipient, hex"ff", uint32(100));
    }

    function test_sendMessage_with_mnt_value_succeeds() external {
        vm.deal(alice, 100);
        bytes memory xDomainCallData =
            Encoding.encodeCrossDomainMessage(L2Messenger.messageNonce(), alice, recipient, 100, 0, 100, hex"ff");
        vm.expectCall(
            address(messagePasser),
            100,
            abi.encodeCall(
                L2ToL1MessagePasser.initiateWithdrawal,
                (0, address(L1Messenger), L2Messenger.baseGas(hex"ff", 100), xDomainCallData)
            )
        );

        // MessagePassed event
        vm.expectEmit(true, true, true, true);
        emit MessagePassed(
            messagePasser.messageNonce(),
            address(L2Messenger),
            address(L1Messenger),
            100,
            0,
            L2Messenger.baseGas(hex"ff", 100),
            xDomainCallData,
            Hashing.hashWithdrawal(
                Types.WithdrawalTransaction({
                    nonce: messagePasser.messageNonce(),
                    sender: address(L2Messenger),
                    target: address(L1Messenger),
                    mntValue: 100,
                    ethValue: 0,
                    gasLimit: L2Messenger.baseGas(hex"ff", 100),
                    data: xDomainCallData
                })
            )
        );

        vm.prank(alice);
        L2Messenger.sendMessage{ value: 100 }(0, recipient, hex"ff", uint32(100));
    }

    function test_sendMessage_twice_succeeds() external {
        uint256 nonce = L2Messenger.messageNonce();
        L2Messenger.sendMessage(0, recipient, hex"aa", uint32(500_000));
        L2Messenger.sendMessage(0, recipient, hex"aa", uint32(500_000));
        // the nonce increments for each message sent
        assertEq(nonce + 2, L2Messenger.messageNonce());
    }

    function test_xDomainSender_senderNotSet_reverts() external {
        vm.expectRevert("CrossDomainMessenger: xDomainMessageSender is not set");
        L2Messenger.xDomainMessageSender();
    }

    function test_relayMessage_v2_reverts() external {
        address target = address(0xabcd);
        address sender = address(L1Messenger);
        address caller = AddressAliasHelper.applyL1ToL2Alias(address(L1Messenger));

        // Expect a revert.
        vm.expectRevert("CrossDomainMessenger: only version 0 or 1 messages are supported at this time");

        // Try to relay a v2 message.
        vm.prank(caller);
        L2Messenger.relayMessage(
            Encoding.encodeVersionedNonce(0, 2), // nonce
            sender,
            target,
            0, // value
            0,
            0,
            hex"1111"
        );
    }

    function test_relayMessage_succeeds() external {
        address target = address(0xabcd);
        address sender = address(L1Messenger);
        address caller = AddressAliasHelper.applyL1ToL2Alias(address(L1Messenger));

        vm.expectCall(target, hex"1111");

        vm.prank(caller);

        vm.expectEmit(true, true, true, true);

        bytes32 hash =
            Hashing.hashCrossDomainMessage(Encoding.encodeVersionedNonce(0, 1), sender, target, 0, 0, 0, hex"1111");

        emit RelayedMessage(hash);

        L2Messenger.relayMessage(
            Encoding.encodeVersionedNonce(0, 1), // nonce
            sender,
            target,
            0, // value
            0,
            0,
            hex"1111"
        );

        // the message hash is in the successfulMessages mapping
        assert(L2Messenger.successfulMessages(hash));
        // it is not in the received messages mapping
        assertEq(L2Messenger.failedMessages(hash), false);
    }

    function test_relayMessage_with_eth_value_succeeds() external {
        address target = address(0xabcd);
        address sender = address(L1Messenger);
        address caller = AddressAliasHelper.applyL1ToL2Alias(address(L1Messenger));

        deal(address(l2ETH), address(L2Messenger), 100);
        vm.store(address(l2ETH), bytes32(uint256(0x2)), bytes32(uint256(100))); //set total supply

        vm.expectCall(target, hex"1111");

        vm.prank(caller);

        vm.expectEmit(true, true, true, true);

        bytes32 hash =
            Hashing.hashCrossDomainMessage(Encoding.encodeVersionedNonce(0, 1), sender, target, 0, 100, 0, hex"1111");

        emit RelayedMessage(hash);

        L2Messenger.relayMessage(
            Encoding.encodeVersionedNonce(0, 1), // nonce
            sender,
            target,
            0, // value
            100,
            0,
            hex"1111"
        );

        // the message hash is in the successfulMessages mapping
        assert(L2Messenger.successfulMessages(hash));
        // it is not in the received messages mapping
        assertEq(L2Messenger.failedMessages(hash), false);
    }

    // relayMessage: should revert if attempting to relay a message sent to an L1 system contract
    function test_relayMessage_toSystemContract_reverts() external {
        address target = address(messagePasser);
        address sender = address(L1Messenger);
        address caller = AddressAliasHelper.applyL1ToL2Alias(address(L1Messenger));
        bytes memory message = hex"1111";

        vm.prank(caller);
        vm.expectRevert("CrossDomainMessenger: message cannot be replayed");
        L1Messenger.relayMessage(Encoding.encodeVersionedNonce(0, 1), sender, target, 0, 0, 0, message);
    }

    // relayMessage: the xDomainMessageSender is reset to the original value
    function test_xDomainMessageSender_reset_succeeds() external {
        vm.expectRevert("CrossDomainMessenger: xDomainMessageSender is not set");
        L2Messenger.xDomainMessageSender();

        address caller = AddressAliasHelper.applyL1ToL2Alias(address(L1Messenger));
        vm.prank(caller);
        L2Messenger.relayMessage(Encoding.encodeVersionedNonce(0, 1), address(0), address(0), 0, 0, 0, hex"");

        vm.expectRevert("CrossDomainMessenger: xDomainMessageSender is not set");
        L2Messenger.xDomainMessageSender();
    }

    // relayMessage: should send a successful call to the target contract after the first message
    // fails and ETH gets stuck, but the second message succeeds
    function test_relayMessage_retry_succeeds() external {
        address target = address(0xabcd);
        address sender = address(L1Messenger);
        address caller = AddressAliasHelper.applyL1ToL2Alias(address(L1Messenger));
        uint256 value = 100;

        bytes32 hash =
            Hashing.hashCrossDomainMessage(Encoding.encodeVersionedNonce(0, 1), sender, target, value, 0, 0, hex"1111");

        vm.etch(target, address(new Reverter()).code);
        vm.deal(address(caller), value);
        vm.prank(caller);
        L2Messenger.relayMessage{ value: value }(
            Encoding.encodeVersionedNonce(0, 1), // nonce
            sender,
            target,
            value,
            0,
            0,
            hex"1111"
        );

        assertEq(address(L2Messenger).balance, value);
        assertEq(address(target).balance, 0);
        assertEq(L2Messenger.successfulMessages(hash), false);
        assertEq(L2Messenger.failedMessages(hash), true);

        vm.expectEmit(true, true, true, true);

        emit RelayedMessage(hash);

        vm.etch(target, address(0).code);
        vm.prank(address(sender));
        L2Messenger.relayMessage(
            Encoding.encodeVersionedNonce(0, 1), // nonce
            sender,
            target,
            value,
            0,
            0,
            hex"1111"
        );

        assertEq(address(L2Messenger).balance, 0);
        assertEq(address(target).balance, value);
        assertEq(L2Messenger.successfulMessages(hash), true);
        assertEq(L2Messenger.failedMessages(hash), true);
    }
}
