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
| 4 | External documentation refresh and projection | Slice 1 | planned |

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

## Slice 1: External documentation refresh and projection

### 4. External documentation refresh and projection · planned · needs a decision

Expose selected documentation paths from multiple external project repositories through a manifest tracked by the vault. Refresh must validate every configured source before committing vault changes, preserve source repository authority, and remain safely rerunnable when a later source fails after an earlier source has advanced.

**Done when:** A vault manifest names each source repository, its selected documentation paths, and its vault location; refresh supports client and server mode boundaries, refuses unsafe or incomplete source states, updates all sources only when the complete set is ready, never commits a partially refreshed manifest or documentation projection, reports any source that advanced before a later failure, and leaves normal vault synchronization explicit and safe.

- [ ] Design it (spec): `/architect external documentation refresh and projection`

## Deferred

These items remain outside the first complete path and can be added after the core transaction boundary is proven.

- Documentation transforms, indexing, or generated navigation beyond direct selected path projection
- Automatic discovery of repositories or documentation paths not named by the vault manifest
- Repository lifecycle commands for adding, removing, or migrating configured sources

## Legend

`existing` means the capability predates this workflow and is recorded for context. `planned` means the feature has not been designed yet. A feature marked `needs a decision` starts with `/architect`; the detailed build milestones and acceptance criteria belong in its spec.

The recommended next step is always the first unticked box. The GA workflow normally continues with `/check verify`, `/test`, `/check review`, and `/document` after development.
