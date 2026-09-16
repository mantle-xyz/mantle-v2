# Mantle Rust Subtree Patches

This file is the authoritative registry of every Mantle modification stacked on top of
the upstream optimism `rust/` subtree in `mantle-v2/rust/`. It is the primary reference
when synchronizing future upstream changes via `git subtree pull`.

**Whenever Mantle changes are added, modified, or removed, update this file.**

## 1. Current baseline

| Item | Value |
|---|---|
| Upstream tracking point | optimism `kona-client/v1.7.0` @ **`64b043ea5bbca9bc6e57e0f1c8df0404b4cf5f68`** (2026-09-05) |
| Bridge tag | `rust-kona-client-v1.7.0` (= bridge split `fa7ef15efb72aaaabcebd6419578a58337507387`) |
| Bridge branch (last sync source) | `sync-kona-client-v1.7.0` |
| Bridge repo | https://github.com/mantle-xyz/optimism-rust-bridge |
| `git subtree add` commit | `ba2cc4514` ("Add 'rust/' from commit '1ad181f05...'") |
| Last subtree-pull merge commit | `dc2f93af30386a8f995844cd2318d26c89adec32` (two parents: the previous branch tip and the bridge split — verify with `git cat-file -p <sha>`; a single-parent commit here means the merge base is lost for the next sync) |
| Previous baseline | `kona-client/v1.5.1` @ `fbbf9089` / bridge split `a6c46d8a` |
| Rust toolchain | 1.95 (see `rust/rust-toolchain.toml`) |

**The source SHA is recorded above.** The v1.5.1 and op-reth/v2.4.2 rounds both omitted it; that
gap is what the rest of this section used to document. Keep recording it.

### How this sync was performed (read before the next one)

| Item | Value |
|---|---|
| Bridge split method | **`git commit-tree`, not `git subtree split`.** A real split walks all 28170 optimism commits (>40 min). Instead: `TREE=$(git rev-parse '<commit>:rust')` then `git commit-tree "$TREE" -p <previous split>`. Semantically equivalent for this purpose — `git subtree pull` only needs a correct merge base, not rewritten per-commit history. |
| Tag resolution trap | `kona-client/v1.7.0` is a **doubly-nested annotated tag**: `32e56287` (tag) → `a99cdaf0` (tag, = v1.7.0-rc.2) → `64b043ea` (commit). `gh api .../git/refs/tags/...`'s `.object.sha` returns the *inner tag object*, not the commit. Always resolve with `git rev-parse '<tag>^{commit}'`. |
| `--no-commit` trap | git 2.39 (Apple Git-154) ships a `git subtree` that **does not support `--no-commit`**, so a dry run needs the equivalent form. `cmd_merge` in the `git-subtree` script is literally `git merge --no-ff -Xsubtree=<prefix> FETCH_HEAD`, so use:<br>`git merge --no-ff --no-commit -Xsubtree=rust/ FETCH_HEAD` |
| Merge base | **`a6c46d8a`** (the v1.5.1 split), via real git ancestry. Note the most recent `git-subtree-split` *trailer* in history says `1ad181f05c` (from the original `subtree add`), because the v1.5.1 sync never wrote a trailer — but that trailer is only consulted by `find_latest_squash` in `--squash` mode. Non-squash merges use ancestry, and `5a629e1a` carries `a6c46d8a` as a real parent. |
| Conflict surface | 184 files. **165 were mechanical** (134 `op-reth/` DU + 26 `revm-ee-tests/` + 5 `op-revm/`, all resolved by policy — see §3.9 / §2.1); only **19 needed judgment**. |
| Do NOT "optimise" the merge base | It is tempting to record an intermediate `-s ours` split at `op-reth/v2.4.2` to shrink the conflict surface (820 → 503 files). **This is wrong.** Probing 67 files that upstream changed between v1.5.1 and v2.4.2 showed 55 byte-identical to **v1.5.1** and 0 to v2.4.2: the tree is *split* — non-kona crates sat at ~v2.4.2 but `kona/` was still v1.5.1. Declaring v2.4.2 as the base would have made the pull **silently skip 187 kona files**. |

### Historical: the op-reth v2.4.2 round (superseded by the v1.7.0 sync above)

The round before this one pulled from `ethereum-optimism/optimism @ op-reth/v2.4.2` directly
rather than through the bridge, left no subtree merge commit, and did not record its source SHA.
**The v1.7.0 sync above went back through the bridge and closed that gap** — `rust/` is once
again bridge-mediated with a recorded upstream commit, and `git subtree pull` works normally.

The table below is kept because two of its traps are still live when auditing this tree:

| Item | Value |
|---|---|
| ⚠️ `op-revm` version trap | optimism's in-tree `op-revm` is a **path crate whose version string (20.0.0) is behind its content**. crates.io's published `op-revm 20.0.0` is *older* and **lacks** `catch_error_tx_error` / `catch_error_failed_deposit` / `discard_and_surface_error` / the `PostExec` arm. **Do not use the crates.io crate as "upstream" when auditing** — use the optimism tree at the anchor tag. |
| ⚠️ `?ref=` trap | `gh api repos/ethereum-optimism/optimism/contents/rust/...` **without `?ref=`** returns the default branch, which is `develop`, not the anchor tag. An audit that omits `?ref=` will silently compare against `develop` and mis-attribute the base. |


### Migration status

| Phase | Scope | Status |
|---|---|---|
| Pre-Phase | Bridge repo + subtree add + backup tags | ✅ |
| Phase 0 | wire mantle-elysium revm + op-alloy/alloy-op-evm adaptations | ✅ |
| Phase 1 (a–g) | kona Mantle protocol migration | ✅ |
| Phase 1.5 (B1–B3) | drop Mantle vendored-but-unused code (≈1066 lines removed) | ✅ |
| Phase 4 | redirect `alloy-evm` to `mantle-xyz/evm @ mantle-v0.34.0` | ↩️ reverted in Phase 5 |
| Sync `rust-develop-20260511` → `rust-kona-client-v1.5.1` | 7 upstream commits, 38 files, 1 trivial conflict + 1 KARST fix | ✅ |
| Phase 5 | remove `op-reth/` (EL node now lives in `mantle-xyz/reth`); revert `alloy-evm` to upstream `alloy-rs/evm` v0.34.0 + drop the 2 dead `token_ratio` stubs in `alloy-op-evm` | ✅ |
| Sync `op-reth/v2.4.2` anchor → `kona-client/v1.7.0` | Back through the bridge. 184 conflicts (165 mechanical / 19 judged); kona compiles again — the 22 errors the previous round left are gone. Brings upstream PR #22126 (span-batch `uvarint` ↔ op-node parity, a **consensus** fix). `[MANTLE]` markers 101 → 208 across 71 files (re-measured 2026-09-16; the 117/44 figure recorded here originally was taken mid-sync and was never trued up). Verified: `check --workspace` 0/0; `cargo test` on the 8 Mantle-touched crates 931 pass / 0 fail; nightly fmt clean; clippy clean outside `op-revm/` (which carries a pre-existing baseline, §2.1); `no_std`/riscv32 20/20. **A full `cargo test --workspace` is not green** — see §4.3 for the four tests parked with `#[ignore]` and why. | ✅ |
| Phase 2 | op-succinct upgrade (independent fork) | ⏸️ |
| Phase 3 | kona security patch follow-up | ⏸️ |

## 2. Architecture decisions

### 2.1 revm sourced from mantle-xyz/revm @ branch `dev/mantle-v1.6.3`

The `[patch.crates-io]` section in `rust/Cargo.toml` redirects **12** revm-family crates
to the `dev/mantle-v1.6.3` branch of `mantle-xyz/revm`:

```
revm, revm-bytecode, revm-context, revm-context-interface, revm-database,
revm-database-interface, revm-handler, revm-inspector, revm-interpreter,
revm-precompile, revm-primitives, revm-state
```

**`op-revm` is deliberately NOT in that list** — bluealloy removed `crates/op-revm` after v107,
so it lives here as the workspace member `op-revm/` and is wired up as
`op-revm = { version = "20.0.0", path = "op-revm/" }`.

That branch ships revm v41 plus Mantle protocol changes (ARSIA/JOVIAN hardforks,
BVM_ETH, token_ratio, DA footprint, Arsia fee validation). This avoids re-implementing
those changes inside `rust/op-revm/`.

⚠️ **The patch points at a mutable branch ref, not a tag or rev.** `Cargo.lock` currently pins
`1903a86a50dde8c8148d8596f0123eadd48af7b7`. Two consequences: release builds are not
reproducible from `Cargo.toml` alone, and during a sync an unexplained `cargo check` change may
come from revm moving rather than from the sync — check that SHA before debugging further.
Kept as a branch on purpose while revm is under active development (jay, 2026-09-10); revisit
once that line settles, mirroring the reth `arsia.1`(branch) → `arsia.2`(tag) precedent.

Consequences of `op-revm` being a compiled member (it was never built while `exclude`d):
(a) lints and `no_std` now apply to it — it is in `justfile`'s `check-no-std` `no_std_packages`;
(b) its `ee-tests` live in the workspace member `revm-ee-tests/`; (c) the 26
`op_revm_testdata/*.json` snapshots were regenerated against revm 41. See the op-reth
v2.2.1 → v2.4.2 upgrade runbook under `rde-v3/docs/`, sections 4.3 and 5.6.

⚠️ **`op-revm/` and `revm-ee-tests/` are owned by the revm maintainer, not by whoever runs a
subtree sync.** The v1.7.0 sync restored both directories wholesale
(`git checkout <pre-sync ref> -- rust/op-revm/ rust/revm-ee-tests/`) rather than resolving their
conflicts, so no upstream op-revm code entered. Keep doing this. Known consequence:
`cargo clippy --workspace --all-features --all-targets --keep-going -- -D warnings` reports
**50 pre-existing errors in `op-revm/`'s lib target and 91 in its lib-test target** (mostly
`doc_markdown`, plus a handful of style lints). Re-measured 2026-09-16; the figures previously
recorded here (51) and in §4.3 (93) were both wrong, and for the same reason — the first was
taken without `--keep-going`, so cargo stopped scheduling after the first failing crate, and the
two were then compared against each other. Always measure both sides of a baseline the same way.
They are byte-for-byte inherited, not
introduced by any sync; `cargo clippy --fix -p op-revm --all-targets` handles most of them.
Note `--exclude op-revm` does **not** silence them: cargo only applies `--cap-lints allow` to
registry/git dependencies, never to a path member of the same workspace.

`reth-revm` is a reth-internal wrapper (from `paradigmxyz/reth`); not a member of the
bluealloy revm family. Its internal `revm` dependency is still redirected by
`[patch.crates-io]`, so the actual EVM execution path runs entirely on `mantle-xyz/revm`.

### 2.2 Version skew with mantle-xyz/revm

**The v19 skew is gone.** `op-revm` is now the in-tree path crate at v20 and `OpSpecId` carries
`KARST` and `LAGOON` alongside Mantle's `OSAKA` / `ARSIA`. The rows below describe the *current*
state; the old "adapt consumers to the v19 API / comment out KARST" guidance no longer applies
and every such workaround has been removed.

| Dimension | Upstream | This tree | Reconciliation |
|---|---|---|---|
| revm major version | v41 | v41 ✅ (`mantle-xyz/revm`, branch `dev/mantle-v1.6.3`) | — |
| op-revm major version | v20 | v20 ✅ (in-tree path crate) | — |
| `OpSpecId` variants | `… JOVIAN, KARST, LAGOON` | `… JOVIAN, OSAKA, ARSIA, KARST, LAGOON` | Mantle adds two variants; **there is no `INTEROP` variant** (upstream renamed that fork to Lagoon). Any `match` must cover OSAKA/ARSIA. |
| Which spec a Mantle chain resolves to | — | **`ARSIA` / `OSAKA` / `ISTHMUS`, never `KARST` or `LAGOON`** | Mantle does not open newly added eth/op hardforks. Enforced structurally in `alloy_op_evm::spec_by_timestamp_after_bedrock`, which short-circuits on `is_mantle()` before the OP fork check. |

⚠️ **`is_mantle()` must be reachable through the `OpHardforks` trait.** The trait's Mantle
predicates all default to `false`, so a type that implements `OpHardforks` without overriding
them is classified as a plain OP chain and resolves `JOVIAN` where `ARSIA` is required — a
consensus divergence. `RollupConfig` overrides all four (see §3.4). Any new `OpHardforks` impl
that can carry a Mantle chain must do the same.

⚠️ **`OpChainHardforks` does not override them, and it is the default `Spec` type parameter of
`OpBlockExecutorFactory<R, Spec, EvmFactory>`.** Everything in this tree passes `RollupConfig`
explicitly, so in-tree behaviour is correct. **Out-of-tree consumers of `alloy-op-evm` —
notably `mantle-xyz/reth` — must not rely on that default**: `OpBlockExecutorFactory::default()`,
or any instantiation leaving `Spec` unspecified, silently classifies a Mantle chain as a plain
OP chain and resolves `JOVIAN` where `ARSIA` is required.

### 2.3 alloy-evm sourced from upstream alloy-rs/evm (crates.io)

`alloy-evm` is **not** patched in `[patch.crates-io]`; it resolves straight from
crates.io, i.e. pristine upstream `alloy-rs/evm`. Phase 5 pinned `0.34.0`; the
op-reth v2.4.2 / revm 41 line moved it to `0.37.x` (whatever op-reth v2.4.2's
`Cargo.toml` says — that file is the version anchor, see §1). Keep this section
version-agnostic; the pin lives in `Cargo.toml`, not here.

**History (Phase 4 → reverted in Phase 5).** Previously `alloy-evm` was redirected to the
`mantle-v0.34.0` branch of `mantle-xyz/evm`, a fork whose only delta over upstream
v0.34.0 was one commit — `b91d0077` "port Mantle token_ratio trait method onto v0.34.0" —
adding `fn token_ratio(&self) -> U256` to the `Evm` trait (`U256::ZERO` default on
`EthEvm`, delegating impl on `Either<L, R>`).

**Why the fork was dropped (Phase 5).** That `token_ratio` trait method was **dead code** —
never called anywhere:

- In this workspace the only `.token_ratio()` call was `alloy-op-evm`'s
  `PostExecEvmAdapter` impl delegating to its inner EVM; `OpEvm::token_ratio` just
  returned `U256::ZERO`.
- In `mantle-xyz/reth@mantle-elysium` there are **zero** `token_ratio()` call sites
  (all 70 `token_ratio` hits there are the `L1BlockInfo.token_ratio` *field* + GasOracle
  reads).

The real Mantle eth/MNT ratio for L1 fees flows through the **`op-alloy`
`L1BlockInfo.token_ratio` field** (§3.2) + the GasOracle contract, and through `op-revm`
(mantle-xyz/revm) — none of which touch the alloy-evm `Evm` trait. So Phase 5 reverted to
upstream and removed the two now-orphaned trait-method impls in `alloy-op-evm`
(`src/lib.rs` `OpEvm::token_ratio`; `src/post_exec/mod.rs`
`PostExecEvmAdapter::token_ratio`, plus the now-unused `U256` import in `lib.rs`).

**Mantle op-side functionality** (unchanged) is provided by:

- `op-revm` from `mantle-xyz/revm@mantle-elysium` (BVM_ETH execution, token_ratio
  computation, ARSIA / JOVIAN protocol changes).
- The locally-vendored `alloy-op-evm/` (this repo) per §3.7.

**Sync caveat.** A future upstream alloy-evm bump just tracks the new crates.io version
(no fork to rebase). Do **not** reintroduce the `token_ratio` trait method unless a real
caller appears. Note `mantle-xyz/reth` still references `mantle-xyz/evm@mantle-v0.34.0`
for its own `alloy-evm`; that is harmless (the dead method simply remains there) and
independent of this workspace.

### 2.4 Mantle data sources use the upstream `EthereumDataSource`

Post Mantle Arsia, all blob submission uses the standard blob format. The Mantle fork
shipped `MantleBlobSource` and `MantleEthereumDataSource` files but **never wired them
into any pipeline** — every real call site (providers-alloy, bin/host, bin/client)
constructs the upstream `EthereumDataSource` with the standard `BlobSource`. Phase 1.5
removed these two orphan modules; see §3.11.

## 3. Mantle changes registry

Every change carries a `[MANTLE]` source comment. Discover all sites with:

```bash
grep -rn "\[MANTLE\]" rust/ --include="*.rs" --include="*.toml"
```

### 3.1 Cargo workspace configuration

| File | Change |
|---|---|
| `Cargo.toml` | `[patch.crates-io]` redirects **12** revm-family crates to `mantle-xyz/revm`, branch `dev/mantle-v1.6.3`. `op-revm` is *not* patched — it is the local path crate. See §2.1. |
| `Cargo.toml` | Workspace `members`: **`op-version/` added** — required, `kona/bin/node` declares `op-version.workspace = true`. `lokahi/` added (standalone CLI, only needs op-version + clap). |
| `Cargo.toml` | Workspace `members`: **`op-reth-test-engine/` deliberately excluded** — it depends on the `reth-optimism-*` crates, which stay out of the workspace along with `op-reth/`. Upstream lists it; we do not. |
| `Cargo.toml` | Workspace `members`: **`kona/sp1/crates/proposer` deliberately excluded** — it `include_str!`s OP's `packages/contracts-bedrock/snapshots/abi/ZKDisputeGame.json`, which Mantle does not ship. Standalone binary, no in-tree dependents; the directory stays on disk. The other five `kona/sp1/crates/*` **are** members and their `kona-sp1-*` path deps are required. |
| `Cargo.toml` | The `# ==================== OP-RETH INTERNAL CRATES ====================` block upstream declares (`op-reth` + 16 `reth-optimism-*` path deps) is omitted. |
| `Cargo.toml` | `op-revm/` and `revm-ee-tests/` are workspace `members`; the old `exclude = ["op-revm"]` is gone. (The only remaining `exclude` is `[workspace.package] exclude = ["**/target"]` — unrelated, and a false positive for any audit grepping `^exclude`.) |
| `Cargo.toml` | `alloy-evm` is **not** patched — resolves from crates.io = upstream alloy-rs/evm. Was v0.34.0 (Phase 5; see §2.3); **on the revm-41 line it is v0.37.x**, aligned to op-reth v2.4.2 together with 35 other `alloy-*` crates. Note `alloy-op-evm` is *not* released in lockstep — its latest crates.io version is 0.32.0, which is what the vendored copy here declares; that is current, not stale. |
| `Cargo.toml` | Workspace `members` / `default-members` drop every `op-reth/*` entry, and the `reth-optimism-* / op-reth / reth-op` block is removed from `[workspace.dependencies]`. As of 2026-09-16 the `op-reth/` **directory is not in this tree at all** — it is filtered out of the bridge split along with `lokahi/` and `op-reth-test-engine/`, see §3.11. EL node lives in `mantle-xyz/reth`. |

### 3.2 op-alloy — TxDeposit gains BVM_ETH fields + L1BlockInfo gains token_ratio

Corresponds to mantle-xyz/op-alloy commits `5f0b879`, `5330f5a`, **`57b9c10`**, `da4e219`,
`6637567`, `79d78a4`, plus the V230 trio: **`3dc9696`** + **`4873ed6`** + **`498abec`**
(2026-05 audit additions). Earlier the registry only listed five commits and attributed
the deposit strict-boundary decode to `6637567`; in fact `6637567` is the *block* RLP
decoder fix, and the deposit strict payload-boundary form actually originates in
`4873ed6`. The L1BlockInfo `token_ratio` field (`57b9c10`), the BVM_ETH boundary +
EIP-2718 round-trip tests (`3dc9696`), and the malformed-input tests (`498abec`) were
missed by the original audit and added in the 2026-05 pass.

| File | Change |
|---|---|
| `op-alloy/crates/consensus/src/transaction/deposit.rs` | Add `eth_value: U256` and `eth_tx_value: Option<U256>` fields with their serde attrs. (Both were `u128` until the v1.7.0 sync — see §3.2b.) |
| same | Update `rlp_decode_fields` / `rlp_encode_fields` / `rlp_encoded_fields_length` / `size()` to include the new fields. |
| same | Switch `rlp_decode` to a `split_at(header.payload_length)` strict-boundary form (port of commit `4873ed6` from V230 — not `6637567`, which is the *block* decoder fix). |
| same | Add the `decode_optional_u256_from_rlp` helper for the trailing optional value. Strict form per `498abec`: returns `Err` on present-but-malformed input rather than swallowing decode errors as `None`. |
| same (tests) | Add 0/None for both fields in 8 in-file `TxDeposit { ... }` literals; add `_` ignores in 1 alloy-compat destructure. |
| same (tests) | Add `test_eth_value_zero`, `test_eth_value_and_eth_tx_value_both_zero`, `test_eth_value_max`, `test_eip2718_encode_decode_with_new_fields`, `test_eip2718_encode_decode_with_eth_tx_value_none`, `test_decode_optional_u128_boundary_values` (port of `3dc9696`). The companion implementation changes in `3dc9696` (lenient `decode_optional_u128_from_rlp` mid-form + `size()` switch from `Option<u128>` to `u128` for `eth_value`) are already present locally — the decode helper was later strictened by `498abec`. |
| same (tests) | Add `test_rlp_decode_fields_rejects_malformed_present_eth_tx_value` and `test_decode_2718_rejects_malformed_present_eth_tx_value` (port of `498abec`'s two new test functions). |
| `op-alloy/crates/consensus/src/transaction/envelope.rs` | Add 0/None in 2 test `TxDeposit` literals. |
| `op-alloy/crates/consensus/src/reth_codec.rs` | `CompactTxDeposit` carries `eth_value` / `eth_tx_value` as `Option<U256>`, placed before `input`. The old **TODO** ("Compact round-trips drop BVM_ETH data") is resolved; layout compatibility is now proven byte-for-byte, see §3.2b. |
| `op-alloy/crates/consensus/src/transaction/deposit.rs` (`bincode_compat`) | Carries both fields. No `skip_serializing_if` — bincode is not self-describing, so skipping a field makes the positional decoder run off the end. |
| `op-alloy/crates/consensus/src/nuts/mod.rs` | NutBundle upgrade-tx literal fills 0/None. |
| `op-alloy/crates/rpc-types/src/transaction/request.rs` | OpTransactionRequest destructure adds `_` ignores for the new fields. |
| `op-alloy/crates/rpc-types/src/receipt.rs` | `L1BlockInfo` gains `pub token_ratio: Option<u128>` at the Jovian-class hardfork section (port of `57b9c10`). `parse_rpc_receipt` test JSON extended with `"tokenRatio": "0x1"` for round-trip coverage. Without this field, RPC clients would not see Mantle's eth/MNT ratio in receipts. |

### 3.2b BVM_ETH widened from `u128` to `U256` (v1.7.0 sync)

**Why.** `OptimismPortal.depositTransaction` takes `_ethTxValue` as an unbounded `uint256`, and
op-node decodes both `msg.value` and `_ethTxValue` with
`new(big.Int).SetBytes(opaqueData[off:off+32])` — the **whole** 32-byte ABI word. kona read only
the low 16 bytes. Any EOA could therefore emit a `TransactionDeposited` with a value at or above
2^128 and make kona derive a *different transaction* from op-node: different deposit hash,
different block hash, consensus split. Once kona-node replaces op-node this also becomes a
liveness hazard, which is why "reject the deposit instead" was rejected as a fix.

**What changed.** `eth_value: u128 -> U256`, `eth_tx_value: Option<u128> -> Option<U256>`,
across five crates: `op-alloy-consensus`, `op-revm`, `alloy-op-evm`, `kona-protocol`,
`kona-hardforks`. `mint` deliberately stays `u128` — op-node holds it as a `big.Int` too, but it
is MNT, whose supply is bounded far below 2^128, and widening it would diverge from upstream
op-alloy for no gain. `kona-protocol` keeps a separate `decode_u128_field` for it.

> This is the one place the "op-revm carries no upstream code" rule (§2.1) was deliberately
> broken: `op-revm` is where the BVM_ETH arithmetic lives, so it had to move with the types.
> Its arithmetic was already `U256` internally; the change removed identity `U256::from(...)`
> conversions at the boundary rather than altering any overflow semantics.

**Why this is consensus-neutral for existing data — measured, not assumed:**

| Layer | Claim | Evidence |
|---|---|---|
| RLP (consensus wire) | Identical bytes below 2^128 | `u128` and `U256` both RLP-encode as minimal big-endian. Pinned by the existing `test_eip2718_encode_decode_*` + serde-JSON contract tests. |
| reth Compact (on-disk) | Identical bytes for every `u128`-representable value | `mantle_compact_layout_tests` in `reth_codec.rs` keeps `FrozenCompactTxDeposit`, a frozen copy of the pre-widening struct, and asserts both encoders emit the same bytes across a 9×9×2 value matrix — plus `widened_decoder_reads_frozen_encoder_output` for the real upgrade path (old bytes, new binary). |
| Real chain | No behaviour change | All 8 sepolia-qa3 executor fixtures still reproduce their real block hashes. |
| bincode-compat | **Byte width DID change** | ruint writes a length-prefixed byte string, not a fixed 16-byte integer. Acceptable because bincode-compat is an in-process/IPC shim, not a persisted format. Nothing in this workspace or in mantle-xyz/reth stores it across restarts. |

**Do not delete `FrozenCompactTxDeposit`.** It is dead code by design: it exists so the layout
claim is checked against the historical shape instead of a hand-computed bitfield width. Its
sensitivity was verified by negative control — swapping two fixed-size fields inside it flips
the bitfield byte (35 -> 19) and fails the test.

**Tests added:** `kona-protocol::deposits::test::test_unmarshal_deposit_version1_bvm_eth_above_u128_max`
and `test_decode_u256_field_preserves_the_full_word` (both verified to fail when
`decode_u256_field` is reverted to the low-16-byte read);
`reth_codec::mantle_compact_layout_tests` (3);
`mantle_txdeposit_compact_tests::roundtrip_bvm_eth_above_u128_max`.

**Adaptations the widening forced, so the next type change knows where to look:**

| surface | what it needed |
|---|---|
| serde | **No `alloy_serde::quantity` wrapper.** It only supports the primitive uints; `U256` already serialises as a hex quantity, which is what op-geth emits (`*hexutil.Big`, `json:"ethValue,omitempty"`) |
| bincode | Both fields carried, and **no `skip_serializing_if`** — bincode is not self-describing, so skipping a field makes the positional decoder run off the end |
| reth `Compact` / DB | `Option<U256>`; layout proven byte-identical, so **no database migration is required** |
| RLP | `decode_u256_field` reads the whole word; `decode_optional_u256_from_rlp` for the trailing field |
| `DepositError::{EthValueDecode, EthTxValueDecode}` | now **unreachable** — a 32-byte `U256::from_be_slice` cannot fail, unlike the old `[u8; 16]` `try_into`. Kept, documented, never constructed |

#### Downstream consumers — this is a breaking API change

`eth_value: u128 -> U256` is source-breaking for anything that constructs a `TxDeposit` literal.

**`mantle-xyz/reth`** pins op-alloy to *this repository*:

```toml
op-alloy-consensus = { git = "https://github.com/mantle-xyz/mantle-v2", branch = "mantle-elysium" }
```

so it breaks the moment the widening reaches `mantle-elysium`. Measured: **18 literals**, all of
them in test code — 8 in `op-reth/crates/rpc/src/eth/receipt.rs`, 4 in
`op-reth/crates/txpool/src/transaction.rs`, 2 in `op-reth/crates/evm/src/l1.rs`, 4 in
`mantle-reth/crates/integration-tests/`. The change is mechanical (`eth_value: 0` →
`eth_value: U256::ZERO`; `eth_tx_value: None` is unaffected). **reth's production code never reads
either field** — `.eth_value` / `.eth_tx_value` have zero hits outside those literals, because the
BVM_ETH arithmetic lives in `op-revm`, which reth consumes. Still, the two repos have to land
inside the same window or reth's CI is red in between.

**`mantle-xyz/op-succinct`** depends on `kona-mpt` / `kona-derive` / `kona-driver` and others by
**tag** (`v1.6.2` at the time of writing), so it is insulated until someone bumps the tag. When
that happens the guest program's STF changes, which means **the vkey changes** and the on-chain
verifier has to be upgraded in coordination — the widening is not a drop-in dependency bump there.

### 3.2c Mantle Skadi — upgrade transactions and the `[Skadi, Arsia)` window

**The shape of the problem.** `AlignOpWithMantle` pins Canyon…Jovian to `mantle_arsia_time`, so
on a Mantle chain **every OP fork predicate is false until Arsia**. Skadi activates months
earlier and turns on a set of behaviours that OP reaches through Canyon/Ecotone/Isthmus. op-node
therefore writes every one of those gates as `IsOpFork(ts) || IsMantleSkadi(ts)`. kona had the
OP half only — correct after Arsia, wrong for the entire window before it.

The window is not hypothetical: on Mantle mainnet it is roughly eight months of blocks, and
every one of them is re-derived by a node syncing from genesis.

**Sites fixed** (each mirrors one op-node call site; each has a regression test):

| kona | op-node reference | Symptom before |
|---|---|---|
| `kona-hardforks::Skadi` (new) + `MantleHardforks::SKADI` | `derive/skadi_upgrade_transactions.go` | kona emitted **no** upgrade transactions at the Skadi activation block. EIP-4788 and EIP-2935 were never deployed. |
| `derive/attributes/stateful.rs` — Skadi bundle emission | `derive/attributes.go:124` | as above |
| `derive/attributes/stateful.rs` — `withdrawals`, `parent_beacon_block_root` | `derive/attributes.go:154,159` | `None` for both where op-node sets `Some([])` / `Some(root)` → different block hash for every block in the window |
| `protocol/batch/single.rs`, `batch/span.rs` — EIP-7702 gate | `derive/batches.go:185` | every 7702 batch in the window dropped → **safe head stalls**, not merely diverges |
| `node/engine/attributes.rs` — `check_withdrawals` | `rollup/attributes/engine_consolidate.go:159` | consolidation took the Bedrock branch and rejected every op-geth block as `BedrockWithdrawals` |
| `node/engine/versions.rs` — all three selectors | `rollup/types.go:727,742,754` | V2 chosen where op-node uses V3/V4; also `getPayloadV5` is reached through `IsMantleLimb`, not Osaka — Mantle never sets `karst_time`, so that branch was dead |
| `node/gossip/handler.rs` — `topic()` | `p2p/gossip.go:619` | published on the V1 topic while the network reads V4 |

Skadi's bytecode, deployer addresses (`0x0B79…C875`, `0x3462…D685`) and 250 000 gas limits are
byte-identical to OP's Ecotone/Isthmus deployments — `Skadi` reuses `Ecotone::eip4788_creation_data`
and `Isthmus::eip2935_creation_data` rather than keeping a second copy that could drift. **Only
the `UpgradeDepositSource` intents differ**, and that is what makes the deposit hashes
Mantle-specific. The two expected source hashes are pinned against values computed with
`cast keccak` — independently of both kona and op-node — so the test cannot pass by agreeing
with a bug in either.

`Skadi::upgrade_gas()` is the trait default 0: op-node appends these transactions without
touching `gasLimit`, and the system-config reconstruction at the next block would otherwise
subtract an amount that was never added.

**Deliberately not changed:**

- `node/engine/query.rs` keeps `is_isthmus_active` for the message-passer storage root. Taking
  the header-`withdrawals_root` shortcut early would be a guess about op-geth's Skadi header
  semantics; the proof path it falls back to yields the true storage root either way.
- `batch/span.rs` keeps its EIP-7702 check even though op-node's `checkSpanBatch` has none
  (only `checkSingularBatch` does). kona is strictly stricter there, independent of Mantle;
  only its *gate* was aligned so it cannot reject what op-node accepts.

**Validated against the live chain.** Mantle Sepolia (`sepolia-testnet-qa1`, L2 chain 5003) is
the config shape that matters — unlike `sepolia-qa3`, whose forks all sit at genesis:

| fork | timestamp | |
|---|---|---|
| genesis | 1702194288 | |
| `mantle_skadi_time` | **1752649200** | |
| `mantle_limb_time` | 1764745200 | |
| `mantle_arsia_time` | **1774422000** | |
| `canyon_time` … `jovian_time` | 1774422000 | all equal Arsia — confirms `AlignOpWithMantle` |
| `regolith_time` | 0 | confirms Regolith is *not* aligned |
| `chain_op_config` | `{2, 8, 8}` | confirms §3.2e |

**The `[Skadi, Arsia)` window on this chain is 252 days.** Skadi activates at block **25552264**
(ts 1752649201; the previous block is 1752649199). That block carries, in order:

| # | from | to | gas | `sourceHash` |
|---|---|---|---|---|
| 0 | `0xdead…0001` | `0x4200…0015` | 1 000 000 | L1-info |
| 1 | `0x0b79…c875` | *create* | 250 000 | `0x195dba1b…ebcb9ec0` |
| 2 | `0x3462…d685` | *create* | 250 000 | `0x70b82ada…9cfb0484` |

Both source hashes are **exactly** the values `test_skadi_source_hashes_match_op_node` pins, which
were derived with `cast keccak` before ever looking at the chain. The deployment inputs are
byte-identical to `bytecode/eip4788_ecotone.hex` (106 B) and `bytecode/eip2935_isthmus.hex`
(92 B). Same source hash + same input ⇒ same deposit hash.

The block shape across the boundary confirms the `withdrawals` / `parent_beacon_block_root` fix
too, and shows how long the pre-fix behaviour would have been wrong for:

| block | `withdrawals` | `parentBeaconBlockRoot` | `withdrawalsRoot` |
|---|---|---|---|
| 25552263 (pre-Skadi) | `null` | null | null |
| 25552264 (Skadi activation) | `[]` | set | set |
| 25652264 (+100k, still pre-Arsia) | `[]` | set | set |

Before these fixes kona emitted `None` for both fields and no upgrade transactions — a different
block hash on **every block of a 252-day window**, plus two contracts that would never have been
deployed.

> Not yet covered: an executor fixture *inside* the window, which would exercise kona's own
> execution rather than comparing against op-geth's output. Generating one needs L1 archive and
> beacon endpoints for Sepolia, which this workstation does not have. Worth doing before the
> next release.

### 3.2d One config, two fork views — `op_fork_activation`

`RollupConfig` answers "is fork X active?" through two independent paths:

- the inherent `is_*_active` methods, via `mantle_op_fork_active` — used by derivation;
- the `OpHardforks` / `EthereumHardforks` trait predicates, via `op_fork_activation` — used by
  `spec_by_timestamp_after_bedrock` and the engine-version selectors.

Only the first honoured the Arsia alignment. The second read the raw per-fork timestamps that
`AlignOpWithMantle` overwrites, so a config whose `ecotone_time` differed from
`mantle_arsia_time` resolved **two different forks at the same instant**, depending on which
caller you were. `mantle_op_fork_condition` closes this for Canyon…Jovian (exactly the forks
`AlignOpWithMantle` rewrites; Bedrock/Regolith and the newer Karst/Lagoon are left alone).

Pinned by `test_mantle_inherent_and_trait_fork_predicates_agree`, which sweeps a config where
every OP timestamp deliberately disagrees with `mantle_arsia_time` and asserts both paths give
the same answer — and that the shared answer is the aligned one, not both-wrong-alike.

> Prefer the `RollupConfig`-intrinsic fix over porting op-node's mutate-at-load
> `AlignOpWithMantle`. kona deserializes `RollupConfig` from rollup.json, the registry and
> dozens of tests; there is no single load point that could be relied on to call an align step.

### 3.2e `MANTLE_BASE_FEE_CONFIG` corrected to `{2, 8, 8}`

`MANTLE_EIP1559_ELASTICITY_MULTIPLIER` / `MANTLE_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR` held
`{4, 50}` since the phase-1c genesis migration (`fb62e096e4`). That pair matches **nothing**:

| Source | Elasticity | Denominator | `DenominatorCanyon` |
|---|---|---|---|
| `AlignOpWithMantle` fallback (`ChainOpConfig == nil`) | 2 | 8 | 8 |
| `deploy-config/mantle-mainnet.json`, `mantle-sepolia.json` | 2 | 8 | — |
| live sepolia-qa3 rollup config (executor fixtures) | 2 | 8 | 8 |
| `deploy-config/mantle-devnet.json` | 10 | 50 | — |
| kona, before this fix | **4** | **50** | **50** |

50 is the *devnet* denominator, whose elasticity is 10 — the old pair looks like two values
crossed from different chains. Nothing in op-node, op-chain-ops or any deploy config produces
`{4, 50}`.

**Blast radius.** The constant only governs chains whose rollup config omits `chain_op_config`
(via `default_mantle_base_fee_config`) or that fall through `base_fee_params(chain_id)` /
`base_fee_config(chain_id)` by chain ID. Real Mantle rollup.json files carry the field, which is
why this never surfaced — and why no existing test caught it. When it does bite it silently
yields a different base fee than op-node, hence a different block, with nothing to complain.

Pinned by `params::tests::mantle_base_fee_fallback_matches_op_node`, which also asserts
`denominator_canyon == denominator` (Mantle has no historical denominator change, matching
`AlignOpWithMantle`'s `dCanyon := c.ChainOpConfig.EIP1559Denominator`) and that the
`BaseFeeParams` pair is not transposed.

> The executor fixtures cannot validate this: they embed a `chain_op_config`, so they exercise
> the JSON value, not the constant. The evidence here is documentary — three independent
> sources agreeing — not a reproduced block.

### 3.2f Arsia deployment code hashes — a real failure hidden by a bare `#[ignore]`

`test_verify_arsia_{l1_block,gas_price_oracle,operator_fee_vault}_deployment_code_hash` were all
three marked `#[ignore] // TODO: fix this test` (`82fc1b98b`). Running them shows two passed and
only the Gas Price Oracle failed — and it failed on the *expectation*, not the deployment:

| | kona expected (before) | op-node asserts | kona computes |
|---|---|---|---|
| L1Block | `0x31281c99…` | `0x31281c99…` | ✅ |
| Gas Price Oracle | `0x0b858803…` | **`0xfc61bf7a…`** | `0xfc61bf7a…` |
| Operator Fee Vault | `0x8fc59a00…` | `0x8fc59a00…` | ✅ |

The authoritative values are op-node's own
`op-e2e/actions/mantletests/proofs/isthmus_fork_test.go:34-37`. `0x0b858803…` matches no op-node
constant and no deployed contract; kona's result was right all along.

Independently confirmed that the bundle itself is correct: all three `bytecode/arsia_*.hex` files
are **byte-identical** to `op-node/rollup/derive/arsia_upgrade_transactions.go` (note the Go
variable for the vault is `operatorFeeVaultArsiaDeploymentByteCode` — capital `C`, easy to miss
when grepping), and both sides emit 7 transactions with the same intents in the same order.

All three tests now run. **This is the argument against bare `#[ignore]`**: a one-line "TODO"
took a passing-by-construction check offline for two of three contracts and buried the fact that
the third was only a stale constant. Every remaining `#[ignore]` in this tree carries a reason —
see §4.3's table.

### 3.2g Mantle Elysium — implemented, and the L1 blob schedule un-pinned

Elysium is Mantle's next scheduled hardfork. Its one production effect in op-node is to **end**
the Arsia-era pin on L1's blob-fee schedule:

```go
// derive/l1_block_info.go:508
if isMantleArsiaActivated && !isMantleElysiumActivated {
    arsiaL1ChainConfig := eth.MantleArsiaL1ChainConfigByChainID(...)   // Ethereum mainnet only
    if arsiaL1ChainConfig == nil { arsiaL1ChainConfig = l1ChainConfig } // …nil everywhere else
    l1BlockInfo.BlobBaseFee = block.BlobBaseFee(arsiaL1ChainConfig)
} else {
    l1BlockInfo.BlobBaseFee = block.BlobBaseFee(l1ChainConfig)
}
```

`MantleArsiaL1ChainConfigByChainID` (`op-service/eth/config.go:28`) is a hand-written Ethereum
mainnet config carrying only the Cancun and Prague blob schedules and **no `OsakaTime`**. So from
Arsia until Elysium, Mantle mainnet prices blobs with Prague parameters no matter what L1 has
activated; from Elysium on, L1's real Osaka/BPO schedule applies. The helper returns `nil` for
every other L1, so **the pin is a no-op on Sepolia** — that chain-scoping is load-bearing.

**What kona had.** The pre-Elysium half only, and implemented in the wrong place:
`kona-registry::l1` forced Ethereum mainnet's `osaka_time` and `bpo1..5_time` to `None`
(`mantle-xyz/kona@72a20ab9`, "Blob fee parameters #26"). Right effect, two problems — it lied
about L1 to every consumer of that config, and **it had no way to stop**: nulling the schedule is
not a thing Elysium can undo.

**What it has now.**

| Piece | Location |
|---|---|
| `mantle_elysium_time` config field, `NONE`/`iter`/`has_any_hardfork` entries | `kona-genesis::MantleHardForkConfig` |
| `is_mantle_elysium_active`, `is_first_mantle_elysium_block` | `kona-genesis::RollupConfig` |
| `is_mantle_elysium_active_at_timestamp` trait override | `RollupConfig`; default `false` in `alloy-op-hardforks` |
| `is_mantle_arsia_blob_schedule_pinned` — the composite gate | `kona-genesis::RollupConfig` |
| the gate's use: ignore the scheduled BPO entries and `osaka_time` while pinned | `L1BlockInfoTx::try_new` |
| `ETHEREUM_MAINNET_CHAIN_ID` — scopes the pin to L1 mainnet | `kona-genesis::chain` |
| Ethereum mainnet's real `osaka_time` / `bpo1..5_time` restored | `kona-registry::l1` |

Both halves of the gate use op-node's "active, but **not** on the activation block itself" form
(`isMantleArsiaButNotFirstBlock`, `isMantleElysiumButNotFirstBlock`) — the L1-info transaction is
emitted before the fork's upgrade transactions run. Concretely: the pin is still in force *on* the
Elysium activation block and lifts on the next one.

**Do not re-null `osaka_time`/`bpo1..5_time` in `kona-registry::l1`.** That would make activating
Elysium a no-op. The registry now carries L1's truth and `try_new` is the only thing suppressing
it — which is also why upstream's `test_get_l1_bpo_mainnet` could be un-ignored (see §4.3).

**Tests** (`info::variant::test`), each negative-controlled:

- `mantle_mainnet_pins_blob_schedule_to_prague_between_arsia_and_elysium` — asserts Prague pricing
  on an L1 header past BPO1, *and* that Prague and BPO1 price that header differently, so the
  assertion cannot pass vacuously. Fails when the pin is disabled.
- `mantle_elysium_restores_the_real_l1_blob_schedule` — activation block still pinned, next block
  not. Fails when the pin is disabled.
- `mantle_on_sepolia_l1_is_never_pinned` — **fails when the `chain_id` guard is removed**, which
  is what proves the guard is doing work rather than decorating.
- `arsia_blob_schedule_pin_window` — the predicate alone, including "never scheduled Elysium stays
  pinned forever" and "non-Mantle chains are never pinned".

### 3.2h Mantle hardfork schedule validation

kona had no equivalent of op-node's `CheckMantleForks` (`rollup/mantle_types.go`), which runs at
startup via `AlignOpWithMantle` (`op-node/cmd/main.go`). A rollup.json that schedules a fork
whose predecessor is missing, or schedules forks out of order, was **rejected by op-node and
accepted silently by kona** — the exact mistake a newly schedulable fork invites.

`MantleHardForkConfig::check_fork_order` ports it pairwise over `ordered()`, the single list that
`iter()` also walks (so a newly added fork is covered by editing one place; pinned by
`every_configured_fork_is_covered_by_the_order_check`). Equal timestamps are allowed — op-node
only rejects `*a > *b` — and a *trailing* run of unscheduled forks is fine.

Wired into both places a `RollupConfig` is read from disk:
`SingleChainHost::read_rollup_config` and `InteropHost::read_rollup_configs`, each with its own
`MantleForkOrder` error variant. Configs built in code or taken from the registry are covered by
tests instead.

### 3.2i `deny_unknown_fields` does not survive `#[serde(flatten)]`

`MantleHardForkConfig` carries `#[serde(deny_unknown_fields)]`, which reads as "a Mantle fork
kona has not implemented is a startup error, not a silent divergence". **That guarantee is false
on the only path production uses.**

`RollupConfig` embeds the struct with `#[serde(flatten)]` — it has to, because op-node writes the
fork times at the *top level* of rollup.json (`op-node/rollup/types.go:138-162`). serde documents
`deny_unknown_fields` as unsupported in combination with `flatten`, and it is **silently inert**
rather than a compile error. Measured, not assumed:

| deserialise | `mantle_someday_time: 40` |
|---|---|
| `MantleHardForkConfig` directly | rejected (`unknown field`) |
| `RollupConfig` (flattened — the real path) | **accepted, value dropped** |

So op-node would activate that fork and kona would not, with nothing logged. The real guard is a
raw-key scan at the two places a config is read from disk —
`kona_host::mantle_config::unknown_mantle_forks`, driven by `MantleHardForkConfig::TIME_KEYS` —
which rejects any `mantle_*_time` key this build does not implement.

Pinned by `flatten_defeats_deny_unknown_fields_on_the_real_path`, which asserts the *broken*
behaviour on purpose so nobody re-derives the wrong conclusion from reading the attribute. If
serde ever fixes this and that test starts failing, the scan becomes belt-and-braces rather than
the only thing standing there — check before deleting it.

> The same hole applies to `HardForkConfig` (the OP forks), which is flattened too. The
> `deny_unknown_fields` hole itself is left alone — it is upstream's struct and upstream's
> problem, and on Mantle every OP fork is pinned to Arsia anyway.
>
> `HardForkConfig` is **not** otherwise untouched, though. `kona/crates/protocol/genesis/src/chain/hardfork.rs`
> carries a Mantle change: `lagoon_time` gets `serde(alias = "interop_time")`, because this
> monorepo's Go op-node still serialises the field as `json:"interop_time"`
> (`op-node/rollup/types.go`) while upstream kona renamed it. `#[serde(flatten)]` means unknown
> keys are *silently dropped* rather than rejected, so without the alias kona would read an
> op-node-produced rollup.json, see no `lagoon_time`, and conclude the fork never activates —
> with no error anywhere. Pinned by `mantle_alias_tests::interop_time_alias_is_accepted`.

### 3.2j The two fork axes — `[Skadi, Arsia)` state root and pre-Skadi receipts root

**Consensus. Both were latent on `main` and on `dev/mantle-v1.6.3`; neither was introduced by the
v1.7.0 sync.** They surfaced only once an executor fixture was taken from *inside* the window —
the gap §3.2c flagged as "not yet covered".

#### The shape of the mistake

A Mantle chain has **two independent fork axes**, and op-node aligns them to *different* Mantle
forks (`op-chain-ops/genesis/mantle_config.go:163-177`, `alignEthWithMantle`):

```text
ShanghaiTime = CancunTime = PragueTime = MantleSkadiTime
OsakaTime                              = MantleLimbTime
CanyonTime .. JovianTime               = MantleArsiaTime
```

kona only ever implemented the **third** line. `RollupConfig::ethereum_fork_activation` routes every
L1 fork through `OpHardfork::activating_op_fork(fork)` → `op_fork_activation`, so Cancun resolved
via Ecotone and Prague via Isthmus — both pinned to Arsia. For the whole `[Skadi, Arsia)` window
(252 days / ~10.9M blocks on Mantle Sepolia) kona believed Cancun and Prague were inactive while
op-geth had had them on since Skadi.

`alloy-evm` gates the pre-block system calls on exactly those predicates —
`is_cancun_active_at_timestamp` for EIP-4788 and `is_prague_active_at_timestamp` for EIP-2935
(`alloy-evm-0.37.1/src/block/system_calls/{eip4788,eip2935}.rs`). So in the window the executor
skipped both. Measured on block 28000000: op-geth writes 4 accounts (L1Block, the depositor's
nonce, the 2935 ring buffer, the 4788 buffer); kona's bundle contained **2**. Every other header
field matched byte for byte — only `state_root` differed.

**Fix**: `RollupConfig::mantle_ethereum_fork_condition` overrides the L1 axis for Mantle chains.
Guarded by `test_mantle_inherent_and_trait_fork_predicates_agree`, which now asserts the two axes
are *separate* (Cancun on at Skadi while Ecotone is still off). That test previously asserted the
**bug** — "Cancun rides Ecotone, Prague rides Isthmus" — so the defect was not merely untested, it
was pinned. Anyone fixing it would have been greeted by a red test.

#### The receipts root

Independently, `compute_receipts_root` gated deposit-nonce stripping on
`is_mantle_skadi_active(timestamp)`, so every block *before* Skadi kept the nonce in the receipts
trie and produced a `receipts_root` the chain disagrees with.

Mantle strips unconditionally. That is the empirical answer, not a translation of upstream's
`[Regolith, Canyon)` window: Mantle's `regolith_time = 0` and `canyon_time = mantle_arsia_time`
would imply `[0, Arsia)`, yet post-Arsia block 43000000 also reproduces *with* stripping. Gate is
now `config.is_mantle()`, guarded by `mantle_receipts_root_tests`.

#### Evidence

`sepolia-testnet-qa1` (chain 5003), rollup config via `optimism_rollupConfig`, using
`cargo run --release -p execution-fixture -- -r <L2 archive RPC> -b <n> --skip-save -c <cfg>`:

| block | position | before | after |
|---|---|---|---|
| 20000000, 25552263 | pre-Skadi | ✗ `receipts_root` | ✓ |
| 25552264 | Skadi activation | ✓ | ✓ |
| 25552265, 28000000 | `[Skadi, Limb)` | ✗ `state_root` | ✓ |
| 31600264 | Limb activation | ✗ | ✓ |
| 33000000, 36438663 | `[Limb, Arsia)` | ✗ | ✓ |
| 36438664 | Arsia activation | ✓ | ✓ |
| 43000000 | post-Arsia | ✓ | ✓ |

`origin/main` was built and run against the same blocks and the same config: **identical failure
set**, which is what establishes these as pre-existing rather than sync regressions.

Both regression tests were negative-controlled — disabling each fix turns its test red, restoring
it turns it green.

#### Not all four remapped forks carry the same weight

Measured by removing them one at a time and re-running all 13 executor fixtures:

| remapped fork | effect | guarded by |
|---|---|---|
| **Cancun** | consensus — gates EIP-4788 pre-block system call | `block-28000000`, `block-34065622` turn red |
| **Prague** | consensus — gates EIP-2935 pre-block system call | same two fixtures turn red |
| Shanghai | **inert today** — its only reachable consumer is `alloy-evm`'s withdrawal balance increment, and the OP executor always passes `withdrawals = None` | unit test only; all 13 fixtures stay green |
| Osaka | **inert today** — its only consumer is `EngineGetPayloadVersion::from_cfg`, which already has an `is_mantle_limb_active` disjunct; the EVM's `OpSpecId` is chosen from the Mantle forks in `alloy_op_evm::spec_by_timestamp_after_bedrock` | unit test only; all 13 fixtures stay green |

Shanghai and Osaka are mapped for parity with op-geth's chain config, not because kona depends on
them today. If an `alloy` upgrade ever routes a live decision through them,
`test_mantle_l1_fork_axis_is_pinned_for_every_variant` is the only warning that will fire.

#### Third file: `kona/crates/node/engine/src/versions.rs`

The fix changed a **third** test file, and this one lost coverage rather than gaining it. Its two
Mantle tests previously pinned the `|| cfg.is_mantle_skadi_active(..)` / `|| cfg.is_mantle_limb_active(..)`
disjuncts in the Engine-version selectors, because their premises (`!is_cancun_active(150)`,
`!is_osaka_active(300)`) made the disjunct the only possible source of truth. Once Cancun/Prague
ride Skadi and Osaka rides Limb, those disjuncts are **redundant** — deleting all three leaves
every `versions.rs` test green, which was verified.

Redundant code cannot be pinned through observable behaviour, so the equivalence itself is pinned
instead, in `rollup.rs::test_mantle_l1_axis_matches_the_engine_disjuncts`. The disjuncts are kept
(they are a local statement of op-node's rule and cost nothing) but the comments above them were
rewritten: they previously claimed to be load-bearing, which is no longer true, and one of them —
"Mantle configs never set `karst_time`, so the Osaka branch alone can never fire" — had become
outright false.

#### Fail-closed default on the L1 axis

`mantle_ethereum_fork_condition`'s catch-all returns `ForkCondition::Never` rather than falling
through to the OP ladder, matching `alignEthWithMantle`'s own default of leaving unlisted L1 forks
nil. This matters because `alloy-op-hardforks` is an **in-tree** crate: if someone adds an
`activates_l1_fork` mapping to any of Canyon..Jovian, a fall-through would light up an L1 fork at
Arsia that op-geth has no configuration for — the exact class of divergence this section is about.

The arm below Shanghai is expressed as an ordering test (`f if f < EthereumHardfork::Shanghai`)
rather than a variant list, because `EthereumHardfork` carries `ArrowGlacier` and `GrayGlacier`
between London and Paris; a variant list drafted from memory omitted them and would have flipped
two `Block(0)` forks to `Never`. `test_mantle_l1_fork_axis_is_pinned_for_every_variant` iterates
`EthereumHardfork::VARIANTS` so that an upstream insertion on either side of the boundary fails
the build rather than changing consensus silently.

#### Closing the fixture gap (§3.2c)

Five fixtures generated from `sepolia-testnet-qa1` were added to
`kona/crates/proof/executor/testdata/`, so the fork window is now covered by the normal
`cargo test` path and not just by ad-hoc live-chain runs:

| fixture | position |
|---|---|
| `block-25552263` | pre-Skadi |
| `block-25552264` | Skadi activation (the two upgrade deposits) |
| `block-28000000` | `[Skadi, Limb)`, L1-info only |
| `block-34065622` | `[Limb, Arsia)`, carries 2 user transactions (`0x02`) alongside the L1-info deposit |
| `block-36438664` | Arsia activation |

Re-running the suite with both fixes disabled is what demonstrates the gap was real:

```text
old 8 fixtures (sepolia-qa3)   ... all ok      <- zero coverage of this defect class
block-25552263                 ... FAILED
block-28000000                 ... FAILED
block-34065622                 ... FAILED
block-25552264, block-36438664 ... ok          <- activation blocks are not sensitive
```

The pre-existing corpus is green with the bug present *and* absent. That is the structural reason
four review rounds and "8/8 fixtures pass" missed a consensus defect: every fixture came from
`sepolia-qa3`, where `mantle_arsia_time = 0`, so no fork-window branch is ever entered. When
adding executor fixtures, check the embedded `rollup_config` has *staggered* fork times —
otherwise the fixture cannot fail for fork-related reasons. See §3.2c for the chain-selection
note (`sepolia-qa3` has every fork at genesis; `sepolia-testnet-qa1` has real staggered times).

### 3.3 kona-hardforks — Arsia + MantleHardforks

Vendored from mantle-xyz/kona in Phase 1a; registered in Phase 1b.

| File | Change |
|---|---|
| `kona/crates/protocol/hardforks/src/arsia.rs` | New file. `Arsia` upgrade-tx bundle (332 lines, 7 deposit txs: L1Block, GasPriceOracle, OperatorFeeVault deployments + proxy updates). |
| `kona/crates/protocol/hardforks/src/mantle_forks.rs` | New file. `MantleHardforks` registry exposing `MantleHardforks::ARSIA`. |
| `kona/crates/protocol/hardforks/src/bytecode/arsia_{gpo,l1_block,ofv}.hex` | New bytecode fixtures referenced by `arsia.rs`. |
| `kona/crates/protocol/hardforks/src/lib.rs` | `mod arsia; pub use arsia::Arsia;` and `mod mantle_forks; pub use mantle_forks::MantleHardforks;`. |

### 3.4 kona-genesis — RollupConfig / SystemConfig Mantle additions

The largest sub-phase. Adds Mantle predicates, hardfork timestamps, and BaseFee config plumbing.

| File | Change |
|---|---|
| `kona/crates/protocol/genesis/src/rollup.rs` | `RollupConfig` gains `pub mantle_hardforks: MantleHardForkConfig`. Inherent methods `is_mantle`, `is_mantle_skadi_active`, `is_mantle_limb_active`, `is_mantle_arsia_active`, `is_first_mantle_arsia_block`. `Default::default` switches `chain_op_config` to `MANTLE_BASE_FEE_CONFIG`; helper `default_mantle_base_fee_config` for serde defaulting. Existing `is_jovian_active` etc. get Mantle gating. **`spec_id` / `revm_spec_id` / `mantle_spec_id` were deleted in the v1.7.0 sync** — upstream dropped kona-genesis's `revm` feature (and its `op-revm` optional dep), which silently `#[cfg]`-ed the whole block out. Spec resolution now lives solely in `alloy_op_evm::spec_by_timestamp_after_bedrock`. |
| `kona/crates/protocol/genesis/src/rollup.rs` (`impl OpHardforks`) | **Overrides `is_mantle`, `is_mantle_skadi_active_at_timestamp`, `is_mantle_limb_active_at_timestamp`, `is_mantle_arsia_active_at_timestamp`**, delegating to the inherent methods. Consensus-critical: the trait defaults return `false`, so without these a Mantle `RollupConfig` passed to `evm_env_for_op_next_block` resolves `JOVIAN` instead of `ARSIA`. Note `is_mantle` duplicates the one-line inherent body on purpose — `Self::is_mantle(self)` resolves to the inherent method today, but becomes unbounded recursion if that method is ever removed. |
| `kona/crates/protocol/genesis/src/chain/mantle_hardfork.rs` | New file. `MantleHardForkConfig` struct with the Mantle upgrade timestamps. |
| `kona/crates/protocol/genesis/src/chain/mantle_hardfork.rs` | `pub const NONE: Self` — all fields `None`. Needed because `Default` is unusable from a `const` initializer and the `const RollupConfig`s in `kona-registry`'s `test_utils/op_{mainnet,sepolia}.rs` must set this field. Adding a Mantle hardfork now only needs a default here. |
| `kona/crates/protocol/genesis/src/chain/mod.rs` | `MANTLE_MAINNET_CHAIN_ID = 5000` / `MANTLE_SEPOLIA_CHAIN_ID = 5003`; register `mod mantle_hardfork`. |
| `kona/crates/protocol/genesis/src/chain/config.rs` | `ChainConfig::rollup_config` initialises `mantle_hardforks: MantleHardForkConfig::default()`. |
| `kona/crates/protocol/genesis/src/updates/base_fee.rs` | New file. `BaseFeeUpdate` type (187 lines), with `apply()` and `TryFrom<&SystemConfigLog>`. |
| `kona/crates/protocol/genesis/src/updates/mod.rs` | `mod base_fee; pub use base_fee::BaseFeeUpdate;`. |
| `kona/crates/protocol/genesis/src/system/kind.rs` | Insert `SystemConfigUpdateKind::BaseFee = 4`; shift `Eip1559/OperatorFee/MinBaseFee/DaFootprintGasScalar` to 5–8. **Wire-format change** intentional per Mantle protocol. |
| `kona/crates/protocol/genesis/src/system/errors.rs` | Add `BaseFeeUpdateError` enum (6 variants) + `SystemConfigUpdateError::BaseFee` arm. |
| `kona/crates/protocol/genesis/src/system/{mod,log,update}.rs` | Plumb `BaseFee` through re-exports, log dispatch, and `SystemConfigUpdate::BaseFee` variant + apply. |
| `kona/crates/protocol/genesis/src/system/config.rs` | `SystemConfig` gains `pub base_fee: Option<U256>` field; serde alias updated. |
| `kona/crates/protocol/genesis/src/params.rs` | New consts `MANTLE_EIP1559_ELASTICITY_MULTIPLIER`, `MANTLE_EIP1559_BASE_FEE_MAX_CHANGE_DENOMINATOR`, `MANTLE_BASE_FEE_PARAMS`, `MANTLE_BASE_FEE_CONFIG`; route Mantle chain IDs to them from `base_fee_params` / `base_fee_params_canyon` / `base_fee_config`. |
| `kona/crates/protocol/genesis/src/lib.rs` | Re-export the new Mantle constants/types (`MANTLE_BASE_FEE_*`, `MANTLE_*_CHAIN_ID`, `MantleHardForkConfig`, `BaseFeeUpdate`, `BaseFeeUpdateError`). |
| `kona/crates/protocol/genesis/src/genesis.rs` | Add `base_fee: None` to a test `SystemConfig` literal. |

### 3.5 kona-derive — Mantle upgrade-tx routing + provider hook

| File | Change |
|---|---|
| `kona/crates/protocol/derive/src/attributes/stateful.rs` | On Mantle chains (`is_mantle()`), the upgrade-tx emission path emits only `MantleHardforks::ARSIA` at its activation; OP hardfork bundles are skipped. Non-Mantle chains keep the full upstream OP path (ECOTONE/FJORD/ISTHMUS/JOVIAN/KARST/INTEROP+CrossL2Inbox). |
| `kona/crates/protocol/protocol/src/info/variant.rs` | L1Info deposit literal fills `eth_value: 0, eth_tx_value: None`. **Adds `L1BlockInfoTx::Arsia` variant** (2026-05; closed the Arsia decoder gap that triggered the cost-estimator panic on mainnet block 95264176). `try_new` post-Ecotone picker adds `is_mantle_arsia_active && !is_first_mantle_arsia_block` branch before Jovian. All `match L1BlockInfoTx` expressions in the crate were patched to be exhaustive. |
| `kona/crates/protocol/protocol/src/deposits.rs` | **2026-07: restore Mantle version-1 deposit decode (lost in the original vendoring).** Upstream OP `decode_deposit` only handles v0, but Mantle's L1 `OptimismPortal` emits `DEPOSIT_VERSION = 1` for **every** user deposit — so the first real user deposit in a proven range failed with `BadEncoding(DepositError(InvalidVersion(0x…01)))`. The op-alloy `TxDeposit` struct/codec (`eth_value`/`eth_tx_value`) was ported in §3.2, but the kona-protocol log decoder that populates them was not. Fix: add `DEPOSIT_EVENT_VERSION_1`, route v0/v1 in `decode_deposit`, port `unmarshal_deposit_version1` + `decode_u128_field`/`decode_u8_field` from `mantle-xyz/kona`. v1 `opaqueData` = `abi.encodePacked(mntValue, mntTxValue, msg.value, ethTxValue, gasLimit, isCreation, data)` → fills `eth_value`/`eth_tx_value` (0→None), matching op-node's `unmarshalDepositVersion1`. Adds `DepositError::{EthValueDecode,EthTxValueDecode}` + 5 v1 tests. |
| `kona/crates/protocol/protocol/src/info/arsia.rs` | **New file (2026-05)**. `L1BlockInfoArsia` is a thin nested wrapper around `L1BlockInfoJovian` with selector `setL1BlockValuesArsia()` = `0x49e72383`. Payload layout is byte-identical to Jovian; only the selector differs (verified by reverse-engineering `arsia_l1_block.hex` dispatcher in `kona-hardforks`). Round-trip + dispatcher + picker tests inline. |
| `kona/crates/protocol/protocol/src/info/{mod.rs, errors.rs}` | `mod arsia;` + `pub use` re-export; inheritance chain comment updated to `... < L1BlockInfoJovian < L1BlockInfoArsia`. `DecodeError::InvalidArsiaLength` added. (`DecodeError::InvalidInteropLength` is a pre-existing legacy variant that has no corresponding decoder — see note below.) |
| `kona/crates/protocol/protocol/src/utils.rs` | `to_system_config`'s `match L1BlockInfoTx` extended for `Arsia` variant. |
| `kona/crates/protocol/protocol/src/info/jovian.rs` | `L1BlockInfoJovianBaseFields` decorated with `#[delegatable_trait]` so `L1BlockInfoArsia` can `ambassador::Delegate` the trait into its embedded Jovian base. |
| `kona/crates/protocol/hardforks/src/{ecotone,fjord,isthmus,jovian,lagoon}.rs` | OP hardfork upgrade-tx literals filled `eth_value: 0, eth_tx_value: None`. **`interop.rs` is gone** — upstream v1.7.0 deleted it and replaced the fork with `lagoon.rs` (same OP upgrade #20, renamed and restructured onto the NUT bundle). Its two literals were patched by hand; see the §6 caveat. |
| `kona/crates/protocol/protocol/src/{batch/single.rs, utils.rs}` test fixtures | Added Mantle `eth_value: 0, eth_tx_value: None` to one `TxDeposit { ... }` literal and `base_fee: None` to three `SystemConfig { ... }` literals (2026-05; previously the kona-protocol lib test target did not compile against the Mantle field additions). |
| `kona/crates/protocol/registry/src/l1/mod.rs` | **Deliberately left at upstream's values — do NOT "restore" the historical Mantle patch here.** `mainnet()` carries Ethereum mainnet's **real** `osaka_time` / `bpo1_time` … `bpo5_time` (`EthereumHardfork::*::mainnet_activation_timestamp()`), and `default_blob_schedule()` includes the Osaka / BPO1 / BPO2 entries. The `test_get_l1_bpo_*` tests are live and passing. <br><br>**History, so nobody re-applies it**: `mantle-xyz/kona@72a20ab9` ("Blob fee parameters #26", 2026-04-24) nulled these fields to pin Mantle mainnet's L1 blob-fee schedule to Prague behaviour. That pin is now expressed **where it belongs** — as the Arsia→Elysium window in `RollupConfig::is_mantle_arsia_blob_schedule_pinned()` (§3.2g), mirroring op-node's `eth.MantleArsiaL1ChainConfigByChainID` (`derive/l1_block_info.go:508`). <br><br>**Consensus hazard if reverted**: re-nulling these fields makes the pin permanent and un-liftable — activating Elysium would become a silent no-op and Mantle mainnet would price blobs with Prague parameters forever. See §3.2g and the `[MANTLE]` note at `l1/mod.rs`. |

**Per-hardfork decoder migration checklist** — every Mantle hardfork that
changes `L1Block` calldata format (new selector or new fields) MUST be
accompanied by:

1. A new `kona-protocol::info::<fork>.rs` module with `L1BlockInfo<Fork>`
   struct, `L1_INFO_TX_SELECTOR` const, `L1_INFO_TX_LEN` const,
   `decode_calldata`, `encode_calldata`.
2. A new variant in `kona-protocol::info::variant.rs::L1BlockInfoTx`.
3. A new arm in `variant.rs::decode_calldata`.
4. A new branch in `variant.rs::try_new` post-Ecotone picker (gated on
   `is_mantle_<fork>_active && !is_first_mantle_<fork>_block`).
5. Arms in every `match L1BlockInfoTx` expression in the crate (and in
   `protocol/src/utils.rs`).
6. A round-trip test in the new module plus dispatcher + picker tests in
   `variant.rs` (the picker test must construct a `RollupConfig` with the
   new fork's timestamp active and assert `try_new` returns the new
   variant).

Omission of any of (1)–(6) will cause `cost-estimator` / derivation
pipeline to panic on the first L2 block produced after the hardfork
activates, exactly as happened with Arsia on Mantle mainnet block 95264176
in 2026-05.

**Audit-methodology note (2026-05):** when verifying "what does upstream
kona have that local doesn't", the canonical upstream for mantle-v2/rust
is the optimism monorepo's `rust/kona/` subtree (`ethereum-optimism/optimism`)
at the §1 sync tag. **Do NOT** treat `mantle-xyz/kona` (a separate Mantle
fork) or `op-rs/kona` (a separate standalone repo) as the upstream — both
contain experimental code (e.g. `info/interop.rs`, `info/common.rs`) that
never landed in optimism upstream. An earlier audit pass in this fix
proposed vendoring an `L1BlockInfoInterop` variant based on
`mantle-xyz/kona@main`, but the real upstream
(`ethereum-optimism/optimism rust/kona/.../info/` @ `kona-client/v1.5.1`)
has no `interop.rs` and no `L1BlockInfoInterop` type, so the proposal was
reverted. The `DecodeError::InvalidInteropLength` enum variant remains as
pre-existing legacy and is currently dead code.

### 3.6 kona-proof — executor uses Mantle-aware revm spec

| File | Change |
|---|---|
| `kona/crates/proof/executor/src/builder/env.rs` | **Rewritten by the v1.7.0 sync.** Upstream folded `evm_cfg_env` + `prepare_block_env` into one `evm_env` that delegates to `alloy_op_evm::evm_env_for_op_next_block`. Mantle-aware spec selection now arrives via the `OpHardforks` overrides (§3.4) instead of an explicit `revm_spec_id` call. **The Arsia base-fee gate had to be re-applied by hand**: pre-Arsia blocks inherit `parent_header.base_fee_per_gas` instead of recomputing. It lived in the deleted `prepare_block_env`; upstream recomputes unconditionally. Dropping it changes the base fee of every pre-Arsia block. |
| `kona/crates/proof/executor/src/builder/assemble.rs` | `compute_receipts_root` gates deposit-nonce stripping on `config.is_mantle_skadi_active(timestamp)` instead of upstream's `is_regolith_active && !is_canyon_active` window. **Was unregistered and carried no `[MANTLE]` marker** until the v1.7.0 sync; a marker is now in place. |
| `kona/crates/proof/executor/src/test_utils.rs` | Test infrastructure additions (`alloy_chains::Chain`, `reqwest::Url` imports; `StatelessL2Builder::new` takes `&rollup_config`). Earlier revisions of this file described `create_static_fixture` as a "placeholder `Ok(true)`" — **that was wrong**: the function is complete (fetch block → build → execute → verify header → write JSON → tar → clean up); `Ok(true)`/`Ok(false)` is just its soft-failure convention. It is what regenerates `testdata/*.tar.gz`, driven by `kona/examples/execution-fixture`. |
| `kona/crates/proof/executor/Cargo.toml` | Declare optional deps `alloy-chains`, `reqwest`, `url`; list `dep:alloy-chains` under the `test-utils` feature. |
| `kona/bin/client/src/fpvm_evm/tx.rs` | `FromTxWithEncoded<TxDeposit>` reads `tx.eth_value` / `tx.eth_tx_value` into `DepositTransactionParts` using the 0→None convention. |

### 3.7 alloy-op-evm — Mantle protocol changes

Corresponds to mantle-xyz/evm commits `707922af`, `5f383c5`, `9fe2c85`, `760129f` (chronological).

**Source-commit notes** (2026-05 audit clarification):

- `707922af` ("feat: mantle feature", PinelliaC, 2025-10-22, fusaka branch → merged via PR #3 "mantle limb") is the original source for:
  - The `ensure_create2_deployer(...)` call commented out + its `use canyon::ensure_create2_deployer;` import removed.
  - `#[allow(dead_code)]` added to `crates/op-evm/src/block/canyon.rs`.
  - A transient *disabling* of the Jovian DA-footprint enforcement (deleted `get_jovian_da_footprint_scalar` / `jovian_da_footprint_estimation`). **Note**: this disable was later reverted upstream by `9fe2c85` "feat: support arsia" (mantle-arsia branch), which re-enabled Jovian DA-footprint as part of Arsia. Local code therefore keeps the enforcement **enabled** (matches final main-branch state).
- `5f383c5` ("feat: support mantle", RealiCZ, 2026-01-15, mantle-arsia branch) re-applied the create2-deployer / dead_code allow on top of the v0.25.2 baseline.
- `9fe2c85` ("feat: support arsia", ivan, 2025-12-31) added Arsia support and re-enabled Jovian DA-footprint enforcement.
- `760129f` ("fix: set deposit_receipt_version to None for Mantle support", RealiCZ, 2026-01-15) set `deposit_receipt_version = None`.

| File | Change |
|---|---|
| `alloy-op-evm/src/tx.rs` | `OpTxTr` impl adds `eth_value()` / `eth_tx_value()` methods (delegated to the wrapped `OpTransaction`). |
| same | `FromTxWithEncoded<TxDeposit>` reads the new BVM_ETH fields into `DepositTransactionParts` (0→None). |
| `alloy-op-evm/src/env.rs` | **The KARST hook is no longer commented out** — op-revm v20 has the variant, and the v1.7.0 sync restored upstream's `is_karst_active_at_timestamp => KARST` / `is_lagoon_active_at_timestamp => LAGOON` arms. Safe because `spec_by_timestamp_after_bedrock` short-circuits on `is_mantle()` *before* the OP fork check, so a Mantle chain never reaches them. |
| same (tests) | The `OpSpecId::KARST` `test_case` is restored, likewise. Mantle-specific coverage lives in `test_mantle_spec_routing_arsia` and `test_non_mantle_chain_uses_standard_routing`. |
| `alloy-op-evm/src/block/mod.rs` | `deposit_receipt_version = None` (corresponds to commit 760129f). |
| same | Comments out the `ensure_create2_deployer(...)` call and its `use canyon::ensure_create2_deployer;` import (origin: 707922af; re-applied in 5f383c5 against v0.25.2 baseline). |
| same | Drops the `spec_id` argument from `operator_fee_charge` in two call sites to match `mantle-xyz/revm`'s 2-arg signature. |
| `alloy-op-evm/src/block/canyon.rs` | Adds `#![allow(dead_code)]` because the function is now unreachable (origin: 707922af). |
| same (Jovian DA-footprint enforcement) | Kept **active** locally (`block/mod.rs` `jovian_da_footprint_estimation` + the pre-execute check + post-execute accumulation). This matches the post-`9fe2c85` Mantle stance; `707922af`'s transient disable is therefore not ported. |

### 3.7b alloy-op-hardforks — Mantle fork enum + `OpHardforks` predicates

⚠️ **This file carries 37 Mantle references and, until the v1.7.0 sync, had zero `[MANTLE]`
markers and no registry entry at all.** A `grep "\[MANTLE\]"` audit could never see it; it was
found only because it surfaced as a merge conflict. If a future sync takes upstream's side here,
Mantle loses its fork enum *and* the trait predicates §3.4 depends on.

| File | Change |
|---|---|
| `alloy-op-hardforks/src/lib.rs` | `hardfork!(MantleHardfork { Skadi, Limb, Arsia })` plus `from_chain_and_timestamp`, `mantle_mainnet()`, `mantle_sepolia()`, and the `MANTLE_*_TIMESTAMP` constants. |
| same | `MANTLE_META_TX_PREFIX` (32-byte tag, permanently disabled since MantleEverest) + `is_mantle_meta_tx`. |
| same (`OpHardforks` trait) | Default methods `is_mantle`, `is_mantle_skadi_active_at_timestamp`, `is_mantle_limb_active_at_timestamp`, `is_mantle_arsia_active_at_timestamp`, all returning `false`. **These defaults are the trap described in §2.2** — an implementor that forgets to override them is silently treated as a non-Mantle chain. |
| same (tests) | `mantle_timestamp_constants` pins the fork ordering with `const { assert!(..) }`, so a bad activation timestamp fails the *build*, not just `cargo test`. |

### 3.8 kona-client fpvm — `OpSpecId` match exhaustiveness

| File | Change |
|---|---|
| `kona/bin/client/src/fpvm_evm/precompiles/provider.rs` | Both `OpSpecId` matches read `JOVIAN \| OSAKA \| ARSIA => jovian()` and `KARST \| LAGOON => karst()` (same shape for `accelerated_*`). The `karst` import is **required** — the old "drop the karst import" guidance is obsolete. Mantle's OSAKA/ARSIA stay on Jovian's precompile set: routing them to `karst()` would be a consensus change. There is no `INTEROP` variant to match on any more. |

> ⚠️ **Known divergence, deliberately not fixed (jay, 2026-09-10): Mantle does not run fault
> proofs.** This provider routes `OSAKA | ARSIA` to `jovian()`, whose bn254-pairing and
> BLS12-381 MSM/pairing precompiles carry Jovian's *reduced* input limits (81,984 / 288,960 /
> 278,784 / 156,672). op-revm's own `OpPrecompiles::new_with_spec` — what production execution
> and the new `kona/sp1` client use — maps `ARSIA.into_eth_spec()` to `SpecId::OSAKA` and hands
> back the plain Ethereum set, with no such limits. A call landing between the two would succeed
> on chain and halt in the proof. This routing predates the v1.7.0 sync and was chosen for match
> exhaustiveness, not consensus (the original comment said as much); the sync preserved it rather
> than changing consensus silently. Revisit if Mantle ever adopts fraud proofs.

### 3.9 op-reth — on disk, but OUT of the workspace

**Superseded posture (v1.7.0 sync).** Phase 5 deleted the `op-reth/` subtree outright. The
v1.7.0 `git subtree pull` re-introduced it — 180 files — exactly as §3.11 predicted, and the
decision this round (jay, 2026-09-10) was **not to re-delete it**: the directory stays on disk
but is deliberately kept out of `[workspace] members`, so it is never compiled, linted,
`cargo deny`-ed or `no_std`-checked. Effectively the old `exclude` posture without the
`exclude` key. `op-reth-test-engine/` is held out the same way (§3.1).

⚠️ Consequence: `op-reth/crates/rpc/src/error.rs` there is the **upstream** version, without
Mantle's `BvmEth(_) | TxL1CostOutOfRange` arm. Nothing consumes it today, but anyone who adds
these crates back to the workspace inherits upstream behaviour, not Mantle's.

Historically, the reasoning for removing it:

The entire `op-reth/` subtree (the Mantle execution-layer node) was **deleted** from
`rust/` in Phase 5. Nothing in `kona` / `op-alloy` / `alloy-op-evm` / `alloy-op-hardforks` ever
depended on the `reth-optimism-*` / `op-reth` / `reth-op` crates — `op-reth` was a
standalone binary that merely shared this workspace (and the shared revm/alloy patches).

The EL node now lives in its own repo, **`mantle-xyz/reth@mantle-elysium`**, which is a
self-contained workspace (it even pulls `op-alloy` / `alloy-op-evm` *back* from
`mantle-xyz/mantle-v2@mantle-elysium`). The former lone Mantle change here — the
`TryFrom<OpTxError>` arm for `BvmEth(_) | TxL1CostOutOfRange` in
`op-reth/crates/rpc/src/error.rs` — lives in that repo now.

CI is unaffected: `.github/workflows/ci-main-migrated.yml` already installs `op-reth`
from `https://github.com/mantle-xyz/reth/releases/...`, not from this workspace.

See §3.11 for the subtree-sync strategy.

### 3.10 op-core — vendored data (outside the rust/ subtree)

| File | Change |
|---|---|
| `<mantle-v2 root>/op-core/nuts/bundles/karst_nut_bundle.json` | Copied verbatim from optimism so `kona-hardforks/build.rs` can find it via its ancestor walk. |
| `<mantle-v2 root>/op-core/nuts/bundles/lagoon_nut_bundle.json` | Same, added in the v1.7.0 sync (blob `d154ba6c`). |

**Note**: these files are *outside* `rust/`, so `git subtree pull` will not sync them. If a
future upstream build.rs looks for additional bundle files, add the corresponding JSONs
under `op-core/nuts/bundles/` manually.

**This note came true in the v1.7.0 sync.** `build.rs` gained a second bundle and the build
died with `read .../lagoon_nut_bundle.json: No such file or directory` — *after* the whole
subtree merge appeared to succeed. Fix:

```bash
git show <upstream commit>:op-core/nuts/bundles/lagoon_nut_bundle.json \
  > op-core/nuts/bundles/lagoon_nut_bundle.json
```

⚠️ Note what `build.rs` does with it: the file is named `lagoon_*`, but the call is
`generate("interop", &lagoon_bundle, ..)` and the label `"interop"` is **deliberate** — the
generated `interop_nut_bundle()` fn and the embedded `fork_name: "Interop"` feed deposit
`source_hash` derivation and must match op-node, which keeps `bundleLabel = "interop"`.
**Do not "tidy" that label to `lagoon`.**

### 3.11 Intentionally absent — Mantle modules removed after review

Phase 1.5 dropped three blocks of vendored-but-unused Mantle code after the code review
in §B confirmed there are no real consumers. **Do not re-add these in a future sync.**

| Item | Origin | Why removed |
|---|---|---|
| `kona/crates/protocol/derive/src/sources/mantle_blob.rs` (817 lines) + `testdata/*.hex` | Mantle fork (originally vendored in Phase 1a) | Never constructed outside its own `#[cfg(test)]` module — every pipeline call site uses `EthereumDataSource::new_from_parts` with the upstream `BlobSource`, in Mantle's fork too. (Phase 1d's "wired" meant module registration, the re-export and the `reset()` plumbing, not construction.) **Deleting it is a deliberate scope decision, not dead-code cleanup — see §3.11.1.** |
| `kona/crates/protocol/derive/src/sources/mantle_ethereum.rs` (222 lines) | Mantle fork (originally vendored in Phase 1a) | Orphan code. Even in Mantle's own fork, every pipeline call site uses the upstream `EthereumDataSource`. The file was an unfinished refactor. |
| `DataAvailabilityProvider::reset()` trait method + `L1Retrieval::reset` calling `self.provider.reset()` | Phase 1d addition | Existed solely to clear `MantleBlobSource::mantle_format_failed` — moot after the above two deletions. The trait method was a default-empty no-op with no overriders. |
| `op-reth/` — entire subtree (bin + the 16 `reth-optimism-*` crates + examples) | optimism `rust/` subtree | **Phase 5.** The Mantle EL node moved to its own repo `mantle-xyz/reth@mantle-elysium`. No kona-side crate depends on it. **Sync note below.** |

#### 3.11.1 kona cannot derive pre-Arsia blocks — accepted boundary

Mantle submitted batches in a **non-standard joined-blob format before Arsia**. op-batcher gates
the two encoders on the fork (`op-batcher/batcher/driver.go:1015`):

```go
if !l.channelMgr.rollupCfg.IsMantleArsia(l.prevCurrentL1.Time) {
    blobs, err = data.MantleBlobs()   // pre-Arsia: frames RLP-encoded as one array, split across blobs
} else {
    blobs, err = data.Blobs()          // post-Arsia: standard, one frame per blob
}
```

op-node reads it back with `MantleBlobDataSource` (`op-node/rollup/derive/data_source.go:88`),
which is **format-probing, not fork-gated**: it tries the Mantle decode first and falls back to
standard per-blob decoding.

The Rust side has no such decoder — `EthereumDataSource` / `BlobSource` only understand the
standard format. Therefore:

- **kona derives post-Arsia blocks correctly.**
- **kona cannot derive any pre-Arsia block from L1.** It hits the first joined-blob batch and
  fails. This applies to syncing from genesis and to proving a pre-Arsia block.

**This gap is accepted.** kona-node serves the post-Arsia range; syncing Mantle from genesis
requires op-node or a snapshot. Two consequences to keep in view:

1. Retiring op-node removes the only client that can replay pre-Arsia history from L1. Confirm
   the snapshot path covers whatever depends on that range before the cutover.
2. Fault-proof coverage stops at Arsia. A dispute over a pre-Arsia block cannot be proven with
   the current Rust stack.

Reopening the decision means restoring `mantle_blob.rs` from `0484a132e5` and wiring it behind
the same `!IsMantleArsia` gate op-batcher uses. The file predates v1.7.0, so it needs adapting to
the current `BlobProvider` trait and `EthereumDataSource` shape.

> The earlier rationale — "post-Arsia all submissions use the standard blob format, so the
> fallback is obsolete by design" — is true of *new* blocks only. The pre-Arsia data is on L1
> permanently.

If a future Mantle hardfork brings non-standard blob submission back, build new code on
top of develop's `EthereumDataSource` / `BlobSource` instead of resurrecting these files.

**Upstream directories filtered out of the bridge (Strategy B — IN FORCE, jay 2026-09-16).**

`op-reth/`, `lokahi/` and `op-reth-test-engine/` are **no longer in this tree**. They are
excluded when the bridge split is assembled, so `git subtree pull` does not carry them and there
is nothing to re-delete after each sync.

| directory | why it is gone |
|---|---|
| `op-reth/` | 180 files / ~60k lines, never a workspace member, never compiled. Mantle's EL node is the separate `mantle-xyz/reth` repository. |
| `op-reth-test-engine/` | not a member either — it depends on the `reth-optimism-*` crates that stay out of the workspace with `op-reth`. |
| `lokahi/` | upstream's OP supernode skeleton; its README says it "currently builds a CLI that prints a greeting and exits". Targets interop, which Mantle does not use. Carried zero Mantle changes. |
| `kona/sp1/` | 64 files / 26,330 lines — upstream's SP1 zkVM integration. Its own README marks it "**Experimental** ... not yet recommended for production use", and its `super-aggregation` program "commits the public values consumed by `ZKDisputeGame`". Mantle submits validity proofs through `OPSuccinctL2OutputOracle`, ships no DisputeGame and does not use interop super-roots. |

`op-version/` is deliberately **kept**: `kona/bin/node` depends on it for version metadata.

**Nothing depended on `kona/sp1/`.** In-tree, the only `kona-sp1-*` references were sp1's own
crates referring to each other plus the five `[workspace.dependencies]` path declarations.
`mantle-xyz/op-succinct` — where Mantle's SP1 proving actually lives — depends on `kona-mpt`,
`kona-derive`, `kona-driver`, `kona-preimage`, `kona-executor`, `kona-proof`, `kona-client`,
`kona-host`, `kona-providers-alloy`, `kona-protocol`, `kona-registry`, `kona-genesis` and the
op-alloy family, and on **no `kona-sp1-*` crate at all**: it builds its own guest on kona's
derivation and execution crates. Note `kona/sp1/crates/proposer` (14,255 of the 26,330 lines) was
already excluded from the workspace for embedding OP's ZKDisputeGame ABI — the rest followed the
same logic.

Dropping `kona/sp1/` also let `sp1-sdk` go from `[workspace.dependencies]`, which removed **310
packages** from `Cargo.lock`. Measured consequences:

- `cargo deny check advisories` went from **9 errors to 6** — the three `rkyv` advisories
  (one use-after-free, two out-of-bounds) arrived through sp1-sdk.
- Five `deny.toml` ignores whose stated justification was "transitive via the SP1 dependency tree"
  stopped matching anything and were removed with it.
- `aws-smithy-json` left the lockfile — the crate behind the duplicate-major breakage recorded
  in §4.3.
- It retires the stale SP1 guest lockfile that made `just check-sp1-guest-lock` and
  `just check-sp1-guest-precompile-patches` fail once the justfile was parseable again.

**The cannon/MIPS64 FPVM prestate family was removed from `rust/justfile` at the same time**
(`build-kona-client-elfs`, `build-kona-prestates{,-auto}`, `generate-kona-prestates`,
`stage-kona-client-elfs`, `lint-kona-cannon`, `build-kona-reproducible-prestate`,
`output-kona-prestate-hash`, `reproducible-kona-prestate`, `clean-kona-prestates`,
`kona-prestate-variants`, plus the MIPS64 cross-toolchain variables — 369 lines). Mantle does not
run fault proofs, so nothing consumes the artifacts.

Verified before removing that the family had **no consumers outside `rust/justfile`**: the six
apparent external references were self-references within it, and the comment listing external
consumers was stale — `ops/prestate-reproducibility/build-prestates.sh` and
`.circleci/continue/rust-e2e.yml` do not exist, and `op-e2e/config/init.go` does not reference the
artifact names. `op-program/scripts/build-prestates.sh` is unaffected: it clones
`ethereum-optimism/optimism` into a temp directory and runs that tree's recipes, never this one.
The FPVM *crates* (`kona/crates/proof/std-fpvm`, `kona/bin/client`'s fpvm modules) are untouched —
only the prestate build tooling went.

**Why this stopped being merely cosmetic.** Carrying 60k unbuilt lines was an active hazard, not
just dead weight. Upstream's `.config/nextest.toml` filters on `binary(e2e_testsuite)`, defined in
`op-reth/crates/node`; nextest validates `binary(...)` predicates against the whole workspace
binary namespace and **hard-errors** when one matches nothing, so `cargo nextest run` exited 96
with zero tests executed. The directory also drew review effort away from code that matters — both
reviewers on the v1.7.0 round had to be told explicitly to scope it out.

**How the filtered split is produced.** The bridge commits are plain `git commit-tree` snapshots
of optimism's `rust/` tree (§1). To filter, drop the unwanted top-level entries before writing the
tree:

```bash
NEWTREE=$(git ls-tree <upstream-commit>:rust \
  | grep -vP '\t(lokahi|op-reth|op-reth-test-engine)$' \
  | git mktree)
git commit-tree "$NEWTREE" -p <previous-split> -m "rust: <message>"
```

Verify before pushing that the diff against the previous split is **only** those deletions:

```bash
git diff --name-only <prev-split>^{tree} "$NEWTREE" | cut -d/ -f1 | sort -u
```

For the 2026-09-16 filter this printed exactly `lokahi`, `op-reth`, `op-reth-test-engine` —
306 deletions, nothing else touched.

**Local follow-ups the filter does not do for you.** Removing the directories leaves references
behind; the sync is not complete until these are cleaned:

- `rust/Cargo.toml` — `lokahi/` was a workspace *member*, so `cargo metadata` fails until it is
  removed from `members`.
- `rust/justfile` — the `build-lokahi` / `build-lokahi-debug` recipes.
- `rust/.config/nextest.toml` — the three op-reth-only overrides (§4.3).

**Strategies considered and rejected**, kept because the reasoning still applies if anyone wants
to bring a directory back:

- **Strategy A — re-delete on every sync** (`git rm -r rust/op-reth` after each pull). Keeps the
  tree clean but repeats the work every sync, and a missed deletion is silent.
- **Strategy C — keep the directory, exclude it from the workspace** (in force 2026-09-10 to
  2026-09-16). Cheapest per sync, but it is what produced the nextest breakage above, and it left
  an `op-reth/crates/rpc/src/error.rs` **without** Mantle's `BvmEth(_) | TxL1CostOutOfRange` arm
  sitting in the tree as a trap for anyone who added those crates back.

An earlier revision recorded that deleting 183 files owned by another team from inside a
subtree-sync PR was the wrong place to make that call. That objection is answered by doing it in
the bridge instead: the EL team's repository is `mantle-xyz/reth`, and nothing they own is
affected by this tree no longer carrying a stale unbuilt copy.

## 4. Sync workflow

### 4.1 Pre-sync dry-run (optional but recommended)

```bash
cd mantle-v2
git checkout -b sync-dryrun-$(date +%Y%m%d)
git subtree pull --prefix=rust/ \
  https://github.com/mantle-xyz/optimism-rust-bridge.git main \
  --no-commit
git diff --name-only --diff-filter=U   # list conflicting files
git merge --abort                       # bail out — this was just a probe
```

### 4.2 Sync run

```bash
git checkout -b rust/sync-$(date +%Y%m)
git subtree pull --prefix=rust/ \
  https://github.com/mantle-xyz/optimism-rust-bridge.git main \
  -m "rust: subtree pull from bridge ($(date +%Y-%m))"

# Resolve each conflict — grep for [MANTLE] markers in conflicted files to make
# sure no Mantle change is dropped.
git diff --name-only --diff-filter=U | xargs grep -l "\[MANTLE\]"
```

### 4.3 Verification

> **The verification entry points were broken and have been repaired.** Before 2026-09-16, none
> of the `just` recipes below could run at all, and `cargo nextest` executed zero tests. Three
> separate causes, all introduced by taking upstream files verbatim:
>
> | file | problem | fix |
> |---|---|---|
> | `rust/justfile` | upstream v1.7.0 uses `[script('bash')]`, which `just` still treats as unstable; `mise.toml` pins just 1.37.0, so **every** recipe aborted during parsing | `set unstable` |
> | `rust/justfile` | `NIGHTLY` is derived by grepping `mise.toml` for a dated nightly that is not there, so it evaluated to `""` and `cargo +{{NIGHTLY}} fmt` became `cargo + fmt` | `NIGHTLY_TOOLCHAIN` falls back to `nightly`; note two call sites embed it as `export RUSTUP_TOOLCHAIN="…"` rather than `cargo +…`, and an earlier pass missed them |
> | `rust/.config/nextest.toml` | upstream's `binary(e2e_testsuite)` override refers to a binary in `op-reth/`, which is not a workspace member; nextest validates `binary(...)` against the whole workspace namespace and **hard-errors**, exit 96, zero tests run | the three op-reth-only overrides removed |
> | `mise.toml` | pinned `rust = "1.94"` while `rust/rust-toolchain.toml` and the workspace `rust-version` require 1.95; mise exports `RUSTUP_TOOLCHAIN`, which **overrides** `rust-toolchain.toml` | bumped to 1.95 |
>
> Predicates differ in how they fail: `binary(...)` and `binary_id(...)` hard-error when nothing
> matches, `test(...)` silently degrades to a no-op. That is why only one of the three overrides
> was actually fatal.
>
> **Repairing them exposed two pre-existing red lights** that had been invisible while the whole
> file was unparseable: `just check-sp1-guest-lock` and `just check-sp1-guest-precompile-patches`
> both fail because the SP1 guest `Cargo.lock` is stale, so `just lint-sp1-guest` is red. Fix with
> `just lock-sp1-guest` and commit `rust/kona/sp1/programs/Cargo.lock`. This is not a regression —
> it is what was already behind the door.
>
> There is still **no CI coverage for `rust/`** (`.github/` and `.circleci/` contain zero `cargo`
> invocations) and `core.hooksPath` points at a non-existent `.husky`, so every gate below is
> manual.

Run all of it. Each layer below caught defects the previous one could not see — see the note
after the block.

```bash
TOOLCHAIN=$(grep channel rust/rust-toolchain.toml | cut -d'"' -f2)
export RUSTUP_TOOLCHAIN=$TOOLCHAIN

# 1. Workspace-wide type check.
cargo check --workspace --manifest-path rust/Cargo.toml

# 2. Tests for the Mantle-touched crates. `cargo check` does NOT build test targets,
#    so this finds errors step 1 cannot.
cargo test --manifest-path rust/Cargo.toml \
  -p kona-genesis -p kona-protocol -p kona-derive -p kona-executor \
  -p alloy-op-evm -p alloy-op-hardforks -p op-alloy-consensus -p kona-hardforks

# 3. Formatting — nightly is mandatory: rustfmt.toml uses unstable options that stable
#    silently ignores.
cargo +nightly fmt --manifest-path rust/Cargo.toml --all -- --check

# 4. Lints, exactly as CI runs them.
cargo clippy --manifest-path rust/Cargo.toml \
  --workspace --all-features --all-targets -- -D warnings

# 5. no_std / riscv32 — the real fault-proof target.
cargo build --manifest-path rust/Cargo.toml --target riscv32imac-unknown-none-elf \
  -p kona-genesis -p kona-protocol -p kona-hardforks -p op-alloy-consensus --no-default-features
# (the MIPS/cannon half needs Docker: `just lint-cannon` from rust/kona)

# 5b. Rustdoc, exactly as `just lint-docs` runs it. NOT covered by any step above, and it has
#     its own failure mode: `/// [MANTLE]` in a *doc* comment is parsed as an intra-doc link and
#     fails under `-D warnings`. Write it as `/// `[MANTLE]`` (backticked); plain `// [MANTLE]`
#     comments are fine. This gate was failing before the v1.7.0 sync (11 pre-existing sites)
#     and is green now.
RUSTDOCFLAGS="-D warnings" cargo doc --manifest-path rust/Cargo.toml \
  --workspace --no-deps --document-private-items

# 6. Audit the [MANTLE] markers against this file's §3 registry.
grep -rn "\[MANTLE\]" rust/ --include="*.rs" --include="*.toml" | wc -l   # 208 after v1.7.0 (across 71 files)
```

**Why every layer matters** — the v1.7.0 sync passed each step and the *next* one still found
something new:

| Step | What only it caught |
|---|---|
| `check --workspace` | upstream API moves (`Predeploys` relocated, `from_payload_and_genesis` deleted) |
| `cargo test` | test-target-only compile errors; every executor fixture being undecodable; `SystemConfigUpdateKind` constants left on upstream's numbering |
| `clippy --all-features` | `kona-registry`'s `test_utils/` missing Mantle struct fields — it compiles only under the `test-utils` feature, which none of the earlier steps enabled |
| `RUSTDOCFLAGS="-D warnings" cargo doc` | every `/// [MANTLE]` doc comment — rustdoc reads the marker as an intra-doc link. Nothing else in the pipeline looks at doc comments |
| the live chain | that `sepolia-qa3` **cannot** validate the Skadi window, the fork alignment, the base-fee constant or the Elysium pin: all of its forks sit at genesis, so those code paths are never taken. Use a chain whose fork times differ (see §3.2c) |

⚠️ **A green `cargo check --workspace` can be luck.** `kona-executor`'s `pub mod test_utils;`
is not `cfg`-gated and imports optional deps; it built only because another member happened to
enable `kona-executor/test-utils` through feature unification. A narrower `-p` selection failed.

**Tests parked with `#[ignore]`, and why.** A full `cargo test --workspace` is green only
because of these. Each is an upstream assertion that Mantle's protocol deliberately breaks —
none is a Mantle defect, and none should be "fixed" by changing Mantle behaviour:

| Test | Reason |
|---|---|
| `hardforks::{ecotone,fjord,isthmus}::test_*_txs_encoded` (3) | The `.hex` vectors are OP's canonical upgrade-tx bytes. Mantle deposits encode one byte longer (an extra `0x80` for `eth_value`), and Mantle never emits the OP bundles anyway. |
| `alloy-op-evm` `structural_tests` operator-fee cases (4) | Upstream asserts the operator fee starts at Isthmus; Mantle starts it at Arsia. |
| ~~`kona-registry::l1::tests::test_get_l1_bpo_mainnet`~~ | **Un-ignored.** It was ignored because the L1 blob schedule was nulled in the registry; that pin moved into `L1BlockInfoTx::try_new` when Elysium was implemented, so the registry carries L1's real values again and upstream's assertion holds. See §3.2g. |
| ~~`hardforks::arsia::test_verify_arsia_*_deployment_code_hash` (3)~~ | **Un-ignored in the v1.7.0 sync.** They sat behind a bare `#[ignore] // TODO: fix this test` from `82fc1b98b`. Two always passed; the third failed only because its *expected* constant was stale. See §3.2f. |
| `op-alloy-rpc-types-engine` `*_non_canonical_encoding` (2) | Upstream tests new in v1.7.0 that do not hold against alloy-consensus 2.4.2: `Signed::fallback_decode` returns `UnexpectedType(0)` before the canonical re-encode check runs. **Both files are byte-identical to upstream** — no Mantle code on the failing path. Report upstream. |

**Failures that are the machine, not the code.** `cargo test --workspace --all-features` leaves
12–20 failures on a normal macOS dev box, the count varying run to run. Confirm each against this table before chasing it —
all were traced during the v1.7.0 sync and none touches Mantle code:

| Count | Tests | Root cause |
|---|---|---|
| 6 | `kona-providers-alloy` `beacon_client` / `buffered_l2_chain_provider` (`httpmock`: "No request has been received by the mock server") | reqwest reads the **macOS system proxy** (`scutil --proxy`) and does not honour its `ExceptionsList`, so even `127.0.0.1` mock-server traffic is forwarded. Unsetting `http_proxy` is **not** enough. Reproduce with `curl -x http://127.0.0.1:<proxy> http://127.0.0.1:<mock>/` → 502, vs 200 with `--noproxy '*'`. |
| 1 | `kona-sp1-host` `metrics::tests::ephemeral_listen_serves_on_the_port_it_reports` | Same proxy; the 502 is the proxy's own response, not the metrics server's. |
| 2 | `kona-disc` `driver::tests::test_online_discv5_driver_bootstrap_{mainnet,testnet}` | Needs real network. |
| 3 | `kona-engine` `state::core::test::test_chain_label_metrics::*` | `metrics::set_global_recorder` is process-global and succeeds once; whichever case runs first passes and the rest fail. Which ones fail **varies between runs** — that variance is the tell. Upstream test-isolation defect. |
| 0–8 | `kona-node-service` `actors::network::*`, `kona-mpt` `list_walker::test_online_*` | Appear only under the full parallel workspace run: the first bind real libp2p ports, the second hit a live RPC. **All pass when run with `-p <crate>`** — that isolation check is how you tell flakiness from a regression, and it is worth doing rather than assuming, since `actors/network/actor.rs` does call into Mantle-modified gossip code. |

⚠️ **`cargo clippy` without `--keep-going` under-reports.** Cargo stops scheduling new units
after the first failure, so a crate with pre-existing errors hides every lint in the crates
behind it. This produced two wrong numbers during the v1.7.0 sync (an "op-revm has 51 errors"
baseline that is really 93, and a "clippy clean outside op-revm" claim that was not). Always
pass `--keep-going`, and when comparing against a baseline, measure **both sides the same way**.

⚠️ **None of the above tells you whether a Mantle fix was silently overwritten.** For that, run
the git-history reconciliation in **§4.4** — it does not depend on this file being complete.

⚠️ **`[MANTLE]` counting is necessary but far from sufficient.** The most serious defect in the
v1.7.0 sync — the DA-footprint truncation order (§5.1) — changed no marker at all and was found
only because it happened to break the build. Read §5.1 and diff the hot spots by hand.

### 4.4 Mantle-delta reconciliation — "did the merge bury one of our fixes?"

§4.3 answers *does it build and pass*. This answers a different and harder question: **is every
change Mantle ever made still there?** Run it before landing.

Do **not** rely on the §3 registry or on `grep "[MANTLE]"` for this. Both are incomplete by
construction, and the v1.7.0 sync proved it twice: the DA-footprint truncation order (§5.1) was
silently replaced without changing a single marker, and `alloy-op-hardforks/src/lib.rs` carried
37 Mantle references with **no marker and no registry entry at all**. Go by git history instead.

**The method.** Mantle's work is, by definition, the delta between the upstream tree and ours.
Compute that delta at the old baseline and at the new one, and diff the two sets:

- `A` = files where **pre-sync** ours ≠ upstream-at-old-split → everything Mantle had touched
- `B` = files where **post-sync** ours ≠ upstream-at-new-split → what Mantle still touches
- `A \ B` = files that used to carry a Mantle delta and are now byte-identical to upstream —
  **each one is either a deliberate adoption of a better upstream version, or a buried fix**

```bash
#!/usr/bin/env bash
# Usage: run from the mantle-v2 root, mid-merge or after committing.
OLD_SPLIT=a6c46d8a…      # bridge split the previous sync came from
NEW_SPLIT=fa7ef15e…      # bridge split this sync came from
PRE=dev/mantle-v1.6.3    # the branch this sync started from
MERGED=$(git rev-parse "$(git write-tree)":rust)   # or <commit>:rust once committed

# `git rev-parse <ref>:<path>` ECHOES THE ARGUMENT BACK when the path is missing, so a bare
# `$(git rev-parse …)` silently yields a garbage "hash". Always guard with cat-file -e.
blob() { git cat-file -e "$1:$2" 2>/dev/null && git rev-parse "$1:$2" || echo ABSENT; }

git diff --name-only "$OLD_SPLIT" "$(git rev-parse $PRE:rust)" \
  | grep -vE '^op-reth/' | while read -r f; do
    pre=$(blob "$PRE" "rust/$f"); old=$(blob "$OLD_SPLIT" "$f")
    [ "$pre" = ABSENT ] || [ "$old" = ABSENT ] && continue   # add/delete, not a Mantle edit
    [ "$pre" = "$old" ] && continue                          # unchanged
    now=$(blob "$MERGED" "$f"); new=$(blob "$NEW_SPLIT" "$f")
    if   [ "$now" = ABSENT ]; then echo "DELETED|$f"
    elif [ "$now" = "$new" ]; then echo "OVERWRITTEN|$f"
    else                           echo "KEPT|$f"; fi
  done
```

**Triaging `OVERWRITTEN`.** Most hits are not Mantle's work at all: a previous round may have
pulled upstream content from an *intermediate* anchor, and v-next legitimately supersedes it.
Filter by comparing our pre-sync blob against that intermediate upstream tree (for the v1.7.0
round: `op-reth/v2.4.2` in a local optimism clone). If `pre == intermediate-upstream`, it was
never Mantle-authored — moving on to the newer upstream is correct. Whatever survives that
filter is the real review list, and it should be short enough to read by hand.

**Worked example — the v1.7.0 sync.** 100 files carried a Mantle delta against v1.5.1:

| Bucket | Count | Outcome |
|---|---|---|
| `KEPT` | 68 | Mantle delta intact |
| `OVERWRITTEN` | 31 | **30** were pure `op-reth/v2.4.2` content pulled in by the previous round — correctly superseded. **1** needed reading. |
| `DELETED` | 1 | `hardforks/src/interop.rs` |

The two that needed judgement, and why each was fine:

- `protocol/src/block.rs` — its only delta against upstream v1.5.1 was a mechanical
  compatibility shim in `from_payload_and_genesis`, self-labelled `⚠️ UNVERIFIED` and scoped to
  "only guarantees that it compiles". No Mantle protocol logic. Upstream v1.7.0 deleted the
  whole function and nothing in the tree calls it, so adopting upstream *removes* an unverified
  adapter — the "upstream has a better way" case.
- `hardforks/src/interop.rs` — deleted upstream, replaced by `lagoon.rs`. Its nine BVM_ETH-bearing
  `TxDeposit` literals were traced: seven moved into the NUT bundle (built by
  `op-alloy/.../nuts/mod.rs::to_deposit_transactions`, which is **byte-identical to pre-sync** and
  still fills the BVM_ETH fields) and two became `lagoon.rs` literals, patched by hand.

**Do not skip the trace step.** "The file was deleted upstream" is not by itself an answer —
follow the content to wherever it moved and confirm the Mantle behaviour came with it.

### 4.5 Land the sync

```bash
git push -u origin rust/sync-$(date +%Y%m)
# Open a PR and merge into the upgrade branch once review passes.
```

## 5. Conflict hot spots and time bombs

### 5.1 High-churn hot spots (likely to conflict every sync)

| Location | Why it churns | Post-sync checks |
|---|---|---|
| `op-alloy/.../deposit.rs` | TxDeposit is a frequently edited struct. | Verify BVM_ETH field positions and RLP order are preserved. |
| `alloy-op-evm/src/block/mod.rs` | The block executor is a high-churn area upstream. | Re-verify `deposit_receipt_version = None`, the commented-out `ensure_create2_deployer`, and the 2-arg `operator_fee_charge` call sites. |
| `kona/crates/protocol/genesis/src/rollup.rs` | RollupConfig and its predicates evolve with every hardfork. | Verify `mantle_hardforks` field + all `is_mantle_*` predicates survive; `Default::default` still routes `chain_op_config` to `MANTLE_BASE_FEE_CONFIG`. |
| `kona/crates/protocol/derive/src/attributes/stateful.rs` | Upgrade-tx emission gains new hardfork branches over time. | Re-check that the `if is_mantle() { ARSIA } else { OP path }` split is preserved. |
| `kona/crates/protocol/protocol/src/info/variant.rs` (`L1BlockInfoTx` enum + `try_new` picker + match arms) | Every new fork adds an enum variant and a picker branch; all matches must stay exhaustive. | If a new Mantle hardfork lands (Skadi / Limb / …), follow the per-hardfork checklist in §3.5 — missing any step reproduces the 2026-05 Arsia panic. Run `cargo check --workspace` after editing variant.rs to surface any non-exhaustive match site (e.g. `protocol/src/utils.rs`). |
| `kona/bin/client/src/fpvm_evm/precompiles/provider.rs` (the `OpSpecId` match arms) | Any new upstream hardfork variant breaks exhaustiveness. | If `cargo check` flags non-exhaustive matches, add the new variant to the appropriate arm. |
| `kona/crates/protocol/hardforks/src/*.rs` (TxDeposit literals) | Each new hardfork adds new upgrade-tx literals missing BVM_ETH fields. | Run the script in §6 on the newly added files. |
| `kona/crates/protocol/genesis/src/system/kind.rs` | Upstream may add new `SystemConfigUpdateKind` variants. | Variants must not collide with Mantle's `BaseFee = 4`; new ones go after `DaFootprintGasScalar = 8`. |
| `alloy-op-evm/src/block/mod.rs` → `jovian_da_footprint_estimation` | **Consensus.** Upstream keeps refactoring this into op-revm helpers. | Mantle computes `(size / 1e6) * scalar`; upstream's `tx_da_footprint` / `encoded_tx_da_footprint` compute `(size * scalar) / 1e6`. These truncate differently (size=1_500_000, scalar=2 → 2 here, 3 upstream) and Mantle has Jovian DA-footprint enforcement on. **The v1.7.0 merge silently adopted upstream's form because this body did not surface as a conflict.** Re-read the function after every sync. |
| `kona-registry::l1` Ethereum mainnet `osaka_time` / `bpo1..5_time` | Upstream bumps these; a merge may reintroduce the old Mantle `None` pin. | **Consensus.** They must carry L1's real values. The Arsia-era pin lives in `L1BlockInfoTx::try_new`, gated on Elysium (§3.2g); re-nulling them here makes activating Elysium a no-op and silently freezes blob pricing at Prague forever. |
| A new `[MANTLE]` marker in a **doc** comment (`///` or `//!`) | Rustdoc parses it as an intra-doc link. | Write it backticked — `` /// `[MANTLE]` ``. Plain `//` comments are unaffected. Caught only by `RUSTDOCFLAGS="-D warnings" cargo doc`, which is `just lint-docs` in CI. |
| Any new `is_canyon_active` / `is_ecotone_active` / `is_isthmus_active` call site | **Consensus + liveness.** On Mantle these are all false until Arsia. | Before adding one, check whether op-node's corresponding gate reads `IsOpFork(ts) \|\| IsMantleSkadi(ts)`. Seven such sites had to be fixed in the v1.7.0 sync (§3.2c). `grep -n 'IsMantleSkadi(' op-node/ --include='*.go'` is the authoritative list. |
| `op-alloy/.../reth_codec.rs` (`CompactTxDeposit`) | **On-disk format.** Field order and types fix the reth Compact bitfield layout. | Never reorder, never move a field after `input`, never change a type without re-running `mantle_compact_layout_tests`. Getting this wrong makes every existing deposit in a reth DB unreadable (the original op-reth-rpc41 sync failure). The `Bytes` field must stay last — `reth_codecs_derive` rejects any other position. |
| `kona/crates/protocol/protocol/src/deposits.rs` (`unmarshal_deposit_version1`) | **Consensus.** Must byte-match op-node's `unmarshalDepositVersion1`. | BVM_ETH fields read the full 32-byte word (§3.2b). Any "optimisation" back to a narrow integer reintroduces an attacker-triggerable consensus split. |
| `kona/crates/protocol/genesis/src/system/config.rs` (test `*_UPDATE_TYPE` consts) | They hard-code discriminants that Mantle shifted. | Mantle's `BaseFee = 4` pushes `Eip1559` to 5 and `OperatorFee` to 6. Upstream's constants (4, 5) address the wrong kinds and fail with `None`/`EIP1559DecodingError`. |
| `kona/crates/proof/executor/testdata/*.tar.gz` | Upstream ships OP-chain fixtures. | **Upstream fixtures cannot be used here at all**: their deposits carry the 8-field OP wire format, while Mantle's `TxDeposit` requires `eth_value`, so decoding overflows into `input`. Regenerate against a Mantle chain — see §3.6's `create_static_fixture` note. |
| `kona/crates/protocol/hardforks/src/{ecotone,fjord,isthmus}.rs` (`test_*_txs_encoded`) | The `.hex` vectors are OP's canonical upgrade-tx bytes. | Mantle's deposits encode one byte longer (an extra `0x80` for `eth_value`), so these can never match. They are `#[ignore]`d: Mantle never emits the OP bundles, and regenerating the vectors would only assert the encoder against itself. |

### 5.2 Time bombs (need active monitoring)

| Risk | Trigger | Mitigation |
|---|---|---|
| **revm major-version bump** | Upstream raises revm past v41. | Coordinate with `mantle-xyz/revm` to catch up before syncing, or defer the sync. The v1.7.0 sync was only safe because the anchors matched exactly (revm 41 / op-revm 20 / revm-inspectors 0.41 / alloy-evm 0.37 / alloy-op-evm 0.32) — **check this before starting, and stop if it does not hold.** |
| ~~**op-revm v19 → v20+ drift**~~ | — | **Resolved.** `op-revm` is now the in-tree path crate at v20; there is no external op-revm to drift from. |
| **Upstream deletes a feature Mantle code hides behind** | e.g. v1.7.0 removed kona-genesis's `revm` feature, which silently `#[cfg]`-ed out `spec_id` / `revm_spec_id` / `mantle_spec_id`. | The only signal was a `unexpected cfg condition value` **warning**. After a sync, grep for that warning and for `#[cfg(feature = ...)]` blocks whose feature no longer exists. |
| **New OpSpecId variant** | Upstream introduces a new hardfork. | `cargo check` will flag the non-exhaustive match; extend the relevant arm. |
| **Mantle's EIP-7825 exemption is dropped** | A sync rewrites a `CfgEnv<OpSpecId>` construction site, or upstream adds a new one. | revm is byte-identical to upstream and caps per-transaction gas at 16,777,216 from `SpecId::OSAKA` on, which Mantle's Limb and Arsia both map to. The exemption is `OpSpecId::tx_gas_limit_cap_override`, applied in `alloy-op-evm/src/env.rs::evm_env_for_op`. Every other construction site must reach it through that function — the proof executor does, via `evm_env_for_op_next_block`. A site that builds its own `CfgEnv` and omits the override rejects real Mantle transactions. |
| **mantle-xyz/revm becomes unreachable** | Network, credentials, or repo permission issues. | Temporarily vendor a copy of the patched branch under `mantle-v2/` and switch the patch entries from `git = ...` to `path = ...`. |
| **Mantle reverts to non-standard blob** | A future Mantle hardfork ships a custom blob format. | Build on top of the upstream `BlobSource`; do not resurrect `MantleBlobSource` as-is. Note kona already cannot derive the pre-Arsia range for this reason — that boundary is accepted and documented in §3.11.1, and a new custom format would extend it. |

## 6. Helper script — batch-patch new TxDeposit literals

Whenever upstream introduces a new hardfork upgrade-tx file (e.g. a new module under
`kona-hardforks/src/`), the new `TxDeposit { ... }` literals will not include the Mantle
BVM_ETH fields. Run this Python helper to add `eth_value: 0` and `eth_tx_value: None`
to every struct literal while leaving `impl ... for TxDeposit { ... }` blocks alone.

```python
#!/usr/bin/env python3
"""Inject eth_value/eth_tx_value into TxDeposit { ... } struct literals.
Skips positions that are not struct literals:
  - `impl SomeTrait for TxDeposit { ... }`  (preceded by `for`)
  - `fn f() -> TxDeposit { ... }`           (preceded by `->`; the `{` opens the fn body)
Usage: python3 this.py file1.rs file2.rs ...
"""
import sys

for path in sys.argv[1:]:
    with open(path) as fh:
        content = fh.read()
    out, i, edits = [], 0, 0
    while True:
        idx = content.find('TxDeposit {', i)
        if idx == -1:
            out.append(content[i:])
            break
        # Look back over whitespace at what precedes the type name.
        k = idx - 1
        while k >= 0 and content[k] in ' \t\n':
            k -= 1
        is_impl = k >= 2 and content[k-2:k+1] == 'for' and (k - 3 < 0 or content[k-3] in ' \t\n')
        # `-> TxDeposit {` is a return type; the brace opens the fn body, not a literal.
        is_ret = k >= 1 and content[k-1:k+1] == '->'
        if is_impl or is_ret:
            out.append(content[i:idx + len('TxDeposit {')])
            i = idx + len('TxDeposit {')
            continue
        # Brace-track to the matching close.
        depth, j = 1, idx + len('TxDeposit {')
        while j < len(content) and depth > 0:
            depth += {'{': 1, '}': -1}.get(content[j], 0)
            j += 1
        block = content[idx:j]
        if 'eth_value' in block:
            out.append(content[i:j])
            i = j
            continue
        nl = content.rfind('\n', idx, j - 1)
        close_line_start = nl + 1
        close_indent = content[close_line_start:j-1]
        field_indent = close_indent + '    '
        out.append(content[i:close_line_start])
        out.append(f"{field_indent}eth_value: 0,\n")
        out.append(f"{field_indent}eth_tx_value: None,\n")
        out.append(content[close_line_start:j])
        i = j
        edits += 1
    with open(path, 'w') as fh:
        fh.write(''.join(out))
    print(f"{path}: {edits} edits")
```

Example:

```bash
python3 /tmp/fix.py rust/kona/crates/protocol/hardforks/src/new_fork.rs
```

**Caveats**

- The script defaults the fields to `0` / `None`, which is correct for OP upgrade transactions
  (no BVM_ETH semantics). If a new hardfork introduces literals that *do* carry BVM_ETH values,
  patch them manually instead.
- **The `->` skip above was added in the v1.7.0 sync after the script corrupted `lagoon.rs`.**
  The earlier version only skipped `for TxDeposit {`, so on a file containing
  `fn set_feature_tx() -> TxDeposit {` it brace-tracked to the *function's* closing brace and
  inserted the fields **outside** the struct literal — a syntax error. `lagoon.rs` has two such
  functions. Always eyeball the diff, and confirm the file still parses.
- `grep -c 'TxDeposit {'` over-counts: it also matches return types and impl headers. To count
  real literals use `grep -E 'TxDeposit \{' f.rs | grep -vE '(->|for)\s*TxDeposit \{'`.
- A literal that ends in `..Default::default()` needs no patch; a naive
  "literal count vs `eth_value` count" check will report those as missing.

## 7. Maintaining this file

When you add, modify, or remove a Mantle change:

1. Add a `[MANTLE]` comment in the source explaining intent.
2. Register the change under the appropriate subsection of §3.
3. If the change is structural (new field, new method, signature change), evaluate
   whether §5.1 needs a new hot spot entry.
4. If you *remove* a Mantle module after concluding it is dead code, log it in §3.11
   with the rationale so the next sync engineer does not reintroduce it from the fork.
5. Reference this file in the commit message so future contributors can find their way back.
