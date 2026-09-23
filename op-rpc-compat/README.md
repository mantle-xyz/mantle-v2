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
  --baseline-name op-geth \
  --target-url http://127.0.0.1:29545 \
  --target-name op-reth
```

The URLs have no defaults. `BASELINE_RPC_URL` and `TARGET_RPC_URL` can supply
them; `BASELINE_NAME` and `TARGET_NAME` can supply logical names. Explicit flags
take precedence over environment variables. Names default to `baseline` and
`target` when omitted. Use stable names such as `op-geth` and `op-reth` when
client-specific known differences should apply.

This is a breaking CLI change from the RDE v3 tool. There are no `--geth`,
`--reth`, `GETH_RPC_URL`, or `RETH_RPC_URL` compatibility aliases.

After RDE v3 updates its `src/mantle-v2` gitlink to a commit containing this
component, the same run can start from the RDE workspace:

```bash
go -C src/mantle-v2 run ./op-rpc-compat \
  --baseline-url http://127.0.0.1:19545 \
  --baseline-name op-geth \
  --target-url http://127.0.0.1:29545 \
  --target-name op-reth
```

The same binary can compare two reth versions. The caller supplies endpoints
from its own network configuration:

```bash
go -C src/mantle-v2 run ./op-rpc-compat \
  --baseline-url "$OLD_RETH_RPC_URL" \
  --baseline-name old-reth \
  --target-url "$NEW_RETH_RPC_URL" \
  --target-name new-reth
```

RDE v4 profile endpoints and integration have not been validated here. That
work is separate from this component.

## Cases And Reports

The default JSON testcase corpus and `known_diffs.json` are embedded, so the
binary can run from any working directory. `--file PATH` loads one explicit OS
file. `--testcases-dir DIR` replaces the embedded corpus with JSON files from an
OS directory. `--exclude FILENAME` removes a file from a corpus run and can be
repeated. Generated reports default to `report.json`; `--output ""` disables
report writing.

The default corpus covers standard `eth_*` queries, Mantle RPC extensions,
error cases, txpool, and debug methods. It does not submit chain transactions,
although methods such as `eth_newFilter` create temporary node-local state. A
live chain can change between the two requests: cases using `latest` or
`pending` may need a fixed block for a stable comparison.

Results have four statuses:

| Status | Meaning | Fails the run |
|---|---|---|
| `PASS` | Responses match | No |
| `COMPATIBLE` | An applicable known difference matches, or both endpoints report an unsupported method | No |
| `WARNING` | Only nonfatal differences were found | No |
| `FAIL` | Responses differ or a request failed | Yes |

Reports use schema version 2 and record both endpoint names, URLs, client
versions, and actual responses:

```json
{
  "schema_version": 2,
  "baseline": {"name": "op-geth", "url": "http://127.0.0.1:19545", "client_version": "Geth/..."},
  "target": {"name": "op-reth", "url": "http://127.0.0.1:29545", "client_version": "mantle-reth/..."},
  "results": [
    {"status": "PASS", "baseline_response": {"result": "0x1"}, "target_response": {"result": "0x1"}}
  ]
}
```

Known differences are matched against the current baseline and target, not
unconditionally skipped. A client-specific rule can select logical names and
optionally `web3_clientVersion` with regular expressions:

```json
{
  "test_name": "eth_hashrate",
  "applies_to": {
    "baseline": {"name_pattern": "^op-geth$"},
    "target": {"name_pattern": "^op-reth$"}
  },
  "baseline_example": {"error": {"code": -32601, "message": "method not found"}},
  "target_example": {"result": "0x0"},
  "reason": "Different responses from this client pair"
}
```

If the endpoint names or versions do not match `applies_to`, the difference is
reported normally. Rules without `applies_to` describe implementation-independent
dynamic values such as client versions and generated filter IDs.

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

```bash
go run ./op-rpc-compat \
  --baseline-url "$BASELINE_RPC_URL" --baseline-name op-geth \
  --target-url "$TARGET_RPC_URL" --target-name op-reth \
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
scenario's invariant.

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
