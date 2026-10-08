# OP Stack Acceptance Tests

## Overview

This directory contains the acceptance tests and configuration for the OP Stack. These tests are executed by `op-acceptor`, which serves as an automated gatekeeper for OP Stack network promotions.

Think of acceptance testing as Gandalf 🧙, standing at the gates and shouting, "You shall not pass!" to networks that don't meet our standards. It enforces the "Don't trust, verify" principle by:

- Running automated acceptance tests
- Providing clear pass/fail results (and tracking these over time)
- Gating network promotions based on test results
- Providing insight into test feature/functional coverage

The `op-acceptor` ensures network quality and readiness by running a comprehensive suite of acceptance tests before features can advance through the promotion pipeline:

Localnet → Alphanet → Betanet → Testnet

This process helps maintain high-quality standards across all networks in the OP Stack ecosystem.

## Architecture

The acceptance testing system supports two orchestrator modes:

### **sysgo (In-process)**
- **Use case**: Fast, isolated testing without external dependencies
- **Benefits**: Quick startup, no external infrastructure needed
- **Dependencies**: None (pure Go services)

### **sysext (External)**
- **Use case**: Testing against Kurtosis-managed devnets or persistent networks
- **Benefits**: Testing against realistic network conditions
- **Dependencies**: Docker, Kurtosis (for Kurtosis devnets)

The system automatically selects the appropriate orchestrator based on your usage pattern.

## Dependencies

### Basic Dependencies
* Mise (install as instructed in CONTRIBUTING.md)

### Additional Dependencies (for external devnet testing)
* Docker
* Kurtosis

Dependencies are managed using the repo-wide `mise` config. Run `mise install` at the repo root to install `op-acceptor` and other tools.

## Usage

### Quick Start

```bash
# Run in-process tests (fast, no external dependencies)
just acceptance-test "" base

# Run against Kurtosis devnets (requires Docker + Kurtosis)
just acceptance-test simple base
just acceptance-test interop interop
```

### Available Commands

```bash
# Default: Run tests against simple devnet with base gate
just

# Run specific devnet and gate combinations
just acceptance-test <devnet> <gate>

# Use specific op-acceptor version
ACCEPTOR_VERSION=v1.0.0 just acceptance-test "" base
```

### Direct CLI Usage

You can also run the acceptance test wrapper directly:

```bash
cd op-acceptance-tests

# In-process testing (sysgo orchestrator)
go run cmd/main.go --orchestrator sysgo --gate base --testdir .. --validators ./acceptance-tests.yaml --acceptor op-acceptor

# External devnet testing (sysext orchestrator)
go run cmd/main.go --orchestrator sysext --devnet simple --gate base --testdir .. --validators ./acceptance-tests.yaml --kurtosis-dir ../kurtosis-devnet --acceptor op-acceptor

# Remote network testing
go run cmd/main.go --orchestrator sysext --devnet "kt://my-network" --gate base --testdir .. --validators ./acceptance-tests.yaml --acceptor op-acceptor
```

## Development Usage

### Fast Development Loop (In-process)

For rapid test development, use in-process testing:

```bash
cd op-acceptance-tests
# Not providing a network uses the sysgo orchestrator (in-memory network) which is faster and easier to iterate with.
just acceptance-test "" base
```

### Testing Against External Devnets

For integration testing against realistic networks:

1. **Automated approach** (rebuilds devnet each time):
   ```bash
   just acceptance-test interop interop
   ```

2. **Manual approach** (once-off)
   ```bash
   cd op-acceptance-tests
   # This spins up a devnet, then runs op-acceptor
   go run cmd/main.go --orchestrator sysext --devnet "interop" --gate interop --testdir .. --validators ./acceptance-tests.yaml
   ```

3. **Manual approach** (faster for repeated testing):
   ```bash
   # Deploy devnet once
   cd kurtosis-devnet
   just isthmus-devnet

   # Run tests multiple times against the same devnet
   cd op-acceptance-tests
   # This runs op-acceptor (devnet spin up is skipped due to `--reuse-devnet`)
   go run cmd/main.go --orchestrator sysext --devnet "interop" --gate interop --testdir .. --validators ./acceptance-tests.yaml --reuse-devnet
   ```

### Configuration

- `acceptance-tests.yaml`: Defines the validation gates and the suites and tests that should be run for each gate.
- `justfile`: Contains the commands for running the acceptance tests.
- `cmd/main.go`: Wrapper binary that handles orchestrator selection and devnet management.

### Logging Configuration

When invoked with `go test`, devstack acceptance tests support configuring logging via CLI flags and environment variables. The following options are available:

* `--log.level LEVEL` (env: `LOG_LEVEL`): Sets the minimum log level. Supported levels: `trace`, `debug`, `info`, `warn`, `error`, `crit`. Default: `trace`.
* `--log.format FORMAT` (env: `LOG_FORMAT`): Chooses the log output format. Supported formats: `text`, `terminal`, `logfmt`, `json`, `json-pretty`. Default: `text`.
* `--log.color` (env: `LOG_COLOR`): Enables colored output in terminal mode. Default: `true` if STDOUT is a TTY.
* `--log.pid` (env: `LOG_PID`): Includes the process ID in each log entry. Default: `false`.

Environment variables override CLI flags. For example:
```bash
# Override log level via flag
go test -v ./op-acceptance-tests/tests/interop/sync/multisupervisor_interop/... -run TestL2CLAheadOfSupervisor -log.format=json | logdy

# Override via env var
LOG_LEVEL=info go test -v ./op-acceptance-tests/tests/interop/sync/multisupervisor_interop/... -run TestL2CLAheadOfSupervisor
```

## Adding New Tests

To add new acceptance tests:

1. Create your test in the appropriate Go package under `tests` (as a regular Go test)
2. Register the test in `acceptance-tests.yaml` under the appropriate gate
3. Follow the existing pattern for test registration:
   ```yaml
   - name: YourTestName
     package: github.com/ethereum-optimism/optimism/op-acceptance-tests/tests/your/package/path
   ```

## Mantle Gates

This fork adds gates for the Mantle network on top of the OP Stack ones above. They follow the
same rules as any other gate: registered in `acceptance-tests.yaml`, run by `op-acceptor`, and
selected by name.

| Gate | Covers |
| --- | --- |
| `mantle-arsia` | Arsia fork behaviour |
| `mantle-base` | Sanity/smoke for the Mantle network; inherits `mantle-arsia` |
| `mantle-elysium` | An Arsia L2 running against an upgraded (Glamsterdam / Amsterdam) L1 |

```bash
cd op-acceptance-tests
just acceptance-test "" mantle-base
just acceptance-test "" mantle-elysium
```

### mantle-elysium

The L2 stays on Arsia rules and only *consumes* an L1 that has upgraded to Glamsterdam — it does
not execute Amsterdam itself. The gate covers both directions of that relationship: that the L2
keeps deriving correctly from L1 blocks carrying the new header fields and DA behaviour, and that
the L2's own output stays byte-for-byte Arsia while it does so.

This gate needs an **Amsterdam-capable L1 execution client**, which is not the in-process default.
`mise` builds one from the go-ethereum commit pinned in the repo-root `mise.toml`; point the
harness at it before running:

```bash
mise install geth
export DEVSTACK_L1EL_KIND=geth
export SYSGO_GETH_EXEC_PATH="$(mise which geth)"

cd op-acceptance-tests
just acceptance-test "" mantle-elysium
```

Without those variables the harness falls back to the in-process client, which does not enforce
Amsterdam header rules. The suite detects this and fails rather than passing vacuously, so a run
that has silently lost its Glamsterdam L1 is reported as a failure, not a green.

Note that the local (non-CI) path rebuilds contract artifacts before running. If they are already
built, that step is the slowest part of an otherwise short run.

#### Cases outside the gate

A few cases live under `mantle-tests/elysium/system` and are deliberately **not** registered:

- those asserting against the beacon API itself, which need a real consensus client rather than
  the in-process stand-in;
- a batch-submission case whose value is that the DA mode comes from the devnet descriptor rather
  than being pinned by the test;
- a long soak and a high-throughput load, both too slow for a per-PR gate.

Run these by hand against a real devnet before a release. They are ordinary Go tests, but they
need the `sysext` orchestrator pointed at a devnet descriptor. With no `DEVNET_ENV_URL` the
harness falls back to a Kurtosis enclave (`kt://interop-devnet`) and fails *there* — the error
talks about a missing Kurtosis engine and says nothing about the descriptor, so it sends you off
in the wrong direction.

A descriptor for the [rde](https://github.com/mantle-xyz/rde-v4) `glam-accept` profile ships at
`mantle-tests/elysium/devnets/glam-accept.json`. rde assigns ports deterministically per profile,
so it works as-is against that profile on localhost.

```bash
cd op-acceptance-tests

export DEVSTACK_ORCHESTRATOR=sysext
export DEVNET_ENV_URL="$PWD/mantle-tests/elysium/devnets/glam-accept.json"

# rde control surface: lets the harness stop and start individual services.
export DEVNET_ENV_CTRL=rde
export RDE_DIR=/path/to/rde-v4
export RDE_PROFILE=glam-accept

# Pins the chain's slot time, which is otherwise only logged. glam-accept runs 12s slots.
export ELYSIUM_EXPECTED_SECONDS_PER_SLOT=12

go test ./mantle-tests/elysium/system/ -run TestL1Glamsterdam_System_RealCL -v
go test ./mantle-tests/elysium/system/ -run TestL1Glamsterdam_Derivation_RealCLBeacon -v
```

**A faucet is required and rde does not run one.** The preset these cases use resolves an L1 and
an L2 faucet at construction and fails without them, and the funding calls are real
`faucet_requestETH` requests rather than local key handling. One `op-faucet` instance serves both
chains — the descriptor points both at `127.0.0.1:9000`:

```yaml
# faucet.yaml
faucets:
  l1: { el_rpc: "http://127.0.0.1:30145", chain_id: 31337, tx_cfg: { private_key: "0xac09...ff80" } }
  l2: { el_rpc: "http://127.0.0.1:30245", chain_id: 1337,  tx_cfg: { private_key: "0xac09...ff80" } }
defaults: { 31337: l1, 1337: l2 }
```

```bash
cd op-faucet && go run ./cmd --config=faucet.yaml --rpc.addr=127.0.0.1 --rpc.port=9000
```

Writing a descriptor for a different devnet: service map keys must be `el` and `cl`, and the
endpoint key differs by role — L1 `cl` uses `http` (the beacon REST port, not prysm's gRPC),
everything else uses `rpc`; `batcher` and `proposer` use `http`. `features: ["mantle"]` makes the
harness pull the rollup config from the running op-node, so it does not have to be transcribed.
`l1.config` must be the full chain config: chain 31337 is not well-known, so a stub loses
`amsterdamTime` and every case asserting on it fails.

Note that `l1.config`'s fork times (`amsterdamTime`, `bpo2Time`) are wall-clock timestamps baked
in when the devnet's genesis was generated, so they change every time rde rebuilds the devnet
(`task clean && task up`). The checked-in `glam-accept.json` carries one such snapshot. If a run
fails at `WaitForGlamsterdamL1` with the L1 head never showing the Amsterdam header fields, the
descriptor's timestamps are stale relative to the running devnet — regenerate them from the
profile's `genesis/metadata/genesis.json`.

## Flake-Shake: Test Stability Validation

Flake-shake is a test stability validation system that runs tests multiple times to detect flakiness before they reach production gates. It serves as a quarantine area where new or potentially unstable tests must prove their reliability.

### Purpose

- Detect flaky tests through repeated execution (100+ iterations)
- Prevent unstable tests from disrupting CI/CD pipelines
- Provide data-driven decisions for test promotion to production gates

### How It Works

Flake-shake runs tests multiple times and aggregates results to determine stability:
- **STABLE**: Tests with 100% pass rate across all iterations
- **UNSTABLE**: Tests with any failures (<100% pass rate)

### Running Flake-Shake

Flake-shake is integrated into op-acceptor and can be run locally or in CI:

```bash
# Run flake-shake with op-acceptor (requires op-acceptor v3.4.0+)
op-acceptor \
  --validators ./acceptance-tests.yaml \
  --gate flake-shake \
  --flake-shake \
  --flake-shake-iterations 10 \
  --orchestrator sysgo

# Run with more iterations for thorough testing
op-acceptor \
  --validators ./acceptance-tests.yaml \
  --gate flake-shake \
  --flake-shake \
  --flake-shake-iterations 100 \
  --orchestrator sysgo
```

### Adding Tests to Flake-Shake

Add new or suspicious tests to the flake-shake gate in `acceptance-tests.yaml`:

```yaml
gates:
  - id: flake-shake
    description: "Test stability validation gate"
    tests:
      - package: github.com/ethereum-optimism/optimism/op-acceptance-tests/tests/yourtest
        timeout: 10m
        metatada:
          owner: stefano
```

### Understanding Reports

Flake-shake stores a daily summary artifact per run:
- **`final-report/daily-summary.json`**: Aggregated counts of stable/unstable tests and per-test pass/fail tallies.

### CI Integration

In CI, flake-shake runs tests across multiple parallel workers:
- 10 workers each run 10 iterations (100 total by default)
- Results are aggregated using the `flake-shake-aggregator` tool
- Reports are stored as CircleCI artifacts

### Automated Promotion (Promoter CLI)

We provide a small CLI that aggregates the last N daily summaries from CircleCI and proposes YAML edits to promote stable tests out of the `flake-shake` gate:

```bash
export CIRCLE_API_TOKEN=...  # CircleCI API token (read artifacts)
go build -o ./op-acceptance-tests/flake-shake-promoter ./op-acceptance-tests/cmd/flake-shake-promoter/main.go
./op-acceptance-tests/flake-shake-promoter \
  --org ethereum-optimism --repo optimism --branch develop \
  --workflow scheduled-flake-shake --report-job op-acceptance-tests-flake-shake-report \
  --days 3 --gate flake-shake --min-runs 300 --max-failure-rate 0.01 --min-age-days 3 \
  --out ./final-promotion --dry-run
```

Outputs written to `--out`:
- `aggregate.json`: Per-test aggregated totals across days
- `promotion-ready.json`: Candidates and skip reasons
- `promotion.yaml`: Proposed edits to `op-acceptance-tests/acceptance-tests.yaml`

### Promotion Criteria

Tests should remain in flake-shake until they demonstrate consistent stability:
- **Immediate promotion**: 100% pass rate across 100+ iterations
- **Investigation needed**: Any failures require fixing before promotion
- **Minimum soak time**: 3 days in flake-shake gate recommended

### Quick Development

For rapid development and testing:

```bash
cd op-acceptance-tests

# Run all tests (sysgo gateless mode) - most comprehensive coverage
just acceptance-test "" ""

# Run specific gate-based tests (traditional mode)
just acceptance-test "" base        # In-process (sysgo) with gate
just acceptance-test simple base    # External devnet (sysext) with gate
```

Using an empty gate (`""`) triggers gateless mode with the sysgo orchestrator, auto-discovering all tests.

## Further Information

For more details about `op-acceptor` and the acceptance testing process, refer to the main documentation or ask the team for guidance.

The source code for `op-acceptor` is available at [github.com/ethereum-optimism/infra/op-acceptor](https://github.com/ethereum-optimism/infra/tree/main/op-acceptor). If you discover any bugs or have feature requests, please open an issue in that repository.
