# Mantle Core Component Versions

> Maintained per *Mantle Unified Version Management Specification V1.0* §3. Lives on the `main` branch of `mantle-xyz/mantle-v2`.
>
> **All core components of the same Mantle hardfork share `MAJOR.MINOR`; `PATCH` advances independently per component.**

## 1. Scope

**What this file is** — the single source of truth for *which component versions make up a given Mantle version*, and an index of every release in the current and previous round.

**What this file is not** —

- **Not a deployment dashboard.** It records *released* versions, not what each network (mainnet / sepolia / hoodi) currently runs. Live deployment state belongs to `mantle-config` and the K8s clusters, so this file never goes stale because of a canary or a rollback.
- **Not a roadmap.** Only versions that have actually been released appear here. Planning, scheduling and code-name assignment live in the specification.
- **Not an upstream dependency manifest.** Upstream versions (go-ethereum, reth, op-succinct, SP1, …) are decoupled from Mantle versions and maintained by each repo's own `go.mod` / `Cargo.toml`.
- **Not a copy of the release notes.** Each release gets **one table row** here. The prose — what changed, why, how to upgrade — lives in the repo's GitHub Release and is linked from the version number. Never duplicate it into this file: it is what makes a changelog unmaintainable, and a copy drifts out of sync with the original.

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
| Every release of a past round | `docs/versions/<mantle-vX.Y>.md` (§3.3) |

---

## 2. Current Versions

The newest released version of each component, under unified numbering. Current round: **`mantle-v1.6` (Elysium)**.

| Component | Version | Released |
|---|---|---|
| op-geth | — | — |
| mantle-v2 | — | — |
| reth | — | — |
| op-succinct | [`mantle-v1.6.0`](https://github.com/mantle-xyz/op-succinct/releases/tag/mantle-v1.6.0) | 2026-09-02 |
| mantle-da-indexer | — | — |
| lithosphere | — | — |

`—` means the component has not yet cut a release under unified numbering; its first one is derived in §4.4.

**This table is exactly six rows, forever.** It always shows the newest release per component — one screen, no scrolling, no history. History lives in §3.

**It is never reset.** Rows roll onto a new `MINOR` one at a time, as each component ships on that round. During a round transition the table legitimately spans two rounds; that is what convergence looks like in progress, and it is why §5.8 checks same-`MINOR` alignment as a round-close gate rather than continuously.

---

## 3. Release Index

One row per released tag. Newest first. Click the version for the full release note.

**Upgrade**: `required` / `recommended` / `optional` — does an operator have to act.
**Consensus**: `none` or `⚠ yes` — whether the release changes consensus or state transition. `none` is a commitment, not a default.

### 3.1 `mantle-v1.6` — Elysium (current)

| Version | Component | Date | Upgrade | Consensus | Summary |
|---|---|---|---|---|---|
| [`mantle-v1.6.0`](https://github.com/mantle-xyz/op-succinct/releases/tag/mantle-v1.6.0) | op-succinct | 2026-09-02 | recommended | none | Merge upstream op-succinct v3.12.0; first release under unified numbering |

<!-- Add new rows directly below the header, newest first. Template in §5.5. -->

### 3.2 `mantle-v1.5` — Arsia (previous)

Retained for one round after close, then archived. Releases cut before this specification took effect are not backfilled; see each repo's GitHub Releases.

### 3.3 Archived rounds

| Round | Code name | Releases |
|---|---|---|
| — | — | *(none archived yet)* |

Archive files are created by the round-transition PR (§5.3), never by a component release. Each is the round's §3 table moved verbatim under a minimal header:

```markdown
# mantle-v1.6 — Elysium

Archived from VERSION.md §3 on <date>. Closed <date>.

| Version | Component | Date | Upgrade | Consensus | Summary |
|---|---|---|---|---|---|
| …the table, verbatim…
```

---

## 4. Versioning Rules

### 4.1 Format

```
mantle-vMAJOR.MINOR.PATCH          release
mantle-vMAJOR.MINOR.PATCH-rc.N     QA candidate
```

### 4.2 Field semantics

| Field | Changes when | Notes |
|---|---|---|
| `MAJOR` | Fundamental rearchitecture of the node / protocol stack | Untouched by routine network upgrades; stays at `1` for the foreseeable future |
| `MINOR` | Deliberately planned `+1`, roughly every 2–4 months | **Every tracked component in a round shares the same `MAJOR.MINOR`.** A round containing a hardfork gets a network code name; a round without one does not. A component with no real change may cut an empty version to stay aligned. Only the version owners may open a new `MINOR` (§5.3) |
| `PATCH` | Grows **independently** per component within the current `MINOR` | Soft upgrades / soft forks, bugs, security, performance, RPC compatibility, circuit optimizations. **Never requires other repos to cut a matching version** |

### 4.3 Why the `mantle-` prefix

Half the tracked repositories carry upstream version lines that a bare `v1.6.x` would walk backwards:

| Repository | Upstream / legacy line | Bare `v1.6.x` |
|---|---|---|
| op-geth, mantle-v2, mantle-da-indexer | already on `v1.x` | continues cleanly |
| reth | `v2.2.1`, upstream `v1.9.3-*` | downgrade, and collides visually with upstream reth's own `v1.x` |
| op-succinct | upstream `v4.x`, legacy `v3.8.1-*` | downgrade |
| lithosphere | `v2.2.x` | downgrade; as a Go project `v2 → v1` violates Go module versioning |

The `mantle-v` prefix opens a separate tag namespace, sidestepping monotonicity entirely while coexisting with each repo's upstream tag line. Docker tags, semver tooling and Go modules all stay well-defined.

### 4.4 PATCH starting point on first adoption

**A component's first unified version continues from the highest `PATCH` it has already consumed on that `MINOR` line, `+1`. A component with no release history on that `MINOR` starts at `.0`.**

A `PATCH` counts as consumed once any tag has used it — including a QA candidate that never shipped, which is why this must be computed from the repository's tags, not from this file. A number is never reused or walked back within a `MINOR`, so *which build is `1.6.2`* always has exactly one answer.

---

## 5. Maintenance

Two kinds of change land in this file, with different owners and different triggers:

| Change | Trigger | Owner | Frequency |
|---|---|---|---|
| **Release** (§5.2) | a component cuts a tag | that repo's owner | ~35× per round |
| **Round transition** (§5.3) | `MINOR` planning decision | version owners | once per round |

Keeping these separate is deliberate: a component owner shipping a patch must never have to perform an org-level migration as a side effect.

### 5.1 When to update

**Within one working day of any tracked component cutting a release tag**, open a PR against `mantle-v2` `main`. The release is not complete until that PR merges.

QA candidates (`-rc.N`) get no row. Note the `PATCH` they consumed in the **Summary** of the release that eventually ships.

### 5.2 Updating for a release

Two edits, both one line:

1. **§2** — update that component's row with the new version and date.
2. **§3.1** — insert one row at the top of the table.

That is the whole job. A release PR touches nothing else — no archiving, no section moves, no changes to other components' rows.

For the full end-to-end procedure — computing the version number, cutting the tag, classifying the fields — see §5.9.

### 5.3 Round transition

A round transition is **not** triggered by a tag, and is **never** performed by a component owner as a side effect of a release. It is a single, separately reviewed PR owned by the version owners.

**Timing:** it lands at `MINOR` planning time — *before* any component cuts a tag on the new `MINOR`. The specification plans each round 2–4 months ahead with agreed scope, so this PR is part of opening that round, not an afterthought when someone happens to tag first.

**One PR does all of it:**

1. Move §3.2 — the round two back — verbatim into `docs/versions/<mantle-vX.Y>.md` using the header in §3.3, and add its row to §3.3.
2. Promote the closing round from §3.1 into §3.2.
3. Open an empty §3.1 for the new round, with its code name if it has one.
4. Update the "Current round" line in §2.
5. Verify the round-close gate: every component in §2 reached the closing round's `MAJOR.MINOR`. A component that shipped nothing must have cut an empty version (§4.2). Record any accepted exception in the archive file.

**§2 itself is not touched** beyond step 4. Its rows keep showing each component's newest release and roll onto the new `MINOR` individually as those releases land.

### 5.4 Worked example — one component releasing repeatedly

The common case: `op-succinct` ships four times during `mantle-v1.6` while other components ship on their own cadence. Each release touches **two sections in two different ways**:

| Section | On each release | Grows with releases? |
|---|---|---|
| §2 Current Versions | **overwrite** that component's row | No — always exactly six rows |
| §3.1 Release Index | **append** one row | Yes — this is the only section that accumulates |

After `mantle-v1.6.0` → `.1` → `.2` → `.3`, §2 shows only the newest:

```markdown
| op-succinct | [`mantle-v1.6.3`](…/releases/tag/mantle-v1.6.3) | 2026-11-04 |
```

…while §3.1 holds all four, interleaved by date with everyone else's releases:

```markdown
| [`mantle-v1.6.3`](…) | op-succinct | 2026-11-04 | required    | ⚠ yes | Range circuit vkey change; provers must upgrade in lockstep |
| [`mantle-v1.6.2`](…) | op-geth     | 2026-10-20 | optional    | none  | … |
| [`mantle-v1.6.2`](…) | op-succinct | 2026-10-11 | recommended | none  | SP1 v6.4.0; ~18% cheaper range proofs |
| [`mantle-v1.6.1`](…) | op-succinct | 2026-09-24 | required    | none  | Fix proposer stall when the prover network returns FailedPrecondition |
| [`mantle-v1.6.0`](…) | op-succinct | 2026-09-02 | recommended | none  | Merge upstream op-succinct v3.12.0 |
```

Note that `op-geth` and `op-succinct` both hold `mantle-v1.6.2` — expected, and exactly what §4.2 means by `PATCH` advancing independently. The `Component` column disambiguates; the pair *(version, component)* is the unique key.

None of these four releases archives anything or opens a section. That happens once, later, in the round-transition PR (§5.3).

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
| Releasing repo's owner | Cuts the tag, publishes the release note, opens the release PR (§5.2) against `mantle-v2` |
| Version owners | Review release PRs; **own the round-transition PR (§5.3)** including archive-file creation; approve `MINOR` planning, code names, round closure, and changes to the tracked-component list |
| Release note final review | Sign-off on published release-note content per specification §5 |

A tag with no release note is an incomplete release, not a released version.

### 5.7 Automation

Synchronising six repositories by hand will leak. Two mechanisms:

- **Propagation.** An `on: push: tags` workflow in each tracked repo fires a `repository_dispatch` at `mantle-v2`; an Action opens the release PR with the §2 and §3.1 edits pre-filled from the tag and its release note. Humans review and sharpen the summary rather than transcribing.
- **Verification.** CI on `mantle-v2` parses the §2 and §3 tables and asserts the invariants in §5.8. Because every entry is a table row rather than prose, this is a parse, not a heuristic — which is the main practical reason the format is tabular.

The round transition (§5.3) is mechanical enough to script — moving a table into a new file and shifting section headers — but stays a reviewed, human-owned PR because it encodes a planning decision, not a mechanical consequence of a tag.

### 5.8 Invariants

CI-checkable on every PR:

1. Every version in §2 exists as a tag in its repository and has a published GitHub Release.
2. Every version in §2 appears as a row in §3.1 or §3.2.
3. `PATCH` numbers within a `MINOR` are never reused or decreased for a given component.
4. Every component listed in §1 appears exactly once in §2.
5. §3 contains at most two round sections; older rounds resolve to a file under `docs/versions/`.

Checked once, as a **round-close gate** in the §5.3 PR rather than continuously:

6. Every component in §2 has reached the closing round's `MAJOR.MINOR`. Mid-round the table legitimately spans two rounds, so this cannot be a per-PR check.

### 5.9 Executable procedure

Written to be followed literally by a person or an agent. Everything needed to cut a release and update this file is specified here; nothing is left to taste.

#### Step 1 — Preconditions

Refuse to proceed unless all hold:

- The `release/xxx` branch has QA approval and is **already merged into `main`** (specification §4).
- `main` is green.
- You know which component you are releasing.

#### Step 2 — Compute the version number

```
MAJOR.MINOR := the round named in §2's "Current round" line.   # never change this yourself
tags        := all tags in the component's repo matching mantle-v<MAJOR>.<MINOR>.*
               (INCLUDING -rc.N tags — an rc consumes its PATCH, §4.4)

if tags is non-empty:
    PATCH := max(PATCH across tags) + 1
else:
    # first adoption — §4.4
    legacy := all tags matching v<MAJOR>.<MINOR>.*  (the pre-prefix line, incl. -rc/-hotfix)
    PATCH  := (legacy non-empty) ? max(PATCH across legacy) + 1 : 0

version := mantle-v<MAJOR>.<MINOR>.<PATCH>
```

Read the tags from the repository (`git ls-remote --tags <repo>`), **not from this file** — rc tags never appear here, and using §2 alone will reuse a consumed `PATCH`.

If `version` already exists as a tag: **stop and escalate.** Do not pick the next free number; a collision means the state assumed above is wrong.

#### Step 3 — Cut the tag

- Tag the **merge commit on `main`** produced by the release branch. Never tag a commit that is not on `main`.
- Use an **annotated** tag (`git tag -a`), so the tag carries a tagger, date and message. Message: first line is the version, body is a two-to-three line summary. The release note, not the tag message, is authoritative.
- Push the tag, then create the GitHub Release **from that existing tag** — do not let the GitHub UI create the tag, which would produce a lightweight one.

#### Step 4 — Publish the release note

Publish it **before** opening the VERSION.md PR: invariant 1 (§5.8) requires the Release to exist by the time the PR lands. Content rules are in specification §5.

#### Step 5 — Classify the two graded fields

**Upgrade** — what an operator has to do:

| Value | Use when |
|---|---|
| `required` | Consensus or state-transition change, a security fix, or the previous version is broken or no longer interoperates with the network |
| `recommended` | Meaningful bug fix, performance or compatibility improvement; safe to defer briefly |
| `optional` | No operational impact — internal refactor, docs, tests, tooling |

**Consensus** — `⚠ yes` if the change can alter block validity, state transition or proof verification: execution semantics, fork activation, precompile set, gas accounting, derivation rules, or a circuit / vkey change. Otherwise `none`.

#### Step 6 — Open the VERSION.md PR

Apply §5.2: overwrite the component's row in §2, insert one row at the top of §3.1 using the §5.5 template.

**Date** is the tag's creation date in UTC (`git log -1 --format=%cd --date=short`), so the value is deterministic and CI-checkable.

#### Prohibitions

An agent performing this procedure must **never**:

1. **Bump `MINOR`.** Only version owners open a round (§4.2, §5.3). If the computed version would start a new `MINOR`, stop and escalate.
2. **Assert `Consensus: none` on its own.** `none` is a commitment (§3). Propose the value and require explicit confirmation from the releasing repo's owner before the PR merges.
3. **Write a version into §2 that is not a pushed tag with a published Release.** This is invariant 1; violating it makes the file actively misleading for the next automated run.
4. **Perform any §5.3 round-transition step** — archiving, section moves, opening a new §3.1 — as part of a release.
5. **Edit or backfill historical rows.** Releases are append-only; a mistake is corrected by a follow-up row, not by rewriting history.
6. **Infer the round from the tag it is about to cut.** The round comes from §2's "Current round" line, which only §5.3 may change.

---

<sub>Source: *Mantle Unified Version Management Specification V1.0*</sub>
