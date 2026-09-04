# Mantle Core Component Versions

## Current Versions

Current round: **`mantle-v1.6` (Elysium)**

| Component | Version | Released | Previous version |
|---|---|---|---|
| op-geth | — | — | `v1.6.1` |
| mantle-v2 | — | — | `v1.6.2` |
| reth | — | — | `op-reth-v2.2.1-mantle-arsia.2` |
| op-succinct | [`mantle-v1.6.0`](https://github.com/mantle-xyz/op-succinct/releases/tag/mantle-v1.6.0) | 2026-09-02 | `v3.8.1-testnet-mantle-arsia.2` |
| mantle-da-indexer | — | — | `v1.6.0` |
| lithosphere | — | — | `v2.2.12` |

`—` means the component has not yet adopted unified numbering; **Previous version** is its newest tag under the old scheme, cleared once it adopts. `PATCH` advances independently per component, so two components can hold the same version number — the pair *(version, component)* is the unique key.

## Release Index — `mantle-v1.6` (Elysium)

- **Upgrade** — whether an operator has to act: `required` / `recommended` / `optional`.
- **Consensus** — whether the release changes consensus or state transition. `none` is a commitment, not a default.

| Version | Component | Date | Upgrade | Consensus | Summary |
|---|---|---|---|---|---|
| [`mantle-v1.6.0`](https://github.com/mantle-xyz/op-succinct/releases/tag/mantle-v1.6.0) | op-succinct | 2026-09-02 | recommended | none | Merge upstream op-succinct v3.12.0; first release under unified numbering |

<!-- Insert new rows directly below the header, newest first. One line per release;
     group by user-visible change, not per PR. Detail belongs in the release note. -->

### Archived rounds

| Round | Code name | Releases |
|---|---|---|
| — | — | *(none archived yet)* |

When a round closes, the index above moves verbatim to `docs/versions/<mantle-vX.Y>.md` and gains a row here — so this file only ever carries the current round, at a fixed size no matter how many rounds pass.
