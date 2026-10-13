# 0003. Project repository submodules

**Date**: 2026-10-13
**Status**: In Progress

## Summary

Project repositories remain ordinary Git submodules recorded by the vault. Maintainers use the existing `vaultctl git -- ...` command for explicit submodule changes. After successful normal client synchronization, Git initializes and checks out the selected submodules recursively at their recorded commits. vaultctl reports the parent and submodule results separately.

## Requirements

**User stories**:

* As a vault maintainer, I want to use native Git through the existing vaultctl command to add, initialize, update, and remove project submodules without a second lifecycle interface.
* As a vault operator, I want normal client sync to check out the commits recorded by the vault so project files are present at the versions selected by the parent repository.
* As a vault operator, I want unsafe updates to preserve my uncommitted submodule work and tell me when the parent sync succeeded but submodule setup did not.

**Acceptance criteria**:

* **AC-1**: Maintainers can perform explicit submodule lifecycle operations through the existing `vaultctl git -- <Git arguments...>` escape hatch. The feature adds no dedicated submodule command, vaultctl manifest, state file, or other submodule metadata.
* **AC-2**: After successful normal client `sync` or `sync --merge`, vaultctl runs `git submodule update --init --recursive --checkout` in the configured vault, including when the parent repository was already synchronized. It runs only after parent synchronization and any required push succeed. Git's normal `submodule.active` configuration selects which submodules to initialize and update; vaultctl does not enumerate paths or override that selection.
* **AC-3**: Git checks out the commit recorded by each selected submodule's immediate parent gitlink. The update does not use `--remote` or `--force`. `--checkout` makes Git use the recorded commit even when local submodule update configuration requests merge or rebase. Git initializes a new submodule using `.gitmodules`; an initialized submodule keeps its local URL override. vaultctl does not run `git submodule sync` or rewrite URLs. If Git needs to clone or fetch and cannot use a configured URL, vaultctl reports Git's failure.
* **AC-4**: During normal sync autosave, vaultctl does not use an unstaged submodule worktree HEAD to change its parent gitlink. It may exclude gitlink paths from `git add -A`, but leaves their existing index entries unchanged. Explicitly staged gitlink additions, removals, and commit changes remain staged and eligible for the existing autosave commit, including a staged removal whose worktree still exists. Unstaged submodule state does not block the parent sync before the native update. Git may preserve changes that do not prevent checkout and refuses unsafe checkout. This narrow exception adds no persisted dirty-state tracking or general-purpose index manager.
* **AC-5**: For normal sync, vaultctl reports successful parent synchronization after all existing parent operations, including any required push, and before it begins the submodule phase. This phase message does not claim overall sync success. After Git exits successfully, vaultctl reports submodule initialization and update as successful and then reports overall success. If Git exits nonzero, vaultctl preserves Git's output, reports on stderr that parent synchronization succeeded but submodule state may be incomplete, and returns failure.
* **AC-6**: vaultctl does not promise that Git stops at the first failing submodule. Git may complete additional submodule operations before returning a nonzero status. vaultctl stops its own sync workflow and returns failure after that status. Any completed updates remain in place, and a later normal client sync can retry. vaultctl adds no rollback or custom submodule traversal.
* **AC-7**: Automatic update does not deinitialize or delete removed submodule worktrees; cleanup is explicit through native Git. `sync --continue` and `sync --abort` keep their existing behavior and output and do not run the new update. Server behavior remains unchanged, and server mode still refuses sync.

## Decision

**Chosen option**: Option 1: Use native Git for lifecycle and client sync

Use the existing Git escape hatch for explicit lifecycle operations. After successful normal parent synchronization, run `git submodule update --init --recursive --checkout`. Git uses its native `submodule.active` selection and per-parent gitlinks. The checkout mode selects each recorded gitlink even if local Git configuration requests merge or rebase. vaultctl returns failure when Git exits nonzero without imposing custom traversal or rollback. The runner up is a dedicated command interface, which would add duplicated behavior and validation without adding a required capability.

**Implementation skills**: `golang-testing` (`samber/cc-skills-golang`, `.agents/skills/golang-testing/`)

## Feature design

**Data model sketch**:

| Entity | Required fields | Relationship and constraints |
|---|---|---|
| Vault superproject | `.gitmodules` entries with submodule name, path, and URL; tree gitlinks with path and commit object ID | One vault can record many submodules. Each gitlink selects one commit for the repository at its path. |
| Project submodule | Repository content at the selected commit | Each submodule is selected by one parent gitlink. A submodule may contain its own `.gitmodules` entries and gitlinks for nested submodules. |

No vaultctl configuration, manifest, state file, or migration is added. `.gitmodules` records project paths and default URLs. Git's normal `submodule.active` configuration selects paths when the recursive update runs. For initialized submodules, an existing local URL override in `.git/config` remains in effect. The selected commit always comes from each parent repository's recorded gitlink.

**State transitions**:

* An uninitialized submodule becomes initialized at the commit recorded by its parent.
* An initialized submodule at another commit is checked out at the recorded commit. Git may preserve local changes that do not prevent checkout.
* If Git cannot safely check out the recorded commit, the update fails and retains the local work that blocks it.
* The recursive update follows Git's active-submodule selection. A path excluded by `submodule.active` is left untouched.
* A recursive update may leave multiple successful submodules updated before Git returns an error. A later normal sync retries selected submodules from the recorded commits.
* A submodule removed from the parent remains on disk if Git leaves its worktree behind. Automatic sync does not deinitialize or delete it; cleanup is explicit.

**API surface**:

| Endpoint | Method | Key inputs | Key outputs | Auth | Key errors |
|---|---|---|---|---|---|
| `vaultctl git -- <Git arguments...>` | CLI action | Existing active configuration and native Git arguments such as `submodule add`, `submodule update`, or `rm` | Native Git output and exit status | Existing local user and Git permissions | Native Git usage, repository, path, or worktree errors |
| `vaultctl sync` and `vaultctl sync --merge` | CLI action | Existing client configuration, vault Git index and worktree, `.gitmodules`, recorded gitlinks, and native Git configuration | Parent sync result followed by native submodule update result | Existing local user and configured vault access | Existing parent sync errors; native submodule update failure; unsafe checkout with local changes |

`sync --continue`, `sync --abort`, and server mode retain their existing behavior. A normal sync reports parent synchronization before the submodule phase, then reports submodule success only after Git succeeds. On failure, Git's output is preserved and vaultctl reports the parent success and possibly incomplete submodule state on stderr.

**Value sourcing**:

| Action | Value produced or displayed | Source |
|---|---|---|
| Add or remove a submodule | Repository URL, path, and Git metadata changes | Native Git arguments passed through `vaultctl git --` |
| Select a submodule URL | Repository URL and path | `.gitmodules` for a new submodule; existing local `.git/config` for an initialized submodule |
| Select the checkout target | Recorded commit object ID | Gitlink in the final parent commit |
| Update nested submodules | Nested URL and recorded commit | `.gitmodules` and gitlinks in each parent submodule commit |
| Report parent sync result | Success or failure and push result | Existing client sync operations |
| Select submodules to update | Active submodule paths | Native Git configuration, including `submodule.active`, and parent gitlinks |
| Preserve staged gitlink decisions during sync autosave | Paths with staged gitlink changes and unstaged worktree gitlink changes to exclude from staging | Current Git index and its staged gitlink diff; no saved vaultctl state |
| Report submodule result | Git output, process result, and incomplete state on failure | Native `git submodule update` result |
| Report phase status | Parent success, then submodule success or failure | Existing parent operation result followed by native Git process result |

**Key invariants**:

* The parent repository's gitlink is the only target for each submodule checkout.
* Automatic sync invokes native recursive update with `--checkout`. It does not pass submodule paths, so Git's active selection applies. It never requests upstream branch tracking or forced checkout.
* Automatic sync uses `.gitmodules` to initialize a new submodule, allowing Git to create its normal local URL configuration. A relative URL follows Git's normal parent remote resolution. It does not run `git submodule sync` or replace URL configuration for an initialized submodule.
* Automatic submodule work starts only after successful normal parent synchronization, including a required push.
* Sync autosave excludes unstaged changes to gitlinks from automatic staging and preserves explicitly staged gitlink additions, removals, and commit changes. The excluded paths come from gitlinks in the current index and staged gitlink changes, so a staged removal is not re-added from a remaining worktree. This is limited to the normal sync save path; it adds no general-purpose index manager.
* Native Git owns submodule checkout safety. vaultctl does not persist submodule state, stash, reset, or roll back submodule changes.
* A failed submodule update is a failed vaultctl command even when parent synchronization succeeded. Git may have completed additional updates before returning; completed updates remain available for retry.
* Automatic update does not deinitialize or delete removed submodule worktrees. Operators use native Git through the escape hatch for cleanup.
* Server mode and sync recovery commands do not run the new operation.

**Security model**:

The local operator running vaultctl remains the actor. Native Git uses the existing vault permissions, repository URLs, Git configuration, and credentials. This feature adds no privilege changes or credential handling. It does not alter the server process boundary.

**Configuration required**:

No new configuration, environment variables, or credentials are required.

**Critical test scenarios**:

* Happy path: an equal parent sync initializes selected absent submodules and nested submodules at their recorded commits. Configure merge or rebase as a submodule update strategy and verify `--checkout` still selects each gitlink, verifies **AC-2** and **AC-3**.
* Active selection: Git's `submodule.active` configuration controls which recorded submodules initialize and update, verifies **AC-2**.
* Parent sync paths: fast forward, push, and `sync --merge` report parent success before running the update. A failed parent operation does not invoke it, verifies **AC-2**, **AC-5**, and **AC-7**.
* Autosave: unstaged submodule checkout drift is not committed as a parent gitlink change; explicitly staged submodule additions, removals, and commit changes remain staged and eligible for the existing sync autosave commit, verifies **AC-4**.
* Dirty work: Git preserves a nonconflicting submodule edit where possible and refuses an unsafe checkout without force or loss of that edit, verifies **AC-4**.
* URL configuration: a new submodule initializes from `.gitmodules`; an initialized submodule retains its local URL override, and Git failure does not rewrite local configuration, verifies **AC-3**.
* Partial update: Git may complete more than one submodule operation before returning nonzero. vaultctl reports parent success and possibly incomplete submodule state, returns failure, retains completed updates, and allows a later normal sync to retry, verifies **AC-5** and **AC-6**.
* Removed worktree: normal recursive update does not deinitialize or delete a removed submodule worktree, verifies **AC-7**.
* Boundaries: `sync --continue`, `sync --abort`, and server mode do not run the update, verifies **AC-7**.
* Lifecycle: native add, initialization, update, and removal remain accessible through the existing Git escape hatch without a new vaultctl command, verifies **AC-1**.

## Build plan

The project uses a Tracer Bullet approach. First prove one complete client sync path with an already synchronized parent and one uninitialized active submodule. Then connect every successful parent sync path and exercise Git's worktree safety and partial update behavior.

1. Add the native recursive checkout to the successful equal history path and narrowly adjust sync autosave so unstaged gitlink drift is not staged while explicit staged gitlink changes remain eligible for commit. Use disposable repositories to prove the end to end path, satisfying **AC-2**, **AC-3**, and **AC-4**.
2. Run the update after every successful normal parent sync path, including fast forward, required push, and `sync --merge`. Report the parent phase before the submodule phase. Keep `sync --continue`, `sync --abort`, and server behavior unchanged, satisfying **AC-2**, **AC-5**, and **AC-7**.
3. Rely on Git's active selection, local URL configuration, checkout behavior, and nonzero status. Report incomplete state without promising first-failure stopping, preserve completed updates, and verify retry. Cover nested pins, staged gitlink changes, dirty work, and partial progress in disposable repositories, satisfying **AC-3**, **AC-4**, **AC-5**, and **AC-6**.
4. Document explicit lifecycle operations through `vaultctl git -- ...`, including cleanup of removed worktrees, and the pinned update behavior of normal sync. Verify relevant behavior on the reusable OpenBSD lab with its synthetic repositories, satisfying **AC-1**, **AC-2**, **AC-3**, and **AC-7**.

## Consequences

**Positive**:

* The parent repository remains the authority for each project version.
* Existing Git behavior supplies lifecycle operations and protection for unsafe worktree changes.
* A retry uses the same recorded commit without a separate vaultctl recovery record.

**Negative / tradeoffs**:

* A failed submodule update may be reported after the parent sync and push have already succeeded.
* Git's active selection can leave recorded but inactive submodules uninitialized or unchanged.
* Git diagnostics are the primary explanation for repository and worktree failures.

**Neutral**:

* Submodule initialization may download project repositories during normal client sync.
* Explicit lifecycle operations remain Git operations, so maintainers use Git's argument and path rules.
