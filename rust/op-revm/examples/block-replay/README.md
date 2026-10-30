# op-revm-block-replay

Replays historical Mantle blocks through this workspace's `op-revm` and compares the
result against an archive node, so a change to the EVM is checked against the chain and
not only against unit tests.

## What it checks, per block

| Layer | Fields |
|---|---|
| receipt | `status`, `gasUsed`, `cumulativeGasUsed`, `logs` (byte-wise, ordered), `logsBloom`, `depositNonce` |
| block | `receiptsRoot` against the header |
| post-state | `balance` / `nonce` / `code_hash` of every touched account, and every touched storage slot (`STATE_VERIFY=true`) |

A failing gas comparison also prints the three terms behind `tx_gas_used` and the inputs
to intrinsic gas, so a mismatch says which term diverged, not just which transaction.

## Running

```bash
cd rust/op-revm/examples/block-replay
cp .env.example .env          # every knob, and which value to pick, is documented there
cargo run --release -p op-revm-block-replay
```

Environment variables override `.env`, which is how shards are launched:

```bash
MANTLE_URL=http://archive:8545 START_BLOCK=94355444 END_BLOCK=94365443 \
STATE_VERIFY=true PROGRESS_FILE=/tmp/p.txt \
  cargo run --release -p op-revm-block-replay
```

## Reading the output

The first line states what this process was told to do — chain, endpoint, fork mode,
range, how much is already done, and the progress file. Progress is then reported every
100 blocks with a rate and an ETA.

A matching block prints nothing. A mismatching block prints its full per-transaction
detail, so anything on stdout other than a progress line is a finding. The run ends with
either `ALL BLOCKS MATCH` or `MISMATCH ... at N / M block(s)` and the block numbers.

`PROGRESS_FILE` gets one `<block> OK|MISMATCH|ERROR` line per block, flushed as it goes.
Rerunning the same range with the same file skips the blocks already listed, so an
interrupted run resumes rather than restarts. `ERROR` is a block that could not be
replayed, usually because the node failed to answer; the range continues, but a shard
gives up after enough consecutive failures rather than marking the rest ERROR.

## Longer ranges

Split the range across separate processes rather than raising `CONCURRENCY`. Each needs a
disjoint range and **its own** `PROGRESS_FILE`; sharing one corrupts both, since the file
is only read at startup and appended to without coordination between processes.

The reference node is the limit, not this tool. Watch its CPU and memory and add shards
gradually.

## Out of scope

Blocks below the Skadi activation. Mantle's earlier ladder (MantleBaseFee → Everest →
Euboea) is not modelled, so those blocks fall back to Bedrock and mismatch by design.
