# op-rpc-compat

`op-rpc-compat` compares the same JSON-RPC cases against a baseline and a target
execution client. The baseline response is the expected side of each diff; it is
not assumed to be the correct implementation. Both endpoints must serve the same
chain and have comparable state.

The caller owns the network. This command does not start nodes, parse an RDE
profile, restart services, or clear chain data. Before running cases it checks
`eth_chainId`, the genesis block hash, and `web3_clientVersion` on both endpoints.
A chain mismatch or unreachable endpoint stops the run before test execution.

## Build And Run

From the `mantle-v2` repository root:

```bash
just ./op-rpc-compat/test
make op-rpc-compat

./op-rpc-compat/bin/op-rpc-compat \
  --baseline-url http://127.0.0.1:19545 \
  --target-url http://127.0.0.1:29545
```

The URLs have no defaults. `BASELINE_RPC_URL` and `TARGET_RPC_URL` can supply
them; explicit flags take precedence over environment variables. The two sides
are identified as `baseline` and `target` in reports. Their observed
`web3_clientVersion` values are recorded separately. Caller-selected names do
not determine which differences are accepted.

This is a breaking CLI change from the RDE v3 tool. There are no `--geth`,
`--reth`, `--baseline-name`, `--target-name`, or corresponding legacy environment
variable aliases.

After RDE v3 updates its `src/mantle-v2` gitlink to a commit containing this
component, the same run can start from the RDE workspace:

```bash
go -C src/mantle-v2 run ./op-rpc-compat \
  --baseline-url http://127.0.0.1:19545 \
  --target-url http://127.0.0.1:29545
```

The same binary can compare two reth versions. The caller supplies endpoints
from its own network configuration:

```bash
go -C src/mantle-v2 run ./op-rpc-compat \
  --baseline-url "$OLD_RETH_RPC_URL" \
  --target-url "$NEW_RETH_RPC_URL"
```

RDE v4 profile endpoints and integration have not been validated here. That
work is separate from this component.

## Cases And Reports

The default JSON testcase corpus and the reviewed policy registry are embedded,
so the binary can run from any working directory. The 113 historical rules are
archived at `policies/archive/legacy-known-diffs.json` and are never loaded.
`--file PATH` loads one explicit OS file. `--testcases-dir DIR` replaces the embedded corpus with JSON files from an
OS directory. `--exclude FILENAME` removes a file from a corpus run and can be
repeated. Generated reports default to `report.json`; `--output ""` disables
report writing.

The default corpus covers standard `eth_*` queries, Mantle RPC extensions,
error cases, txpool, and debug methods. It does not submit chain transactions,
although methods such as `eth_newFilter` create temporary node-local state. A
live chain can change between the two requests. For `latest`, `safe`, and
`finalized`, the tool checks both endpoints' block hashes before and after the
case. It reports an unestablished snapshot as `INCONCLUSIVE`. `pending` has no
shared canonical block and is currently `INCONCLUSIVE`. Filter creation cases
create, query, and uninstall each endpoint's own filter; generated IDs are not
compared. `web3_clientVersion` is collected during preflight, not compared as a
testcase value.

Results have these statuses:

| Status | Meaning | Fails the run |
|---|---|---|
| `PASS` | Responses match | No |
| `WARNING` | Every difference was matched by an exact reviewed rule in accepted mode | No |
| `FAIL` | Responses differ or a request failed | Yes |
| `INCONCLUSIVE` | A comparable snapshot could not be established | Yes |
| `NOT_APPLICABLE` | The case is handled as preflight metadata | Does not count as a passing case |
| `COMPATIBLE` | Reserved for explicitly asserted transaction scenarios | No |

The default `--diff-policy accepted` applies only reviewed, directional,
per-difference rules from the embedded registry. That registry is initially
empty. `--diff-policy strict` shows the same raw differences without downgrading
them. Unknown build identity never matches a build-specific rule. The current
`mantle-v1.6.1` and development reth binaries report the same RPC version when
`--identity` is omitted, so neither is identified by that string alone.

Reports use schema version 3 and record raw and effective outcomes, the
registry ID and digest, endpoint versions and identity sources, and actual
responses. Invalid response bodies are retained losslessly in
`baseline_raw_body_base64` or `target_raw_body_base64` so a malformed RPC reply
cannot prevent the report from being saved:

```json
{
  "schema_version": 3,
  "policy_mode": "accepted",
  "registry_id": "accepted-rpc-differences",
  "baseline": {"name": "baseline", "url": "http://127.0.0.1:19545", "client_version": "Geth/..."},
  "target": {"name": "target", "url": "http://127.0.0.1:29545", "client_version": "mantle-reth/..."},
  "results": [
    {"observed_status": "PASS", "status": "PASS", "baseline_response": {"result": "0x1"}, "target_response": {"result": "0x1"}}
  ]
}
```

Rules bind the comparison direction, reviewed build pair, chain, corpus,
method, request digest, JSON Pointer, difference type, presence, and exact
values. External corpora cannot claim the embedded corpus identity. A rule
whose expected difference disappears is listed as stale in the report.

## Stateful Modes

`--tx` sends transactions, deploys and calls contracts, and inspects txpool and
receipts. It changes chain state and should run on a disposable network. It runs
transaction cases instead of the default JSON corpus. The default transaction
suite includes preconfirmation scenarios; `--tx --tx-standard-only` retains
Legacy, EIP-1559, EIP-7702, contract, and txpool coverage while skipping
preconfirmation. `--tx-standard-only` alone is an error. The default signing key
is a public local-devnet test key and must not be used on a funded network.
The full preconfirmation parity scenario is calibrated for an op-geth sequencer;
use standard-only mode on a reth sequencer.
The JSON report includes each transaction assertion alongside its underlying
RPC comparisons. Balance, contract-call, and fee-history checks using `latest`
require the same canonical block on both endpoints before and after the call;
an unestablished snapshot is `INCONCLUSIVE` and exits nonzero. Distinct
transaction receipts exclude only inclusion identifiers such as transaction
hash and block position; fee, gas, status, and other receipt fields remain
comparable, and both raw responses are retained. Any failed assertion makes
the command exit with status 1.

```bash
go run ./op-rpc-compat \
  --baseline-url "$BASELINE_RPC_URL" \
  --target-url "$TARGET_RPC_URL" \
  --tx --tx-standard-only
```

`preconf` runs preconfirmation scenarios against a sequencer and two forwarding
verifiers. It can deploy contracts, fund test accounts, mint and approve tokens,
and send transactions. `--heavy` adds throughput and ordering scenarios. The
sequencer and both verifier URLs are required; op-node and L1 URLs are needed
for scenarios that exercise those services. The network must already have the
appropriate preconfirmation allowlists and checker configured.
Preconfirmation scenarios report `PASS`, `FAIL`, or `INCONCLUSIVE` separately.
An inconclusive result does not fail the command, but it does not establish the
scenario's invariant. `--only` must name a registered scenario; selecting a
heavy scenario also requires `--heavy`. Invalid selections fail before setup.

```bash
go run ./op-rpc-compat preconf \
  --sequencer-url "$SEQUENCER_RPC_URL" \
  --baseline-verifier-url "$BASELINE_VERIFIER_RPC_URL" \
  --target-verifier-url "$TARGET_VERIFIER_RPC_URL" \
  --op-node-url "$OP_NODE_RPC_URL" \
  --l1-url "$L1_RPC_URL"
```

`stream` sends repeated transactions and/or watches block ordering. It defaults
to both modes; `--send=false` or `--watch=false` selects one. Sending needs
`--rpc` or `STREAM_RPC_URL`. Watching uses `--watch-rpc` or
`STREAM_WATCH_RPC_URL`, falling back to the send endpoint when neither is set.
This mode also changes chain state when sending is enabled.

```bash
go run ./op-rpc-compat stream \
  --rpc "$SEND_RPC_URL" \
  --watch-rpc "$WATCH_RPC_URL" \
  --private-key "$TEST_PRIVATE_KEY"
```

RDE owns network setup and recovery. The destructive `rpc_probe.py` and
`rpc_sethead_test.py` helpers remain in RDE v3 and are not part of this
component.
