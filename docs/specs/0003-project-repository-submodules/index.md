# 0003. Project repository submodules

**Date**: 2026-10-14
**Status**: Accepted

## Summary

Tracking is the default for direct project submodules in the vault. A tracked `vaultctl-pinned = true` entry keeps a submodule at the revision recorded by Git, while other submodules follow an explicit branch or the remote default branch. vaultctl synchronizes each tracking child, then records all changed child commits in one parent commit after every child succeeds. A retry keeps successful child updates available, and child conflicts use the existing sync recovery commands with a path.

## Rationale

Reasoning and options: see [rationale.md](rationale.md).

## Requirements

**User stories**:

* As a vault maintainer, I want to use native Git through the existing vaultctl command to add, initialize, update, and remove project submodules without a second lifecycle interface.
* As a vault operator, I want pinned submodules checked out at the commits recorded by the vault.
* As a vault operator, I want tracking submodules to reconcile safe local work with their configured upstream branch and push the resulting commits.
* As a vault operator, I want child conflicts and partial progress to have clear recovery without losing completed child updates.
* As a documentation maintainer, I want source repositories used for projections to remain read only.

**Acceptance criteria**:

* **AC-1**: Maintainers can perform explicit submodule lifecycle operations through the existing `vaultctl git -- <Git arguments...>` escape hatch. The feature adds no dedicated lifecycle command, separate vaultctl manifest, or persistent recovery state; mode and branch metadata use the tracked `.gitmodules` file.
* **AC-2**: After initial parent reconciliation and push, vaultctl processes only direct submodules selected by Git's active submodule configuration. It defers the existing recursive checkout until child metadata and projection protections are validated. A pinned submodule is initialized or checked out at its parent recorded commit. A missing tracking submodule is initialized at its recorded commit; an initialized tracking worktree stays at its current `HEAD` and is reconciled with its selected upstream branch. Sync recovery commands and server mode do not start a normal child synchronization.
* **AC-3**: A tracked `vaultctl-pinned = true` key in the submodule's `.gitmodules` section opts that direct submodule into traditional recorded revision behavior. When the key is absent, tracking is the default. If the key is present more than once or its single value is not the literal `true`, sync fails closed.
* **AC-4**: For a tracking submodule, one concrete `branch` value in its `.gitmodules` section selects the upstream branch. Duplicate branch values and the special value `.` are rejected. When no branch is configured, vaultctl follows the one symbolic default branch reported by `git ls-remote --symref origin HEAD` in that child. It resolves and fetches that exact branch before autosaving child work, and uses the same branch for reconciliation and push. If the remote, branch name, fetched target, or symbolic default branch cannot be resolved reliably, sync reports the child path and stops before committing child work or pushing. It does not guess a branch or rewrite Git configuration.
* **AC-5**: Tracking starts from the child's current `HEAD`, including a detached `HEAD` created by ordinary Git submodule checkout. A child attached to a branch with a different name from the selected tracking branch fails before mutation. After the selected branch and exact fetched target are validated, vaultctl saves safe child work with the existing sync autosave rules, classifies history, rebases by default or merges when the parent command used `--merge`, and pushes with a non force refspec to the selected branch on `origin`. It never stashes, resets, forces checkout, or changes the child's branch configuration.
* **AC-6**: Git's initialized `remote.origin.url` supplies the child remote URL and remains unchanged. A local URL override continues to work. Branch selection and push use the same configured `origin`; vaultctl does not run `git submodule sync` or replace URLs.
* **AC-7**: Normal parent autosave keeps the existing gitlink safeguards. It does not stage unstaged child checkout drift, and it preserves explicitly staged gitlink additions, removals, and commit changes. After initial parent synchronization, vaultctl captures the parent index gitlinks. After all selected tracking children succeed, it requires that index baseline to remain unchanged with no other staged changes, compares each successful child's final `HEAD` with its captured gitlink, and stages only differing tracking gitlinks, including children already updated by an earlier attempt. It refuses grouped recording if the parent index changed unexpectedly.
* **AC-8**: A child conflict leaves that child rebase or merge available for manual resolution. The operator can continue or abort it with `vaultctl sync --continue --submodule <path>` or `vaultctl sync --abort --submodule <path>`. The path must identify one direct registered submodule in the configured vault. Child continuation stages resolved files and completes the existing Git operation, but does not push. It does not run other child work or parent sync; the operator runs normal sync again to validate the current target, push the child, and finish the group.
* **AC-9**: A tracking child operation never pushes until its reconciliation completes. On conflict, Git diagnostics and the child path are reported, and the operator receives the scoped recovery commands. Scoped continuation does not need to recover a prior push destination because it completes only the Git operation. The next normal sync resolves and fetches the currently configured selected target before it can push.
* **AC-10**: Tracking submodules are processed one at a time in ascending path order. A child that completes is pushed before the next child starts. If a later child fails, completed child commits and worktrees remain available, no parent gitlink commit is made, and vaultctl reports which child failed and that parent link recording is incomplete. A later normal sync leaves initialized tracking worktrees at their current commits, fetches their selected branches again, and can complete without discarding successful child updates. No rollback or recovery ledger is added.
* **AC-11**: After every selected tracking child succeeds, vaultctl records together in one parent commit only the tracking gitlinks that differ from the post-initial-sync parent index. If none differ, it skips the grouped commit and parent follow up. Otherwise it reconciles and pushes that parent commit through the existing parent synchronization functions, without repeating parent autosave, projection refresh, or child synchronization. The existing recursive checkout then runs as the final phase, whether or not a grouped commit was needed. If parent reconciliation, push, or recursive checkout fails, completed child commits and any parent commit remain, and existing recovery or a later normal sync applies.
* **AC-12**: Pinned submodules retain Git's recorded commit checkout behavior, including native recursive checkout for nested submodules. Tracking applies only to direct submodules recorded by the configured vault. Nested submodules continue to use their immediate parent's gitlinks and are updated by the final existing recursive checkout after all selected children succeed. That checkout retains Git's dirty-worktree refusal behavior; failure stops sync and leaves completed pushes and commits available for retry.
* **AC-13**: External documentation projection remains read only with respect to its sources. Projection may fetch source Git metadata and read resolved commit trees, but never checks out a source worktree, stages or commits source changes, or pushes to a source remote. Before any pinned checkout, tracking update, scoped recovery, or recursive checkout, vaultctl resolves canonical worktree paths for every repository that may be touched and rejects equal or ancestor/descendant overlap with a configured projection source worktree. Before a tracking child autosave commit, scoped continuation commit, or child push, it compares every effective child push URL with every effective fetch and push URL for configured projection source remotes. Git's effective remote URL query applies `url.*.insteadOf` rewriting and includes all configured URLs. Local paths, including relative paths and `file://` URLs, are resolved against the repository worktree and canonicalized through symlinks; network URLs are compared by their exact effective Git URL. If a path or remote endpoint cannot be resolved reliably, sync fails closed before that operation.
* **AC-14**: The initial parent synchronization is reported before child work begins. Overall sync success is reported only after pinned checkout, all selected tracking children, any grouped parent gitlink commit, the parent follow up, and its recursive checkout succeed. A child failure reports that parent synchronization succeeded but the child or parent link phase is incomplete. Server mode continues to reject sync.

## Decision

**Chosen option**: Extend the current client sync and recovery flow

Use the existing Git runner, autosave, history classification, rebase or merge, push, and conflict recovery behavior for the parent and each tracking child. Store the default tracking policy and the explicit pin marker in the tracked `.gitmodules` file. Child `sync --continue --submodule <path>` completes the Git operation without pushing; a later normal sync selects, validates, and pushes the current target. After all selected tracking children succeed, commit their changed gitlinks together and run the existing parent reconciliation and push flow.

**Implementation skills**: `golang-testing` (`samber/cc-skills-golang`, `.agents/skills/golang-testing/`) · `golang-safety` (`samber/cc-skills-golang`, `.agents/skills/golang-safety/`)

## Feature design

**Data model sketch**:

| Entity | Required fields | Relationship and constraints |
|---|---|---|
| Vault superproject | Existing `.gitmodules` name, path, and URL; optional concrete `branch`; optional `vaultctl-pinned = true`; parent tree gitlink commit | One vault records direct submodules. Each gitlink selects one commit for its path. Missing pin marker means tracking. |
| Tracking child repository | Existing Git worktree, `remote.origin.url`, current `HEAD`, fetched branch commit | The selected branch comes from the parent's `.gitmodules` branch or the remote symbolic default branch. No local branch setting or vaultctl state is added. |
| Pinned child repository | Existing Git worktree and parent recorded gitlink | Git checks out the recorded commit and retains its existing local URL. |
| Projection source | Existing external documentation manifest source, source repository path, named remote, and branch | The projection reads the fetched commit tree. It does not use the source worktree as a sync target. |

No database, vaultctl configuration, manifest, migration file, or persistent submodule recovery state is added. The pin marker is the valid Git config key `vaultctl-pinned` in the submodule's `.gitmodules` section. The participating paths, modes, and gitlinks come from the parent `.gitmodules`, active submodule configuration, and gitlink index after initial parent synchronization. Duplicate path, branch, or pin entries, a path/gitlink mismatch, and `branch = .` are rejected. Nested submodules remain at the commits recorded by their immediate parent.

**State transitions**:

* After initial parent synchronization, vaultctl reads active direct submodule paths, `.gitmodules` mode and branch settings, and parent index gitlinks. It sorts participating paths in ascending order and rejects ambiguous metadata before child operations.
* A missing selected child is initialized at the recorded parent commit. An initialized tracking child is never checked out to its parent gitlink during a normal retry. A pinned child is checked out at its recorded commit using native Git behavior.
* An unpinned direct child resolves the target branch and fetches its exact target before saving safe local work, reconciles its current history, and pushes without force.
* A completed child remains at its synchronized commit. After every selected tracking child succeeds, the parent records all successful tracking child heads that differ from current parent index gitlinks, including prior successful updates.
* A conflict leaves the child operation in Git's in progress state. Scoped continue or abort operates only on that child. Continue completes the Git operation without pushing; a later normal sync validates and pushes the result.
* If a later child fails, prior pushed updates remain. A later normal sync fetches the currently selected branch tips and can retry without checking initialized tracking children back to stale parent gitlinks.
* The parent follow up runs existing parent history reconciliation and push functions only when the grouped gitlink commit exists. It does not repeat parent autosave, projection refresh, or child synchronization. The existing recursive checkout runs after the optional parent follow up as the final normal sync phase.
* If the grouped parent commit cannot be pushed, it remains local for the existing parent sync recovery path. A later recursive checkout failure also leaves child and parent commits available.
* A projection source path that overlaps any submodule worktree subject to checkout is rejected before checkout. A tracking push is rejected if any effective destination matches a configured projection source remote endpoint.

**API surface**:

| Endpoint | Method | Key inputs | Key outputs | Auth | Key errors |
|---|---|---|---|---|---|
| `vaultctl sync` | CLI action | Client config, parent Git state, selected direct submodules, `.gitmodules`, child Git state | Parent result, per child progress, optional grouped parent commit, final result | Existing local user and configured Git permissions | Parent Git errors, child branch resolution failure, child conflict or push failure, source overlap |
| `vaultctl sync --merge` | CLI action | Same as normal sync, with merge strategy | Same as normal sync | Existing local user and configured Git permissions | Same child and parent errors, with merge conflict recovery |
| `vaultctl sync --continue --submodule <path>` | CLI action | One registered direct child path and its in progress rebase or merge | Child operation continuation result | Existing local user | Invalid path, unresolved conflict, unsafe path, or Git continuation failure |
| `vaultctl sync --abort --submodule <path>` | CLI action | One registered direct child path and its in progress operation | Child abort result | Existing local user | Invalid path, unsafe path, or Git abort failure |
| `vaultctl sync --continue` and `vaultctl sync --abort` | CLI action | Existing parent repository operation | Existing parent recovery result | Existing local user and parent remote access | Existing parent recovery errors |
| `vaultctl git -- <Git arguments...>` | CLI action | Native Git arguments | Native Git output and exit status | Existing local user and Git permissions | Native Git errors |

The `--submodule` option is valid only with `--continue` or `--abort`. It accepts a vault-relative direct registered path, not a repository name or arbitrary filesystem path. The path is normalized, resolved within the canonical vault worktree, and must identify exactly one registered direct submodule. Symlink escape, path ambiguity, or source-worktree overlap fails closed. Existing parent recovery syntax and behavior remain available without that option.

**Value sourcing**:

| Action | Value produced or displayed | Source |
|---|---|---|
| Select participating children | Direct submodule paths and active selection | Parent `.gitmodules` and Git's active submodule configuration |
| Select pin behavior | Pinned or tracking mode | Exactly one `vaultctl-pinned = true` value in the child's parent `.gitmodules` section; absence means tracking |
| Select tracking branch | Concrete branch name and fetched target commit | One parent `.gitmodules` `branch` value, or the unique symbolic `HEAD` branch from `git ls-remote --symref origin HEAD`; fetch validates the target before child autosave |
| Select child remote | Fetch and push URL | Initialized child's existing `remote.origin.url` and Git's effective push URL |
| Resolve child history | Exact fetched commit object ID | One matching child `FETCH_HEAD` result after fetching `refs/heads/<branch>` |
| Save child work | Child paths and existing commit message | Child Git status and index, existing autosave exclusions, and existing `commitMessage()` behavior |
| Choose reconcile strategy | Rebase or merge | Normal `sync` uses rebase; `sync --merge` uses merge |
| Recover child conflict | Child repository and operation | A vault-relative registered direct path and Git's in progress state; continue completes the operation only, and a later normal sync selects and validates the push target |
| Record child result | Changed gitlink paths and commits | All successful tracking child `HEAD` values compared with parent index gitlinks after initial parent sync and after all selected tracking children succeed |
| Push parent result | Parent branch, remote, and target ref | Existing parent upstream configuration and existing parent sync functions |
| Identify projection sources | Canonical source paths and effective remotes | Active client `source_root`, tracked external documentation manifest, and each manifest source's configured remote |
| Report phase result | Parent, child, and follow up status | Existing Git process results and the ordered sync phases |

**Key invariants**:

* Absence of `vaultctl-pinned` means tracking for direct vault submodules. Only one literal `true` value opts into pinned behavior; duplicates and other values fail closed.
* A configured `branch` is one concrete Git branch name. Duplicate values and `.` fail closed. The remote's default branch is accepted only when Git reports one unambiguous symbolic `HEAD` target under `refs/heads/`; the exact selected ref is fetched and validated before child autosave.
* The child remote is the initialized `origin`. vaultctl does not guess a remote, change branch configuration, rewrite URLs, or run `git submodule sync`.
* Child sync compares the current `HEAD` with the exact commit fetched for the selected branch. The push names the explicit target `refs/heads/<branch>` and never uses force.
* A tracking child is not checked out to a stale parent gitlink during a retry. Pinned children keep the native recorded commit checkout.
* Existing parent autosave continues to exclude unstaged gitlink drift while retaining explicit staged gitlink changes. The grouped update captures post-initial-sync index gitlinks, requires the index baseline to remain unchanged, then stages and commits only differing tracking paths. It refuses unexpected staged parent changes and uses the existing sync commit message source.
* No parent gitlink commit is created until every selected tracking child has synchronized and pushed successfully.
* Child Git operations remain the source of conflict state. Scoped continue stages resolved files and completes the operation, but does not push. The next normal sync resolves and fetches its currently selected target before reconciling or pushing. Abort and continue validate the vault-relative path, worktree containment, and projection source overlap before operating.
* Partial child success is durable because each successful child is pushed before the next starts. No custom rollback, stash, reset, forced checkout, journal, or retry loop is added.
* A projection source worktree is never passed to child sync or recursive checkout. Canonical equal or ancestor/descendant paths block all affected checkout and recovery operations. Every child effective push URL is checked against every effective fetch and push URL of configured projection source remotes; unresolved identity fails closed.
* The server mode and parent `sync --continue` or `sync --abort` path do not start child synchronization.

**Security model**:

The local operator remains the actor. Git uses the existing local credentials, repository permissions, and remotes. Tracking can push local child commits to the explicitly selected child branch, so the configured remote and branch are the authority. The projection source worktree and its configured upstream remain read only; the projection code may fetch Git metadata and read commit trees only. The implementation adds no privilege changes or credential handling.

**Configuration required**:

No new vaultctl JSON settings are required. A maintainer may add `branch = <name>` to a direct submodule section in `.gitmodules`. To keep a submodule pinned, the maintainer adds `vaultctl-pinned = true`. All other direct submodules track by default.

**Critical test scenarios**:

* End to end: one active tracking child with an explicit branch has safe local work committed and pushed, and its changed gitlink is committed and pushed by the parent only after child success, verifies **AC-5**, **AC-7**, and **AC-11**.
* Default branch: omit `branch`, return one symbolic remote `HEAD`, and verify tracking uses that branch. Return no symbolic target, multiple candidates, or a missing branch and verify sync fails before committing child work, verifies **AC-4**.
* Pin behavior: mark one child with `vaultctl-pinned = true` and leave another unmarked. Verify the first uses its parent gitlink and the second tracks, verifies **AC-2**, **AC-3**, and **AC-12**.
* Strategy: diverge a tracking child from its branch and verify default rebase, then `sync --merge` merge. Verify both push without force, verifies **AC-5**.
* Gitlink safeguards: combine unstaged child checkout drift, explicit staged gitlink additions, changes, and removals with tracking updates. Verify the first autosave preserves the old index decisions and the final grouped commit stages only changed tracking paths, verifies **AC-7**.
* Child recovery: cause a tracking rebase and merge conflict. Resolve and continue with the child path, then verify continuation does not push until a normal sync revalidates the target; abort another child operation. Verify wrong, unregistered, unsafe, and source-overlapping paths fail closed, verifies **AC-8** and **AC-9**.
* Partial progress: let one child push and make a later child fail. Verify no grouped parent link commit exists, the first child remains at its pushed commit, and a later sync can finish without resetting it to the stale parent gitlink, verifies **AC-10** and **AC-11**.
* Parent recovery: cause the grouped parent commit to conflict or fail to push. Verify the commit and child pushes remain available to the existing parent recovery path, verifies **AC-11**.
* Projection isolation: use a disposable source worktree and remote also listed by a source manifest. Verify projection fetches metadata without changing the source worktree, creating a source commit, or pushing to its remote. Verify equal and nested source path overlaps, recursive checkout overlap, multiple effective push URLs, and unresolved identities stop before the affected mutation, verifies **AC-13**.
* Boundaries: verify parent `sync --continue`, parent `sync --abort`, and server mode do not start child sync. Verify explicit lifecycle operations remain available through `vaultctl git --`, verifies **AC-1** and **AC-14**.
* Platform: run relevant synthetic repository cases on the reusable OpenBSD lab as well as local disposable repositories, verifies **AC-1** through **AC-14**.

## Build plan

The project uses a Tracer Bullet approach. First prove one complete tracking child path through child commit, child push, grouped parent gitlink commit, and parent sync. Then add the branch fallback, pin behavior, and recovery cases.

1. Extend the successful client sync path for one explicit branch tracking child. Reuse the existing child autosave, fetch, history classification, rebase or merge, and push behavior. Commit the changed gitlink only after the child push succeeds, then synchronize the parent, satisfies **AC-5**, **AC-7**, and **AC-11**.
2. Add direct child mode selection, reliable remote default branch resolution, and pinned checkout behavior. Preserve Git's active selection and existing URL overrides, satisfies **AC-2**, **AC-3**, **AC-4**, **AC-6**, and **AC-12**.
3. Extend conflict detection and recovery with a validated child path. Preserve completed pushes and the in progress child operation across failures, and verify a retry does not reset tracking children to old gitlinks, satisfies **AC-8**, **AC-9**, and **AC-10**.
4. Add source path and push target checks, reporting for parent and child phases, lifecycle documentation, and disposable repository coverage. Run relevant checks on the reusable OpenBSD lab, satisfies **AC-1**, **AC-13**, and **AC-14**.

## Migration plan

**Strategy**: One release with an explicit pin marker for projects that must retain recorded revision behavior.

**Phases**:

1. Before the first sync on the new version, maintainers add `vaultctl-pinned = true` to each existing direct submodule that must stay pinned and commit the updated `.gitmodules`.
2. A normal sync treats every other direct submodule as tracking, resolves its branch, reconciles and pushes child work, then records changed child commits in the parent.

**Rollback**: Add the pin marker to stop tracking a child and use the existing Git commands to restore a desired parent gitlink. Reverting vaultctl does not undo commits already pushed to a child remote.

**Risks**: An unmarked existing submodule begins tracking by default and may push its safe local commits to its configured branch. Maintainers must mark intended pins before using the new sync behavior. A failed later child leaves earlier successful child pushes in place until a later sync records their gitlinks.

## Consequences

**Positive**:

* Tracking behavior has one default and a clear opt out.
* Git and the existing vaultctl sync flow continue to own commits, conflict state, and retries.
* Parent links change as one commit only after all selected tracking children succeed.
* Documentation projection source worktrees and upstreams remain protected.

**Negative / tradeoffs**:

* Normal sync can push commits from a tracking child before the parent vault records its new gitlink.
* A later child failure leaves earlier child pushes in place, so operators may need another normal sync to finish parent recording.
* Reusing recovery commands requires the maintainer to pass the exact child path.
* Remote default branch lookup requires the remote to publish an unambiguous symbolic `HEAD`.

**Neutral**:

* Nested submodules continue to use recorded gitlinks rather than tracking behavior in this extension.
* Git's diagnostics remain the primary explanation for child repository failures.
