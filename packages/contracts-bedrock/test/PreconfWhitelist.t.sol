// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { console } from "forge-std/console.sol";
import { Vm } from "forge-std/Vm.sol";
import { Portal_Initializer } from "./CommonTest.t.sol";
import { AddressAliasHelper } from "src/vendor/AddressAliasHelper.sol";
import { Predeploys } from "src/libraries/Predeploys.sol";
import { PreconfWhitelist } from "src/L2/PreconfWhitelist.sol";

/// @title PreconfWhitelist_Test
/// @notice Tests the three-form rule routing, the alias-based authorization gate, the batch guard,
///         and — critically — the storage layout and event topic that op-reth depends on.
contract PreconfWhitelist_Test is Portal_Initializer {
    /// @notice The one L1 address allowed to govern the allowlist.
    address internal constant AUTHORIZED_L1 = address(0xAbC0);

    /// @notice Seeded exact rule.
    address internal constant P_FROM = address(0x1111);
    address internal constant P_TO = address(0x2222);
    /// @notice Seeded sender wildcard.
    address internal constant FW = address(0x3333);
    /// @notice Seeded recipient wildcard.
    address internal constant TW = address(0x4444);

    PreconfWhitelist internal wl;

    event WhitelistUpdated(uint256 exactPairCount, uint256 fromWildcardCount, uint256 toWildcardCount);
    event AuthorizedL1Updated(address indexed previousAuthorizedL1, address indexed newAuthorizedL1);

    function setUp() public virtual override {
        super.setUp();
        // One of each form, so every test starts from a state that exercises all three sets.
        PreconfWhitelist.Rule[] memory seed = new PreconfWhitelist.Rule[](3);
        seed[0] = _p(P_FROM, P_TO);
        seed[1] = _p(FW, address(0));
        seed[2] = _p(address(0), TW);
        wl = new PreconfWhitelist(AUTHORIZED_L1, seed);
    }

    // ===== helpers =====

    function _p(address _from, address _to) internal pure returns (PreconfWhitelist.Rule memory) {
        return PreconfWhitelist.Rule({ from: _from, to: _to });
    }

    /// @notice Single-element rule array.
    function _one(address _from, address _to) internal pure returns (PreconfWhitelist.Rule[] memory out_) {
        out_ = new PreconfWhitelist.Rule[](1);
        out_[0] = _p(_from, _to);
    }

    /// @notice Empty rule array.
    function _none() internal pure returns (PreconfWhitelist.Rule[] memory out_) {
        out_ = new PreconfWhitelist.Rule[](0);
    }

    /// @notice Makes the next call look like a deposit that `_l1Sender` originated on L1.
    /// @dev    Pranks the **aliased** address, because that is what `msg.sender` actually is here:
    ///         `OptimismPortal.depositTransaction` applies the alias to every contract depositor,
    ///         and governance is a contract. Pranking the raw `_l1Sender` would exercise a gate
    ///         this contract deliberately does not have — see `test_updatePreconfs_unaliasedAuthorizedL1_reverts`.
    function _asL1(address _l1Sender) internal {
        vm.prank(AddressAliasHelper.applyL1ToL2Alias(_l1Sender));
    }

    /// @notice `updatePreconfs` as the authorized L1 governor.
    function _gov(PreconfWhitelist.Rule[] memory _add, PreconfWhitelist.Rule[] memory _remove) internal {
        _asL1(AUTHORIZED_L1);
        wl.updatePreconfs(_add, _remove);
    }

    /// @notice Reads the address stored `_off` slots past `_base`.
    function _addrAt(bytes32 _base, uint256 _off) internal view returns (address) {
        return address(uint160(uint256(vm.load(address(wl), bytes32(uint256(_base) + _off)))));
    }

    function _counts() internal view returns (uint256, uint256, uint256) {
        return (wl.exactPairsLength(), wl.fromWildcardsLength(), wl.toWildcardsLength());
    }

    /// @notice Adds two exact pairs, one sender wildcard and two recipient wildcards on top of
    ///         `setUp`'s one-of-each, leaving the three sets at **3 / 2 / 3**. The sizes are
    ///         deliberately distinct: a getter that sliced the wrong bounds cannot then pass by
    ///         symmetry. The resulting concatenation, which `getAllRules` and `getRules` share, is
    ///
    ///           0..2  exact    (P_FROM,P_TO) (0xA1,0xB1) (0xA2,0xB2)
    ///           3..4  sender    FW           0xF1
    ///           5..7  recipient TW           0xD1        0xD2
    function _seedMixed() internal {
        PreconfWhitelist.Rule[] memory add = new PreconfWhitelist.Rule[](5);
        add[0] = _p(address(0xA1), address(0xB1));
        add[1] = _p(address(0xA2), address(0xB2));
        add[2] = _p(address(0xF1), address(0));
        add[3] = _p(address(0), address(0xD1));
        add[4] = _p(address(0), address(0xD2));
        _gov(add, _none());
    }

    // ===== constructor / seeding =====

    function test_constructor_seedsAllThreeForms_succeeds() external view {
        assertEq(wl.authorizedL1(), AUTHORIZED_L1);
        assertTrue(wl.isExactPair(P_FROM, P_TO));
        assertTrue(wl.isFromWildcard(FW));
        assertTrue(wl.isToWildcard(TW));
        (uint256 p, uint256 f, uint256 t) = _counts();
        assertEq(p, 1);
        assertEq(f, 1);
        assertEq(t, 1);
    }

    function test_constructor_emitsWhitelistUpdated_succeeds() external {
        PreconfWhitelist.Rule[] memory seed = new PreconfWhitelist.Rule[](2);
        seed[0] = _p(address(0xAA), address(0xBB));
        seed[1] = _p(address(0xCC), address(0));

        vm.expectEmit(true, true, true, true);
        emit WhitelistUpdated(1, 1, 0);
        new PreconfWhitelist(AUTHORIZED_L1, seed);
    }

    function test_constructor_zeroAuthorizedL1_reverts() external {
        vm.expectRevert("PreconfWhitelist: authorized L1 sender is the zero address");
        new PreconfWhitelist(address(0), _none());
    }

    /// @notice The all-zero form is rejected in the constructor too — `_apply` is the single place
    ///         the three forms are interpreted, so both entry points agree by construction.
    function test_constructor_allZeroPair_reverts() external {
        vm.expectRevert("PreconfWhitelist: pair is all-zero");
        new PreconfWhitelist(AUTHORIZED_L1, _one(address(0), address(0)));
    }

    // ===== routing: the three forms =====

    /// @notice A rule with both halves set is an exact pair and touches neither wildcard set.
    function test_updatePreconfs_exactPair_routesToExactPairs_succeeds() external {
        _gov(_one(address(0x5555), address(0x6666)), _none());

        assertTrue(wl.isExactPair(address(0x5555), address(0x6666)));
        assertFalse(wl.isExactPair(address(0x6666), address(0x5555)), "direction matters");
        assertFalse(wl.isFromWildcard(address(0x5555)));
        assertFalse(wl.isToWildcard(address(0x6666)));
        (uint256 p, uint256 f, uint256 t) = _counts();
        assertEq(p, 2);
        assertEq(f, 1);
        assertEq(t, 1);
    }

    /// @notice `to == 0` means "everything from this sender".
    function test_updatePreconfs_zeroTo_routesToFromWildcards_succeeds() external {
        _gov(_one(address(0x5555), address(0)), _none());

        assertTrue(wl.isFromWildcard(address(0x5555)));
        assertFalse(wl.isToWildcard(address(0x5555)));
        (uint256 p, uint256 f,) = _counts();
        assertEq(p, 1, "must not create a pair");
        assertEq(f, 2);
    }

    /// @notice `from == 0` means "everything to this recipient".
    function test_updatePreconfs_zeroFrom_routesToToWildcards_succeeds() external {
        _gov(_one(address(0), address(0x6666)), _none());

        assertTrue(wl.isToWildcard(address(0x6666)));
        assertFalse(wl.isFromWildcard(address(0x6666)));
        (uint256 p,, uint256 t) = _counts();
        assertEq(p, 1, "must not create a pair");
        assertEq(t, 2);
    }

    /// @notice "All transactions are eligible" is the node operator's switch (`--preconf.all`), not
    ///         governance's. Reverting also catches an uninitialized array element in a governance
    ///         script, which would otherwise vanish silently.
    function test_updatePreconfs_allZeroPair_reverts() external {
        _asL1(AUTHORIZED_L1);
        vm.expectRevert("PreconfWhitelist: pair is all-zero");
        wl.updatePreconfs(_one(address(0), address(0)), _none());
    }

    /// @notice The all-zero check applies to removes as well, so a malformed revoke cannot be
    ///         mistaken for a no-op.
    function test_updatePreconfs_allZeroPairInRemove_reverts() external {
        _asL1(AUTHORIZED_L1);
        vm.expectRevert("PreconfWhitelist: pair is all-zero");
        wl.updatePreconfs(_none(), _one(address(0), address(0)));
    }

    /// @notice No normalization across the sets: an exact pair and a covering wildcard coexist, and
    ///         removing one leaves the other. This is the property that makes the OR in the
    ///         sequencer's predicate honest — see the contract docs.
    function test_pairAndWildcardCoexist_succeeds() external {
        _gov(_one(FW, address(0x7777)), _none());
        assertTrue(wl.isExactPair(FW, address(0x7777)));
        assertTrue(wl.isFromWildcard(FW));

        // Revoking the exact rule does not revoke the wildcard that also covers it.
        _gov(_none(), _one(FW, address(0x7777)));
        assertFalse(wl.isExactPair(FW, address(0x7777)));
        assertTrue(wl.isFromWildcard(FW), "the wildcard still authorizes this traffic");
    }

    // ===== eligibility predicate =====

    /// @notice Each of the three terms authorizes on its own. Seeded state has exactly one entry
    ///         per set, and each case below matches through a different one.
    function test_isWhitelist_anySingleTermAuthorizes_succeeds() external view {
        assertTrue(wl.isWhitelist(P_FROM, P_TO), "exact pair");
        assertTrue(wl.isWhitelist(FW, address(0x9999)), "sender wildcard, arbitrary recipient");
        assertTrue(wl.isWhitelist(address(0x9999), TW), "recipient wildcard, arbitrary sender");
    }

    function test_isWhitelist_noTermMatches_returnsFalse() external view {
        assertFalse(wl.isWhitelist(address(0x9999), address(0x8888)));
    }

    /// @notice The halves are not interchangeable: a sender wildcard does not authorize traffic
    ///         *to* that address, and a recipient wildcard does not authorize traffic *from* it.
    function test_isWhitelist_wildcardSidesAreNotSymmetric_returnsFalse() external view {
        assertFalse(wl.isWhitelist(address(0x9999), FW), "FW is a sender wildcard, not a recipient one");
        assertFalse(wl.isWhitelist(TW, address(0x9999)), "TW is a recipient wildcard, not a sender one");
    }

    /// @notice A contract creation is expressed as `_to == address(0)` and needs no special case:
    ///         the zero address never reaches storage, so the pair and recipient terms are both
    ///         false and the answer is exactly `isFromWildcard(_from)`. This is the property the
    ///         sequencer's `Whitelist::is_eligible` reaches through an explicit `Option<Address>`.
    function test_isWhitelist_creationFallsBackToFromWildcard_succeeds() external {
        assertTrue(wl.isWhitelist(FW, address(0)), "sender wildcard authorizes a creation");
        assertFalse(wl.isWhitelist(P_FROM, address(0)), "an exact rule does not authorize a creation");

        // Revoking the wildcard removes the only term that could have authorized it.
        _gov(_none(), _one(FW, address(0)));
        assertFalse(wl.isWhitelist(FW, address(0)));
    }

    /// @notice No precedence and no deny list: revoking the exact pair leaves the traffic eligible
    ///         while a wildcard still covers it. The wildcard has to be revoked too.
    function test_isWhitelist_revokingPairLeavesWildcardCover_succeeds() external {
        _gov(_one(FW, address(0x7777)), _none());
        assertTrue(wl.isWhitelist(FW, address(0x7777)));

        _gov(_none(), _one(FW, address(0x7777)));
        assertFalse(wl.isExactPair(FW, address(0x7777)), "the exact rule is gone");
        assertTrue(wl.isWhitelist(FW, address(0x7777)), "but the sender wildcard still authorizes it");

        _gov(_none(), _one(FW, address(0)));
        assertFalse(wl.isWhitelist(FW, address(0x7777)), "only now is it revoked");
    }

    /// @notice The zero address is a calldata-only marker, so it is never a party in its own right:
    ///         an all-zero query matches nothing even though every set is non-empty.
    function test_isWhitelist_allZeroQuery_returnsFalse() external view {
        assertFalse(wl.isWhitelist(address(0), address(0)));
    }

    // ===== add / remove semantics =====

    function test_updatePreconfs_removeAcrossAllThreeSets_succeeds() external {
        PreconfWhitelist.Rule[] memory rm = new PreconfWhitelist.Rule[](3);
        rm[0] = _p(P_FROM, P_TO);
        rm[1] = _p(FW, address(0));
        rm[2] = _p(address(0), TW);
        _gov(_none(), rm);

        assertFalse(wl.isExactPair(P_FROM, P_TO));
        assertFalse(wl.isFromWildcard(FW));
        assertFalse(wl.isToWildcard(TW));
        (uint256 p, uint256 f, uint256 t) = _counts();
        assertEq(p, 0);
        assertEq(f, 0);
        assertEq(t, 0);
    }

    function test_updatePreconfs_emitsWhitelistUpdated_succeeds() external {
        vm.expectEmit(true, true, true, true, address(wl));
        emit WhitelistUpdated(2, 1, 1);
        _gov(_one(address(0x5555), address(0x6666)), _none());
    }

    /// @notice Adds are applied before removes, so a rule in both arguments ends up removed.
    function test_updatePreconfs_addThenRemoveSamePair_succeeds() external {
        _gov(_one(address(0x5555), address(0x6666)), _one(address(0x5555), address(0x6666)));
        assertFalse(wl.isExactPair(address(0x5555), address(0x6666)));
    }

    function test_updatePreconfs_addExisting_isNoop() external {
        _gov(_one(P_FROM, P_TO), _none());
        (uint256 p,,) = _counts();
        assertEq(p, 1);
        assertTrue(wl.isExactPair(P_FROM, P_TO));
    }

    function test_updatePreconfs_removeAbsent_isNoop() external {
        PreconfWhitelist.Rule[] memory rm = new PreconfWhitelist.Rule[](3);
        rm[0] = _p(address(0xDEAD), address(0xBEEF));
        rm[1] = _p(address(0xDEAD), address(0));
        rm[2] = _p(address(0), address(0xBEEF));
        _gov(_none(), rm);

        (uint256 p, uint256 f, uint256 t) = _counts();
        assertEq(p, 1);
        assertEq(f, 1);
        assertEq(t, 1);
    }

    /// @notice Swap-and-pop must keep `exactPairIndex` in step for the element that moved. Removing a
    ///         *middle* element is the only case that exercises the swap; removing the last one
    ///         takes a degenerate path where the element is swapped with itself.
    function test_rmExactPair_swapPop_keepsIndexInStep_succeeds() external {
        PreconfWhitelist.Rule[] memory add = new PreconfWhitelist.Rule[](2);
        add[0] = _p(address(0xA1), address(0xB1));
        add[1] = _p(address(0xA2), address(0xB2));
        _gov(add, _none()); // exactPairs = [seed, A1B1, A2B2]

        _gov(_none(), _one(address(0xA1), address(0xB1))); // remove the middle one

        (uint256 p,,) = _counts();
        assertEq(p, 2);
        assertFalse(wl.isExactPair(address(0xA1), address(0xB1)));
        assertTrue(wl.isExactPair(address(0xA2), address(0xB2)), "the moved element stays indexed");
        // And its recorded position must be the one it actually occupies now.
        assertEq(wl.exactPairIndex(address(0xA2), address(0xB2)), 2);
        (address f, address t) = wl.exactPairs(1);
        assertEq(f, address(0xA2));
        assertEq(t, address(0xB2));
    }

    /// @notice Removing the final element: the swap writes it over itself, so the subsequent
    ///         `delete` is what actually clears the index. A guard against "restore then forget".
    function test_rmExactPair_lastElement_succeeds() external {
        _gov(_one(address(0xA1), address(0xB1)), _none());
        _gov(_none(), _one(address(0xA1), address(0xB1)));

        (uint256 p,,) = _counts();
        assertEq(p, 1);
        assertFalse(wl.isExactPair(address(0xA1), address(0xB1)));
        assertEq(wl.exactPairIndex(address(0xA1), address(0xB1)), 0);
    }

    // ===== authorization: the alias gate =====

    /// @notice An ordinary L2 caller with no alias relationship to the governor.
    function test_updatePreconfs_arbitraryCaller_reverts() external {
        vm.prank(alice);
        vm.expectRevert("PreconfWhitelist: caller is not the authorized L1 sender");
        wl.updatePreconfs(_one(address(0x9999), address(0x8888)), _none());
    }

    /// @notice A legitimate but unauthorized L1 depositor: correctly aliased, wrong address.
    function test_updatePreconfs_unauthorizedL1Sender_reverts() external {
        _asL1(alice);
        vm.expectRevert("PreconfWhitelist: caller is not the authorized L1 sender");
        wl.updatePreconfs(_one(address(0x9999), address(0x8888)), _none());
    }

    /// @notice **The test that pins the gate's shape.** `AUTHORIZED_L1` reaching us *unaliased* is
    ///         refused, because the gate compares `undoL1ToL2Alias(msg.sender)` rather than
    ///         `msg.sender`.
    /// @dev    Not a hypothetical. An EOA depositing through the portal is the one caller that is
    ///         **not** aliased (`msg.sender == tx.origin`), so this is exactly what an EOA
    ///         governor's message would look like on arrival — and it must fail, or the contract
    ///         would appear to work while `authorizedL1` was set to an unusable address.
    ///
    ///         It is also what makes the impersonation argument hold: were the raw address
    ///         accepted, any L1 contract deployed at `AUTHORIZED_L1` on the L1 side could speak for
    ///         the same address on L2. Deleting this test would leave that unguarded, since every
    ///         other test here passes through `_asL1` and so never exercises the raw form.
    function test_updatePreconfs_unaliasedAuthorizedL1_reverts() external {
        vm.prank(AUTHORIZED_L1);
        vm.expectRevert("PreconfWhitelist: caller is not the authorized L1 sender");
        wl.updatePreconfs(_one(address(0x9999), address(0x8888)), _none());
    }

    /// @notice **Channel exclusivity.** A message relayed through the CrossDomainMessenger arrives
    ///         with the L2 messenger predeploy as `msg.sender`, whose alias preimage is an
    ///         unrelated address — so the wrong channel reverts rather than taking effect.
    /// @dev    The operational constraint this pins is that governance must call
    ///         `OptimismPortal.depositTransaction` directly and never
    ///         `L1CrossDomainMessenger.sendMessage`. Asserting it here makes the constraint
    ///         fail-closed by test as well as by construction.
    function test_updatePreconfs_viaCrossDomainMessenger_reverts() external {
        vm.prank(Predeploys.L2_CROSS_DOMAIN_MESSENGER);
        vm.expectRevert("PreconfWhitelist: caller is not the authorized L1 sender");
        wl.updatePreconfs(_one(address(0x9999), address(0x8888)), _none());
    }

    // ===== authorization: rotation =====

    /// @notice A rotation moves the privilege wholesale: the new address gains it and the old one
    ///         loses it in the same call. Asserting only the first half would pass for a setter
    ///         that appended to an allowlist of governors.
    function test_setAuthorizedL1_movesThePrivilege_succeeds() external {
        address newGov = address(0x9E01);

        _asL1(AUTHORIZED_L1);
        wl.setAuthorizedL1(newGov);
        assertEq(wl.authorizedL1(), newGov);

        // The new governor can update ...
        _asL1(newGov);
        wl.updatePreconfs(_one(address(0x5555), address(0x6666)), _none());
        assertTrue(wl.isExactPair(address(0x5555), address(0x6666)));

        // ... and the old one cannot.
        _asL1(AUTHORIZED_L1);
        vm.expectRevert("PreconfWhitelist: caller is not the authorized L1 sender");
        wl.updatePreconfs(_one(address(0x7777), address(0x8888)), _none());
    }

    function test_setAuthorizedL1_emitsBothEnds_succeeds() external {
        address newGov = address(0x9E01);

        vm.expectEmit(true, true, true, true, address(wl));
        emit AuthorizedL1Updated(AUTHORIZED_L1, newGov);

        _asL1(AUTHORIZED_L1);
        wl.setAuthorizedL1(newGov);
    }

    /// @notice The setter is gated by the same modifier as `updatePreconfs`, so an unauthorized
    ///         caller cannot seize governance. Aliased-but-wrong is the interesting case: it proves
    ///         the gate is checked, not merely that a raw caller is rejected.
    function test_setAuthorizedL1_unauthorizedCaller_reverts() external {
        _asL1(alice);
        vm.expectRevert("PreconfWhitelist: caller is not the authorized L1 sender");
        wl.setAuthorizedL1(alice);

        assertEq(wl.authorizedL1(), AUTHORIZED_L1, "the governor is unchanged");
    }

    /// @notice Rotating to zero would disable governance permanently, and disabling the fast path
    ///         is the node operator's switch rather than a state this contract can enter. Matches
    ///         the constructor's guard, so both entry points agree about the one refused value.
    function test_setAuthorizedL1_zeroAddress_reverts() external {
        _asL1(AUTHORIZED_L1);
        vm.expectRevert("PreconfWhitelist: authorized L1 sender is the zero address");
        wl.setAuthorizedL1(address(0));

        assertEq(wl.authorizedL1(), AUTHORIZED_L1, "the governor is unchanged");
    }

    /// @notice Rotation touches governance only — the three sets and the layout marker are not
    ///         part of it. A setter that reset state would be a silent way to empty the allowlist.
    function test_setAuthorizedL1_leavesTheAllowlistAlone_succeeds() external {
        _asL1(AUTHORIZED_L1);
        wl.setAuthorizedL1(address(0x9E01));

        (uint256 p, uint256 f, uint256 t) = _counts();
        assertEq(p, 1);
        assertEq(f, 1);
        assertEq(t, 1);
        assertTrue(wl.isExactPair(P_FROM, P_TO));
        assertEq(wl.layoutVersion(), 2);
    }

    // ===== batch guard =====

    /// @notice Builds `_n` distinct exact pairs — the most expensive rule form, which is what
    ///         `MAX_BATCH` is sized against.
    function _pairBatch(uint256 _n, uint256 _seed) internal pure returns (PreconfWhitelist.Rule[] memory out_) {
        out_ = new PreconfWhitelist.Rule[](_n);
        for (uint256 i = 0; i < _n; i++) {
            out_[i] = PreconfWhitelist.Rule({
                from: address(uint160(_seed + i + 1)),
                to: address(uint160(_seed + i + 0x100000))
            });
        }
    }

    function test_updatePreconfs_atMaxBatch_succeeds() external {
        uint256 max = wl.MAX_BATCH();
        _gov(_pairBatch(max, 0), _none());
        (uint256 p,,) = _counts();
        assertEq(p, max + 1, "on top of the seeded pair");
    }

    /// @notice Also asserts that nothing changed: the guard reverts before any SSTORE, so a
    ///         rejected governance message is cheap and leaves no partial application behind.
    /// @dev    The batch is built *before* `_asL1`, deliberately. `wl.MAX_BATCH()` is an
    ///         external call; evaluated inside the argument list it would consume the `vm.prank`
    ///         and the `vm.expectRevert`, leaving the test to assert against the wrong call. That
    ///         mistake passes as a green test, so it is worth the extra line.
    function test_updatePreconfs_overMaxBatch_revertsAndChangesNothing() external {
        PreconfWhitelist.Rule[] memory over = _pairBatch(wl.MAX_BATCH() + 1, 0);

        _asL1(AUTHORIZED_L1);
        vm.expectRevert("PreconfWhitelist: batch too large");
        wl.updatePreconfs(over, _none());

        (uint256 p, uint256 f, uint256 t) = _counts();
        assertEq(p, 1);
        assertEq(f, 1);
        assertEq(t, 1);
    }

    /// @notice The cap is on the SUM across both arguments, not per-array.
    function test_updatePreconfs_sumAcrossArgsExceedsMax_reverts() external {
        uint256 max = wl.MAX_BATCH();
        _asL1(AUTHORIZED_L1);
        vm.expectRevert("PreconfWhitelist: batch too large");
        wl.updatePreconfs(_pairBatch(max / 2 + 1, 0), _pairBatch(max / 2 + 1, 0x1000000));
    }

    // ===== pagination =====

    function test_getExactPairs_paging_succeeds() external {
        _gov(_pairBatch(4, 0), _none()); // 5 total, including the seed

        assertEq(wl.exactPairsLength(), 5);
        assertEq(wl.getExactPairs(0, 2).length, 2, "a full page");
        assertEq(wl.getExactPairs(4, 10).length, 1, "an over-long limit truncates to the end");
        assertEq(wl.getExactPairs(5, 10).length, 0, "offset == length is empty, not a revert");
        assertEq(wl.getExactPairs(99, 10).length, 0, "offset past the end is empty, not a revert");
        assertEq(wl.getExactPairs(0, type(uint256).max).length, 5, "the limit must not overflow");
    }

    /// @notice The page contents must line up with the underlying indices, or a caller walking the
    ///         list would silently skip or repeat rules.
    function test_getExactPairs_pageContents_succeeds() external {
        _gov(_pairBatch(4, 0), _none());

        PreconfWhitelist.Rule[] memory page = wl.getExactPairs(1, 2);
        (address f1, address t1) = wl.exactPairs(1);
        (address f2, address t2) = wl.exactPairs(2);
        assertEq(page[0].from, f1);
        assertEq(page[0].to, t1);
        assertEq(page[1].from, f2);
        assertEq(page[1].to, t2);
    }

    function test_getWildcards_paging_succeeds() external {
        PreconfWhitelist.Rule[] memory add = new PreconfWhitelist.Rule[](4);
        add[0] = _p(address(0xF1), address(0));
        add[1] = _p(address(0xF2), address(0));
        add[2] = _p(address(0), address(0xD1));
        add[3] = _p(address(0), address(0xD2));
        _gov(add, _none());

        assertEq(wl.getFromWildcards(0, 10).length, 3, "over-long limit truncates");
        assertEq(wl.getFromWildcards(3, 1).length, 0, "offset == length is empty");
        assertEq(wl.getToWildcards(1, 1).length, 1);
        assertEq(wl.getToWildcards(0, type(uint256).max).length, 3, "the limit must not overflow");
    }

    // ===== full-list getters =====

    /// @notice An empty allowlist yields empty arrays rather than a revert, so tooling does not
    ///         have to special-case a fresh deployment.
    function test_getAll_emptyAllowlist_returnsEmptyArrays_succeeds() external {
        PreconfWhitelist fresh = new PreconfWhitelist(AUTHORIZED_L1, _none());
        assertEq(fresh.getAllExactPairs().length, 0);
        assertEq(fresh.getAllFromWildcards().length, 0);
        assertEq(fresh.getAllToWildcards().length, 0);
        assertEq(fresh.getAllRules().length, 0);
    }

    /// @notice The unbounded getter and the paginated one are two views of the same array, and a
    ///         caller that outgrows the former is expected to switch to the latter — so they have
    ///         to agree element for element, not just in length.
    function test_getAllExactPairs_agreesWithPaging_succeeds() external {
        _gov(_pairBatch(4, 0), _none()); // 5 total, including the seed

        PreconfWhitelist.Rule[] memory all = wl.getAllExactPairs();
        assertEq(all.length, wl.exactPairsLength(), "length matches the counter");
        assertEq(all.length, 5);

        PreconfWhitelist.Rule[] memory page = wl.getExactPairs(0, type(uint256).max);
        for (uint256 i = 0; i < all.length; i++) {
            assertEq(all[i].from, page[i].from);
            assertEq(all[i].to, page[i].to);
        }
    }

    /// @notice Same agreement for the two wildcard sets. Their sizes are made to differ so a
    ///         getter reading the wrong array cannot pass by coincidence.
    function test_getAllWildcards_agreeWithPaging_succeeds() external {
        PreconfWhitelist.Rule[] memory add = new PreconfWhitelist.Rule[](3);
        add[0] = _p(address(0xF1), address(0));
        add[1] = _p(address(0), address(0xD1));
        add[2] = _p(address(0), address(0xD2));
        _gov(add, _none()); // 2 sender wildcards, 3 recipient wildcards

        address[] memory allFrom = wl.getAllFromWildcards();
        address[] memory allTo = wl.getAllToWildcards();
        assertEq(allFrom.length, 2);
        assertEq(allTo.length, 3);

        address[] memory pageFrom = wl.getFromWildcards(0, type(uint256).max);
        address[] memory pageTo = wl.getToWildcards(0, type(uint256).max);
        for (uint256 i = 0; i < allFrom.length; i++) {
            assertEq(allFrom[i], pageFrom[i]);
        }
        for (uint256 i = 0; i < allTo.length; i++) {
            assertEq(allTo[i], pageTo[i]);
        }
    }

    /// @notice `getAllRules` is the three sets concatenated in declaration order, with wildcards
    ///         re-encoded into the zero-address wire form. That layout is part of the interface,
    ///         not an implementation detail: exact pairs first, then sender wildcards as `(A, 0)`,
    ///         then recipient wildcards as `(0, B)`.
    function test_getAllRules_concatenatesThreeSetsInOrder_succeeds() external view {
        PreconfWhitelist.Rule[] memory all = wl.getAllRules();
        assertEq(all.length, 3, "one of each seeded form");

        assertEq(all[0].from, P_FROM, "exact pairs come first");
        assertEq(all[0].to, P_TO);

        assertEq(all[1].from, FW, "then sender wildcards, as (A, 0)");
        assertEq(all[1].to, address(0));

        assertEq(all[2].from, address(0), "then recipient wildcards, as (0, B)");
        assertEq(all[2].to, TW);
    }

    /// @notice The length is the sum of all three sets — and, just as much, a guard on the
    ///         `_seedMixed` fixture itself. `test_getRules_pageStraddlingBothBoundaries_succeeds`
    ///         hardcodes indices read off the 3 / 2 / 3 layout, so if that helper ever drifts, this
    ///         is where it should surface rather than as a puzzling failure over there.
    function test_getAllRules_lengthIsSumOfThreeSets_succeeds() external {
        _seedMixed();

        assertEq(wl.exactPairsLength(), 3);
        assertEq(wl.fromWildcardsLength(), 2);
        assertEq(wl.toWildcardsLength(), 3);
        assertEq(wl.getAllRules().length, 8);
    }

    /// @notice The documented migration path: `getAllRules()` output is valid constructor and
    ///         `updatePreconfs` input, so an allowlist can be cloned onto a fresh deployment with
    ///         no off-chain reassembly. This is what makes a `LAYOUT_VERSION` bump survivable.
    ///
    ///         The clone's constructor also doubles as an assertion that the output never contains
    ///         an all-zero rule: `_apply` reverts on that form, so an emitted `(0, 0)` would fail
    ///         this test rather than silently disappear.
    function test_getAllRules_roundTripsIntoAFreshDeployment_succeeds() external {
        _seedMixed();

        PreconfWhitelist.Rule[] memory all = wl.getAllRules();
        PreconfWhitelist clone = new PreconfWhitelist(AUTHORIZED_L1, all);

        assertEq(clone.exactPairsLength(), wl.exactPairsLength());
        assertEq(clone.fromWildcardsLength(), wl.fromWildcardsLength());
        assertEq(clone.toWildcardsLength(), wl.toWildcardsLength());

        // Counts alone would survive a wildcard that came back with the wrong half zeroed, so
        // check that every rule landed in the set it came from.
        for (uint256 i = 0; i < all.length; i++) {
            if (all[i].from == address(0)) {
                assertTrue(clone.isToWildcard(all[i].to), "recipient wildcard");
            } else if (all[i].to == address(0)) {
                assertTrue(clone.isFromWildcard(all[i].from), "sender wildcard");
            } else {
                assertTrue(clone.isExactPair(all[i].from, all[i].to), "exact pair");
            }
        }
    }

    /// @notice `getRules` pages over the same concatenation `getAllRules` returns, so walking it
    ///         one element at a time has to reproduce that array exactly. Stepping by one puts
    ///         every index — including both segment boundaries — in its own call, which is where
    ///         an off-by-one in the index mapping would show up.
    function test_getRules_pagingReproducesGetAllRules_succeeds() external {
        _seedMixed();

        PreconfWhitelist.Rule[] memory all = wl.getAllRules();
        assertEq(all.length, 8);

        for (uint256 i = 0; i < all.length; i++) {
            PreconfWhitelist.Rule[] memory one = wl.getRules(i, 1);
            assertEq(one.length, 1);
            assertEq(one[0].from, all[i].from, "from at index");
            assertEq(one[0].to, all[i].to, "to at index");
        }
    }

    /// @notice One page straddling both segment boundaries at once. Indices are read off the
    ///         3 / 2 / 3 layout documented on `_seedMixed`: 2 is the last exact pair, 3 and 4 the
    ///         sender wildcards, 5 the first recipient one. A segment-mapping error cannot survive
    ///         a page that crosses both.
    function test_getRules_pageStraddlingBothBoundaries_succeeds() external {
        _seedMixed();

        PreconfWhitelist.Rule[] memory page = wl.getRules(2, 4);
        assertEq(page.length, 4);

        assertEq(page[0].from, address(0xA2), "index 2: last exact pair");
        assertEq(page[0].to, address(0xB2));

        assertEq(page[1].from, FW, "index 3: first sender wildcard, as (A, 0)");
        assertEq(page[1].to, address(0));

        assertEq(page[2].from, address(0xF1), "index 4: second sender wildcard");
        assertEq(page[2].to, address(0));

        assertEq(page[3].from, address(0), "index 5: first recipient wildcard, as (0, B)");
        assertEq(page[3].to, TW);
    }

    /// @notice The boundary behaviour matches the per-set paginated getters: out-of-range offsets
    ///         yield an empty array rather than reverting, and an over-long limit truncates.
    function test_getRules_pagingEdgeCases_succeeds() external {
        _seedMixed(); // 8 rules in total

        assertEq(wl.getRules(0, 3).length, 3, "a full page");
        assertEq(wl.getRules(6, 10).length, 2, "an over-long limit truncates to the end");
        assertEq(wl.getRules(8, 10).length, 0, "offset == total is empty, not a revert");
        assertEq(wl.getRules(99, 10).length, 0, "offset past the end is empty, not a revert");
        assertEq(wl.getRules(0, type(uint256).max).length, 8, "the limit must not overflow");
        assertEq(wl.getRules(0, 0).length, 0, "a zero limit is an empty page");
    }

    /// @notice An empty allowlist pages to an empty array. The total is a sum of three lengths, so
    ///         this also covers the case where every segment is empty at once.
    function test_getRules_emptyAllowlist_returnsEmptyArray_succeeds() external {
        PreconfWhitelist fresh = new PreconfWhitelist(AUTHORIZED_L1, _none());
        assertEq(fresh.getRules(0, 10).length, 0);
    }

    /// @notice `rulesLength` is the bound for `getRules` and the size check to make before calling
    ///         `getAllRules`, so it has to agree with both — a counter that drifts from what the
    ///         getters actually return would send a caller past the end or into the gas cliff it
    ///         was consulted to avoid.
    function test_rulesLength_agreesWithTheSetsAndGetAllRules_succeeds() external {
        assertEq(wl.rulesLength(), 3, "the seeded one-of-each");

        _seedMixed();

        (uint256 p, uint256 f, uint256 t) = _counts();
        assertEq(wl.rulesLength(), p + f + t, "the sum of the three sets");
        assertEq(wl.rulesLength(), wl.getAllRules().length, "the length getAllRules returns");
        assertEq(wl.getRules(wl.rulesLength(), 1).length, 0, "it is exactly the exclusive bound");
    }

    /// @notice Zero on an empty allowlist rather than a revert, matching the per-set counters.
    function test_rulesLength_emptyAllowlist_isZero_succeeds() external {
        PreconfWhitelist fresh = new PreconfWhitelist(AUTHORIZED_L1, _none());
        assertEq(fresh.rulesLength(), 0);
    }

    // ===== op-reth coupling: storage layout + event topic =====

    /// @notice op-reth reads these slots directly. If this test fails, the Rust constants in
    ///         `mantle-reth/crates/preconf/src/whitelist.rs` must change in lockstep.
    ///
    ///         The `exactPairs` assertions also pin the **two-slot element stride**, which is the whole
    ///         basis of the Rust reader's `base + 2i` / `base + 2i + 1` addressing. Two addresses
    ///         are 40 bytes and cannot share a slot; this proves the compiler agrees at runtime,
    ///         rather than leaving it to a reading of the layout rules.
    function test_storageLayout_matchesRethExpectations_succeeds() external {
        _gov(_one(address(0xA9), address(0xB9)), _none()); // a second pair, so the stride shows

        // Array lengths live at the declaring slot.
        assertEq(uint256(vm.load(address(wl), bytes32(uint256(0)))), 2, "exactPairs must be slot 0");
        assertEq(uint256(vm.load(address(wl), bytes32(uint256(2)))), 1, "fromWildcards must be slot 2");
        assertEq(uint256(vm.load(address(wl), bytes32(uint256(4)))), 1, "toWildcards must be slot 4");

        bytes32 pairsBase = keccak256(abi.encode(uint256(0)));
        assertEq(_addrAt(pairsBase, 0), P_FROM, "exactPairs[0].from at base+0");
        assertEq(_addrAt(pairsBase, 1), P_TO, "exactPairs[0].to at base+1");
        assertEq(_addrAt(pairsBase, 2), address(0xA9), "exactPairs[1].from at base+2 -- the 2-slot stride");
        assertEq(_addrAt(pairsBase, 3), address(0xB9), "exactPairs[1].to at base+3");

        assertEq(_addrAt(keccak256(abi.encode(uint256(2))), 0), FW, "fromWildcards[0]");
        assertEq(_addrAt(keccak256(abi.encode(uint256(4))), 0), TW, "toWildcards[0]");
    }

    /// @notice The three index mappings sit immediately after their arrays, at slots 1, 3 and 5.
    ///         This pins the whole 0..5 block so an inserted variable cannot slide the arrays
    ///         unnoticed. `exactPairIndex` is nested, so its slot is a double hash.
    function test_storageLayout_indexMappingSlots_succeeds() external view {
        bytes32 inner = keccak256(abi.encode(P_FROM, uint256(1)));
        assertEq(uint256(vm.load(address(wl), keccak256(abi.encode(P_TO, inner)))), 1, "exactPairIndex must be slot 1");
        assertEq(
            uint256(vm.load(address(wl), keccak256(abi.encode(FW, uint256(3))))), 1, "fromWildcardIndex must be slot 3"
        );
        assertEq(
            uint256(vm.load(address(wl), keccak256(abi.encode(TW, uint256(5))))), 1, "toWildcardIndex must be slot 5"
        );
    }

    /// @notice The layout marker op-reth checks at cold start. It has to be **after** every array,
    ///         or bumping it would shift the very slots it is meant to protect — so the slot number
    ///         is asserted, not just the value.
    ///
    ///         The previous cross-product contract never wrote this slot, so it reads back as `0`
    ///         there. That is what lets a sequencer built for this layout refuse to start against
    ///         it, instead of reading its recipient list out of slot 2 and installing it as sender
    ///         wildcards.
    function test_storageLayout_layoutVersion_succeeds() external view {
        assertEq(uint256(vm.load(address(wl), bytes32(uint256(6)))), 2, "layoutVersion must be slot 6");
        assertEq(wl.layoutVersion(), wl.LAYOUT_VERSION());
        assertEq(wl.LAYOUT_VERSION(), 2);
    }

    /// @notice `authorizedL1` is appended *after* `layoutVersion`, so it moves none of the slots
    ///         op-reth reads — which is why it does not bump `LAYOUT_VERSION`. Asserting the slot
    ///         number is the guard, and it matters more for this variable than for any other:
    ///         `authorizedL1` is the one piece of configuration a reader would naturally declare at
    ///         the *top* of the contract, next to the constants, which would put it in slot 0 and
    ///         slide every array down one. op-reth would then read `exactPairIndex` as `exactPairs`
    ///         and `fromWildcardIndex` as `fromWildcards`, while `layoutVersion` — moved to slot 7
    ///         — read back as 0 and produced an error naming the wrong cause.
    ///
    ///         Asserted after a rotation rather than on the constructor's value, so that a setter
    ///         writing to some other slot cannot pass by leaving the constructor's write in place.
    function test_storageLayout_authorizedL1IsAppendedAtSlot7_succeeds() external {
        address newGov = address(0x9E01);
        _asL1(AUTHORIZED_L1);
        wl.setAuthorizedL1(newGov);

        assertEq(_addrAt(bytes32(0), 7), newGov, "authorizedL1 must be slot 7");
        assertEq(wl.authorizedL1(), newGov);
        assertEq(uint256(vm.load(address(wl), bytes32(uint256(6)))), 2, "layoutVersion still slot 6");
        assertEq(uint256(vm.load(address(wl), bytes32(uint256(0)))), 1, "exactPairs still slot 0");
        assertEq(wl.LAYOUT_VERSION(), 2, "appending a variable is not a layout change");
    }

    /// @notice The marker is written by the constructor, so every deployment carries it — there is
    ///         no window in which a live contract reads back as version 0.
    function test_constructor_writesLayoutVersion_succeeds() external {
        PreconfWhitelist fresh = new PreconfWhitelist(AUTHORIZED_L1, _none());
        assertEq(fresh.layoutVersion(), 2, "even an empty deployment declares its layout");
    }

    /// @notice `updatePreconfs` must never touch it: it describes the shape of storage, not its
    ///         contents, and governance has no say over the shape.
    function test_updatePreconfs_leavesLayoutVersionAlone_succeeds() external {
        _gov(_one(address(0x5555), address(0x6666)), _one(P_FROM, P_TO));
        assertEq(wl.layoutVersion(), 2);
    }

    /// @notice op-reth filters canonical logs on this topic0 (`WHITELIST_UPDATED_TOPIC0` in
    ///         `mantle-reth/crates/preconf/src/whitelist.rs`); both sides pin the same literal. If
    ///         the signature changes, the sequencer stops refreshing and says nothing about it — it
    ///         keeps serving whatever list it read at bootstrap. That is the failure mode this
    ///         assertion exists to prevent.
    /// @dev    Read off the **emitted log**, deliberately, rather than hashing the signature as a
    ///         string. A string-literal assertion is independent of the contract: changing the
    ///         event and the test's local `WhitelistUpdated` declaration together — the natural way
    ///         to refactor it — leaves such a test green while topic0 silently moves. Recording the
    ///         log makes the literal answerable to what the contract actually emits.
    function test_whitelistUpdatedTopic0_matchesTheEmittedLog_succeeds() external {
        vm.recordLogs();
        _gov(_one(address(0x5555), address(0x6666)), _none());

        Vm.Log[] memory logs = vm.getRecordedLogs();
        assertEq(logs.length, 1, "exactly one event per update");
        assertEq(logs[0].emitter, address(wl));
        assertEq(
            logs[0].topics[0],
            0x532fe709f340eda40c9d51e7dbbacf9d5b255b36429ed90f865bd2a3131ef1bc,
            "op-reth pins this same literal; both sides move together or neither does"
        );
    }

    // ===== MAX_BATCH sizing =====
    //
    // `MAX_BATCH = 256` is calibrated so that a full batch of the most expensive rule form fits in
    // the gas a governance deposit can actually be given. That budget comes from **L1**, not from
    // the L2 block: `OptimismPortal.depositTransaction` is `metered(_gasLimit)`, so it meters what
    // governance asks for directly — no `baseGas` conversion, no EIP-150 63/64 term on this channel
    // — and `ResourceMetering` caps the deposit gas bought per L1 block at `maxResourceLimit`
    // (20,000,000). Subtracting the deposit's own worst-case intrinsic cost (285,256 for a full
    // batch's 16,516 calldata bytes) leaves 19,714,744. The full derivation is on `MAX_BATCH` in
    // `PreconfWhitelist.sol`; extrapolating the figures below puts the hard ceiling at 289.
    //
    // That justification lives entirely in the two measurements below, so they assert the ceiling
    // rather than only logging. A change that inflates per-rule cost has to either stay under the
    // bound or force whoever made it to re-derive `MAX_BATCH` — which is the point. The pair bound
    // is the real 19,714,744 figure, which sits 13.2% above the recorded cost, so compiler-version
    // noise is not a tripwire. Run with `-vv` to read the numbers; if a bound trips, re-measure and
    // update the table in `PreconfWhitelist.sol`.

    /// @notice A full `MAX_BATCH` of exact-pair adds — the expensive form `MAX_BATCH` is sized
    ///         against. Recorded at 17,419,375 (68,044 per rule).
    function test_gas_maxBatchOfPairAdds() external {
        uint256 max = wl.MAX_BATCH();
        PreconfWhitelist.Rule[] memory adds = _pairBatch(max, 0);
        _asL1(AUTHORIZED_L1);

        uint256 g = gasleft();
        wl.updatePreconfs(adds, _none());
        uint256 used = g - gasleft();

        console.log("MAX_BATCH             ", max);
        console.log("gas, all pair adds    ", used);
        console.log("gas per pair add      ", used / max);

        assertLt(used, 19_714_744, "a full batch must fit in the _gasLimit L1 can sell -- re-derive MAX_BATCH");
    }

    /// @notice A **single** pair add — the un-amortized cost. `MAX_BATCH`'s figure above is an
    ///         average over 256 rules, which divides the per-call fixed cost (dispatch, calldata
    ///         decode, the three cold SLOADs the event reads, the cold length SSTORE) by 256; a
    ///         one-rule call pays all of it. This is the number `PreconfWhitelistGov.BASE_GAS`
    ///         exists to cover, and it is measured here because only the L2 side can measure it.
    function test_gas_singleRuleAdd() external {
        _asL1(AUTHORIZED_L1);

        uint256 g = gasleft();
        wl.updatePreconfs(_one(address(0xA9), address(0xB9)), _none());
        uint256 used = g - gasleft();

        console.log("gas, single pair add  ", used);
        console.log("gas, per-rule average ", uint256(68_044));
        console.log("un-amortized excess   ", used - 68_044);
        assertLt(used, 100_000, "single add got more expensive -- re-derive PreconfWhitelistGov.BASE_GAS");
    }

    /// @notice The same batch made entirely of wildcards, to confirm the pair form really is the
    ///         expensive one. Recorded at 11,700,841 (45,706 per rule).
    /// @dev    Bounded above by the pair figure as well as by an absolute number: if a wildcard add
    ///         ever became the costlier of the two, `MAX_BATCH` would be sized against the wrong
    ///         operation and the ceiling above would stop being an upper bound at all.
    function test_gas_maxBatchOfWildcardAdds() external {
        uint256 max = wl.MAX_BATCH();
        PreconfWhitelist.Rule[] memory adds = new PreconfWhitelist.Rule[](max);
        for (uint256 i = 0; i < max; i++) {
            adds[i] = _p(address(uint160(i + 1)), address(0));
        }
        _asL1(AUTHORIZED_L1);

        uint256 g = gasleft();
        wl.updatePreconfs(adds, _none());
        uint256 used = g - gasleft();

        console.log("gas, all wildcard adds", used);
        console.log("gas per wildcard add  ", used / max);

        assertLt(used, 12_400_000, "wildcard adds got more expensive -- re-measure");
        assertLt(used, 17_419_375, "the exact-pair form must stay the expensive one");
    }
}
