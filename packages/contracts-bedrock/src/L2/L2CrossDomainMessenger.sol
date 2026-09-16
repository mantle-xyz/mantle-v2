// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { AddressAliasHelper } from "../vendor/AddressAliasHelper.sol";
import { Predeploys } from "../libraries/Predeploys.sol";
import { CrossDomainMessenger } from "../universal/CrossDomainMessenger.sol";
import { Semver } from "../universal/Semver.sol";
import { L2ToL1MessagePasser } from "./L2ToL1MessagePasser.sol";
import { SafeCall } from "../libraries/SafeCall.sol";
import { Hashing } from "../libraries/Hashing.sol";
import { Encoding } from "../libraries/Encoding.sol";
import { Constants } from "../libraries/Constants.sol";
import { L1CrossDomainMessenger } from "../L1/L1CrossDomainMessenger.sol";
import { IERC20 } from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import { SafeERC20 } from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";

/// @custom:proxied
/// @custom:predeploy 0x4200000000000000000000000000000000000007
/// @title L2CrossDomainMessenger
/// @notice The L2CrossDomainMessenger is a high-level interface for message passing between L1 and
///         L2 on the L2 side. Users are generally encouraged to use this contract instead of lower
///         level message passing contracts.
contract L2CrossDomainMessenger is CrossDomainMessenger, Semver {
    using SafeERC20 for IERC20;

    /// @notice Address of the Mantle Token on L1.
    address public immutable L1_MNT_ADDRESS;

    /// @custom:semver 1.6.0
    ///
    /// @param _l1CrossDomainMessenger Address of the L1CrossDomainMessenger contract.
    constructor(
        address _l1CrossDomainMessenger,
        address l1mnt
    )
        Semver(1, 6, 0)
        CrossDomainMessenger(_l1CrossDomainMessenger)
    {
        L1_MNT_ADDRESS = l1mnt;
        initialize();
    }

    /// @notice Initializer.
    function initialize() public initializer {
        __CrossDomainMessenger_init();
    }

    /// @custom:legacy
    /// @notice Legacy getter for the remote messenger. Use otherMessenger going forward.
    ///
    /// @return Address of the L1CrossDomainMessenger contract.
    function l1CrossDomainMessenger() public view returns (address) {
        return OTHER_MESSENGER;
    }

    /// @inheritdoc CrossDomainMessenger
    function _sendMessage(uint256 _ethValue, address _to, uint64 _gasLimit, bytes memory _data) internal override {
        L2ToL1MessagePasser(payable(Predeploys.L2_TO_L1_MESSAGE_PASSER)).initiateWithdrawal{ value: msg.value }(
            _ethValue, _to, _gasLimit, _data
        );
    }

    /// @inheritdoc CrossDomainMessenger
    function sendMessage(
        uint256 _ethAmount,
        address _target,
        bytes calldata _message,
        uint32 _minGasLimit
    )
        external
        payable
        override
    {
        require(_target != tx.origin || msg.value == 0, "once target is an EOA, msg.value must be zero");
        require(_target != L1_MNT_ADDRESS, "target must not be MNT address on L1");
        if (_ethAmount != 0) {
            IERC20(Predeploys.BVM_ETH).safeTransferFrom(msg.sender, address(this), _ethAmount);
        }

        // Triggers a message to the other messenger. Note that the amount of gas provided to the
        // message is the amount of gas requested by the user PLUS the base gas value. We want to
        // guarantee the property that the call to the target contract will always have at least
        // the minimum gas limit specified by the user.
        _sendMessage(
            _ethAmount,
            OTHER_MESSENGER,
            baseGas(_message, _minGasLimit),
            abi.encodeWithSelector(
                L1CrossDomainMessenger.relayMessage.selector,
                messageNonce(),
                msg.sender,
                _target,
                msg.value,
                _ethAmount,
                _minGasLimit,
                _message
            )
        );

        emit SentMessage(_target, msg.sender, _message, messageNonce(), _minGasLimit);
        emit SentMessageExtension1(msg.sender, msg.value, _ethAmount);

        unchecked {
            ++msgNonce;
        }
    }

    /// @inheritdoc CrossDomainMessenger
    function sendMessage(address _target, bytes calldata _message, uint32 _minGasLimit) external payable override {
        require(_target != tx.origin || msg.value == 0, "once target is an EOA, msg.value must be zero");
        require(_target != L1_MNT_ADDRESS, "target must not be MNT address on L1");

        // Triggers a message to the other messenger. Note that the amount of gas provided to the
        // message is the amount of gas requested by the user PLUS the base gas value. We want to
        // guarantee the property that the call to the target contract will always have at least
        // the minimum gas limit specified by the user.
        _sendMessage(
            0,
            OTHER_MESSENGER,
            baseGas(_message, _minGasLimit),
            abi.encodeWithSelector(
                L1CrossDomainMessenger.relayMessage.selector,
                messageNonce(),
                msg.sender,
                _target,
                msg.value,
                0,
                _minGasLimit,
                _message
            )
        );

        emit SentMessage(_target, msg.sender, _message, messageNonce(), _minGasLimit);
        emit SentMessageExtension1(msg.sender, msg.value, 0);

        unchecked {
            ++msgNonce;
        }
    }

    /// @notice Relays a message that was sent by the other CrossDomainMessenger contract. Can only
    ///         be executed via cross-chain call from the other messenger OR if the message was
    ///         already received once and is currently being replayed.
    ///
    /// @param _nonce       Nonce of the message being relayed.
    /// @param _sender      Address of the user who sent the message.
    /// @param _target      Address that the message is targeted at.
    /// @param _mntValue    MNT value to send with the message.
    /// @param _ethValue    ETH value to send with the message.
    /// @param _minGasLimit Minimum amount of gas that the message can be executed with.
    /// @param _message     Message to send to the target.
    function relayMessage(
        uint256 _nonce,
        address _sender,
        address _target,
        uint256 _mntValue,
        uint256 _ethValue,
        uint256 _minGasLimit,
        bytes calldata _message
    )
        external
        payable
        override
    {
        (, uint16 version) = Encoding.decodeVersionedNonce(_nonce);
        require(version < 2, "CrossDomainMessenger: only version 0 or 1 messages are supported at this time");

        // If the message is version 0, then it's a migrated legacy withdrawal. We therefore need
        // to check that the legacy version of the message has not already been relayed.
        if (version == 0) {
            bytes32 oldHash = Hashing.hashCrossDomainMessageV0(_target, _sender, _message, _nonce);
            require(successfulMessages[oldHash] == false, "CrossDomainMessenger: legacy withdrawal already relayed");
        }

        // We use the v1 message hash as the unique identifier for the message because it commits
        // to the value and minimum gas limit of the message.
        bytes32 versionedHash =
            Hashing.hashCrossDomainMessageV1(_nonce, _sender, _target, _mntValue, _ethValue, _minGasLimit, _message);

        if (_isOtherMessenger()) {
            // These properties should always hold when the message is first submitted (as
            // opposed to being replayed).
            assert(msg.value == _mntValue);
            assert(!failedMessages[versionedHash]);
        } else {
            require(msg.value == 0, "CrossDomainMessenger: value must be zero unless message is from a system address");

            require(failedMessages[versionedHash], "CrossDomainMessenger: message cannot be replayed");
        }

        require(
            _isUnsafeTarget(_target) == false, "CrossDomainMessenger: cannot send message to blocked system address"
        );

        require(successfulMessages[versionedHash] == false, "CrossDomainMessenger: message has already been relayed");

        // If there is not enough gas left to perform the external call and finish the execution,
        // return early and assign the message to the failedMessages mapping.
        // We are asserting that we have enough gas to:
        // 1. Call the target contract, including approval and recipient account creation
        //    (_minGasLimit + RELAY_CALL_OVERHEAD + RELAY_GAS_CHECK_BUFFER + RELAY_NEW_ACCOUNT_OVERHEAD).
        //   1.a. The RELAY_CALL_OVERHEAD is included in `hasMinGas`.
        // 2. Finish the execution after the external call (RELAY_RESERVED_GAS).
        //
        // If `xDomainMsgSender` is not the default L2 sender, this function
        // is being re-entered. This marks the message as failed to allow it to be replayed.
        if (
            !SafeCall.hasMinGas(_minGasLimit, RELAY_RESERVED_GAS + RELAY_GAS_CHECK_BUFFER + RELAY_NEW_ACCOUNT_OVERHEAD)
                || xDomainMsgSender != Constants.DEFAULT_L2_SENDER
        ) {
            failedMessages[versionedHash] = true;
            emit FailedRelayedMessage(versionedHash);

            // Revert in this case if the transaction was triggered by the estimation address. This
            // should only be possible during gas estimation or we have bigger problems. Reverting
            // here will make the behavior of gas estimation change such that the gas limit
            // computed will be the amount required to relay the message, even if that amount is
            // greater than the minimum gas limit specified by the user.
            if (tx.origin == Constants.ESTIMATION_ADDRESS) {
                revert("CrossDomainMessenger: failed to relay message");
            }

            return;
        }
        if (_ethValue != 0) {
            // The ethSuccess variable of approve is either true or the approve function reverted.
            // It will never be false whenever its value is evaluated.
            IERC20(Predeploys.BVM_ETH).approve(_target, _ethValue);
        }
        xDomainMsgSender = _sender;
        bool success = SafeCall.call(_target, gasleft() - RELAY_RESERVED_GAS, _mntValue, _message);
        xDomainMsgSender = Constants.DEFAULT_L2_SENDER;
        if (_ethValue != 0) {
            IERC20(Predeploys.BVM_ETH).approve(_target, 0);
        }

        if (success) {
            require(!successfulMessages[versionedHash], "versionedHash has already be marked as successful");
            successfulMessages[versionedHash] = true;
            emit RelayedMessage(versionedHash);
        } else {
            failedMessages[versionedHash] = true;
            emit FailedRelayedMessage(versionedHash);

            // Revert in this case if the transaction was triggered by the estimation address. This
            // should only be possible during gas estimation or we have bigger problems. Reverting
            // here will make the behavior of gas estimation change such that the gas limit
            // computed will be the amount required to relay the message, even if that amount is
            // greater than the minimum gas limit specified by the user.
            if (tx.origin == Constants.ESTIMATION_ADDRESS) {
                revert("CrossDomainMessenger: failed to relay message");
            }
        }
    }

    /// @inheritdoc CrossDomainMessenger
    function _isOtherMessenger() internal view override returns (bool) {
        return AddressAliasHelper.undoL1ToL2Alias(msg.sender) == OTHER_MESSENGER;
    }

    /// @inheritdoc CrossDomainMessenger
    function _isUnsafeTarget(address _target) internal view override returns (bool) {
        return _target == address(this) || _target == address(Predeploys.L2_TO_L1_MESSAGE_PASSER);
    }
}
