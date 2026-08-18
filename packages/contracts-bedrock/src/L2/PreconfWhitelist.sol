// SPDX-License-Identifier: MIT
pragma solidity 0.8.15;

import { L2CrossDomainMessenger } from "./L2CrossDomainMessenger.sol";

/// @title PreconfWhitelist
/// @notice Authoritative on-chain storage for the sequencer's preconfirmation allowlist. The
///         sequencer (op-reth) mirrors these three sets into memory and consults them when
///         deciding whether a transaction is eligible for the preconf fast path.
///
///         Eligibility is an **explicit `(from, to)` relation**, not a cross product:
///
///           eligible(from, to)  <=>  isExactPair(from, to)
///                                ||  isFromWildcard(from)
///                                ||  isToWildcard(to)
///
///         The predicate is a plain OR with no precedence and no deny list. In particular,
///         removing `(A, X)` from the exact set does NOT stop `A -> X` if `A` is still a from
///         wildcard; the wildcard has to be removed too.
///
///         Updates may only originate from a single authorized L1 address (the governance Safe),
///         relayed through the standard OP-Stack L1->L2 CrossDomainMessenger channel. No L1
///         contract is deployed for this: governance calls `L1CrossDomainMessenger.sendMessage`
///         directly, which becomes an OptimismPortal deposit and lands here via
///         `L2CrossDomainMessenger.relayMessage`.
///
///         This is a regular deployed contract, NOT a predeploy — it is deployed with a normal L2
///         transaction and its address is passed to op-reth via `--preconf.whitelist-address`.
///
/// @dev    A note on vocabulary, because one word used to carry two meanings. A [`Rule`] is the
///         **wire format**: the struct governance submits, in any of the three forms below. An
///         **exact pair** is one of the three *sets* — the rules whose halves are both non-zero,
///         held in [`exactPairs`]. Every exact pair is a rule; a wildcard is a rule too, but not
///         an exact pair. Identifiers follow that split: `Rule` and `getAllRules` speak about the
///         wire format, everything spelled `exactPair*` speaks about the one set.
///
///         STORAGE LAYOUT IS LOAD-BEARING. op-reth reads `exactPairs` (slot 0), `fromWildcards`
///         (slot 2) and `toWildcards` (slot 4) directly out of state — it never calls the view
///         functions below, because the classifier's eligibility check is a synchronous,
///         no-IO hot path that reads an in-memory mirror. The six state variables must therefore
///         keep their exact order and slot assignment. Do NOT insert, reorder, or remove state
///         variables, and do not add a base contract that declares storage. The Rust side pins the
///         same numbers in `mantle-reth/crates/preconf/src/whitelist.rs`
///         (`PAIRS_SLOT` / `FROM_WILDCARDS_SLOT` / `TO_WILDCARDS_SLOT`);
///         `test/PreconfWhitelist.t.sol` asserts the layout so a drift breaks CI here first.
///         `layoutVersion` (slot 6) is the runtime guard for the same coupling — see its docs.
///
///         Note especially that a `Rule` element spans **two** slots: two addresses are 40 bytes,
///         so they cannot share one. `exactPairs[i].from` lives at `keccak256(0) + 2i` and
///         `exactPairs[i].to` at `keccak256(0) + 2i + 1`. The Rust reader depends on that stride.
///
///         Renaming anything below is safe for op-reth as long as the *declaration order* holds:
///         the Rust side binds no view function and reads slots by number, and the sole ABI
///         couplings are the `updatePreconfs` selector and the `WhitelistUpdated` topic0, neither
///         of which depends on a parameter or variable name. A rename must NOT bump
///         [`LAYOUT_VERSION`] — nothing moved.
contract PreconfWhitelist {
    /// @notice One allowlist rule, in the form governance submits it. Both halves non-zero means an
    ///         exact match; exactly one zero half means a wildcard on the other side (see
    ///         `updatePreconfs`). Stored entries in `exactPairs` always have both halves non-zero —
    ///         the zero address is a calldata-only marker and never reaches storage.
    struct Rule {
        address from;
        address to;
    }

    /// @notice The L2CrossDomainMessenger predeploy — the only permitted direct caller of
    ///         `updatePreconfs`. Cross-domain messages are relayed to us through it.
    /// @dev    Held as an `address` because a cast to a contract type is not a compile-time
    ///         constant; the cast happens at the call site (same as `CrossDomainOwnable2`).
    ///         Spelled as a literal rather than `Predeploys.L2_CROSS_DOMAIN_MESSENGER` because
    ///         solc 0.8.15 rejects a library constant in a constant initializer. There is no
    ///         explicit `assertEq` pinning the two: `MESSENGER` is `internal` and unreachable from
    ///         a test, so such an assertion would compare two constants and say nothing about this
    ///         contract. Equality is proven implicitly instead — `test/PreconfWhitelist.t.sol`
    ///         declares its messenger *as* that predeploy, and the first gate below admits only
    ///         `msg.sender == MESSENGER`, so every passing governance call in that file depends on
    ///         it. See the note on `test_updatePreconfs_notMessenger_reverts`.
    address internal constant MESSENGER = 0x4200000000000000000000000000000000000007;

    /// @notice The one L1 address permitted to govern this allowlist (the governance Safe), stored
    ///         unaliased. This is the address that calls `L1CrossDomainMessenger.sendMessage`, as
    ///         reported by `xDomainMessageSender()` during relay.
    /// @dev    Immutable, so it occupies no storage slot and cannot shift the layout below.
    ///         Rotating the governance Safe therefore requires redeploying this contract and
    ///         repointing op-reth at the new address.
    address public immutable AUTHORIZED_L1;

    /// @notice Exact `(from, to)` rules — the rules with **both halves non-zero**, as distinct from
    ///         the two wildcard sets below. Slot 0 — see the storage-layout note above. Elements
    ///         span two slots each.
    Rule[] public exactPairs;

    /// @notice Membership index for `exactPairs`. Slot 1. Stores `index + 1`, so 0 means "not
    ///         present".
    /// @dev    Nested rather than a single `bytes32` key over `keccak256(abi.encode(from, to))`:
    ///         the nested form is measurably cheaper (Solidity computes mapping slots in scratch
    ///         space, while `abi.encode` pays for memory allocation — ~355 gas per SSTORE,
    ///         ~379 per SLOAD), and its generated getter takes the two addresses directly instead
    ///         of forcing every off-chain caller to reproduce a hashing scheme. It cannot be a
    ///         `bool`: `_rmExactPair` needs the element's array position.
    mapping(address => mapping(address => uint256)) public exactPairIndex;

    /// @notice Senders whose transactions are all eligible, whatever the recipient. Slot 2 — see
    ///         the storage-layout note above.
    address[] public fromWildcards;

    /// @notice Membership index for `fromWildcards`. Slot 3. Stores `index + 1`.
    mapping(address => uint256) public fromWildcardIndex;

    /// @notice Recipients that make any transaction to them eligible, whatever the sender. Slot 4
    ///         — see the storage-layout note above.
    address[] public toWildcards;

    /// @notice Membership index for `toWildcards`. Slot 5. Stores `index + 1`.
    mapping(address => uint256) public toWildcardIndex;

    /// @notice Which storage layout this contract was deployed with — [`LAYOUT_VERSION`]. Slot 6.
    /// @dev    **Appended after every array on purpose**, so bumping it can never shift the slots
    ///         op-reth reads.
    ///
    ///         It exists because the has-code check on the sequencer side proves only that
    ///         *something* is deployed at the configured address, not that it is this contract at
    ///         this layout. Without a marker, a version skew in either direction is silent and
    ///         actively wrong rather than merely stale:
    ///
    ///         * a binary built for the previous cross-product layout reads `exactPairs` with a
    ///           one-slot stride, so it takes alternating `from`/`to` halves for a sender list;
    ///         * a binary built for this layout, pointed at the previous contract, reads that
    ///           contract's *recipient* list out of slot 2 and installs it as **sender
    ///           wildcards** — authorizing traffic governance never approved.
    ///
    ///         A storage variable rather than a `constant`: constants live in code, and op-reth
    ///         reads state, not code. One SSTORE at deployment buys a one-slot check at every cold
    ///         start.
    uint256 public layoutVersion;

    /// @notice Maximum number of rules a single `updatePreconfs` call may touch, summed across
    ///         both arguments.
    /// @dev    Adding an exact pair is the most expensive per-rule operation (two array slots plus
    ///         one mapping slot, against one array slot plus one mapping slot for a wildcard), so
    ///         bounding the *total* count by the exact-pair-add capacity makes
    ///         `MAX_BATCH * gas(_addExactPair)` a strict bound on the call's gas regardless of how
    ///         the batch is composed. Oversized batches revert before any SSTORE, so they are cheap
    ///         and change nothing.
    ///
    ///         **Measured**, by `test_gas_maxBatchOfPairAdds` / `..OfWildcardAdds`:
    ///
    ///           pair add      69,098 gas   ->  256 of them = 17,689,309
    ///           wildcard add  46,647 gas   ->  256 of them = 11,941,833
    ///
    ///         What that has to fit in is **not** the L2 block gas limit — on Mantle that is orders
    ///         of magnitude larger and never binds. The binding constraint is on **L1**: governance
    ///         reaches us through `L1CrossDomainMessenger.sendMessage`, which asks
    ///         `OptimismPortal.depositTransaction` for `baseGas(_message, _minGasLimit)` gas, and
    ///         that call is `metered`. `ResourceMetering` caps the deposit gas bought per L1 block
    ///         at `maxResourceLimit` — 20,000,000 in `Constants.DEFAULT_RESOURCE_CONFIG`. So the
    ///         ceiling on the L2 execution governance can pay for is what survives `baseGas`:
    ///
    ///           20,000,000  -  683,088 fixed overhead   (200k constant + 16,516 calldata bytes at
    ///                                                    16+2 each + 800 + 40k + 90k + 55k)
    ///                       =  19,316,912, then x 63/64  (the EIP-150 dynamic term)
    ///                       =  19,015,085 of `_minGasLimit`
    ///
    ///         256 pair adds need 17,689,309 of that — **7% headroom**. Extrapolating the two
    ///         measurements puts the hard ceiling near 276 rules, so 256 is the power of two just
    ///         below it. The headroom is not decoration: `maxResourceLimit` is a budget
    ///         *shared across every depositor in the L1 block*, so the 19M figure is the best case
    ///         of an otherwise-idle block. Buying near it is also expensive, since the target is
    ///         `maxResourceLimit / elasticityMultiplier` = 2,000,000 and the deposit base fee climbs
    ///         against that target.
    ///
    ///         Raising this constant means re-running those two tests and re-deriving the ceiling
    ///         above, not scaling from the ratio. Lowering `maxResourceLimit` on L1 lowers the
    ///         ceiling too, and nothing here would notice — see the warning on `updatePreconfs`.
    uint256 public constant MAX_BATCH = 256;

    /// @notice The layout this contract declares, written to [`layoutVersion`] at construction.
    /// @dev    Bump on **any** change to the storage layout above — a reordering, an insertion, or
    ///         a change to `Rule`'s field count. Renaming a variable is not such a change; the
    ///         layout is positional. op-reth pins the same number in
    ///         `mantle-reth/crates/preconf/src/whitelist.rs` (`EXPECTED_LAYOUT_VERSION`) and
    ///         refuses to start against anything else, so the two move together or the node says
    ///         so at boot instead of mis-reading state for the rest of its life.
    ///
    ///         `1` was the cross-product layout (`preconfFromList` / `preconfToList`); it never
    ///         wrote this slot, so it reads back as `0` and is rejected on the same path.
    uint256 public constant LAYOUT_VERSION = 2;

    /// @notice Emitted whenever the allowlist changes. op-reth watches for this (topic0
    ///         `keccak256("WhitelistUpdated(uint256,uint256,uint256)")`) and re-reads all three
    ///         sets from state.
    /// @dev    Changing this signature changes topic0 and would silently stop the sequencer from
    ///         refreshing — it would keep running on whatever it read at bootstrap, with no error.
    ///         The Rust constant is `WHITELIST_UPDATED_TOPIC0` in
    ///         `mantle-reth/crates/preconf/src/whitelist.rs`; both sides assert the same literal.
    ///         Parameter *names* are not part of the signature, so renaming them is free.
    /// @param exactPairCount    Number of exact rules after the update.
    /// @param fromWildcardCount Number of sender wildcards after the update.
    /// @param toWildcardCount   Number of recipient wildcards after the update.
    event WhitelistUpdated(uint256 exactPairCount, uint256 fromWildcardCount, uint256 toWildcardCount);

    /// @notice Seeds the initial allowlist so the contract is usable the moment it is deployed,
    ///         with no follow-up governance message required.
    /// @dev    Deliberately not subject to `MAX_BATCH`: deployment gas is bounded by whoever sends
    ///         the deployment transaction, not by a relayed message's `minGasLimit`.
    /// @param _authorizedL1 L1 governance address permitted to update the allowlist.
    /// @param _initRules    Initial rules, in the same three forms `updatePreconfs` accepts. A
    ///                      `getAllRules()` reading from an existing deployment can be passed here
    ///                      verbatim to clone its allowlist.
    constructor(address _authorizedL1, Rule[] memory _initRules) {
        require(_authorizedL1 != address(0), "PreconfWhitelist: authorized L1 sender is the zero address");
        AUTHORIZED_L1 = _authorizedL1;
        layoutVersion = LAYOUT_VERSION;
        for (uint256 i = 0; i < _initRules.length; i++) {
            _apply(_initRules[i].from, _initRules[i].to, true);
        }
        emit WhitelistUpdated(exactPairs.length, fromWildcards.length, toWildcards.length);
    }

    /// @notice Restricts a function to cross-domain messages sent by `AUTHORIZED_L1`.
    /// @dev    Two independent checks, both required:
    ///         (1) the caller is the L2CrossDomainMessenger, proving we were reached by a relayed
    ///             cross-domain message rather than by a deposit aimed straight at this contract;
    ///         (2) the original L1 sender is `AUTHORIZED_L1`, rejecting relayed messages from any
    ///             other L1 caller. Forgery is prevented upstream by the messenger's own gate,
    ///             which only accepts deposits whose `from` is the aliased L1CrossDomainMessenger.
    modifier onlyL1Gov() {
        require(msg.sender == MESSENGER, "PreconfWhitelist: caller is not the messenger");
        require(
            L2CrossDomainMessenger(payable(MESSENGER)).xDomainMessageSender() == AUTHORIZED_L1,
            "PreconfWhitelist: caller is not the authorized L1 sender"
        );
        _;
    }

    /// @notice Applies a batch of allowlist changes. Each `Rule` is routed by its zero halves:
    ///
    ///           (from != 0, to != 0)  ->  exact rule, `exactPairs`
    ///           (from == 0, to != 0)  ->  recipient wildcard, `toWildcards`
    ///           (from != 0, to == 0)  ->  sender wildcard, `fromWildcards`
    ///           (from == 0, to == 0)  ->  reverts
    ///
    ///         The all-zero form would mean "every transaction is eligible". That capability is
    ///         deliberately absent: turning preconf on for everything is the node operator's
    ///         switch (`--preconf.all`), not governance's, and reverting also catches a governance
    ///         script that shipped an uninitialized array element — which would otherwise vanish
    ///         silently and surface later as "why is this rule not in effect".
    ///
    ///         Adds are applied before removes, so a rule present in both arguments ends up
    ///         removed. Every individual operation is idempotent: adding a present rule or
    ///         removing an absent one is a no-op rather than a revert, so a delta computed against
    ///         slightly stale state still applies cleanly.
    ///
    ///         No normalization or deduplication happens across the three sets. `(A, B)` and
    ///         `(A, 0)` may both be present; both match, and removing one leaves the other. Which
    ///         of them expresses governance's intent is not something this contract can infer.
    /// @dev    [`MAX_BATCH`] bounds this call's gas, but the matching *supply* of gas is an L1-side
    ///         decision this contract cannot see or check: governance picks `_minGasLimit` when it
    ///         calls `sendMessage`, and the portal's `maxResourceLimit` caps what that may be. If
    ///         either is too small, the relayed call runs out of gas inside
    ///         `L2CrossDomainMessenger.relayMessage`, which records the message as failed rather
    ///         than reverting the block — so the allowlist silently stays as it was, and the L1
    ///         transaction that sent it still succeeded. Governance must verify on L2 that the
    ///         update landed; a batch at or below [`MAX_BATCH`] is a necessary condition, not a
    ///         sufficient one.
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

    // ===== views =====
    //
    // Each set is readable two ways: a paginated getter and an unbounded `getAll*` one. The whole
    // allowlist is readable the same two ways, through `getRules` / `getAllRules`.
    //
    // The unbounded form is the convenient one and is what most tooling should reach for, but it
    // has a cliff: the returned array is built in memory and every element costs a cold SLOAD, so
    // past some length the call runs out of gas. For an `eth_call` that ceiling is the RPC node's
    // `--rpc.gascap` (50M by default), which puts the boundary on the order of 10^4 exact pairs —
    // far beyond any expected allowlist, but a real limit that moves when governance grows the
    // list rather than when the caller chooses. For a *contract* caller the block gas limit binds
    // much sooner. The failure mode is a reverting call, not a truncated result.
    //
    // That is what the paginated form is for: it turns the cliff into an explicit parameter, so a
    // caller that outgrows `getAll*` has somewhere to go without an interface change. The `getAll`
    // prefix is deliberate — it puts the unboundedness at the call site instead of hiding it
    // behind an overload of the same name.
    //
    // op-reth is unaffected either way: it reads storage directly and calls none of these.

    /// @notice Number of exact rules.
    /// @return Length of `exactPairs`, i.e. the upper bound for `getExactPairs`'s `_offset`.
    function exactPairsLength() external view returns (uint256) {
        return exactPairs.length;
    }

    /// @notice Number of sender wildcards.
    /// @return Length of `fromWildcards`, i.e. the upper bound for `getFromWildcards`'s `_offset`.
    function fromWildcardsLength() external view returns (uint256) {
        return fromWildcards.length;
    }

    /// @notice Number of recipient wildcards.
    /// @return Length of `toWildcards`, i.e. the upper bound for `getToWildcards`'s `_offset`.
    function toWildcardsLength() external view returns (uint256) {
        return toWildcards.length;
    }

    /// @notice Number of rules in total, across all three sets.
    /// @dev    The length `getAllRules` returns, i.e. the upper bound for `getRules`'s `_offset`.
    ///         Also the size to check before reaching for `getAllRules`: that is the largest of the
    ///         unbounded getters and so the first to reach the gas cliff described above.
    /// @return Sum of the three set lengths.
    function rulesLength() external view returns (uint256) {
        return exactPairs.length + fromWildcards.length + toWildcards.length;
    }

    /// @notice Returns up to `_limit` exact rules starting at `_offset`.
    /// @dev    An out-of-range `_offset` returns an empty array and an over-long `_limit` is
    ///         truncated, so a caller paging to the end never has to special-case the last page.
    /// @param _offset Index of the first rule to return. Order is not stable across updates —
    ///                `_rmExactPair` swaps the last element into the freed position.
    /// @param _limit  Maximum number of rules to return.
    /// @return The requested page, shorter than `_limit` when fewer rules remain.
    function getExactPairs(uint256 _offset, uint256 _limit) external view returns (Rule[] memory) {
        return _pageExact(_offset, _limit);
    }

    /// @notice Returns up to `_limit` sender wildcards starting at `_offset`.
    /// @param _offset Index of the first wildcard to return; at or past the end yields an empty
    ///                array. Order is not stable across updates — `_rmWildcard` swaps the last element
    ///                into the freed position.
    /// @param _limit  Maximum number to return, truncated to what remains after `_offset`.
    /// @return The requested page.
    function getFromWildcards(uint256 _offset, uint256 _limit) external view returns (address[] memory) {
        return _pageWildcards(fromWildcards, _offset, _limit);
    }

    /// @notice Returns up to `_limit` recipient wildcards starting at `_offset`.
    /// @param _offset Index of the first wildcard to return; at or past the end yields an empty
    ///                array. Order is not stable across updates — `_rmWildcard` swaps the last element
    ///                into the freed position.
    /// @param _limit  Maximum number to return, truncated to what remains after `_offset`.
    /// @return The requested page.
    function getToWildcards(uint256 _offset, uint256 _limit) external view returns (address[] memory) {
        return _pageWildcards(toWildcards, _offset, _limit);
    }

    /// @notice Returns up to `_limit` rules starting at `_offset`, paging over the **whole**
    ///         allowlist without distinguishing exact pairs from wildcards — the paginated
    ///         counterpart of `getAllRules`, with the same concatenation order and the same
    ///         zero-address encoding for wildcards.
    /// @param _offset Index of the first rule to return, over the concatenated view.
    /// @param _limit  Maximum number of rules to return, truncated to what remains.
    /// @return The requested page, in wire form.
    function getRules(uint256 _offset, uint256 _limit) external view returns (Rule[] memory) {
        return _pageRules(_offset, _limit);
    }

    /// @notice Every exact rule, in one call. Unbounded — see the cliff described above the
    ///         paginated getters, and fall back to `getExactPairs` if the list ever outgrows this.
    /// @return All of `exactPairs`, in storage order.
    function getAllExactPairs() external view returns (Rule[] memory) {
        return _pageExact(0, type(uint256).max);
    }

    /// @notice Every sender wildcard, in one call. Unbounded — see the note above the paginated
    ///         getters.
    /// @return All of `fromWildcards`, in storage order.
    function getAllFromWildcards() external view returns (address[] memory) {
        return _pageWildcards(fromWildcards, 0, type(uint256).max);
    }

    /// @notice Every recipient wildcard, in one call. Unbounded — see the note above the paginated
    ///         getters.
    /// @return All of `toWildcards`, in storage order.
    function getAllToWildcards() external view returns (address[] memory) {
        return _pageWildcards(toWildcards, 0, type(uint256).max);
    }

    /// @notice The entire allowlist as one flat `Rule[]`, without distinguishing exact pairs from
    ///         wildcards — the three sets concatenated, in the order `exactPairs`, `fromWildcards`,
    ///         `toWildcards`.
    /// @return Every rule in the allowlist, wildcards included, in wire form.
    function getAllRules() external view returns (Rule[] memory) {
        return _pageRules(0, type(uint256).max);
    }

    /// @notice The eligibility predicate itself: true if `_from -> _to` is allowed by **any** of the
    ///         three sets. This is the question the sequencer answers per transaction; the three
    ///         accessors below expose the individual terms.
    /// @param _from Sender of the transaction.
    /// @param _to   Recipient, or `address(0)` for a contract creation.
    /// @return True if the transaction is eligible for the preconf fast path.
    function isWhitelist(address _from, address _to) external view returns (bool) {
        return exactPairIndex[_from][_to] != 0 || fromWildcardIndex[_from] != 0 || toWildcardIndex[_to] != 0;
    }

    /// @notice Returns true if `_from -> _to` is an exact rule. Does NOT consider wildcards — see
    ///         `isFromWildcard` / `isToWildcard`, and the OR in the contract docs.
    /// @param _from Sender half of the rule.
    /// @param _to   Recipient half of the rule. Neither half is ever the zero address in storage,
    ///              so a zero argument never matches.
    /// @return True if the exact pair is present.
    function isExactPair(address _from, address _to) external view returns (bool) {
        return exactPairIndex[_from][_to] != 0;
    }

    /// @notice Returns true if every transaction from `_addr` is eligible.
    /// @param _addr Sender to test. The zero address is never stored, so it never matches.
    /// @return True if `_addr` is a sender wildcard.
    function isFromWildcard(address _addr) external view returns (bool) {
        return fromWildcardIndex[_addr] != 0;
    }

    /// @notice Returns true if every transaction to `_addr` is eligible.
    /// @param _addr Recipient to test. The zero address is never stored, so it never matches.
    /// @return True if `_addr` is a recipient wildcard.
    function isToWildcard(address _addr) external view returns (bool) {
        return toWildcardIndex[_addr] != 0;
    }

    // ===== internals =====

    /// @notice Routes one rule to its set and adds or removes it. The single place the three
    ///         `Rule` forms are interpreted, so the constructor and `updatePreconfs` cannot
    ///         disagree about them.
    /// @dev    Takes the halves as value-type arguments rather than a `Rule`, so the same body
    ///         serves the constructor's `memory` array and `updatePreconfs`'s `calldata` one.
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

    /// @notice Appends `(_from, _to)` to `exactPairs` and records its position. No-op if already
    ///         present. Both halves are known non-zero — `_apply` routed us here precisely
    ///         because of that.
    /// @param _from Sender half, known non-zero.
    /// @param _to   Recipient half, known non-zero.
    function _addExactPair(address _from, address _to) internal {
        if (exactPairIndex[_from][_to] != 0) return;
        exactPairs.push(Rule({ from: _from, to: _to }));
        exactPairIndex[_from][_to] = exactPairs.length;
    }

    /// @notice Removes `(_from, _to)` from `exactPairs` by swapping in the final element and
    ///         popping, keeping `exactPairIndex` in step. No-op if absent. Order is not preserved;
    ///         the sequencer treats this as a set.
    /// @dev    Removing the last element is the degenerate case of the same code: the swap writes
    ///         the element over itself, the index write restores it, and the `delete` below then
    ///         clears it for good.
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

    /// @notice Appends `_addr` to `_list` and records its position in `_idxMap`. No-op if already
    ///         present. `_addr` is known non-zero: `_apply` only routes here when the *other* half
    ///         was the zero marker, and it rejects the all-zero form outright. Nothing may write a
    ///         zero entry — an unset array element reads back as the zero address on the sequencer
    ///         side, so a stored zero would be indistinguishable from a slot that was never
    ///         written.
    /// @param _list   Wildcard array to append to — `fromWildcards` or `toWildcards`.
    /// @param _idxMap That array's membership index, written in the same call to stay in step.
    /// @param _addr   Address to authorize, known non-zero.
    function _addWildcard(address[] storage _list, mapping(address => uint256) storage _idxMap, address _addr) internal {
        if (_idxMap[_addr] != 0) return;
        _list.push(_addr);
        _idxMap[_addr] = _list.length;
    }

    /// @notice Removes `_addr` from `_list` by swapping in the final element and popping, keeping
    ///         `_idxMap` in step. No-op if `_addr` is absent.
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
    /// @param _offset Index of the first rule to return; at or past the end yields an empty array.
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
    /// @param _offset Index of the first element to return; at or past the end yields an empty
    ///                array.
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
    ///         concatenation of the three sets onto the array that actually holds it, re-encoding
    ///         wildcards into the zero-address wire form on the way out.
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
