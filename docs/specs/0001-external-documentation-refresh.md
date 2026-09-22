# 0001. External documentation refresh and projection

**Date**: 2026-09-21
**Status**: Accepted

## Summary

This feature refreshes selected documentation from named external Git repositories into named paths in the vault during client sync. The manifest is tracked by the vault, while a small generated state file records the last projected commit and the paths each source owns. The first slice proves one safe client path and leaves server refresh, pinned revisions, and migration commands for later decisions.

## Context

vaultctl manages a Git backed Obsidian vault. The existing client sync flow already checks repository state, uses explicit upstream configuration, and stops for conflicts or incomplete operations. Server mode deliberately does not run sync.

The vault needs selected documentation from several external project repositories. The external repositories remain authoritative. The vault must receive a deterministic projection of selected commit content without changing unrelated notes, choosing conflict resolutions, or mutating source working trees.

The feature needs a durable record of the last projected commit so a movable source reference can advance only forward. It also needs a small ownership record so a later manifest edit cannot make vaultctl delete or claim an unknown path. The first complete path must remain small enough to verify in disposable repositories.

> ⚠️ Premise note: This topic could grow into separate client refresh, server refresh, revision pinning, and migration decisions. This spec focuses on one client sync path with tracked source references and explicit mappings. Server refresh, pinned revisions, and migration or removal commands are deferred.

## Requirements

**User stories**:

1. As a vault operator, I want client sync to refresh selected external documentation so the vault contains current approved source content.
2. As a vault operator, I want source repositories to remain authoritative so refresh never changes their working trees or chooses source history resolutions.
3. As a vault operator, I want unrelated vault work preserved so a refresh cannot absorb or delete content outside its managed paths.

**Acceptance criteria**:

1. **AC-1**: When `.vaultctl/external-docs.json` is absent, client sync preserves its current behavior exactly and does not require `source_root`.
2. **AC-2**: When the manifest exists, vaultctl requires valid strict JSON with at least one source. During the nonmutating sync preflight, before vaultctl stages or commits any local vault change, the manifest must be tracked and identical to `HEAD`; any staged or unstaged manifest change, or an untracked manifest, fails before source resolution or the normal automatic save. Each source has a unique explicit identifier, a repository path relative to configured client `source_root`, one existing Git remote name, one branch-only reference, and one or more explicit source to vault path mappings. Unknown fields or duplicate object members, duplicate identifiers, missing fields, invalid revision selectors, unsafe paths, and overlapping destinations fail before projection.
3. **AC-3**: For each source, client refresh performs one fetch using only the manifest named remote and the exact branch ref `refs/heads/<reference>`, with `--no-tags` and no broad remote fetch or tag discovery. It captures the single matching fetched result from `FETCH_HEAD` immediately after that fetch, before any later fetch, and requires exactly one full object identifier whose Git object type is `commit`. It resolves no local branch, remote tracking ref, symbolic expression, or other possibly stale name. It reads only the resolved commit tree. It never checks out, merges, rebases, stashes, or otherwise modifies a source repository working tree. A source with an unfinished Git operation fails. A tracked reference must resolve to the previously recorded commit or a descendant. Divergence, disappearance, an absent or ambiguous fetched result, a non commit result, or fetch failure stops refresh and identifies the source and reason.
4. **AC-4**: Before writing the vault, refresh validates every source, every selected mapping root and its selected tree entries, every destination boundary, and every ownership relationship. Missing source paths, symlinks, submodules, special entries, path escapes, destination overlaps including case insensitive collisions, mappings at or below `.vaultctl`, unsafe or symlinked destination ancestry, unsafe or symlinked control paths, uncommitted generated state, and unsafe repository or vault states fail the complete refresh before projection changes begin. Unrelated entries elsewhere in a source repository are outside this validation.
5. **AC-5**: On the first refresh, every configured destination must be absent. With existing state, each existing source identifier must retain its recorded owned destination paths. A new source is allowed only when all its destinations are absent. Removing a source identifier or changing the ownership paths of an existing identifier fails without deleting or claiming vault content.
6. **AC-6**: Before refresh validation and its isolated commit, the refresh phase does not stage or commit unrelated tracked, untracked, or staged vault changes outside managed projection paths. A refresh rejects an in-progress vault Git operation, unmerged index entries, dirty managed destinations, changes overlapping a managed destination or generated state, and any index or worktree condition that prevents exact staging of only the refresh owned paths and state file. After a successful refresh commit, the existing normal automatic save may process unrelated local changes.
7. **AC-7**: A successful projection makes each mapping destination exactly reflect its selected source path. A regular source file projects its bytes as one regular destination file, and its destination must not already be a directory. A regular source directory projects its recursively selected regular files and directories beneath the destination directory, and its destination must not already be a non-directory. Empty directories are not represented. Source or destination symlinks, symlinked destination ancestry, Git submodules, and other unsupported Git tree entries are rejected rather than followed or materialized. Replacement and pruning are limited to the explicit mapping destinations. Repeating refresh at the same source commits produces no additional vault changes or commit.
8. **AC-8**: When refresh changes the projection or generated state, it creates exactly one vault commit for the refresh and invokes the commit with the subject `Refresh external documentation`. That commit contains only managed projection changes and `.vaultctl/external-docs-state.json`. The state records each source identifier, its resolved commit, and its normalized owned destination paths. Commit correctness is determined by the parent and tree predicate in AC-9, not by the subject.
9. **AC-9**: Refresh has three handled outcomes. First, if a failure is conclusively known to have occurred before refresh commit creation, refresh restores the refresh owned projection paths, generated state, and any index entries changed by refresh to their exact pre refresh forms, while preserving unrelated staged entries and worktree changes. Second, if the expected refresh commit is conclusively confirmed from the captured pre refresh `HEAD` and committed tree, refresh succeeds; a later command or normal sync failure does not roll it back, reset `HEAD`, rewrite history, or create a compensating commit. Third, if commit creation may have occurred but the expected result cannot be conclusively confirmed, refresh does not restore paths, reset `HEAD`, rewrite history, or attempt another commit. It stops in a terminal manual recovery state and reports the captured pre refresh `HEAD`, current `HEAD`, expected refresh allowed path set consisting of all mapping destinations and `.vaultctl/external-docs-state.json`, and the reason confirmation failed. Abrupt process termination, host failure, or power loss is outside the v1 recovery guarantee and may leave managed paths, index entries, or the lock requiring manual recovery; v1 adds no journal or crash recovery subsystem. External source fetch progress is not rolled back. The command reports sources that resolved or fetched before the failure, the failed source or operation, and the reason.
10. **AC-10**: For a new normal client sync, the existing nonmutating local sync and Git preflight runs first. If a manifest exists, manifest, generated state, refresh Git-state, and managed-path validation run next, before any normal automatic save. External documentation refresh and its isolated commit then run. Only after refresh succeeds may the existing normal automatic save of unrelated local vault changes run, followed by the existing normal vault remote fetch, merge or rebase, commit, and push synchronization. If that later sync portion fails, the successful refresh commit remains durable and vaultctl does not roll it back, rewrite history, or create a compensating commit. If no manifest exists, existing sync behavior is preserved. A sync continuation or abort never runs external documentation refresh.
11. **AC-11**: Refresh and the refresh phase of sync are serialized by one atomic lock at `.vaultctl/external-docs.lock`, held for the complete refresh lifetime through final `HEAD` inspection and released only after the refresh reaches a terminal result. If the lock already exists or cannot be acquired atomically, the new operation fails clearly without changing the vault. This slice does not recover stale locks automatically.

## Options considered

### Option 1: Client sync with a small tracked state model

Run refresh only in client sync. Use one operator manifest, one generated state file, movable remote references, explicit mappings, and a bounded commit transaction.

**Pros**:

1. Proves the requested path end to end with the existing client Git boundary.
2. Keeps the state necessary for forward safety and ownership checks without building a migration system.
3. Preserves the existing rule that server mode never runs sync.

**Cons**:

1. Server deployments cannot refresh external documentation yet.
2. Pinned commits and ownership migrations require later design work.

### Option 2: Symmetric client and server refresh with flexible revision modes

Implement refresh in both modes, allow pinned commits and tracked references, and define source relocation and migration behavior in the same feature.

**Pros**:

1. Provides broader deployment coverage in one change.
2. Supports more repository workflows immediately.

**Cons**:

1. Doubles the process and path boundary before the client transaction is proven.
2. Adds migration, deletion, and revision semantics that are not needed for the first real use case.

### Option 3: Project from the current source working tree without generated state

Copy selected paths from source worktrees and use the current manifest as the only ownership record.

**Pros**:

1. Has the smallest apparent file model.
2. Avoids fetching and state comparison logic.

**Cons**:

1. Dirty or incomplete source worktrees can produce content that is not tied to a known commit.
2. Without generated state, forward only updates and safe ownership checks cannot be enforced.
3. It would make a source worktree rather than a resolved Git revision the effective authority.

## Decision

**Chosen option**: Option 1: Client sync with a small tracked state model

Implement external documentation refresh as an automatic client sync phase when the tracked manifest exists. Use only explicit movable remote references in the first slice. Read source content from resolved Git commit trees, validate the complete source set before projection, and create one isolated vault commit for projection and generated state.

**Implementation skills**: `golang-testing` (`samber/cc-skills-golang`, `.agents/skills/golang-testing/`) · `golang-safety` (`samber/cc-skills-golang`, `.agents/skills/golang-safety/`)

## Rationale

The chosen slice follows the repository's Tracer Bullet approach. It puts one real source resolution path, one projection path, one generated state record, and one vault commit through the existing Go and Git boundaries before adding deployment symmetry or migration behavior. Client mode is the only place where sync exists today, so keeping server refresh out avoids changing a settled safety rule.

The state file is small but necessary. The last resolved commit establishes the forward only ancestry check. The full owned path list prevents a changed manifest from silently pruning or claiming paths. The manifest remains operator authored, so refresh never rewrites desired configuration merely because a reference advances. Reading commit trees instead of source worktrees makes unrelated source edits irrelevant and avoids checkout or conflict behavior.

The runner up would provide broader coverage, but it would make the first verification target much larger. A working tree copy without state would be simpler only by dropping the safety properties that make the feature trustworthy.

## Feature design

**Data model sketch**:

1. Client configuration adds `source_root` as a required host local path only when the external documentation manifest exists or refresh is invoked. It is not required for installations that do not use this feature. The active client configuration resolves repository names beneath this root.
2. `.vaultctl/external-docs.json` is operator authored strict JSON with this exact shape:

   ```json
   {
     "version": 1,
     "sources": [
       {
         "id": "docs",
         "repository": "project",
         "remote": "origin",
         "reference": "main",
         "mappings": [
           {
             "source": "docs",
             "destination": "external/project"
           }
         ]
       }
     ]
   }
   ```

   The top level requires integer `version` equal to `1` and a nonempty `sources` array. Each source requires string fields `id`, `repository`, `remote`, and `reference`, plus a nonempty `mappings` array. Each mapping requires string fields `source` and `destination`. The manifest accepts any member ordering, but rejects unknown fields, duplicate object member names, null or wrong typed values, missing fields, duplicate source identifiers, duplicate mappings, and trailing JSON values. `id` is a nonempty string without control characters and is unique by exact value. A reference is a branch name accepted by Git branch-name validation, not a commit identifier, tag, symbolic expression, or local branch lookup. Repository and source values use normalized relative path rules. Destination values use the destination path rules below. The manifest remains human authored, so its cosmetic member ordering is not prescribed.
3. `.vaultctl/external-docs-state.json` is generated strict JSON with this exact shape:

   ```json
   {
     "version": 1,
     "sources": [
       {
         "id": "docs",
         "resolved_commit": "<full commit object id>",
         "owned_paths": [
           "external/project"
         ]
       }
     ]
   }
   ```

   The top level requires integer `version` equal to `1` and a nonempty `sources` array. Each entry requires string fields `id` and `resolved_commit`, plus a nonempty array `owned_paths` of strings. `resolved_commit` is the full object identifier of a Git object whose type is `commit`. `owned_paths` is the complete normalized, duplicate free set of mapping destination paths, not every projected file. State entries relate to manifest entries by identifier. Unknown fields, duplicate object member names, duplicate state identifiers, duplicate owned paths, missing or malformed fields, and trailing JSON values fail before projection. Generated state uses fixed member order, sources sorted by identifier, owned paths sorted by normalized path, two space indentation, and one trailing newline. The current owned path set must compare exactly with the previous set for an existing source.
4. Source identifiers are unique in the manifest and state. State entries may be a subset of manifest sources before a first refresh for a newly added source, but cannot contain an unknown identifier. A source has many mappings. Each mapping owns one bounded destination path. Destination paths cannot overlap within or across sources, including case insensitive aliases.
5. State entries may be added for new identifiers only when every new destination is absent. Existing identifiers must retain the same normalized owned path list. Existing state identifiers cannot be removed in this slice.

**State transitions**:

1. No manifest: refresh is not configured and normal client sync is unchanged.
2. Manifest with no state: first refresh is allowed only when every destination is absent. Successful projection creates state and one commit.
3. State at a recorded commit: refresh resolves the configured reference, accepts only the same commit or a descendant, and projects the selected tree when content or state changes.
4. Invalid or unsafe state: refresh stops before projection and reports the source or vault condition. It does not attempt repair or migration.
5. External documentation refresh is considered only for a new normal `vaultctl sync`, after the existing local sync and Git preflight. It is skipped entirely for `sync --continue` and `sync --abort`.

**API surface**:

| Endpoint | Method | Key inputs | Key outputs | Auth | Key errors |
|---|---|---|---|---|---|
| `vaultctl sync` | CLI action | active client config, `.vaultctl/external-docs.json`, local Git state | source progress, resolved commits, projection commit, then existing sync output | local user with existing vault and source filesystem access | invalid manifest, missing source, failed fetch, divergent reference, dirty managed destination, overlap, unsafe destination ancestry, active refresh, Git conflict, commit failure |

Refresh has no separate command or flag in the first slice. The presence of `.vaultctl/external-docs.json` is the explicit opt in for a new normal client `vaultctl sync` only. The operation order is: existing nonmutating local sync and Git preflight, manifest and generated state detection and validation, refresh Git-state and managed-path validation, external documentation refresh and its isolated commit when the manifest exists, existing normal automatic save of unrelated local vault changes, then the existing normal vault remote fetch, merge or rebase, commit, and push synchronization. No normal automatic save may stage or commit local changes before manifest and refresh validation and the isolated refresh commit complete. A preflight or refresh failure prevents the normal automatic save and normal remote synchronization, and the absence of a manifest preserves the existing path. The server mode `sync` refusal remains unchanged, and `sync --continue` and `sync --abort` never run refresh. The runtime refresh lock is `.vaultctl/external-docs.lock`; it is not generated state and is not included in the refresh commit. While held, the lock path is excluded from vault cleanliness and refresh staging checks as the operation's sole permitted transient path under `.vaultctl`.

**Value sourcing**:

| Action | Value produced or displayed | Source |
|---|---|---|
| Load configuration | `source_root` | active client JSON configuration |
| Load manifest | source identifiers, repository names, remotes, references, source paths, destination paths | `.vaultctl/external-docs.json` |
| Resolve repository | absolute source repository path | canonicalized `source_root` plus manifest `repository` |
| Capture pre refresh HEAD | exact vault commit identifier | installed Git in the configured vault, captured before projection and commit |
| Fetch source | one fetched branch result and available commit | installed Git, one `git fetch --no-tags <remote> refs/heads/<reference>` invocation, the existing Git remote named by manifest `remote`, and the manifest branch `reference`; the single matching `FETCH_HEAD` result is captured immediately before any later fetch |
| Resolve revision | exact commit identifier | the captured full object identifier after requiring exactly one matching result and Git object type `commit`, never a local branch, remote tracking ref, symbolic expression, or stale name |
| Check forward movement | previous commit | matching source entry in `.vaultctl/external-docs-state.json`, absent on first refresh |
| Read projection content | regular file bytes and directory entries | selected source paths in the resolved commit tree |
| Check ownership | previous owned paths | matching generated state entry |
| Check current vault | tracked and untracked changes and Git operation state | installed Git in the configured vault |
| Report source progress | source identifier, operation, previous and resolved commit where applicable | manifest identifier plus resolved and state commits |
| Write state | identifier, resolved commit, owned paths | manifest source identifier, resolved Git commit, normalized mapping destinations |
| Confirm refresh commit | success or an ambiguity or unexpected result | captured pre refresh `HEAD`, post command `HEAD`, its exact parent, and the resulting committed tree and changed path set |
| Commit refresh | requested subject `Refresh external documentation` | this specification; subject is not part of commit confirmation correctness |

**Key invariants**:

1. The manifest and state are version 1 strict JSON with the exact shapes defined above. The manifest must be tracked and identical to `HEAD` before refresh. Unknown fields, duplicate object members, duplicate identifiers, unknown state identifiers, malformed selectors, malformed commit identifiers, wrong types, and invalid paths are errors.
2. Repository names and source paths use normalized relative path rules and cannot escape their configured roots. Destination paths use slash separated relative paths with one or more nonempty segments. They reject empty paths, empty segments, `.`, `..`, NUL, backslash, absolute forms, Windows volume forms, Windows UNC forms, and any path that escapes the vault. Destination paths are compared for exact and case insensitive ancestor or descendant overlap, and case insensitive aliases are rejected on every host.
3. Every source has one explicit existing remote name and one branch-only movable reference accepted by Git branch-name validation. Pinned commit selectors are deferred.
4. For each source, client refresh runs one fetch for the named remote and exact branch ref `refs/heads/<reference>` with `--no-tags`, captures exactly one matching `FETCH_HEAD` result immediately, requires its full object identifier to resolve to a commit, and reads that commit tree only. It never guesses a remote, depends on a local branch or inferred tracking ref, changes Git configuration, changes privileges, checks out, stashes, merges, rebases, or modifies a source worktree.
5. Source content comes only from the resolved commit tree under selected mapping roots. A source mapping must select a regular file or directory. Regular files and directories are supported. Symlinks, submodules, and special entries anywhere under a selected directory are rejected, and source or destination symlinks are never followed or materialized. Empty directories and source mode bits are not part of the projection contract.
6. Every source and mapping is validated before any vault projection write begins.
7. Managed destinations do not overlap, do not target `.vaultctl` or any case insensitive alias of it, and do not pass through unsafe or symlinked destination ancestry. Existing managed destinations must be absent on first refresh and clean on later refreshes. `owned_paths` is normalized, sorted, duplicate free, and compared as an exact set.
8. Existing unrelated vault changes, including staged changes, remain outside the refresh commit. The vault must have no in-progress Git operation or unmerged index entry. Changes overlapping a managed destination or generated state, including changes to an existing state file, are rejected. The generated state file must not already have uncommitted changes.
9. A source reference may stay at the recorded commit or move to a descendant. Divergence or disappearance fails closed.
10. A refresh change is represented by one commit containing only managed projection paths and generated state. A no change refresh creates no commit. The commit command requests the fixed subject, but the subject is not used to confirm correctness.
11. A handled failure conclusively known to precede refresh commit creation restores the managed projection paths, generated state, and any index entries changed by refresh exactly, while preserving unrelated staged entries and worktree changes. A commit that is conclusively confirmed remains durable. An indeterminate result enters terminal manual recovery without restoring paths, resetting `HEAD`, rewriting history, or attempting another commit. Abrupt process termination, host failure, and power loss are outside the v1 rollback guarantee and may require manual recovery. No journal, durable crash recovery, general rollback system, or history rewriting is introduced. External fetch progress may remain in the source repository and is reported.
12. Only one refresh may run at a time, using one atomic lock held through final `HEAD` inspection. The lock path is the only permitted transient path under `.vaultctl` and is excluded from refresh cleanliness and staging checks. An active operation or existing lock causes a clear failure, and this slice does not recover stale locks automatically.
13. A commit is confirmed only when post command `HEAD` differs from the captured pre refresh `HEAD`, its first parent is exactly that pre refresh `HEAD`, and its resulting committed tree has exactly the prepared projection and state contents with no changed path outside the refresh allowed path set, consisting of all mapping destinations and `.vaultctl/external-docs-state.json`. If the predicate is not met, a result conclusively known to precede commit creation follows the handled restoration path; a result where commit creation may have occurred enters terminal manual recovery and is reported without restoring paths, resetting `HEAD`, rewriting history, or creating a compensating commit.
14. The later normal sync path never rolls back a successful refresh commit.

**Security model**:

The local operator is the only actor. The manifest may name source paths and Git remotes, but it contains no credentials. Git uses credentials and transport policy already configured by the operator. vaultctl does not alter Git configuration, privileges, remotes, upstreams, or source repository working trees.

The canonical vault root is the boundary for all control paths. `.vaultctl` must be a real directory beneath that root, not a symlink, and every component from the canonical vault root to `.vaultctl`, the manifest, the state file, and the lock path must contain no symlink. Existing manifest and state files must be regular files. An existing lock path must be a regular file before the atomic lock acquisition fails it as already active. A missing lock path is created only through the atomic lock operation, and its ancestry is checked first.

The configured `source_root` bounds repository lookup. Canonical path checks reject repository paths that escape it. Vault destination checks reject paths that escape the vault, overlap another source, or alias another destination under case insensitive comparison. Only the explicit managed destinations and generated state are eligible for the refresh commit. No regulated data or external service account is introduced by this feature.

**Configuration required**:

1. `source_root` in the active client configuration, required only when `.vaultctl/external-docs.json` exists or refresh is invoked.
2. Existing Git remote credentials and filesystem permissions. vaultctl does not add or manage secrets.

**Critical test scenarios**:

1. Thin happy path: one source performs one exact no tag branch fetch, captures one unambiguous commit result, projects one regular file into an empty destination, writes version 1 state, and the new normal sync creates one isolated commit whose parent and committed tree satisfy the refresh predicate, verifies **AC-2**, **AC-3**, **AC-7**, **AC-8**, and **AC-9**.
2. Repeat path: refresh at the same resolved commits produces no file changes and no additional commit, verifies **AC-7** and **AC-8**.
3. Multiple source path: an earlier source advances, a later source is missing or invalid, the vault remains unchanged and diagnostics identify both sources, verifies **AC-4** and **AC-9**.
4. Forward safety: a reference diverges from the recorded commit, or the fetched result is missing, ambiguous, or not a commit, refresh stops before projection and reports the source and reason, verifies **AC-3** and **AC-9**.
5. Source boundary: a selected source file is a symlink, a selected directory contains a symlink, submodule, or special entry, or an unfinished Git operation exists, refresh stops before projection; unsupported entries in unrelated source paths are ignored, verifies **AC-3** and **AC-4**.
6. Vault boundary: a managed destination is dirty, overlaps another destination, uses an empty, dot, absolute, Windows volume, Windows UNC, escaping, or case insensitive alias path, already exists on first refresh, is a wrong type for its source mapping, contains a symlink, or has an ownership mismatch, refresh stops without changing it. Symlinked `.vaultctl`, manifest, state, lock, or control ancestry also fails, verifies **AC-4**, **AC-5**, **AC-6**, and **AC-9**.
7. Unrelated work: unrelated staged and unstaged vault changes exist, the refresh commit excludes them and leaves them untouched, verifies **AC-6** and **AC-8**.
8. Later sync failure: projection commits successfully and the following normal fetch, merge, rebase, or push fails, the projection commit remains and no rollback occurs, verifies **AC-10**.
9. Configuration absence: no manifest exists, sync follows the existing behavior and does not require `source_root`, verifies **AC-1**.
10. Recoverable application failure: a handled projection, state, or staging failure after it starts restores managed paths, generated state, and refresh changed index entries exactly, while unrelated staged and worktree changes remain, and no refresh commit exists, verifies **AC-6** and **AC-9**.
11. Commit confirmation: a commit command that advances `HEAD` from the captured parent with an exact prepared tree is accepted regardless of subject, while an unchanged parent, wrong parent, extra path, or unexpected content is reported without history rewriting, verifies **AC-8** and **AC-9**.
12. Sync boundaries: refresh runs after existing local preflight and before the normal vault remote fetch, merge or rebase, commit, and push; it does not run for `sync --continue` or `sync --abort`, and a later normal sync failure leaves the refresh commit, verifies **AC-10**.
13. JSON contract: manifest and state version, required fields, types, unknown fields, duplicate object members, duplicate identifiers, duplicate paths, state ordering, and deterministic state serialization are enforced, verifies **AC-2**, **AC-4**, and **AC-8**.
14. Serialization: a second refresh while one is active, or a stale lock remains, fails clearly without changing the vault, verifies **AC-11**.

## Build plan

The scope records a Tracer Bullet approach. First prove one thin client path through configuration, one explicit branch fetch, one regular file projection, one state write, one isolated commit, and normal sync integration. Then thicken that path with directories, multiple sources, ownership, boundary validation, and handled failure recovery. Do not build server behavior, migration commands, pinned revisions, or durable crash recovery as hidden prerequisites.

1. Build the first complete path with client `source_root` configuration, the minimum version 1 manifest and state parsing needed for one source and one regular file mapping, the atomic lock, one exact no tag branch fetch resolved from its immediate `FETCH_HEAD` result, projection into an empty destination, state generation, isolated staging and commit, the pre refresh `HEAD` capture and minimal post commit tree predicate, and integration into a new normal `vaultctl sync` with nonmutating preflight before any normal automatic save. Preserve the no manifest path and skip refresh for continuation and abort actions. This proves the thin happy path and only its implemented negative cases, and exercises selected portions of **AC-1**, **AC-3**, **AC-8**, **AC-10**, and **AC-11**. Full **AC-2**, **AC-7**, and **AC-9** acceptance waits for the later validation and recovery steps.
2. Thicken the working path with directory mappings, multiple mappings and sources, same or descendant ancestry checks, exact ownership state, deterministic state ordering, first use and later refresh behavior, replacement and pruning within owned paths, and isolated temporary repository tests. This completes the projection and ownership portions of **AC-3**, **AC-5**, **AC-7**, and **AC-8** that depend on these broader cases.
3. Add the remaining boundary validation: strict duplicate member and unknown field rejection, complete relative path grammar, case insensitive destination collision checks, control directory and file symlink checks, source tree entry validation, source and vault Git operation checks, dirty destination checks, and exact staging scope. This completes **AC-2**, **AC-4**, **AC-6**, and the remaining validation portions of **AC-7**.
4. Add handled failure recovery for projection, state, staging, and commit preparation. Snapshot and restore only refresh managed worktree paths, generated state, and refresh changed index entries for failures conclusively known to precede commit creation. Preserve unrelated staged and worktree changes, validate the three commit outcomes, report ambiguous results in the terminal manual recovery state without history rewriting, and document abrupt termination as outside the v1 guarantee. This completes **AC-9** and the remaining recovery portions of **AC-11**.
5. Complete the normal sync integration tests for the explicit coarse order, later remote fetch, merge or rebase, commit, and push failure, durable refresh success, and no manifest compatibility. Satisfies **AC-1**, **AC-9**, and **AC-10**.
6. Keep all tests in disposable temporary repositories with named table driven unit tests for the JSON contract, path safety, ancestry, tree validation, ownership, exact staging, managed index restoration, commit confirmation, serialization, idempotence, partial failure, sync mode boundaries, and later sync failure. Use the installed Git executable. Satisfies **AC-1** through **AC-11**.

## Consequences

**Positive**:

1. The first implementation has one end to end client path that can be verified against real Git repositories.
2. Source revisions are auditable through exact commits, while movable references advance only forward.
3. Unrelated vault work and source worktree changes remain outside the refresh transaction.
4. The small state document makes ownership and retry behavior visible without introducing a history database.

**Negative / tradeoffs**:

1. Server mode cannot refresh external documentation until a later spec proves its separate process boundary.
2. Operators cannot change managed mappings or remove a source through this feature. Those changes fail until an explicit migration design exists.
3. A source fetch may advance external Git state even when a later source fails. The command reports this and does not attempt rollback.
4. The implementation needs careful temporary file recovery and exact Git staging to protect unrelated changes. Abrupt termination is outside the v1 rollback guarantee and can require manual recovery of refresh managed paths, index entries, or the lock.

**Neutral**:

1. A refresh commit is separate from any later normal sync commit or push.
2. The first slice supports regular files and directories only. Git links, submodules, transforms, indexes, and generated navigation remain outside the projection contract.

## Follow-up

1. Design server mode refresh after the client path is implemented, verified, and its transaction boundary has real use evidence.
2. Design explicit source removal and managed path migration with deliberate deletion semantics.
3. Consider pinned commit selectors as a separate revision feature if a real use case requires them.
4. Consider transforms, indexing, and generated navigation only after direct projection is stable.
