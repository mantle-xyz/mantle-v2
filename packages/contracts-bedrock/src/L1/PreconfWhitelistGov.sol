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

    /// @notice Gas the computed limit buys per rule. A pair add measures 68,044 on L2 — the worst
    ///         per-rule cost — so this carries a 2.9% margin, and a full batch buys 18.2M of the 20M
    ///         a deposit can. Raising `MAX_BATCH` past 281 breaks that; see the test.
    uint64 public constant GAS_PER_RULE = 70_000;

    /// @notice Gas a rotation buys on L2 above the portal's calldata floor. `setAuthorizedL1` is one
    ///         SSTORE plus an event — 2,769 measured warm, so this is an order of magnitude of
    ///         margin. It takes no array, so there is nothing for a caller to size.
    uint64 public constant ROTATE_GAS = 60_000;

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

    /// @notice Relays a rule delta to L2, sizing the gas from the batch so that nothing about gas is
    ///         left to the caller. Verify on L2 all the same: a starved deposit fails there while
    ///         this L1 call still succeeds, and no permissionless replay exists to recover through.
    /// @param _add    Rules to authorize.
    /// @param _remove Rules to revoke.
    function updatePreconfs(
        PreconfWhitelist.Rule[] calldata _add,
        PreconfWhitelist.Rule[] calldata _remove
    )
        external
        onlyOwner
    {
        _update(_add, _remove, 0);
    }

    /// @notice The same, with the L2 gas limit named explicitly. For when the L2 side has been
    ///         redeployed with costs [`GAS_PER_RULE`] no longer describes — a separate function
    ///         rather than a sentinel so that the multisig queue shows which path was taken.
    /// @param _add      Rules to authorize.
    /// @param _remove   Rules to revoke.
    /// @param _gasLimit L2 gas to buy. Zero is rejected: that is [`updatePreconfs`]'s job.
    function updatePreconfsWithGasLimit(
        PreconfWhitelist.Rule[] calldata _add,
        PreconfWhitelist.Rule[] calldata _remove,
        uint64 _gasLimit
    )
        external
        onlyOwner
    {
        require(_gasLimit != 0, "PreconfWhitelistGov: gas limit is zero");

        _update(_add, _remove, _gasLimit);
    }

    /// @notice Bounds the batch, encodes it, and buys the deposit.
    /// @param _add      Rules to authorize.
    /// @param _remove   Rules to revoke.
    /// @param _gasLimit L2 gas to buy, or zero to size it at [`GAS_PER_RULE`] per rule on top of the
    ///                  portal's own calldata floor.
    function _update(
        PreconfWhitelist.Rule[] calldata _add,
        PreconfWhitelist.Rule[] calldata _remove,
        uint64 _gasLimit
    )
        internal
    {
        uint256 count = _add.length + _remove.length;
        require(count <= MAX_BATCH, "PreconfWhitelistGov: batch too large");

        bytes memory data = abi.encodeCall(PreconfWhitelist.updatePreconfs, (_add, _remove));
        if (_gasLimit == 0) {
            _gasLimit = PORTAL.minimumGasLimit(uint64(data.length)) + GAS_PER_RULE * uint64(count);
        }
        _deposit(data, _gasLimit);
    }

    /// @notice Hands governance of the L2 allowlist to another L1 address. One-way and unconfirmed.
    ///         The has-code check is the one guard only L1 can run — L2 cannot inspect an L1
    ///         address, and its gate admits only aliased (i.e. contract) senders.
    /// @param _authorizedL1 New L1 governance address, unaliased. Must already hold code and differ
    ///                      from this contract; the checks filter EOAs, typos and the no-op rotation
    ///                      that would still emit `AuthorizedL1Updated` on L2, not every wrong one.
    function rotateAuthorizedL1(address _authorizedL1) external onlyOwner {
        require(_authorizedL1 != address(0), "PreconfWhitelistGov: authorized L1 sender is the zero address");
        require(_authorizedL1 != address(this), "PreconfWhitelistGov: authorized L1 sender is this contract");
        require(_authorizedL1.code.length > 0, "PreconfWhitelistGov: authorized L1 sender is not a contract");

        bytes memory data = abi.encodeCall(PreconfWhitelist.setAuthorizedL1, (_authorizedL1));
        _deposit(data, PORTAL.minimumGasLimit(uint64(data.length)) + ROTATE_GAS);
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
