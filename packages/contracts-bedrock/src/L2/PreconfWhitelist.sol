// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { AddressAliasHelper } from "../vendor/AddressAliasHelper.sol";

// Backing value for `PreconfWhitelist.MAX_BATCH`, declared at file level so `PreconfWhitelistGov`
// on L1 can import the same constant. A contract's `constant` is not reachable as
// `PreconfWhitelist.MAX_BATCH` in solc 0.8.15, so without this the L1 cap would be a copy.
uint256 constant PRECONF_MAX_BATCH = 256;

/// @title PreconfWhitelist
/// @notice Preconf fast-path allowlist, governed only from `authorizedL1` by direct portal deposit.
///         LAYOUT IS LOAD-BEARING: op-reth reads slots 0/2/4/6 and a `Rule` spans two; never reorder.
contract PreconfWhitelist {
    /// @notice One allowlist rule as governance submits it. Both halves non-zero is an exact pair;
    ///         exactly one zero half is a wildcard on the other side. The zero address is a
    ///         calldata-only routing marker and never reaches storage.
    struct Rule {
        address from;
        address to;
    }

    /// @notice Exact `(from, to)` rules — both halves non-zero. Slot 0, two slots per element.
    Rule[] public exactPairs;

    /// @notice Membership index for `exactPairs`. Slot 1. Stores `index + 1`, so 0 means absent.
    ///         Nested rather than keyed on a hash of the pair: cheaper, and the generated getter
    ///         takes two addresses. Not a `bool` — `_rmExactPair` needs the array position.
    mapping(address => mapping(address => uint256)) public exactPairIndex;

    /// @notice Senders whose transactions are all eligible, whatever the recipient. Slot 2.
    address[] public fromWildcards;

    /// @notice Membership index for `fromWildcards`. Slot 3. Stores `index + 1`.
    mapping(address => uint256) public fromWildcardIndex;

    /// @notice Recipients that make any transaction to them eligible, whatever the sender. Slot 4.
    address[] public toWildcards;

    /// @notice Membership index for `toWildcards`. Slot 5. Stores `index + 1`.
    mapping(address => uint256) public toWildcardIndex;

    /// @notice Which storage layout this deployment uses — [`LAYOUT_VERSION`]. Slot 6. Appended
    ///         after every array so bumping it cannot shift them. State, not a constant, because
    ///         op-reth reads state; it refuses to start on a version it cannot read.
    uint256 public layoutVersion;

    /// @notice The one L1 address permitted to govern, stored **unaliased**. Slot 7. Must be an L1
    ///         contract — the portal aliases only contracts, so an EOA is locked out. Declared after
    ///         `layoutVersion`: at the top it would take slot 0 and shift every array.
    address public authorizedL1;

    /// @notice Maximum rules one `updatePreconfs` may touch, summed across both arguments. Sized on
    ///         a pair add (68,044 gas): 256 need 17,419,375 of the 19,714,744 a deposit can buy
    ///         (20,000,000 less 285,256 intrinsic). Ceiling is 289; re-measure before raising.
    uint256 public constant MAX_BATCH = PRECONF_MAX_BATCH;

    /// @notice The layout this contract declares, written to [`layoutVersion`] at construction. Bump
    ///         on any positional change to the storage above; a rename is not one. op-reth pins the
    ///         same number as `EXPECTED_LAYOUT_VERSION` and refuses anything else.
    uint256 public constant LAYOUT_VERSION = 2;

    /// @notice Emitted whenever the allowlist changes; op-reth watches topic0 to trigger a re-read.
    ///         Changing this signature moves topic0 and silently stops the sequencer refreshing.
    ///         `WHITELIST_UPDATED_TOPIC0` pins the same literal; parameter names are not part of it.
    /// @param exactPairCount    Number of exact rules after the update.
    /// @param fromWildcardCount Number of sender wildcards after the update.
    /// @param toWildcardCount   Number of recipient wildcards after the update.
    event WhitelistUpdated(uint256 exactPairCount, uint256 fromWildcardCount, uint256 toWildcardCount);

    /// @notice Emitted when [`authorizedL1`] is rotated, carrying both ends — the replaced value is
    ///         the only record of who authorized the replacement. op-reth does not watch this.
    /// @param previousAuthorizedL1 The address permitted to govern until this call.
    /// @param newAuthorizedL1      The address permitted to govern from now on.
    event AuthorizedL1Updated(address indexed previousAuthorizedL1, address indexed newAuthorizedL1);

    /// @notice Seeds the allowlist so the contract is usable the moment it is deployed. Not subject
    ///         to `MAX_BATCH` — deployment gas is bounded by the deployer, not by a relayed message.
    /// @param _authorizedL1 L1 governance address permitted to update the allowlist.
    /// @param _initRules    Initial rules, in the three forms `updatePreconfs` accepts. A
    ///                      `getAllRules()` result is valid here verbatim, which is how a clone works.
    constructor(address _authorizedL1, Rule[] memory _initRules) {
        require(_authorizedL1 != address(0), "PreconfWhitelist: authorized L1 sender is the zero address");
        authorizedL1 = _authorizedL1;
        layoutVersion = LAYOUT_VERSION;
        for (uint256 i = 0; i < _initRules.length; i++) {
            _apply(_initRules[i].from, _initRules[i].to, true);
        }
        emit WhitelistUpdated(exactPairs.length, fromWildcards.length, toWildcards.length);
    }

    /// @notice Restricts a function to deposits originated on L1 by [`authorizedL1`]. Comparing the
    ///         alias-UNDONE sender keeps `authorizedL1` human-checkable and closes impersonation: a
    ///         raw compare would let any L1 contract speak for the same address on L2.
    modifier onlyL1Gov() {
        require(
            AddressAliasHelper.undoL1ToL2Alias(msg.sender) == authorizedL1,
            "PreconfWhitelist: caller is not the authorized L1 sender"
        );
        _;
    }

    /// @notice Rotates the L1 address permitted to govern this allowlist. Governance rotates itself,
    ///         so this can strand the contract, and it fails silently from L1. Rotating to an EOA is
    ///         terminal; only `PreconfWhitelistGov` can screen for code, since L2 cannot see L1.
    /// @param _authorizedL1 New L1 governance address, unaliased. Must be a contract on L1.
    function setAuthorizedL1(address _authorizedL1) external onlyL1Gov {
        require(_authorizedL1 != address(0), "PreconfWhitelist: authorized L1 sender is the zero address");
        emit AuthorizedL1Updated(authorizedL1, _authorizedL1);
        authorizedL1 = _authorizedL1;
    }

    /// @notice Applies a batch. `from == 0` is a recipient wildcard, `to == 0` a sender wildcard,
    ///         both zero reverts; adds run before removes and every operation is idempotent. No
    ///         replay guard needed (a deposit lands once), but a starved `_gasLimit` fails on L2.
    /// @param _add    Rules to authorize.
    /// @param _remove Rules to revoke.
    function updatePreconfs(Rule[] calldata _add, Rule[] calldata _remove) external onlyL1Gov {
        require(_add.length + _remove.length <= MAX_BATCH, "PreconfWhitelist: batch too large");
        for (uint256 i = 0; i < _add.length; i++) {
            _apply(_add[i].from, _add[i].to, true);
        }
        for (uint256 i = 0; i < _remove.length; i++) {
            _apply(_remove[i].from, _remove[i].to, false);
        }
        emit WhitelistUpdated(exactPairs.length, fromWildcards.length, toWildcards.length);
    }

    // ===== views (op-reth calls none of these; it reads storage directly) =====
    // Each set reads paginated or unbounded via `getAll*`. The unbounded form costs one cold SLOAD
    // per element, so past ~10^4 entries it exceeds an eth_call's gas cap and reverts, not truncates.

    /// @notice Number of exact rules.
    /// @return Length of `exactPairs`, the exclusive upper bound for `getExactPairs`'s `_offset`.
    function exactPairsLength() external view returns (uint256) {
        return exactPairs.length;
    }

    /// @notice Number of sender wildcards.
    /// @return Length of `fromWildcards`, the exclusive bound for `getFromWildcards`'s `_offset`.
    function fromWildcardsLength() external view returns (uint256) {
        return fromWildcards.length;
    }

    /// @notice Number of recipient wildcards.
    /// @return Length of `toWildcards`, the exclusive bound for `getToWildcards`'s `_offset`.
    function toWildcardsLength() external view returns (uint256) {
        return toWildcards.length;
    }

    /// @notice Total rules across all three sets — the length `getAllRules` returns, the bound for
    ///         `getRules`, and the size to check before reaching for the unbounded getters.
    /// @return Sum of the three set lengths.
    function rulesLength() external view returns (uint256) {
        return exactPairs.length + fromWildcards.length + toWildcards.length;
    }

    /// @notice Up to `_limit` exact rules starting at `_offset`.
    /// @param _offset Index of the first rule; at or past the end yields an empty array. Order is
    ///                not stable across updates — removal swaps the last element into the gap.
    /// @param _limit  Maximum number to return, truncated to what remains after `_offset`.
    /// @return The requested page, shorter than `_limit` when fewer rules remain.
    function getExactPairs(uint256 _offset, uint256 _limit) external view returns (Rule[] memory) {
        return _pageExact(_offset, _limit);
    }

    /// @notice Up to `_limit` sender wildcards starting at `_offset`.
    /// @param _offset Index of the first wildcard; at or past the end yields an empty array. Order
    ///                is not stable across updates.
    /// @param _limit  Maximum number to return, truncated to what remains after `_offset`.
    /// @return The requested page.
    function getFromWildcards(uint256 _offset, uint256 _limit) external view returns (address[] memory) {
        return _pageWildcards(fromWildcards, _offset, _limit);
    }

    /// @notice Up to `_limit` recipient wildcards starting at `_offset`.
    /// @param _offset Index of the first wildcard; at or past the end yields an empty array. Order
    ///                is not stable across updates.
    /// @param _limit  Maximum number to return, truncated to what remains after `_offset`.
    /// @return The requested page.
    function getToWildcards(uint256 _offset, uint256 _limit) external view returns (address[] memory) {
        return _pageWildcards(toWildcards, _offset, _limit);
    }

    /// @notice Up to `_limit` rules from `_offset` over the whole allowlist — the paginated
    ///         counterpart of `getAllRules`, same concatenation order and wildcard encoding.
    /// @param _offset Index into the concatenated view; at or past the total yields an empty array.
    /// @param _limit  Maximum number to return, truncated to what remains after `_offset`.
    /// @return The requested page, in wire form.
    function getRules(uint256 _offset, uint256 _limit) external view returns (Rule[] memory) {
        return _pageRules(_offset, _limit);
    }

    /// @notice Every exact rule in one call. Unbounded — see the gas cliff noted above.
    /// @return All of `exactPairs`, in storage order.
    function getAllExactPairs() external view returns (Rule[] memory) {
        return _pageExact(0, type(uint256).max);
    }

    /// @notice Every sender wildcard in one call. Unbounded — see the note above.
    /// @return All of `fromWildcards`, in storage order.
    function getAllFromWildcards() external view returns (address[] memory) {
        return _pageWildcards(fromWildcards, 0, type(uint256).max);
    }

    /// @notice Every recipient wildcard in one call. Unbounded — see the note above.
    /// @return All of `toWildcards`, in storage order.
    function getAllToWildcards() external view returns (address[] memory) {
        return _pageWildcards(toWildcards, 0, type(uint256).max);
    }

    /// @notice The whole allowlist as one flat `Rule[]`: the three sets concatenated in the order
    ///         `exactPairs`, `fromWildcards`, `toWildcards`, wildcards in zero-address wire form.
    /// @return Every rule in the allowlist, wildcards included, in wire form.
    function getAllRules() external view returns (Rule[] memory) {
        return _pageRules(0, type(uint256).max);
    }

    /// @notice The eligibility predicate: true if any of the three sets allows `_from -> _to`. This
    ///         is the question the sequencer answers per transaction.
    /// @param _from Sender of the transaction.
    /// @param _to   Recipient, or the zero address for a contract creation, which needs no case.
    /// @return True if the transaction is eligible for the preconf fast path.
    function isWhitelist(address _from, address _to) external view returns (bool) {
        return exactPairIndex[_from][_to] != 0 || fromWildcardIndex[_from] != 0 || toWildcardIndex[_to] != 0;
    }

    /// @notice True if `_from -> _to` is an exact rule. Does NOT consider wildcards, so this alone
    ///         does not answer whether traffic is eligible — see `isWhitelist`.
    /// @param _from Sender half of the rule.
    /// @param _to   Recipient half. Neither half is ever zero in storage, so zero never matches.
    /// @return True if the exact pair is present.
    function isExactPair(address _from, address _to) external view returns (bool) {
        return exactPairIndex[_from][_to] != 0;
    }

    /// @notice True if every transaction from `_addr` is eligible.
    /// @param _addr Sender to test. The zero address is never stored, so it never matches.
    /// @return True if `_addr` is a sender wildcard.
    function isFromWildcard(address _addr) external view returns (bool) {
        return fromWildcardIndex[_addr] != 0;
    }

    /// @notice True if every transaction to `_addr` is eligible.
    /// @param _addr Recipient to test. The zero address is never stored, so it never matches.
    /// @return True if `_addr` is a recipient wildcard.
    function isToWildcard(address _addr) external view returns (bool) {
        return toWildcardIndex[_addr] != 0;
    }

    // ===== internals =====

    /// @notice Routes one rule to its set and adds or removes it — the single place the three forms
    ///         are interpreted, so the constructor and `updatePreconfs` cannot disagree. Takes
    ///         halves rather than a `Rule` so one body serves `memory` and `calldata` callers.
    /// @param _from  Sender half, or the zero address to route this rule to `toWildcards`.
    /// @param _to    Recipient half, or the zero address to route it to `fromWildcards`.
    /// @param _isAdd True to authorize the rule, false to revoke it.
    function _apply(address _from, address _to, bool _isAdd) internal {
        require(_from != address(0) || _to != address(0), "PreconfWhitelist: pair is all-zero");
        if (_from == address(0)) {
            if (_isAdd) _addWildcard(toWildcards, toWildcardIndex, _to);
            else _rmWildcard(toWildcards, toWildcardIndex, _to);
        } else if (_to == address(0)) {
            if (_isAdd) _addWildcard(fromWildcards, fromWildcardIndex, _from);
            else _rmWildcard(fromWildcards, fromWildcardIndex, _from);
        } else {
            if (_isAdd) _addExactPair(_from, _to);
            else _rmExactPair(_from, _to);
        }
    }

    /// @notice Appends `(_from, _to)` and records its position. No-op if already present.
    /// @param _from Sender half, known non-zero — that is why `_apply` routed here.
    /// @param _to   Recipient half, known non-zero.
    function _addExactPair(address _from, address _to) internal {
        if (exactPairIndex[_from][_to] != 0) return;
        exactPairs.push(Rule({ from: _from, to: _to }));
        exactPairIndex[_from][_to] = exactPairs.length;
    }

    /// @notice Removes `(_from, _to)` by swap-and-pop, keeping the index in step. No-op if absent.
    ///         Removing the last element is the degenerate case of the same code: the swap writes it
    ///         over itself, the index write restores it, and the `delete` then clears it for good.
    /// @param _from Sender half of the rule to revoke.
    /// @param _to   Recipient half of the rule to revoke.
    function _rmExactPair(address _from, address _to) internal {
        uint256 idx = exactPairIndex[_from][_to];
        if (idx == 0) return;
        uint256 i = idx - 1;
        Rule memory last = exactPairs[exactPairs.length - 1];
        exactPairs[i] = last;
        exactPairIndex[last.from][last.to] = i + 1;
        exactPairs.pop();
        delete exactPairIndex[_from][_to];
    }

    /// @notice Appends `_addr` to `_list` and records its position. No-op if already present.
    ///         Nothing may store a zero: op-reth reads an unwritten slot back as the zero address,
    ///         so a stored one would be indistinguishable from absence.
    /// @param _list   Wildcard array to append to — `fromWildcards` or `toWildcards`.
    /// @param _idxMap That array's membership index, written in the same call to stay in step.
    /// @param _addr   Address to authorize, known non-zero.
    function _addWildcard(
        address[] storage _list,
        mapping(address => uint256) storage _idxMap,
        address _addr
    )
        internal
    {
        if (_idxMap[_addr] != 0) return;
        _list.push(_addr);
        _idxMap[_addr] = _list.length;
    }

    /// @notice Removes `_addr` from `_list` by swap-and-pop, keeping `_idxMap` in step. No-op if absent.
    /// @param _list   Wildcard array to remove from — `fromWildcards` or `toWildcards`.
    /// @param _idxMap That array's membership index, rewritten for the swapped-in element.
    /// @param _addr   Address to revoke.
    function _rmWildcard(address[] storage _list, mapping(address => uint256) storage _idxMap, address _addr) internal {
        uint256 idx = _idxMap[_addr];
        if (idx == 0) return;
        uint256 i = idx - 1;
        address last = _list[_list.length - 1];
        _list[i] = last;
        _idxMap[last] = i + 1;
        _list.pop();
        delete _idxMap[_addr];
    }

    /// @notice Shared paging body for `getExactPairs` and `getAllExactPairs`.
    /// @param _offset Index of the first rule; at or past the end yields an empty array.
    /// @param _limit  Maximum number to return, truncated to what remains after `_offset`.
    /// @return out_ The requested page.
    function _pageExact(uint256 _offset, uint256 _limit) internal view returns (Rule[] memory out_) {
        uint256 len = exactPairs.length;
        if (_offset >= len) return new Rule[](0);
        uint256 n = len - _offset;
        if (_limit < n) n = _limit;
        out_ = new Rule[](n);
        for (uint256 i = 0; i < n; i++) {
            out_[i] = exactPairs[_offset + i];
        }
    }

    /// @notice Shared paging body for the two wildcard getters and their `getAll*` counterparts.
    /// @param _list   Wildcard array to page over.
    /// @param _offset Index of the first element; at or past the end yields an empty array.
    /// @param _limit  Maximum number to return, truncated to what remains after `_offset`.
    /// @return out_ The requested page.
    function _pageWildcards(
        address[] storage _list,
        uint256 _offset,
        uint256 _limit
    )
        internal
        view
        returns (address[] memory out_)
    {
        uint256 len = _list.length;
        if (_offset >= len) return new address[](0);
        uint256 n = len - _offset;
        if (_limit < n) n = _limit;
        out_ = new address[](n);
        for (uint256 i = 0; i < n; i++) {
            out_[i] = _list[_offset + i];
        }
    }

    /// @notice Shared paging body for `getRules` and `getAllRules`. Maps a position in the virtual
    ///         concatenation of the three sets onto the array holding it, re-encoding wildcards into
    ///         the zero-address wire form on the way out.
    /// @param _offset Index into the concatenated view; at or past the total yields an empty array.
    /// @param _limit  Maximum number to return, truncated to what remains after `_offset`.
    /// @return out_ The requested page.
    function _pageRules(uint256 _offset, uint256 _limit) internal view returns (Rule[] memory out_) {
        uint256 eLen = exactPairs.length;
        uint256 fLen = fromWildcards.length;
        uint256 tLen = toWildcards.length;
        uint256 total = eLen + fLen + tLen;
        if (_offset >= total) return new Rule[](0);
        uint256 n = total - _offset;
        if (_limit < n) n = _limit;
        out_ = new Rule[](n);
        for (uint256 i = 0; i < n; i++) {
            uint256 j = _offset + i;
            if (j < eLen) {
                out_[i] = exactPairs[j];
            } else if (j < eLen + fLen) {
                out_[i] = Rule({ from: fromWildcards[j - eLen], to: address(0) });
            } else {
                out_[i] = Rule({ from: address(0), to: toWildcards[j - eLen - fLen] });
            }
        }
    }
}
