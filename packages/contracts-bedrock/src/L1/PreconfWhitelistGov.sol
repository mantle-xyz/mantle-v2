// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { Ownable } from "@openzeppelin/contracts/access/Ownable.sol";
import { OptimismPortal } from "./OptimismPortal.sol";
import { PreconfWhitelist, PRECONF_MAX_BATCH } from "../L2/PreconfWhitelist.sol";

/// @title PreconfWhitelistGov
/// @notice L1 endpoint governing `PreconfWhitelist`; it is that contract's `authorizedL1`. Deposits
///         via OptimismPortal, never the messenger: a delta needs exactly-once, in-order delivery.
contract PreconfWhitelistGov is Ownable {
    /// @notice The same constant `PreconfWhitelist` caps a batch with, imported rather than copied
    ///         so the two cannot drift. An oversized batch reverts on L2 only after the deposit is
    ///         bought, so rejecting it here — where the length is plain calldata — is free.
    uint256 public constant MAX_BATCH = PRECONF_MAX_BATCH;

    /// @notice The OptimismPortal this contract deposits through. Immutable — it does not move.
    // nosemgrep: sol-safety-no-immutable-variables -- this contract is not proxied
    OptimismPortal public immutable PORTAL;

    /// @notice The L2 `PreconfWhitelist` under governance. Settable: it cannot exist at construction
    ///         time, because it takes our address.
    address public whitelist;

    /// @notice Emitted when the governed L2 contract is set or repointed.
    /// @param whitelist The L2 `PreconfWhitelist` this contract now governs.
    event WhitelistSet(address indexed whitelist);

    /// @notice The L1-side record of every deposit sent. The L2 outcome is not observable from here.
    /// @param target   L2 contract the deposit is addressed to.
    /// @param gasLimit L2 gas bought for it.
    /// @param data     Calldata the L2 transaction will carry.
    event GovernanceDeposited(address indexed target, uint64 gasLimit, bytes data);

    /// @notice Wires this endpoint to its portal and owner.
    /// @param _portal The OptimismPortal to deposit through.
    /// @param _owner  Address permitted to govern, normally the governance Safe.
    constructor(OptimismPortal _portal, address _owner) {
        require(address(_portal) != address(0), "PreconfWhitelistGov: portal is the zero address");
        require(_owner != address(0), "PreconfWhitelistGov: owner is the zero address");
        PORTAL = _portal;
        _transferOwnership(_owner);
    }

    /// @notice Points this contract at the L2 `PreconfWhitelist` it governs.
    /// @param _whitelist L2 address. Not checked for code — it lives on the other chain, so nothing
    ///                   here could check it.
    function setWhitelist(address _whitelist) external onlyOwner {
        whitelist = _whitelist;
        emit WhitelistSet(_whitelist);
    }

    /// @notice Relays a rule delta to L2. A starved `_gasLimit` fails on L2 while this L1 call still
    ///         succeeds, and there is no permissionless replay to recover through — so verify on L2,
    ///         and re-send larger if it did not land.
    /// @param _add      Rules to authorize.
    /// @param _remove   Rules to revoke.
    /// @param _gasLimit L2 gas to buy. Not checked here beyond the portal's own `minimumGasLimit`:
    ///                  what a batch costs is a property of the L2 bytecode, which this side cannot
    ///                  measure. Size it from the `MAX_BATCH` table in `PreconfWhitelist`.
    function updatePreconfs(
        PreconfWhitelist.Rule[] calldata _add,
        PreconfWhitelist.Rule[] calldata _remove,
        uint64 _gasLimit
    )
        external
        onlyOwner
    {
        require(_add.length + _remove.length <= MAX_BATCH, "PreconfWhitelistGov: batch too large");

        _deposit(abi.encodeCall(PreconfWhitelist.updatePreconfs, (_add, _remove)), _gasLimit);
    }

    /// @notice Hands governance of the L2 allowlist to another L1 address. One-way and unconfirmed.
    ///         The has-code check is the one guard only L1 can run — L2 cannot inspect an L1
    ///         address, and its gate admits only aliased (i.e. contract) senders.
    /// @param _authorizedL1 New L1 governance address, unaliased. Must already hold code; the check
    ///                      filters EOAs and typos, not every wrong contract.
    /// @param _gasLimit     L2 gas to buy; `setAuthorizedL1` is one packed SSTORE plus an event.
    function rotateAuthorizedL1(address _authorizedL1, uint64 _gasLimit) external onlyOwner {
        require(_authorizedL1 != address(0), "PreconfWhitelistGov: authorized L1 sender is the zero address");
        require(_authorizedL1.code.length > 0, "PreconfWhitelistGov: authorized L1 sender is not a contract");

        _deposit(abi.encodeCall(PreconfWhitelist.setAuthorizedL1, (_authorizedL1)), _gasLimit);
    }

    /// @notice Buys one L2 deposit addressed at [`whitelist`]. All four value arguments are zero: a
    ///         non-zero `_mntValue` would make the portal pull MNT from this contract.
    /// @param _data     Calldata for the L2 transaction.
    /// @param _gasLimit L2 gas to buy, metered directly against `maxResourceLimit` with no
    ///                  `baseGas` conversion on this channel.
    function _deposit(bytes memory _data, uint64 _gasLimit) internal {
        address target = whitelist;
        require(target != address(0), "PreconfWhitelistGov: whitelist not set");

        emit GovernanceDeposited(target, _gasLimit, _data);
        PORTAL.depositTransaction({
            _ethTxValue: 0,
            _mntValue: 0,
            _to: target,
            _mntTxValue: 0,
            _gasLimit: _gasLimit,
            _isCreation: false,
            _data: _data
        });
    }

    /// @notice Always reverts. Renouncing would strand the L2 allowlist: only this contract can
    ///         write to it, and reaching it needs an owner. [`rotateAuthorizedL1`] is the way out.
    function renounceOwnership() public pure override {
        revert("PreconfWhitelistGov: ownership cannot be renounced");
    }
}
