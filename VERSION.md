# Mantle Core Component Versions

> Maintained per *Mantle Unified Version Management Specification V1.0* §3. Lives on the `main` branch of `mantle-xyz/mantle-v2`.
>
> **All core components of the same Mantle hardfork share `MAJOR.MINOR`; `PATCH` advances independently per component.**

## 1. Scope

**What this file is** — the single source of truth for *which component versions make up a given Mantle version*, and an index of every release in the current and previous round.

**What this file is not** —

- **Not a deployment dashboard.** It records *released* versions, not what each network (mainnet / sepolia / hoodi) currently runs. Live deployment state belongs to `mantle-config` and the K8s clusters, so this file never goes stale because of a canary or a rollback.
- **Not a roadmap.** Only versions that have actually been released appear here. Planning, scheduling and code-name assignment live in the specification.
- **Not a copy of the numbering rules.** Format and field semantics live in the specification (§4.1).
- **Not a copy of the release notes.** Each release gets **one table row** here, linking out. Never restate the prose: it is what makes a changelog unmaintainable, and a copy drifts out of sync with the original.

### Tracked components

| Component | Repository | Role |
|---|---|---|
| op-geth | [mantle-xyz/op-geth](https://github.com/mantle-xyz/op-geth) | Execution layer |
| op-node / op-batcher / op-gasoracle | [mantle-xyz/mantle-v2](https://github.com/mantle-xyz/mantle-v2) | Consensus layer, batching, gas oracle |
| op-reth | [mantle-xyz/reth](https://github.com/mantle-xyz/reth) | Alternative execution layer |
| op-succinct | [mantle-xyz/op-succinct](https://github.com/mantle-xyz/op-succinct) | Validity proving |
| mantle-da-indexer | [mantle-xyz/mantle-da-indexer](https://github.com/mantle-xyz/mantle-da-indexer) | DA indexing |
| lithosphere | [mantle-xyz/lithosphere](https://github.com/mantle-xyz/lithosphere) | Bridge indexing / API |

Adding or removing a component is a specification-level change and requires review by the version owners (§5.6).

### Where to look

| I want to know… | Go to |
|---|---|
| What builds make up the current round | §2 |
| Every release in the current round | §3.1 |
| What actually changed in one release | click the version number → GitHub Release |
| Which releases force a coordinated upgrade | §3, scan the **Consensus** column |
| What a past release depended on | §4.4 |
| Every release of a past round | `docs/versions/<mantle-vX.Y>.md` (§3.3) |

---

## 2. Current Versions

Current round: **`mantle-v1.6` (Elysium)**.

| Component | Unified version | Released | Previous version |
|---|---|---|---|
| op-geth | — | — | `v1.6.1` |
| mantle-v2 | — | — | `v1.6.2` |
| reth | — | — | `op-reth-v2.2.1-mantle-arsia.2` |
| op-succinct | [`mantle-v1.6.0`](https://github.com/mantle-xyz/op-succinct/releases/tag/mantle-v1.6.0) | 2026-09-02 | `v3.8.1-testnet-mantle-arsia.2` |
| mantle-da-indexer | — | — | `v1.6.0` |
| lithosphere | — | — | `v2.2.12` |

**Previous version** is the component's newest tag under its pre-unified naming. It is there because `—` alone would read as *"never shipped"*, which is false — every component above has a live release. It is also the input §4.3 derives the first unified `PATCH` from. **The column is transitional:** a component's entry is dropped once it has adopted unified numbering, and the whole column goes away once all six have.

**This table is exactly six rows, forever.** It always shows the newest release per component — one screen, no scrolling, no history. History lives in §3.

**It is never reset.** Rows roll onto a new `MINOR` one at a time, as each component ships on that round. During a round transition the table legitimately spans two rounds; that is why §5.7 checks same-`MINOR` alignment as a round-close gate rather than continuously.

---

## 3. Release Index

One row per released tag. Newest first. Click the version for the full release note.

**Upgrade**: `required` / `recommended` / `optional` — does an operator have to act.
**Consensus**: `none` or `⚠ yes` — whether the release changes consensus or state transition. `none` is a commitment, not a default.

### 3.1 `mantle-v1.6` — Elysium (current)

| Version | Component | Date | Upgrade | Consensus | Summary |
|---|---|---|---|---|---|
| [`mantle-v1.6.0`](https://github.com/mantle-xyz/op-succinct/releases/tag/mantle-v1.6.0) | op-succinct | 2026-09-02 | recommended | none | Merge upstream op-succinct v3.12.0; first release under unified numbering |

<!-- Insert new rows directly below the header, newest first. Template in §5.5. -->

### 3.2 `mantle-v1.5` — Arsia (previous)

Retained for one round after close, then archived. Releases cut before this specification took effect are not backfilled; see each repo's GitHub Releases.

### 3.3 Archived rounds

| Round | Code name | Releases |
|---|---|---|
| — | — | *(none archived yet)* |

Archive files are created by the round-transition PR (§5.3), never by a component release.

---

## 4. Versioning Rules

### 4.1 Numbering

Tag format and the semantics of `MAJOR` / `MINOR` / `PATCH` are defined by *Mantle Unified Version Management Specification V1.0* §3 and are **not restated here** — one definition, one place to edit. Two consequences matter for reading this file:

- **`PATCH` advances independently per component** within a round, so two components legitimately hold the same version number. The pair *(version, component)* is the unique key, not the version alone.
- **Only the version owners open a new `MINOR`** (§5.3). A release never infers its round; the round is an input.

This file deviates from the specification in exactly one respect — the tag prefix — for the reason below.

### 4.2 Why the `mantle-v` prefix

The specification writes `vMAJOR.MINOR.PATCH`. Tags here carry a `mantle-v` prefix because a bare number is a *version downgrade* for half the tracked repos: reth is on `v2.2.1` (upstream `v1.9.3-*`), op-succinct on `v3.8.1-*` (upstream `v4.x`), lithosphere on `v2.2.12` — and lithosphere is a Go project, where `v2 → v1` violates Go module versioning. The prefix opens a separate tag namespace, so Mantle numbering coexists with each repo's upstream line and Docker tags, semver tooling and Go modules all stay well-defined. `op-succinct` adopted it first, with `mantle-v1.6.0`.

### 4.3 PATCH starting point on first adoption

**A component's first unified version continues from the highest `PATCH` it has already consumed on that `MINOR` line, `+1`. A component with no release history on that `MINOR` starts at `.0`.**

A `PATCH` counts as consumed once any tag has used it — including a QA candidate that never shipped, which is why this must be computed from the repository's tags and not from this file. A number is never reused or walked back within a `MINOR`, so *which build is `1.6.2`* always has exactly one answer.

Implemented by `scripts/next-version.sh` (§5.8 step 2).

### 4.4 Dependency pinning

**A release must pin every inter-component and upstream dependency to an immutable ref — a tag or a commit sha, never a branch.** A branch is a moving target: the same tag rebuilt later resolves to different code, and the release stops being reproducible.

Not hypothetical. In `mantle-xyz/reth`:

| Tag | `mantle-v2` dependency | Reproducible |
|---|---|---|
| `op-reth-v2.2.1-mantle-arsia.1` | `branch = "mantle-elysium"` | no — rolling branch |
| `op-reth-v2.2.1-mantle-arsia.2` | `tag = "v1.6.0"` | yes |

Because manifests are themselves versioned, pinning also makes any past release's dependency set recoverable without this file having to record it:

```bash
git show <tag>:go.mod     | grep op-geth      # mantle-v2   -> op-geth
git show <tag>:Cargo.toml | grep mantle-v2    # reth / op-succinct -> mantle-v2
```

Upstream versions (go-ethereum, reth, op-succinct, SP1, …) are decoupled from Mantle numbering and are maintained solely in each repo's `go.mod` / `Cargo.toml`.

---

## 5. Maintenance

Two kinds of change land in this file, with different owners and different triggers:

| Change | Trigger | Owner | Frequency |
|---|---|---|---|
| **Release** (§5.2) | a component cuts a tag | that repo's owner | ~35× per round |
| **Round transition** (§5.3) | `MINOR` planning decision | version owners | once per round |

Keeping these separate is deliberate: a component owner shipping a patch must never have to perform an org-level migration as a side effect.

### 5.1 When to update

**Within one working day of any tracked component cutting a release tag**, open a PR against `mantle-v2` `main`. The release is not complete until that PR merges. Automating this propagation is out of scope for this file and tracked separately; until then it is a manual PR.

QA candidates (`-rc.N`) get no row. Note the `PATCH` they consumed in the **Summary** of the release that eventually ships.

### 5.2 Updating for a release

Two edits, both one line:

1. **§2** — update that component's row with the new version and date; clear its **Previous version** cell if this is its first unified release.
2. **§3.1** — insert one row at the top of the table.

That is the whole job. A release PR touches nothing else — no archiving, no section moves, no changes to other components' rows. Full procedure in §5.8.

### 5.3 Round transition

Closing a round and opening the next is **owned by the version owners** and lands as a **single separate PR** at `MINOR` planning time, before any component tags on the new `MINOR`. It is never triggered by a tag and never performed by a component owner as a side effect of a release.

That PR archives the round two back to `docs/versions/<mantle-vX.Y>.md`, shifts §3.1 into §3.2, opens an empty §3.1, updates the current-round line in §2, and checks the round-close gate (§5.7). The step-by-step procedure is deliberately left undefined until the first real archival, which is at least one full round away.

### 5.4 Worked example — one component releasing repeatedly

The common case: `op-succinct` ships four times during `mantle-v1.6` while other components ship on their own cadence. Each release touches **two sections in two different ways**:

| Section | On each release | Grows with releases? |
|---|---|---|
| §2 Current Versions | **overwrite** that component's row | No — always exactly six rows |
| §3.1 Release Index | **insert** a row at the top | Yes — this is the only section that accumulates |

After `mantle-v1.6.0` → `.1` → `.2` → `.3`, §2 shows only the newest:

```markdown
| op-succinct | [`mantle-v1.6.3`](…/releases/tag/mantle-v1.6.3) | 2026-11-04 | |
```

…while §3.1 holds all four, interleaved by date with everyone else's releases:

```markdown
| [`mantle-v1.6.3`](…) | op-succinct | 2026-11-04 | required    | ⚠ yes | Range circuit vkey change; provers must upgrade in lockstep |
| [`mantle-v1.6.2`](…) | op-geth     | 2026-10-20 | optional    | none  | … |
| [`mantle-v1.6.2`](…) | op-succinct | 2026-10-11 | recommended | none  | SP1 v6.4.0; ~18% cheaper range proofs |
| [`mantle-v1.6.1`](…) | op-succinct | 2026-09-24 | required    | none  | Fix proposer stall when the prover network returns FailedPrecondition |
| [`mantle-v1.6.0`](…) | op-succinct | 2026-09-02 | recommended | none  | Merge upstream op-succinct v3.12.0 |
```

`op-geth` and `op-succinct` both holding `mantle-v1.6.2` is expected — see §4.1. None of these four releases archives anything or opens a section; that happens once, later, in the round-transition PR (§5.3).

To read one component's history in the round: `grep op-succinct VERSION.md`, or open that repo's releases page. §3.1 stays sorted by date because its primary question is *what shipped recently across the stack*.

### 5.5 Row template

```markdown
| [`<tag>`](<release-note-url>) | <component> | <YYYY-MM-DD> | required\|recommended\|optional | none\|⚠ yes | <one line: what a reader needs to decide whether to care> |
```

The **Summary** is one line, grouped by user-visible change — not one phrase per PR. A feature split across 4 PRs is one clause. If a summary needs a second sentence, it belongs in the release note instead.

Release-note content rules are in specification §5; reference example: [op-reth-v2.2.1-mantle-arsia.2](https://github.com/mantle-xyz/reth/releases/tag/op-reth-v2.2.1-mantle-arsia.2).

### 5.6 Ownership

| Role | Responsibility |
|---|---|
| Releasing repo's owner | Cuts the tag, publishes the release note, opens the release PR (§5.2) |
| Version owners | Review release PRs; **own the round-transition PR (§5.3)** including archive-file creation; approve `MINOR` planning, code names, round closure, and changes to the tracked-component list |
| Release note final review | Sign-off on published release-note content per specification §5 |

A tag with no release note is an incomplete release, not a released version.

### 5.7 Invariants

Checkable on every PR:

1. Every unified version in §2 exists as a tag in its repository and has a published GitHub Release.
2. Every unified version in §2 appears as a row in §3.1 or §3.2.
3. `PATCH` numbers within a `MINOR` are never reused or decreased for a given component.
4. Every component listed in §1 appears exactly once in §2.
5. §3 contains at most two round sections; older rounds resolve to a file under `docs/versions/`.

Checked once, as a **round-close gate** in the §5.3 PR rather than continuously:

6. Every component in §2 has reached the closing round's `MAJOR.MINOR`. Mid-round the table legitimately spans two rounds, so this cannot be a per-PR check.

### 5.8 Release procedure

1. **Preconditions** — the `release/xxx` branch has QA approval and is already merged into `main` (specification §4); `main` is green.
2. **Version number** — `scripts/next-version.sh <repo> <MAJOR.MINOR>`, which applies §4.3 against the repository's tags. `MAJOR.MINOR` is an input, taken from §2's current-round line. If the script reports a collision, stop and escalate — do not pick the next free number.
3. **Tag** — annotated (`git tag -a`), on the release branch's merge commit on `main`; never a commit that is not on `main`. Push the tag, *then* create the GitHub Release from that existing tag, so the UI does not create a lightweight one instead.
4. **Release note** — publish before opening the VERSION.md PR; invariant 1 requires it to exist by the time the PR lands.
5. **VERSION.md PR** — apply §5.2. **Date** is the tag's creation date in UTC: `git log -1 --format=%cd --date=short <tag>`.

Grading the two judged fields:

| `Upgrade` | Use when |
|---|---|
| `required` | Consensus or state-transition change, a security fix, or the previous version no longer interoperates with the network |
| `recommended` | Meaningful bug fix, performance or compatibility improvement; safe to defer briefly |
| `optional` | No operational impact — internal refactor, docs, tests, tooling |

`Consensus` is `⚠ yes` if the change can alter block validity, state transition or proof verification: execution semantics, fork activation, precompile set, gas accounting, derivation rules, or a circuit / vkey change. Otherwise `none`.

#### Prohibitions

An automated agent running this procedure must **never**:

1. **Bump `MINOR`.** Only version owners open a round (§4.1, §5.3). If the next version would start one, stop and escalate.
2. **Assert `Consensus: none` on its own.** `none` is a commitment (§3). Propose the value; require explicit confirmation from the releasing repo's owner before merge.
3. **Write a version into §2 that is not a pushed tag with a published Release.** This is invariant 1; violating it corrupts the input to the next run.
4. **Perform any §5.3 round-transition step** as part of a release.
5. **Edit or backfill historical rows.** The index is append-only; a mistake is corrected by a new row, not by rewriting history.

---

<sub>Source: *Mantle Unified Version Management Specification V1.0*</sub>
