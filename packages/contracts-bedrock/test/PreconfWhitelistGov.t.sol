// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { Portal_Initializer } from "./CommonTest.t.sol";
import { AddressAliasHelper } from "src/vendor/AddressAliasHelper.sol";
import { OptimismPortal } from "src/L1/OptimismPortal.sol";
import { PreconfWhitelist } from "src/L2/PreconfWhitelist.sol";
import { PreconfWhitelistGov } from "src/L1/PreconfWhitelistGov.sol";

/// @title PreconfWhitelistGov_Test
/// @notice Tests the L1 endpoint that governs `PreconfWhitelist`. Three things are worth asserting
///         here and are not covered by the L2 suite: the two guards only this side can run (the
///         batch cap and the has-code check), the exact shape of the deposit it buys, and — the
///         cross-chain seam — that the `from` the portal derives is the address the L2 gate admits.
contract PreconfWhitelistGov_Test is Portal_Initializer {
    /// @notice `OptimismPortal.DEPOSIT_VERSION`. Internal there, so it is restated rather than read.
    uint256 internal constant DEPOSIT_VERSION = 1;

    address internal constant OWNER = address(0xA5A5);

    PreconfWhitelistGov internal gov;
    address internal whitelistL2;

    // `TransactionDeposited` is inherited from `CommonTest`, which declares it for the portal.
    event WhitelistSet(address indexed whitelist);
    event GovernanceDeposited(address indexed target, uint64 gasLimit, bytes data);

    function setUp() public virtual override {
        super.setUp();
        gov = new PreconfWhitelistGov(op, OWNER);
        whitelistL2 = address(0xBEEF);
        vm.prank(OWNER);
        gov.setWhitelist(whitelistL2);
    }

    // ===== helpers =====

    function _one(address _from, address _to) internal pure returns (PreconfWhitelist.Rule[] memory out_) {
        out_ = new PreconfWhitelist.Rule[](1);
        out_[0] = PreconfWhitelist.Rule({ from: _from, to: _to });
    }

    function _none() internal pure returns (PreconfWhitelist.Rule[] memory out_) {
        out_ = new PreconfWhitelist.Rule[](0);
    }

    /// @notice The `opaqueData` the portal emits for a zero-value deposit carrying `_data`.
    /// @dev    Mirrors `OptimismPortal.depositTransaction`'s
    ///         `abi.encodePacked(_mntValue, _mntTxValue, msg.value, _ethTxValue, _gasLimit,
    ///         _isCreation, _data)`. Written out in full rather than pulled from a helper so that a
    ///         value silently becoming non-zero — MNT in particular, which the portal would pull
    ///         from the governance contract — fails the assertion.
    function _opaque(uint64 _gasLimit, bytes memory _data) internal pure returns (bytes memory) {
        return abi.encodePacked(uint256(0), uint256(0), uint256(0), uint256(0), _gasLimit, false, _data);
    }

    // ===== constructor =====

    function test_constructor_setsPortalAndOwner_succeeds() external view {
        assertEq(address(gov.PORTAL()), address(op));
        assertEq(gov.owner(), OWNER);
    }

    /// @notice Both caps resolve to the file-level `PRECONF_MAX_BATCH`, so they cannot drift at the
    ///         source. Asserted against a real deployment rather than the literal 256: that is what
    ///         would still catch someone re-hardcoding either side later.
    function test_maxBatch_isTheSameConstantAsTheL2Contract_succeeds() external {
        PreconfWhitelist wl = new PreconfWhitelist(address(0xA11CE), _none());
        assertEq(gov.MAX_BATCH(), wl.MAX_BATCH());
    }

    function test_constructor_zeroPortal_reverts() external {
        vm.expectRevert("PreconfWhitelistGov: portal is the zero address");
        new PreconfWhitelistGov(OptimismPortal(payable(address(0))), OWNER);
    }

    function test_constructor_zeroOwner_reverts() external {
        vm.expectRevert("PreconfWhitelistGov: owner is the zero address");
        new PreconfWhitelistGov(op, address(0));
    }

    // ===== setWhitelist =====

    function test_setWhitelist_succeeds() external {
        vm.expectEmit(true, true, true, true, address(gov));
        emit WhitelistSet(address(0xC0FFEE));

        vm.prank(OWNER);
        gov.setWhitelist(address(0xC0FFEE));
        assertEq(gov.whitelist(), address(0xC0FFEE));
    }

    function test_setWhitelist_notOwner_reverts() external {
        vm.prank(alice);
        vm.expectRevert("Ownable: caller is not the owner");
        gov.setWhitelist(address(0xC0FFEE));
    }

    // ===== the deposit itself =====

    /// @notice **The cross-chain seam.** The portal aliases this contract on the way out, and the
    ///         address it derives has to be the one `PreconfWhitelist.onlyL1Gov` admits — that gate
    ///         undoes the alias and compares against `authorizedL1`. Asserting the round trip here
    ///         is what ties the two repositories' halves together; each side alone proves nothing.
    ///
    ///         The aliasing happens at all only because this contract is a contract: the portal
    ///         transforms the depositor exactly when `msg.sender != tx.origin`. That is the same
    ///         property `rotateAuthorizedL1`'s has-code check exists to preserve.
    function test_updatePreconfs_depositFromIsTheAliasTheL2GateExpects_succeeds() external {
        address expectedFrom = AddressAliasHelper.applyL1ToL2Alias(address(gov));

        // `from` is an indexed topic, so a mismatch fails here. Data is not checked — the payload
        // has its own test; this one is about the address the portal derives.
        vm.expectEmit(true, true, true, false, address(op));
        emit TransactionDeposited(expectedFrom, whitelistL2, DEPOSIT_VERSION, "");

        uint64 gasLimit = 200_000;
        vm.prank(OWNER);
        gov.updatePreconfs(_one(address(0x1111), address(0x2222)), _none(), gasLimit);

        // And the transform `PreconfWhitelist.onlyL1Gov` applies inverts it back to this contract,
        // which is what `authorizedL1` is set to. This is the round trip the two repos share.
        assertEq(AddressAliasHelper.undoL1ToL2Alias(expectedFrom), address(gov));
        assertTrue(expectedFrom != address(gov), "the alias must actually move the address");
    }

    /// @notice The whole deposit, field by field. The four value arguments must be zero — a non-zero
    ///         `_mntValue` would make the portal pull MNT from this contract — and the calldata must
    ///         be exactly the `updatePreconfs` call, since that is what op-reth decodes.
    function test_updatePreconfs_depositsExactPayload_succeeds() external {
        PreconfWhitelist.Rule[] memory add = _one(address(0x1111), address(0x2222));
        uint64 gasLimit = 200_000;
        bytes memory data = abi.encodeCall(PreconfWhitelist.updatePreconfs, (add, _none()));

        vm.expectEmit(true, true, true, true, address(op));
        emit TransactionDeposited(
            AddressAliasHelper.applyL1ToL2Alias(address(gov)), whitelistL2, DEPOSIT_VERSION, _opaque(gasLimit, data)
        );

        vm.prank(OWNER);
        gov.updatePreconfs(add, _none(), 200_000);
    }

    /// @notice The L1-side record, so an operator can see what was asked for without decoding the
    ///         portal's packed `opaqueData`.
    function test_updatePreconfs_emitsGovernanceDeposited_succeeds() external {
        PreconfWhitelist.Rule[] memory add = _one(address(0x1111), address(0x2222));
        uint64 gasLimit = 200_000;

        vm.expectEmit(true, true, true, true, address(gov));
        emit GovernanceDeposited(whitelistL2, gasLimit, abi.encodeCall(PreconfWhitelist.updatePreconfs, (add, _none())));

        vm.prank(OWNER);
        gov.updatePreconfs(add, _none(), 200_000);
    }

    function test_updatePreconfs_notOwner_reverts() external {
        vm.prank(alice);
        vm.expectRevert("Ownable: caller is not the owner");
        gov.updatePreconfs(_one(address(0x1111), address(0x2222)), _none(), 200_000);
    }

    function test_updatePreconfs_whitelistNotSet_reverts() external {
        PreconfWhitelistGov fresh = new PreconfWhitelistGov(op, OWNER);

        vm.prank(OWNER);
        vm.expectRevert("PreconfWhitelistGov: whitelist not set");
        fresh.updatePreconfs(_one(address(0x1111), address(0x2222)), _none(), 200_000);
    }

    // ===== guards this side runs so L2 does not have to pay for them =====

    /// @notice The batch mirror. An oversized batch reverts on L2 too, but only after the deposit
    ///         gas has been bought and paid for on L1 — rejecting it here is free.
    /// @dev    Any `gov.*` or `op.*` view is hoisted above the cheatcodes deliberately, here and
    ///         throughout this file. Those are **external calls**: evaluated inside an argument list
    ///         one would consume the `vm.prank` and then the `vm.expectRevert`, leaving the
    ///         assertion pointed at the view instead of at `updatePreconfs`. The same note is on
    ///         `test_updatePreconfs_overMaxBatch_revertsAndChangesNothing` in `PreconfWhitelist.t.sol`.
    function test_updatePreconfs_overMaxBatch_reverts() external {
        uint256 over = gov.MAX_BATCH() + 1;
        PreconfWhitelist.Rule[] memory add = new PreconfWhitelist.Rule[](over);
        for (uint256 i = 0; i < over; i++) {
            add[i] = PreconfWhitelist.Rule({ from: address(uint160(i + 1)), to: address(uint160(i + 0x100000)) });
        }

        vm.prank(OWNER);
        vm.expectRevert("PreconfWhitelistGov: batch too large");
        gov.updatePreconfs(add, _none(), 200_000);
    }

    /// @notice The cap is on the sum across both arguments, matching the L2 contract.
    function test_updatePreconfs_sumAcrossArgsExceedsMax_reverts() external {
        uint256 half = gov.MAX_BATCH() / 2 + 1;
        PreconfWhitelist.Rule[] memory a = new PreconfWhitelist.Rule[](half);
        PreconfWhitelist.Rule[] memory b = new PreconfWhitelist.Rule[](half);
        for (uint256 i = 0; i < half; i++) {
            a[i] = PreconfWhitelist.Rule({ from: address(uint160(i + 1)), to: address(uint160(i + 0x100000)) });
            b[i] = PreconfWhitelist.Rule({ from: address(uint160(i + 0x200000)), to: address(uint160(i + 0x300000)) });
        }

        vm.prank(OWNER);
        vm.expectRevert("PreconfWhitelistGov: batch too large");
        gov.updatePreconfs(a, b, 200_000);
    }

    /// @notice **The only floor left on `_gasLimit`, and it is the portal's.** This contract carries
    ///         no gas estimate of its own: what a batch costs is a property of the L2 bytecode,
    ///         which L1 cannot measure, so a constant here would be a stale copy that fails in the
    ///         dangerous direction — under-estimating, and being trusted while it does.
    ///
    ///         What survives is `OptimismPortal.minimumGasLimit`, calibrated for calldata rather
    ///         than execution. It rules out the absurd values without pretending to know the cost;
    ///         sufficiency is checked by governance on L2, which is the only place it is knowable.
    function test_updatePreconfs_belowPortalGasFloor_reverts() external {
        PreconfWhitelist.Rule[] memory add = _one(address(0x1111), address(0x2222));
        bytes memory data = abi.encodeCall(PreconfWhitelist.updatePreconfs, (add, _none()));
        uint64 portalFloor = op.minimumGasLimit(uint64(data.length));

        vm.prank(OWNER);
        vm.expectRevert("OptimismPortal: gas limit too small");
        gov.updatePreconfs(add, _none(), portalFloor - 1);

        // Exactly the portal's floor is accepted, so this contract adds nothing above it.
        vm.prank(OWNER);
        gov.updatePreconfs(add, _none(), portalFloor);
    }

    // ===== rotateAuthorizedL1 =====

    /// @notice **The guard only L1 can run.** `PreconfWhitelist` admits a caller by undoing the
    ///         portal's alias, and the portal aliases a depositor exactly when it is a contract — so
    ///         an EOA `authorizedL1` is permanently locked out of the L2 gate. L2 cannot check this
    ///         (`extcodesize` there says nothing about an L1 address); this contract runs on L1 and
    ///         can. `alice` is a plain EOA, which is also what a mistyped address looks like.
    function test_rotateAuthorizedL1_eoa_reverts() external {
        vm.prank(OWNER);
        vm.expectRevert("PreconfWhitelistGov: authorized L1 sender is not a contract");
        gov.rotateAuthorizedL1(alice, uint64(60_000));
    }

    function test_rotateAuthorizedL1_zeroAddress_reverts() external {
        vm.prank(OWNER);
        vm.expectRevert("PreconfWhitelistGov: authorized L1 sender is the zero address");
        gov.rotateAuthorizedL1(address(0), uint64(60_000));
    }

    /// @notice A contract passes, and the deposit carries the `setAuthorizedL1` call rather than an
    ///         `updatePreconfs` one — the two paths share `_deposit`, so the calldata is the only
    ///         thing that distinguishes them.
    function test_rotateAuthorizedL1_contract_succeeds() external {
        address newGov = address(new PreconfWhitelistGov(op, OWNER));
        uint64 gasLimit = 60_000;

        vm.expectEmit(true, true, true, true, address(op));
        emit TransactionDeposited(
            AddressAliasHelper.applyL1ToL2Alias(address(gov)),
            whitelistL2,
            DEPOSIT_VERSION,
            _opaque(gasLimit, abi.encodeCall(PreconfWhitelist.setAuthorizedL1, (newGov)))
        );

        vm.prank(OWNER);
        gov.rotateAuthorizedL1(newGov, gasLimit);
    }

    function test_rotateAuthorizedL1_notOwner_reverts() external {
        address newGov = address(new PreconfWhitelistGov(op, OWNER));

        vm.prank(alice);
        vm.expectRevert("Ownable: caller is not the owner");
        gov.rotateAuthorizedL1(newGov, uint64(60_000));
    }

    // ===== ownership =====

    /// @notice Renouncing would strand the L2 allowlist: only this contract can write to it, and
    ///         reaching it needs an owner. `rotateAuthorizedL1` is the supported way out instead.
    function test_renounceOwnership_reverts() external {
        vm.prank(OWNER);
        vm.expectRevert("PreconfWhitelistGov: ownership cannot be renounced");
        gov.renounceOwnership();
    }

    function test_transferOwnership_succeeds() external {
        vm.prank(OWNER);
        gov.transferOwnership(alice);
        assertEq(gov.owner(), alice);
    }
}
