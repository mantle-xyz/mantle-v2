# Mantle Rust Subtree Patches

This file is the authoritative registry of every Mantle modification stacked on top of
the upstream optimism `rust/` subtree in `mantle-v2/rust/`. It is the primary reference
when synchronizing future upstream changes via `git subtree pull`.

**Whenever Mantle changes are added, modified, or removed, update this file.**

## 1. Current baseline

| Item | Value |
|---|---|
| Upstream tracking point | optimism `kona-client/v1.5.1` @ `fbbf9089` (2026-05-12) |
| Bridge tag | `rust-kona-client-v1.5.1` (= bridge split `a6c46d8a`) |
| Bridge branch (last sync source) | `sync-kona-client-v1.5.1` |
| Bridge repo | https://github.com/mantle-xyz/optimism-rust-bridge |
| `git subtree add` commit | `ba2cc4514` ("Add 'rust/' from commit '1ad181f05...'") |
| Last subtree-pull merge commit | `5a629e1a` ("rust: subtree pull from bridge (sync-kona-client-v1.5.1)") |
| Rust toolchain | 1.94 (see `rust/rust-toolchain.toml`) |

### Migration status

| Phase | Scope | Status |
|---|---|---|
| Pre-Phase | Bridge repo + subtree add + backup tags | ✅ |
| Phase 0 | wire mantle-elysium revm + op-alloy/alloy-op-evm adaptations | ✅ |
| Phase 1 (a–g) | kona Mantle protocol migration | ✅ |
| Phase 1.5 (B1–B3) | drop Mantle vendored-but-unused code (≈1066 lines removed) | ✅ |
| Phase 4 | redirect `alloy-evm` to `mantle-xyz/evm @ mantle-v0.34.0` | ✅ |
| Sync `rust-develop-20260511` → `rust-kona-client-v1.5.1` | 7 upstream commits, 38 files, 1 trivial conflict + 1 KARST fix | ✅ |
| Phase 2 | op-succinct upgrade (independent fork) | ⏸️ |
| Phase 3 | kona security patch follow-up | ⏸️ |

## 2. Architecture decisions

### 2.1 revm sourced from mantle-xyz/revm @ mantle-elysium

The `[patch.crates-io]` section in `rust/Cargo.toml` redirects every revm-family crate
to the `mantle-elysium` branch of `mantle-xyz/revm`:

```
revm, revm-bytecode, revm-context, revm-context-interface, revm-database,
revm-database-interface, revm-handler, revm-inspector, revm-interpreter,
revm-precompile, revm-primitives, revm-state, op-revm
```

`mantle-elysium` ships revm v38 plus Mantle protocol changes (ARSIA/JOVIAN hardforks,
BVM_ETH, token_ratio, DA footprint, Arsia fee validation). This avoids re-implementing
those changes inside `rust/op-revm/`; that subtree is excluded from the workspace.

`reth-revm` is a reth-internal wrapper (from `paradigmxyz/reth`); not a member of the
bluealloy revm family. Its internal `revm` dependency is still patched to mantle-elysium
via `[patch.crates-io]`, so the actual EVM execution path is 100% on mantle-elysium.

### 2.2 Version skew with mantle-elysium

| Dimension | What develop expects | What mantle-elysium provides | Reconciliation |
|---|---|---|---|
| revm major version | v38 | v38 ✅ | — |
| op-revm major version | v20 | v19 ⚠️ | Adapt Mantle consumers to v19 API |
| `OpSpecId` variants | Includes `KARST` | No `KARST`; includes `OSAKA` + `ARSIA` | Replace KARST references with OSAKA/JOVIAN/ARSIA fallbacks or comment them out |

### 2.3 alloy-evm sourced from mantle-xyz/evm @ mantle-v0.34.0

`Cargo.toml` redirects `alloy-evm` to the `mantle-v0.34.0` **branch** (not tag) of
`mantle-xyz/evm`. That branch contains **two commits**:

- `6022e02e` "chore: release 0.34.0" — upstream alloy-rs/evm v0.34.0 baseline.
- `b91d0077` "feat(evm): port Mantle token_ratio trait method onto v0.34.0" — adds
  `fn token_ratio(&self) -> U256` to the `Evm` trait (with `U256::ZERO` default on
  `EthEvm` and a delegating impl on `Either<L, R>`).

This is **structurally divergent from `mantle-xyz/evm@main`**. `main` is still on an
alloy-rs/evm v0.22.x lineage and carries Mantle op-side patches in
`crates/evm/src/op/tx.rs` (BVM_ETH `eth_value` / `eth_tx_value` propagation through
`TxEnv` / `OpTransaction`). **v0.34.0 deleted both `crates/op-evm/` and
`crates/evm/src/op/`** — so those Mantle patches have no landing zone on
`mantle-v0.34.0` and are not ported. This is the explicit decision documented in the
`b91d0077` commit message.

The Mantle op-side functionality is instead provided by:

- `op-revm` from `mantle-xyz/revm@mantle-elysium` (BVM_ETH execution, token_ratio
  computation, ARSIA / JOVIAN protocol changes).
- The locally-vendored `alloy-op-evm/` (this repo) per §3.7 — hand-ported Mantle
  protocol changes that target the v0.32-era alloy-rs/evm `OpTxTr` / `OpBlockExecutor`
  API.

**Audit note (2026-05):** an audit comparing local against `mantle-xyz/evm@main`
crates/evm/ surfaced no real gaps. Every Mantle change on main's `crates/evm/`
falls into one of two buckets:

1. `token_ratio` trait method (evm.rs / eth/mod.rs / either.rs) — already ported by
   `b91d0077`.
2. Edits to `crates/evm/src/op/tx.rs` (BVM_ETH plumbing) — target a path that
   v0.34.0 deleted; covered locally by `alloy-op-evm/src/tx.rs` per §3.7.

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
| `Cargo.toml` | `[patch.crates-io]` redirects all 13 revm-family crates to `mantle-xyz/revm@mantle-elysium`. |
| `Cargo.toml` | Workspace `members` drops `"op-revm/"`; `exclude = ["op-revm"]` keeps the orphan subtree out of the build. |

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
| `op-alloy/crates/consensus/src/transaction/deposit.rs` | Add `eth_value: u128` and `eth_tx_value: Option<u128>` fields with their serde attrs. |
| same | Update `rlp_decode_fields` / `rlp_encode_fields` / `rlp_encoded_fields_length` / `size()` to include the new fields. |
| same | Switch `rlp_decode` to a `split_at(header.payload_length)` strict-boundary form (port of commit `4873ed6` from V230 — not `6637567`, which is the *block* decoder fix). |
| same | Add the `decode_optional_u128_from_rlp` helper for the trailing optional u128. Strict form per `498abec`: returns `Err` on present-but-malformed input rather than swallowing decode errors as `None`. |
| same (tests) | Add 0/None for both fields in 8 in-file `TxDeposit { ... }` literals; add `_` ignores in 1 alloy-compat destructure. |
| same (tests) | Add `test_eth_value_zero`, `test_eth_value_and_eth_tx_value_both_zero`, `test_eth_value_max`, `test_eip2718_encode_decode_with_new_fields`, `test_eip2718_encode_decode_with_eth_tx_value_none`, `test_decode_optional_u128_boundary_values` (port of `3dc9696`). The companion implementation changes in `3dc9696` (lenient `decode_optional_u128_from_rlp` mid-form + `size()` switch from `Option<u128>` to `u128` for `eth_value`) are already present locally — the decode helper was later strictened by `498abec`. |
| same (tests) | Add `test_rlp_decode_fields_rejects_malformed_present_eth_tx_value` and `test_decode_2718_rejects_malformed_present_eth_tx_value` (port of `498abec`'s two new test functions). |
| `op-alloy/crates/consensus/src/transaction/envelope.rs` | Add 0/None in 2 test `TxDeposit` literals. |
| `op-alloy/crates/consensus/src/reth_codec.rs` | `From<CompactTxDeposit>` fills 0/None for the new fields. **TODO**: `CompactTxDeposit` itself does not carry the new fields, so reth Compact round-trips drop BVM_ETH data. |
| `op-alloy/crates/consensus/src/transaction/deposit.rs` (`bincode_compat`) | Same situation as `reth_codec`. **TODO**. |
| `op-alloy/crates/consensus/src/nuts/mod.rs` | NutBundle upgrade-tx literal fills 0/None. |
| `op-alloy/crates/rpc-types/src/transaction/request.rs` | OpTransactionRequest destructure adds `_` ignores for the new fields. |
| `op-alloy/crates/rpc-types/src/receipt.rs` | `L1BlockInfo` gains `pub token_ratio: Option<u128>` at the Jovian-class hardfork section (port of `57b9c10`). `parse_rpc_receipt` test JSON extended with `"tokenRatio": "0x1"` for round-trip coverage. Without this field, RPC clients would not see Mantle's eth/MNT ratio in receipts. |

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
| `kona/crates/protocol/genesis/src/rollup.rs` | `RollupConfig` gains `pub mantle_hardforks: MantleHardForkConfig` field. New methods: `is_mantle`, `revm_spec_id`, `is_mantle_skadi_active`, `is_mantle_limb_active`, `is_mantle_arsia_active`, `is_first_mantle_arsia_block`. `Default::default` switches `chain_op_config` to `MANTLE_BASE_FEE_CONFIG`. Existing `is_jovian_active` etc. get Mantle gating. New helper `default_mantle_base_fee_config` for serde defaulting. Comment out the `is_karst_active → OpSpecId::KARST` arm in `spec_id` — mantle-elysium's op-revm v19 has no KARST variant (post-sync addition, see §2.2 / §5.2). |
| `kona/crates/protocol/genesis/src/chain/mantle_hardfork.rs` | New file. `MantleHardForkConfig` struct with the Mantle upgrade timestamps. |
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
| `kona/crates/protocol/protocol/src/info/arsia.rs` | **New file (2026-05)**. `L1BlockInfoArsia` is a thin nested wrapper around `L1BlockInfoJovian` with selector `setL1BlockValuesArsia()` = `0x49e72383`. Payload layout is byte-identical to Jovian; only the selector differs (verified by reverse-engineering `arsia_l1_block.hex` dispatcher in `kona-hardforks`). Round-trip + dispatcher + picker tests inline. |
| `kona/crates/protocol/protocol/src/info/{mod.rs, errors.rs}` | `mod arsia;` + `pub use` re-export; inheritance chain comment updated to `... < L1BlockInfoJovian < L1BlockInfoArsia`. `DecodeError::InvalidArsiaLength` added. (`DecodeError::InvalidInteropLength` is a pre-existing legacy variant that has no corresponding decoder — see note below.) |
| `kona/crates/protocol/protocol/src/utils.rs` | `to_system_config`'s `match L1BlockInfoTx` extended for `Arsia` variant. |
| `kona/crates/protocol/protocol/src/info/jovian.rs` | `L1BlockInfoJovianBaseFields` decorated with `#[delegatable_trait]` so `L1BlockInfoArsia` can `ambassador::Delegate` the trait into its embedded Jovian base. |
| `kona/crates/protocol/hardforks/src/{ecotone,fjord,interop,isthmus,jovian}.rs` | 31 OP hardfork upgrade-tx literals filled `eth_value: 0, eth_tx_value: None` via the script in §6. |
| `kona/crates/protocol/protocol/src/{batch/single.rs, utils.rs}` test fixtures | Added Mantle `eth_value: 0, eth_tx_value: None` to one `TxDeposit { ... }` literal and `base_fee: None` to three `SystemConfig { ... }` literals (2026-05; previously the kona-protocol lib test target did not compile against the Mantle field additions). |
| `kona/crates/protocol/registry/src/l1/mod.rs` | `default_blob_schedule()` excludes Osaka / BPO1 / BPO2 entries (already commented locally — kept that state). **2026-05 update**: `mainnet()` sets `osaka_time` / `bpo1_time` … `bpo5_time` to `None` instead of `EthereumHardfork::Osaka/Bpo1-5.mainnet_activation_timestamp()`, pinning Ethereum L1 blob-fee schedule to Prague behaviour on Mantle. Mirrors `mantle-xyz/kona@72a20ab9` ("Blob fee parameters #26", 2026-04-24, authored by QianXing). **Sepolia / Holesky `L1Config` unchanged** — `mantle-xyz/kona` only forced Mantle-mainnet onto Prague. **Known follow-up**: the kona-registry `test_get_l1_bpo_*` tests (originally added by upstream `59d420fc`) are now stale against this disabled schedule and will fail to compile/run; this matches mantle-xyz/kona@main's own state and is tracked as separate cleanup work. |

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
| `kona/crates/proof/executor/src/builder/env.rs` | `evm_cfg_env` calls `self.config.revm_spec_id(timestamp)` (Mantle-aware) instead of `spec_id(timestamp)`. `spec_id` continues to drive kona protocol-layer feature checks; `revm_spec_id` is the executor-facing variant that gates Jovian/Holocene/Granite behind `mantle_arsia` on Mantle chains. |
| `kona/crates/proof/executor/src/test_utils.rs` | Test infrastructure additions (`alloy_chains::Chain`, `reqwest::Url` imports; `StatelessL2Builder::new` takes `&rollup_config`; placeholder `Ok(true)` return for `create_static_fixture`). |
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
| `alloy-op-evm/src/env.rs` | Comments out the `is_karst_active_at_timestamp => KARST` hook (no KARST on mantle-elysium). |
| same (tests) | Comments out the `OpSpecId::KARST` `test_case`. |
| `alloy-op-evm/src/block/mod.rs` | `deposit_receipt_version = None` (corresponds to commit 760129f). |
| same | Comments out the `ensure_create2_deployer(...)` call and its `use canyon::ensure_create2_deployer;` import (origin: 707922af; re-applied in 5f383c5 against v0.25.2 baseline). |
| same | Drops the `spec_id` argument from `operator_fee_charge` in two call sites to match mantle-elysium's older 2-arg signature. |
| `alloy-op-evm/src/block/canyon.rs` | Adds `#![allow(dead_code)]` because the function is now unreachable (origin: 707922af). |
| same (Jovian DA-footprint enforcement) | Kept **active** locally (`block/mod.rs` `jovian_da_footprint_estimation` + the pre-execute check + post-execute accumulation). This matches the post-`9fe2c85` Mantle stance; `707922af`'s transient disable is therefore not ported. |

### 3.8 kona-client fpvm — adapts to mantle-elysium op-revm v19

| File | Change |
|---|---|
| `kona/bin/client/src/fpvm_evm/precompiles/provider.rs` | Drop the `karst` import. Collapse the `KARST` match arms into `JOVIAN \| OSAKA \| ARSIA \| INTEROP` and route them to `jovian()` / `accelerated_jovian` so the match remains exhaustive. |

### 3.9 op-reth/rpc — handles the Mantle-specific OpTransactionError variants

| File | Change |
|---|---|
| `op-reth/crates/rpc/src/error.rs` | `TryFrom<OpTxError>` adds an arm for `BvmEth(_) \| TxL1CostOutOfRange`. Placeholder maps to `MissingEnvelopedTx`. **TODO**: extend `OpInvalidTransactionError` with proper variants and dedicated RPC error codes. |

### 3.10 op-core — vendored data (outside the rust/ subtree)

| File | Change |
|---|---|
| `<mantle-v2 root>/op-core/nuts/bundles/karst_nut_bundle.json` | Copied verbatim from optimism develop so `kona-hardforks/build.rs` can find it via its ancestor walk. |

**Note**: this file is *outside* `rust/`, so `git subtree pull` will not sync it. If a
future upstream build.rs looks for additional bundle files, add the corresponding JSONs
under `op-core/nuts/bundles/` manually.

### 3.11 Intentionally absent — Mantle modules removed after review

Phase 1.5 dropped three blocks of vendored-but-unused Mantle code after the code review
in §B confirmed there are no real consumers. **Do not re-add these in a future sync.**

| Item | Origin | Why removed |
|---|---|---|
| `kona/crates/protocol/derive/src/sources/mantle_blob.rs` (817 lines) + `testdata/*.hex` | Mantle fork (originally vendored in Phase 1a) | Orphan code. Mantle's own fork constructs `EthereumDataSource::new_from_parts` everywhere — `MantleBlobSource` was never wired into any pipeline. The `mantle_format_failed` fallback is obsolete because post-Arsia all submissions use the standard blob format. |
| `kona/crates/protocol/derive/src/sources/mantle_ethereum.rs` (222 lines) | Mantle fork (originally vendored in Phase 1a) | Orphan code. Even in Mantle's own fork, every pipeline call site uses the upstream `EthereumDataSource`. The file was an unfinished refactor. |
| `DataAvailabilityProvider::reset()` trait method + `L1Retrieval::reset` calling `self.provider.reset()` | Phase 1d addition | Existed solely to clear `MantleBlobSource::mantle_format_failed` — moot after the above two deletions. The trait method was a default-empty no-op with no overriders. |

If a future Mantle hardfork brings non-standard blob submission back, build new code on
top of develop's `EthereumDataSource` / `BlobSource` instead of resurrecting these files.

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

```bash
TOOLCHAIN=$(grep channel rust/rust-toolchain.toml | cut -d'"' -f2)

# 1. Workspace-wide type check.
RUSTUP_TOOLCHAIN=$TOOLCHAIN cargo check --workspace \
  --manifest-path rust/Cargo.toml

# 2. Full build (including tests) for the Mantle-touched crates.
RUSTUP_TOOLCHAIN=$TOOLCHAIN cargo build --tests \
  --manifest-path rust/Cargo.toml \
  -p op-alloy -p op-alloy-consensus -p op-alloy-network \
  -p op-alloy-provider -p op-alloy-rpc-jsonrpsee \
  -p op-alloy-rpc-types -p op-alloy-rpc-types-engine \
  -p alloy-op-evm

# 3. Audit the [MANTLE] markers against this file's §3 registry.
grep -rn "\[MANTLE\]" rust/ --include="*.rs" --include="*.toml" | wc -l
```

### 4.4 Land the sync

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

### 5.2 Time bombs (need active monitoring)

| Risk | Trigger | Mitigation |
|---|---|---|
| **revm major-version bump** | Upstream raises revm to v39+. | Coordinate with mantle-xyz/revm to catch up before syncing, or defer the sync. |
| **op-revm v19 → v20+ drift widens** | mantle-elysium does not track upstream op-revm. | Let `cargo check` surface the differences and adapt site-by-site (potentially extending the `OpTxTr` impl, adjusting signatures, etc.). |
| **New OpSpecId variant** | Upstream introduces a new hardfork. | `cargo check` will flag the non-exhaustive match; extend the relevant arm. |
| **mantle-xyz/revm becomes unreachable** | Network, credentials, or repo permission issues. | Temporarily vendor a copy of mantle-elysium under `mantle-v2/` and switch the patch entries from `git = ...` to `path = ...`. |
| **Mantle reverts to non-standard blob** | A future Mantle hardfork ships a custom blob format. | Build on top of the upstream `BlobSource`; do not resurrect `MantleBlobSource` (see §3.11 rationale). |

## 6. Helper script — batch-patch new TxDeposit literals

Whenever upstream introduces a new hardfork upgrade-tx file (e.g. a new module under
`kona-hardforks/src/`), the new `TxDeposit { ... }` literals will not include the Mantle
BVM_ETH fields. Run this Python helper to add `eth_value: 0` and `eth_tx_value: None`
to every struct literal while leaving `impl ... for TxDeposit { ... }` blocks alone.

```python
#!/usr/bin/env python3
"""Inject eth_value/eth_tx_value into TxDeposit { ... } struct literals.
Skips `impl SomeTrait for TxDeposit { ... }` impl blocks (preceded by `for `).
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
        # Skip impl blocks: look back over whitespace for the keyword `for`.
        k = idx - 1
        while k >= 0 and content[k] in ' \t\n':
            k -= 1
        if k >= 2 and content[k-2:k+1] == 'for' and (k - 3 < 0 or content[k-3] in ' \t\n'):
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

**Caveat**: the script defaults the fields to `0` / `None`, which is correct for OP
upgrade transactions (no BVM_ETH semantics). If a new hardfork introduces literals that
*do* carry BVM_ETH values, patch them manually instead.

## 7. Maintaining this file

When you add, modify, or remove a Mantle change:

1. Add a `[MANTLE]` comment in the source explaining intent.
2. Register the change under the appropriate subsection of §3.
3. If the change is structural (new field, new method, signature change), evaluate
   whether §5.1 needs a new hot spot entry.
4. If you *remove* a Mantle module after concluding it is dead code, log it in §3.11
   with the rationale so the next sync engineer does not reintroduce it from the fork.
5. Reference this file in the commit message so future contributors can find their way back.
