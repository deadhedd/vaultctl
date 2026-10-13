# Verify: project repository submodules · spec 0003 · updated 2026-10-13
_Steps derived from spec 0003 acceptance criteria and value sources. `/check verify` runs these; `/test` locks the durable ones._

## UI / manual
- [x] In a disposable configured client vault, use `vaultctl git -- submodule add`, `vaultctl git -- submodule update`, and `vaultctl git -- rm` for a local fixture repository; confirm native Git output and metadata with no added vaultctl submodule command, manifest, or state file. → AC-1
- [x] Run normal sync for an equal parent, fast-forward, required push, and `sync --merge`, with selected and inactive fixture submodules; confirm the recursive update runs only after parent operations succeed and follows `submodule.active`. → AC-2, AC-5
- [x] Set a selected submodule update strategy to merge or rebase and make its checkout differ from the parent gitlink; sync, then compare submodule `HEAD` with the gitlink commit. Confirm the target is the recorded commit and the invocation uses neither `--remote` nor `--force`. → AC-3
- [x] Exercise autosave with unstaged submodule checkout drift, staged gitlink addition/change, and staged gitlink removal while its worktree remains. Include a later autosave with the removed repository still on disk; confirm explicit index decisions remain eligible, unrelated content commits, and the removed gitlink stays absent. → AC-4
- [x] Make one submodule checkout safe and another unsafe because of local edits; run sync and capture stdout, stderr, and exit status. Confirm parent success is reported before the submodule phase, Git diagnostics remain visible, incomplete state is reported on stderr, and the command fails. → AC-4, AC-5
- [x] Cause a recursive update to fail after a fixture submodule has updated; confirm completed progress remains, vaultctl stops after Git returns failure, and a later normal sync retries from the recorded commits without rollback. → AC-6
- [x] Remove a submodule from the parent while leaving its worktree, then run normal sync; confirm the worktree is not deinitialized or deleted. Confirm `sync --continue`, `sync --abort`, and server mode do not run the submodule update. → AC-7

## Commands
- [x] In a disposable repository, pass URL, path, and lifecycle arguments through `vaultctl git --`; confirm native Git applies those exact arguments and metadata changes. → AC-1
- [x] In a disposable fixture, leave `.gitmodules` pointing to the original repository and set a distinct usable `remote.origin.url` inside the initialized client submodule. Record a gitlink to a commit absent from both the client and original source, run sync, and confirm the submodule reaches the gitlink while retaining its local URL. `TestSyncPreservesInitializedSubmoduleRemoteOriginURL` passed under `go test -race -shuffle=on ./...`. → AC-3
- [x] Pin a submodule and a nested submodule to known local commits; run recursive update and compare each worktree `HEAD` to the gitlink in its immediate parent commit, with nested URLs read from that parent's `.gitmodules`. → AC-3
- [x] Make the parent sync require a push, then make the push fail in a disposable remote; capture output and confirm the parent result reflects the existing sync operations and no submodule phase starts before parent sync succeeds. → AC-2, AC-5
- [x] Set `submodule.active` to include one fixture path and exclude another; run sync and inspect initialized worktrees against the active paths and parent gitlinks. → AC-2
- [x] Run the named autosave cases for unstaged drift and staged add, change, and removal. On OpenBSD, record `git diff --cached --raw`, the parent tree entry, the retained worktree `HEAD`, and Git's untracked path before and after the two-sync removal sequence. The later autosave saw `project/` as untracked, committed its unrelated file, and left the parent gitlink absent without saved vaultctl state. → AC-4
- [x] Force a fixture submodule update error; compare vaultctl's process status and stderr with Git's result and output, confirming the incomplete-state message is emitted only after parent success. → AC-5, AC-6
- [x] On the reusable OpenBSD guest, run the same sync and submodule checks against the documented synthetic repositories after guest host-key identity is verified; record fixture paths, commands, and observed outcomes. → AC-1, AC-2, AC-3, AC-7

## Acceptance-criteria coverage
- AC-1 covered by the lifecycle escape-hatch checks · AC-2 by successful sync paths and active selection · AC-3 by URL and parent gitlink checks · AC-4 passed in local autosave cases and the OpenBSD two-sync regression · AC-5 by phase ordering and failure reporting · AC-6 by partial progress and retry · AC-7 by removed-worktree and sync-boundary checks

## AC-4 evidence

- Local: `go test -race -shuffle=on ./...` passed. The autosave table covers unstaged checkout drift, staged gitlink change, staged addition, staged removal, and the later unrelated autosave after removal. The staged addition, change, and removal remain in the autosave commit; the removed worktree remains present.
- OpenBSD 7.9: the installed build passed a disposable two-sync sequence. Before the first autosave, `git diff --cached --raw` showed a `160000` to `000000` deletion for `project`; after it, the diff was empty, the parent tree had no `project` entry, and the worktree `HEAD` was unchanged. Before the second autosave, `git ls-files --others --exclude-standard` reported `project/`; after it, the parent tree still had no gitlink, `second.md` was committed, and the worktree remained at the same commit. The temporary repositories were removed by the test trap.
