# Reviewing RPC Differences

`op-rpc-compat` keeps raw JSON-RPC differences separate from the effective
accepted-mode verdict. A passing or warning run establishes relative behavior
for the selected cases, not protocol correctness. The embedded accepted registry
starts empty; archived `known_diffs.json` entries are not approvals.

## Run And Review

1. Run the same core cases against endpoints with matching chain ID, genesis,
   and a comparable block snapshot. Save both `--diff-policy strict` and default
   accepted reports. Use `--suite diagnostic` separately for moving tags.
2. Compare a new report with the previous comparable run using
   `op-rpc-compat triage --previous OLD --current NEW --output triage.json`.
   A resolved difference requires the case to have run again; excluded cases
   appear as `not_run`, and executed but inconclusive cases as `uncompared`.
3. Investigate each new or changed group using the original requests and both
   responses. Decide whether the target needs a fix, a method-specific semantic
   assertion, a reviewed rule, or a diagnostic-only comparison.
4. Submit the decision and evidence for code review. AI may summarize groups
   and draft reasons; it must not edit the registry, set verdicts, or treat an
   earlier report as approval.

## Rule Scope

Registry schema v2 has two rule kinds:

| Scope | Appropriate use | Runtime selector |
|---|---|---|
| `behavior_invariant` | A documented difference remains acceptable when a reviewed client changes branch or SHA | Directional family selectors; same-family pairs require one exact-build anchor and two distinct recognizable builds |
| `exact_build` | A temporary or version-specific difference | Both exact client versions and build IDs, plus the reviewed directed pair |

Each rule also binds chain ID, genesis, embedded corpus identity, case ID,
method, normalized request digest, RFC 6901 pointer, difference type, both
sides' presence, JSON type, and exact value. Every failing difference in a case
must be owned by one rule or passing semantic assertion before accepted mode
can report `WARNING`. Reversed direction, new fields, changed values, invalid
responses, and overlapping rule ownership cannot be accepted silently.

For `behavior_invariant`, `reviewed_pairs` records the strict run that justified
cross-build reuse; the pair is evidence, not a runtime allowlist. The review
must explain why the difference remains acceptable across builds and link to
the interface contract and raw report. For `exact_build`, the pair is both
evidence and a matching condition. A generic family such as `Geth/` does not
prove a particular fork; use exact-build scope where the fork identity matters.
Both family and build information come from the self-reported
`web3_clientVersion`. A custom `--identity` without a recognizable family
cannot select accepted rules. An old reth binary without a build SHA cannot
serve as the exact anchor for a same-family old/new comparison.

Do not register changing fee quotes, node IDs, pending state, or other
environment-specific values as exact-value waivers. A semantic assertion must
validate both responses, name its owned paths, and leave all other differences
as failures. The current `admin_nodeInfo` assertion covers valid node-local
identity values only; protocol configuration remains strict. Fee semantics
require their own reviewed contract before any downgrade.

## Report And CI Boundaries

The default core suite is the CI gate. The diagnostic suite remains visible
and can return `INCONCLUSIVE`; CI may run it as a nonblocking job. Skipped cases
are listed in the report and never counted as `PASS`. Keep a strict report for
each build even when accepted mode has warnings. Report schema v4 records
the rule scope or semantic assertion ID alongside raw and effective outcomes.
Triage output is advisory and never changes the command's exit status.

Before enabling exact-build rules for the new Mantle reth, test its actual
`web3_clientVersion` against the policy parser: reth PR #135 emits a seven-digit
short SHA, whereas the previous policy parser required at least eight digits.
The v2 parser accepts seven through forty digits; this is build provenance,
not a substitute for review of a behavioral rule.
