# Verify: project repository submodules · spec 0003 · updated 2026-10-14

The original Feature 7 results below are historical evidence for its former AC-1 through AC-7. The extension uses AC-1 through AC-14. Its runtime results are recorded in the local and OpenBSD sections below.

## Original Feature 7 verification (historical · 2026-10-13)

These results describe the shipped pinned recursive-checkout behavior before the tracking-by-default extension. The completed checkboxes and command evidence are retained as recorded.

### UI / manual

- [x] In a disposable configured client vault, use `vaultctl git -- submodule add`, `vaultctl git -- submodule update`, and `vaultctl git -- rm` for a local fixture repository; confirm native Git output and metadata with no added vaultctl submodule command, manifest, or state file. → Original AC-1
- [x] Run normal sync for an equal parent, fast-forward, required push, and `sync --merge`, with selected and inactive fixture submodules; confirm the recursive update runs only after parent operations succeed and follows `submodule.active`. → Original AC-2, AC-5
- [x] Set a selected submodule update strategy to merge or rebase and make its checkout differ from the parent gitlink; sync, then compare submodule `HEAD` with the gitlink commit. Confirm the target is the recorded commit and the invocation uses neither `--remote` nor `--force`. → Original AC-3
- [x] Exercise autosave with unstaged submodule checkout drift, staged gitlink addition/change, and staged gitlink removal while its worktree remains. Include a later autosave with the removed repository still on disk; confirm explicit index decisions remain eligible, unrelated content commits, and the removed gitlink stays absent. → Original AC-4
- [x] Make one submodule checkout safe and another unsafe because of local edits; run sync and capture stdout, stderr, and exit status. Confirm parent success is reported before the submodule phase, Git diagnostics remain visible, incomplete state is reported on stderr, and the command fails. → Original AC-4, AC-5
- [x] Cause a recursive update to fail after a fixture submodule has updated; confirm completed progress remains, vaultctl stops after Git returns failure, and a later normal sync retries from the recorded commits without rollback. → Original AC-6
- [x] Remove a submodule from the parent while leaving its worktree, then run normal sync; confirm the worktree is not deinitialized or deleted. Confirm `sync --continue`, `sync --abort`, and server mode do not run the submodule update. → Original AC-7

### Commands

- [x] In a disposable repository, pass URL, path, and lifecycle arguments through `vaultctl git --`; confirm native Git applies those exact arguments and metadata changes. → Original AC-1
- [x] In a disposable fixture, leave `.gitmodules` pointing to the original repository and set a distinct usable `remote.origin.url` inside the initialized client submodule. Record a gitlink to a commit absent from both the client and original source, run sync, and confirm the submodule reaches the gitlink while retaining its local URL. `TestSyncPreservesInitializedSubmoduleRemoteOriginURL` passed under `go test -race -shuffle=on ./...`. → Original AC-3
- [x] Pin a submodule and a nested submodule to known local commits; run recursive update and compare each worktree `HEAD` to the gitlink in its immediate parent commit, with nested URLs read from that parent's `.gitmodules`. → Original AC-3
- [x] Make the parent sync require a push, then make the push fail in a disposable remote; capture output and confirm the parent result reflects the existing sync operations and no submodule phase starts before parent sync succeeds. → Original AC-2, AC-5
- [x] Set `submodule.active` to include one fixture path and exclude another; run sync and inspect initialized worktrees against the active paths and parent gitlinks. → Original AC-2
- [x] Run the named autosave cases for unstaged drift and staged add, change, and removal. On OpenBSD, record `git diff --cached --raw`, the parent tree entry, the retained worktree `HEAD`, and Git's untracked path before and after the two-sync removal sequence. The later autosave saw `project/` as untracked, committed its unrelated file, and left the parent gitlink absent without saved vaultctl state. → Original AC-4
- [x] Force a fixture submodule update error; compare vaultctl's process status and stderr with Git's result and output, confirming the incomplete-state message is emitted only after parent success. → Original AC-5, AC-6
- [x] On the reusable OpenBSD guest, run the same sync and submodule checks against the documented synthetic repositories after guest host-key identity is verified; record fixture paths, commands, and observed outcomes. → Original AC-1, AC-2, AC-3, AC-7

### Original AC-4 evidence

- Local: `go test -race -shuffle=on ./...` passed. The autosave table covers unstaged checkout drift, staged gitlink change, staged addition, staged removal, and the later unrelated autosave after removal. The staged addition, change, and removal remain in the autosave commit; the removed worktree remains present.
- OpenBSD 7.9: the installed build passed a disposable two-sync sequence. Before the first autosave, `git diff --cached --raw` showed a `160000` to `000000` deletion for `project`; after it, the diff was empty, the parent tree had no `project` entry, and the worktree `HEAD` was unchanged. Before the second autosave, `git ls-files --others --exclude-standard` reported `project/`; after it, the parent tree still had no gitlink, `second.md` was committed, and the worktree remained at the same commit. The temporary repositories were removed by the test trap.

## Feature 7 extension verification plan (in progress)

Run these checks against disposable repositories. Completed items are checked; remaining scenarios are still pending. Recorded execution details follow the checklist.

### Local functional checks

- [x] **AC-1**: Verify lifecycle operations still pass through `vaultctl git -- ...`, with no dedicated lifecycle command, separate submodule manifest, or recovery state.
- [x] **AC-2**: Exercise initial parent reconciliation followed by active direct submodules. Confirm the recursive checkout is deferred until metadata and projection checks pass, pinned children use recorded gitlinks, missing tracking children initialize at recorded gitlinks, and initialized tracking children are not reset on retry.
- [x] **AC-3**: Configure one `vaultctl-pinned = true` child and one unmarked child. Confirm the marked child remains pinned and the unmarked child tracks by default. Verify duplicate pin entries and any value other than the single literal `true` fail closed.
- [x] **AC-4**: Verify a concrete `.gitmodules` branch is selected and fetched before child autosave. With no branch configured, verify the unique symbolic `HEAD` branch is selected. Verify missing refs, duplicate branches, `branch = .`, absent or ambiguous symbolic `HEAD`, and malformed targets fail before child commits or pushes.
- [x] **AC-5**: Exercise a detached child `HEAD`, safe local child work, equal/ahead/behind/diverged histories, default rebase, and `sync --merge`. Verify an attached child on a differently named branch fails before mutation, pushes target the selected branch without force, and no reset, stash, forced checkout, or branch configuration change occurs.
- [x] **AC-6**: Set an initialized child's `remote.origin.url` to a usable local override that differs from `.gitmodules`. Confirm fetch and push use that origin and vaultctl does not run `git submodule sync` or rewrite it.
- [x] **AC-7**: Combine unstaged child checkout drift with staged parent gitlink additions, changes, and removals. Confirm parent autosave preserves those choices. After child sync, verify only tracking gitlinks differing from the post-parent-sync index baseline are staged; a prior successful child is included on retry even if its `HEAD` did not change during that retry. Verify unexpected index changes stop grouped recording.
- [x] **AC-8 and AC-9**: Cause child rebase and merge conflicts. Verify `sync --continue --submodule <path>` completes the Git operation without pushing, and `sync --abort --submodule <path>` aborts only that child. Verify invalid, unregistered, non-direct, escaping, and source-overlapping paths fail closed. Then run normal sync and confirm it reselects and fetches the current target before pushing.
- [x] **AC-10**: Let one child push successfully and make a later child fail. Verify no grouped parent gitlink commit is made, report the failed path and incomplete parent-link phase, and confirm completed child work remains. Retry and verify initialized tracking worktrees are not checked out to stale gitlinks and all changed links are recorded after every child succeeds.
- [x] **AC-11**: With multiple successful tracking children, verify exactly one grouped parent commit contains only the changed tracking gitlinks. Verify parent follow-up uses existing reconciliation and push behavior without repeating autosave, projection refresh, or child sync. Verify no group commit or follow-up occurs when no links differ. Exercise parent conflict and push failure recovery.
- [x] **AC-12**: Verify pinned direct children and nested submodules use their immediate parent recorded gitlinks. For tracking children, confirm nested recorded revisions are updated by the final recursive checkout after all children succeed. Make a nested checkout unsafe and confirm Git's dirty-worktree protection stops sync while completed commits remain available for retry.
- [x] **AC-13**: Use disposable projection sources to verify fetch and tree reads do not alter source worktrees, create source commits, or push to source remotes. Check equal and ancestor/descendant worktree overlaps, including nested repositories touched by recursive checkout and scoped recovery. Check all child push URLs against all source fetch and push URLs, including multiple URLs, Git URL rewriting, relative and `file://` local paths, and symlinks. Verify unresolved identities fail closed before checkout, child commit, or push.
- [x] **AC-14**: Capture stdout, stderr, and exit status for parent, child, grouped-link, and recursive-checkout failures. Confirm parent success is reported before child work, overall success follows every required phase, child failures identify the incomplete phase, and server mode rejects sync.

### OpenBSD 7.9 integration

- [x] Build and install the OpenBSD/amd64 executable in the reusable lab, then repeat the relevant extension checks using only the documented synthetic repositories. Verify exact Git state and command output for tracking, pins, recovery, grouped gitlinks, partial-progress retry, projection protections, and sync boundaries. Preserve the original Feature 7 evidence above as historical evidence; report any extension scenario that cannot run as pending.

## Recorded extension results · 2026-10-14

### Local

- `go mod tidy` completed; `go.mod` and `go.sum` had no diff. `gofmt -l .` returned no files, `go vet ./...` passed, `go test -race -shuffle=on ./...` passed, Linux, OpenBSD/amd64, and Windows/amd64 builds passed, and `git diff --check` passed.
- The package tests cover explicit and symbolic-default branch selection, missing active tracking initialization, inactive-child preservation, pin and branch metadata validation, branch mismatch refusal, child conflict continuation without push, projection source worktree and push-remote protection, grouped-link skip and safeguards, and partial progress retry. The existing Git lifecycle and gitlink autosave tests also passed in the full suite.
- Explicit and symbolic-default branch selection, unavailable explicit branches, duplicate branches, `branch = .`, and an unresolved symbolic default were exercised; each failure left child work uncommitted and left the child remote unchanged. Child rebase and merge strategies both pushed to the selected branch, and attached-branch mismatch preserved child `HEAD`, worktree changes, and remote state.
- Effective child push URL rewriting and multiple push URLs were rejected when they matched a projection source. Missing tracked projection manifests, direct source worktree overlap, grouped-index mutation, and a child `HEAD` that changed after the successful snapshot were also rejected before grouped commit.

### OpenBSD 7.9 reusable lab

- Scholomance reported the exact `vaultctl-lab-proof` VM and overlay as ready at `192.168.122.185`. The existing strict SSH host-key entry matched the runbook's ED25519 fingerprint `SHA256:FvH8r2mUQvQOQyosuCrh5QmL6ku8qV036bHhJ2upYwc`; strict SSH succeeded. The comprehensive disposable run used cross-build SHA256 `7b023f08560115fa9c551ef64035b8042ea3578854a30acc05c4cc3cb4a9f136`.
- The final OpenBSD/amd64 build had SHA256 `98016e3eafbf139be0b99d0b758f71149f5f153cc95fc71ef75ad5c0b75f8c62` on Ghost and in `~/bin/vaultctl-feature7` on the guest. It passed an active include/exclude pathspec check with a grouped tracking update and left the excluded child uninitialized. Its server and client `doctor` and `status` checks passed against the documented fixture configs; server-mode `sync` was rejected.
- Both disposable `/tmp/vaultctl-feature7.*` fixture trees were removed by exit traps. The temporary binaries and transfer files were removed from Ghost, Scholomance, and the guest.
- The disposable run verified an active child initialized and synchronized in a fresh clone while unselected children stayed uninitialized; a pinned child returned to its recorded commit; a tracking child's local `origin` override was preserved and received the push; an earlier child push survived a later child push failure without a parent link commit; retry recorded all changed tracking gitlinks in one parent commit; a projection source remote blocked scoped continuation before staging; scoped abort succeeded; scoped continuation completed without pushing; and a later normal sync pushed and recorded the resolved child. The parent and child remote refs and final worktree status were checked directly.
- The 2026-10-13 OpenBSD run did not exercise nested tracked submodules, child merge conflict recovery, parent follow up conflict recovery, or projection symlink and network URL cases. The review rerun below covers child merge recovery and projection file URL aliases on OpenBSD.

### Review rerun · 2026-10-14

- Local regression tests reproduced the stale protection failure before the fix: after a child commit, the test changed a projection source remote to the child push endpoint and the old guard allowed the push. With the fix, the same scenario refused the push and retained the local child commit. Fresh source and child effective URLs are checked before guarded child commits and pushes.
- `TestSyncSubmoduleMergeConflictRecoveryDoesNotPush` passed. Scoped continuation completed the resolved child merge without changing either remote or the parent; a later normal sync pushed the merge and recorded its gitlink.
- `TestChildPushProtectionCanonicalizesFileURLSourceAliases` passed for a file URL matching a local child push path and for a file URL through a symlink alias. Both cases refused the overlapping child push endpoint.
- `go test -race -shuffle=on ./...`, `go vet ./...`, formatting, and `git diff --check` passed. The OpenBSD/amd64 and Windows/amd64 CLI and package test binary cross builds passed.
- The current guest reported `192.168.122.185`; strict SSH verified its ED25519 fingerprint as `SHA256:FvH8r2mUQvQOQyosuCrh5QmL6ku8qV036bHhJ2upYwc`. The installed CLI and test binary matched Ghost SHA256 values `606396a62768c36940c3ddf8264afad029a6a5bccb6e49d668307d0bee7678dd` and `f261f0c4d7ed92a9e2e65d0d174b4f6b2336797566bcbfe1d6cec7710b2b5391`.
- Client and server `doctor` and `status` passed against the documented fixture configs. The OpenBSD package test binary passed the stale source endpoint, scoped child merge continuation, and both file URL alias subtests. Those tests used their temporary repositories; the review binaries and uploads were removed after the run.

### Repository identity blocker rerun: 2026-10-14

* Before the fix, `TestSyncRejectsStandaloneRepositoryAtRegisteredSubmodulePath` showed normal sync committing and pushing an unrelated repository at the registered child path. Scoped abort and continue also acted on an unrelated repository's active rebase.
* The fix checks that the registered path has no symlink alias, then compares the child worktree, Git directory, and reported superproject with the parent `.gitmodules` entry and Git module directory. It runs before child commits, reconciliation, pushes, pinned checkout, and scoped recovery.
* `TestValidateRegisteredSubmoduleIdentityAcceptsInitializedChild`, `TestSyncRejectsStandaloneRepositoryAtRegisteredSubmodulePath`, `TestSyncSubmoduleRecoveryRejectsUnrelatedRepositoryIdentity` (abort and continue), `TestSyncSubmoduleRecoveryRejectsInVaultSymlinkAlias`, and `TestRecordTrackingGitlinksRejectsUnrelatedSubmoduleIdentityBeforeStaging` passed. The existing `TestSyncPreservesInitializedSubmoduleRemoteOriginURL` also passed; the identity check left the local origin override unchanged.
* `go mod tidy` completed with no `go.mod` or `go.sum` diff. Formatting, `go vet ./...`, `go test -race -shuffle=on ./...`, Linux, OpenBSD/amd64, and Windows/amd64 CLI builds, OpenBSD and Windows package test binary cross builds, and `git diff --check` passed.
* On the reusable OpenBSD guest at `192.168.122.185`, strict SSH verified the recorded ED25519 fingerprint `SHA256:FvH8r2mUQvQOQyosuCrh5QmL6ku8qV036bHhJ2upYwc`. The OpenBSD package test binary passed the valid child, standalone repository, scoped abort and continue, in vault symlink alias, grouped gitlink refusal, and local origin override tests using disposable repositories. The new CLI passed client and server `doctor` and `status` against the documented fixture configs.
* The final OpenBSD CLI and package test binary SHA256 values were `8c7262eff65b69bd87af20ed7deba6f812736cf727108b17dc050c067620519d` and `87683aae8819abd8d1e67cdc4d9761373df7c931fc5c97b6be20c89f692356bb`. The temporary binaries and uploads were removed from Ghost, Scholomance, and the guest.

### Extension AC coverage

AC-1: lifecycle boundary · AC-2: parent and child phase ordering · AC-3: default tracking and pin marker · AC-4: branch selection and early target validation · AC-5: child reconciliation and push · AC-6: initialized origin preservation · AC-7: parent gitlink safeguards and retry baseline · AC-8/9: child conflict recovery · AC-10: partial progress and retry · AC-11: grouped parent recording and follow-up · AC-12: pinned and nested recorded revisions · AC-13: projection read-only protections · AC-14: reporting and server boundary
