# Scope: vaultctl external project documentation

`vaultctl` is a conservative command line tool for Git backed Obsidian vaults. This slice lets you expose selected documentation from multiple external project repositories inside the vault while leaving those source repositories authoritative and avoiding application source duplication.

**Build approach:** Tracer Bullet (one real path through every layer before broadening it).
**Workflow:** GA (after `/develop`, run `/check verify`, `/test`, a fresh model `/check review`, then `/document`). The project default level of rigor. `/architect` is the recommended first stop when a feature has a real design decision.

_These are recommendations to keep the build orderly, not requirements. You can skip anything that does not fit._

## At a glance

| # | Feature | Phase | Status |
|---|---------|-------|--------|
| 1 | Configuration and mode separation | Foundation | existing |
| 2 | Vault inspection and save | Foundation | existing |
| 3 | Client synchronization and recovery | Foundation | existing |
| 4 | External documentation refresh and projection | Slice 1 | done |
| 5 | Exclusive ownership for projected destinations | Slice 1 | done |
| 6 | Repository tooling maturity | Foundation | done |

## Foundations

### 1. Configuration and mode separation · existing

The CLI loads strict JSON configuration and keeps client and server repository layouts and capabilities separate. Server mode does not run `sync`.

Code in `internal/vaultctl/config.go`, `internal/vaultctl/cli.go`, and `internal/vaultctl/app.go`.

### 2. Vault inspection and save · existing

The CLI exposes status, diff, log, doctor, save, and the explicit Git escape hatch. Save stages and commits local vault changes without silently changing remotes, privileges, or Git configuration.

Code in `internal/vaultctl/app.go` and `internal/vaultctl/git.go`.

### 3. Client synchronization and recovery · existing

Client synchronization checks repository state and explicit upstream configuration, classifies history safely, and stops for manual conflict recovery instead of choosing a resolution.

Code in `internal/vaultctl/sync.go`.

### 6. Repository tooling maturity · Beta · done

Align the README and local verification instructions with the checks enforced by CI, and add Windows CI coverage for this supported client platform. Keep OpenBSD verification manual, with no new OpenBSD CI infrastructure.

**Done when:** README and local instructions cover module tidiness, formatting, vet, shuffled race tests, and build verification consistently with CI; Windows CI runs the repository checks; OpenBSD remains a manual verification target. No third party linter suites, pre commit frameworks, coverage thresholds, dependency bots, broad Go or OS matrices, release automation, task runners, or unrelated repository and process changes are added.
- [x] Build it: `/develop repository tooling maturity`
  1. [x] Align README and root local verification instructions with the CI module, formatting, vet, shuffled race, and build checks.
  2. [x] Run those repository checks on Linux and Windows in CI, while keeping OpenBSD verification manual.
  Code in `.github/workflows/ci.yml`, `README.md`, and `AGENTS.md`.
- [x] Verify it: `/check verify repository tooling maturity`
- [x] Test it: `/test repository tooling maturity`

## Slice 1: External documentation refresh and projection

### 4. External documentation refresh and projection · done

Expose selected documentation paths from multiple external project repositories through a manifest tracked by the vault. Refresh must validate every configured source before committing vault changes, preserve source repository authority, and remain safely rerunnable when a later source fails after an earlier source has advanced.

**Done when:** A vault manifest names each source repository, its selected documentation paths, and its vault location; client sync refreshes all configured sources through explicit tracked references, refuses unsafe or incomplete source states, updates all sources only when the complete set is ready, creates one isolated refresh commit, reports any source that advanced before a later failure, and leaves server refresh and migration behavior deferred.

- [x] Design it (spec): `/architect external documentation refresh and projection` ([0001](../specs/0001-external-documentation-refresh.md))
- [x] Build it: `/develop external documentation refresh and projection`
  1. [x] Prove the thin client path with nonmutating preflight, one exact branch fetch, one regular file projection, generated state, an isolated refresh commit, and normal sync integration before automatic save, satisfies selected AC-1, AC-3, AC-8, AC-10, AC-11
  2. [x] Broaden projection and ownership for directories, multiple mappings and sources, ancestry, deterministic state, and reruns, satisfies AC-3, AC-5, AC-7, AC-8
  3. [x] Complete schema, path, symlink, source tree, vault Git state, dirty destination, and exact staging validation, satisfies AC-2, AC-4, AC-6, remaining AC-7
  4. [x] Complete handled restoration, managed index recovery, commit outcome handling, terminal manual recovery, and sync boundary tests, satisfies AC-9, AC-10, AC-11
- [x] Verify it: `/check verify external documentation refresh and projection`
- [x] Test it: `/test external documentation refresh and projection`
- [x] Review it (fresh model): `/check review external documentation refresh and projection`
- [x] Document it: `/document external documentation refresh and projection`

### 5. Exclusive ownership for projected destinations · done

Treat every configured projection destination as owned by vaultctl. Refresh must reject ignored files, untracked files, unexpected empty directories, and other content outside the expected projection, while keeping projection one way from the external repository into the vault.

**Done when:** Refresh fails before mutation when a managed destination contains unexpected content, successful refresh can remove stale projected content, and the transaction keeps the required pre commit rollback guarantee for unrelated vault work with substantially simpler recovery state where the ownership rule allows it.
- [x] Design it (spec): `/architect exclusive ownership for projected destinations` ([0002](../specs/0002-exclusive-ownership-projected-destinations.md))
- [x] Build it: `/develop exclusive ownership for projected destinations`
  1. [x] Build version 2 path only ownership state and reject legacy root only state, satisfying AC-3
  2. [x] Add the pre fetch ownership proof, deterministic diagnostics, and in memory revalidation baseline, satisfying AC-1, AC-2, and AC-6
  3. [x] Implement exact projection inventories, stale cleanup, type changes, safe mutation ordering, and separate staging paths, satisfying AC-4, AC-5, and AC-7
  4. [x] Adapt exact rollback and commit confirmation, then complete disposable repository and sync boundary tests, satisfying AC-8 and AC-9
  Code in `internal/vaultctl/external_docs.go` and `internal/vaultctl/external_docs_test.go`.
- [x] Verify it: `/check verify exclusive ownership for projected destinations`
- [x] Test it: `/test exclusive ownership for projected destinations`
- [x] Review it (fresh model): `/check review exclusive ownership for projected destinations`
- [x] Document it: `/document exclusive ownership for projected destinations`

## Deferred

These items remain outside the first complete path and can be added after the core transaction boundary is proven.

- Documentation transforms, indexing, or generated navigation beyond direct selected path projection
- Automatic discovery of repositories or documentation paths not named by the vault manifest
- Repository lifecycle commands for adding, removing, or migrating configured sources

## Legend

`existing` means the capability predates this workflow and is recorded for context. `planned` means the feature has not been designed yet. A feature marked `needs a decision` starts with `/architect`; the detailed build milestones and acceptance criteria belong in its spec.

The recommended next step is always the first unticked box. The GA workflow normally continues with `/check verify`, `/test`, `/check review`, and `/document` after development.
