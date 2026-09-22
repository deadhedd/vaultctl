# 0002. Exclusive ownership for projected destinations

**Date**: 2026-09-21
**Status**: Accepted

## Summary

This decision makes each configured projection destination an exclusive vaultctl area. Before fetching a source, refresh proves that the existing destination exactly matches the previous ownership record and committed projection. Refresh may then remove only proven stale paths, while preserving unrelated vault work and stopping on any uncertainty.

## Context

The existing external documentation refresh projects selected source paths into the vault and records the resolved source commit plus destination roots in generated state. It validates that managed paths are clean, but a destination root does not identify every file and directory that vaultctl may safely remove. A recursive destination can therefore contain content that is not distinguished from stale projected content.

The requested behavior is stricter. Ignored files, untracked files, unexpected empty directories, symlinks, special entries, and other content inside a configured destination must stop refresh before source fetch or vault mutation. At the same time, a source directory can lose files across a forward source revision, and successful refresh must be able to remove those stale projected paths.

The feature must remain inside the existing client sync transaction. It must not introduce automatic adoption, migration, source repository mutation, server refresh, or a new public command. Existing state files with destination roots only cannot prove the complete ownership inventory, so accepting them would silently claim content that may have been added by an operator.

## Requirements

**User stories**:

1. As a vault operator, I want each configured projection destination to be exclusively owned by vaultctl so unexpected vault content cannot be overwritten or deleted by refresh.
2. As a vault operator, I want stale projected files and directories removed when the source directory no longer contains them.
3. As a vault operator, I want unrelated staged and unstaged vault work preserved when ownership validation or refresh fails.
4. As a vault operator, I want older ownership state rejected clearly rather than silently converted into a broader ownership claim.

**Acceptance criteria**:

1. **AC-1**: A normal client `sync` without `.vaultctl/external-docs.json` preserves the existing behavior, does not require `source_root`, and does not run the new ownership scan. Server mode, `sync --continue`, and `sync --abort` do not run external documentation refresh.
2. **AC-2**: Before any configured source fetch, refresh validates every existing projection destination against the previous generated ownership inventory and the committed vault projection. The actual filesystem entry set must contain exactly the recorded owned paths under each destination. An owned path with owned descendants is a directory. An owned path with no owned descendants is a file. Any unexpected regular file, ignored file, untracked file, empty directory, symlink, special entry, missing previously owned path, ownership mismatch, dirty managed path, changed state, unsafe ancestry, or committed projection disagreement fails before source fetch and before vault projection mutation. Destinations are checked in normalized slash path order, then entries in relative bytewise lexical order. The error reports only the first violation and names the destination, relative path, and stable category when a destination applies.
3. **AC-3**: Generated state uses a new explicit version for the complete inventory format. Each source entry records its identifier, resolved full commit object identifier, and the normalized sorted unique path strings of every projected file and materialized directory, including each configured destination root. The owned path set must form a valid tree: duplicate paths are rejected, a file cannot be an ancestor of another owned path, and directory ancestry is derived from descendants. Existing version 1 state that records destination roots only is rejected before source fetch with a clear migration required error. Refresh never infers ownership from the current directory or rewrites legacy state automatically.
4. **AC-4**: First ownership establishment is allowed only when every new source destination is absent. Existing source identifiers must retain their recorded destination roots. Removing a source identifier or changing the owned destination roots fails without claiming or deleting vault content. A source mapping root that is absent from the newly resolved source commit fails. A source directory mapping that still exists may lose selected descendants, and those previously owned stale descendants may be removed.
5. **AC-5**: After source plans are prepared, refresh computes the next complete ownership inventory from the resolved source commit tree. A mapping may change between a regular file and a regular directory only when the previous destination subtree is fully proven owned. Replacement follows the required safe sequence: remove old only files deepest first, remove old only directories deepest first only when empty, resolve proven owned type conflicts, create new directories shallowest first, write new files in sorted order, and write generated state last. All removal is restricted to the union of prior and next inventories. It never removes an arbitrary configured destination subtree.
6. **AC-6**: Before the first source fetch, refresh captures an in memory baseline for the current operation consisting of vault `HEAD`, manifest bytes, generated state bytes, managed index and worktree state, and the canonical prior ownership scan result. Immediately before the first projection write, it revalidates every baseline value. Any difference fails before mutation. The scan is complete and synchronous, has no feature specific entry or byte limit, and fails closed on traversal or permission errors. No persisted boundary object or control file is created.
7. **AC-7**: A successful refresh writes the next projection and version 2 generated state. The replacement authorization set is the union of prior and next ownership inventories. The staging set contains only exact tracked file additions, modifications, and deletions produced by refresh, plus generated state when it changes. The expected commit changed path set is exactly the refresh staging set. Directories are filesystem concepts only and are not independently staged or expected as Git commit paths. The refresh creates one isolated commit when content or state changed, with no unrelated staged or unstaged vault changes. A repeated refresh at the same source commits creates no additional change.
8. **AC-8**: A failure before commit invocation is recoverable. A commit command failure followed by a conclusively unchanged `HEAD` is recoverable. For recoverable failures, refresh restores the prior projection from the already validated prior committed projection and ownership inventory, restores generated state to its prior bytes, and restores only the pre refresh index entries for exact refresh files. An advanced `HEAD` is accepted only when the existing parent, changed path, state, and prepared tree checks confirm the expected refresh commit. If `HEAD` cannot be inspected, advances unexpectedly, or the outcome is otherwise ambiguous, refresh enters the existing terminal manual recovery state without resetting history, restoring paths, or creating a compensating commit. Unrelated staged and unstaged vault work remains untouched.
9. **AC-9**: The refresh keeps the existing explicit Git boundaries. It reads source content only from the resolved commit tree, does not change source working trees or source Git configuration, does not guess remotes or branches, and keeps all source fetch progress and diagnostics governed by the existing refresh behavior.

## Options considered

### Option 1: Extend the existing refresh with a complete ownership inventory

Keep the existing manifest, generated state file, client sync entry point, Git process boundary, and transaction recovery. Extend generated state with every projected file and materialized directory, scan the old boundary before source fetch, and apply changes by exact path sets.

**Pros**:

1. Reuses the existing tested refresh transaction and preserves the client only scope.
2. Makes stale deletion depend on durable prior ownership rather than the newly fetched source tree.
3. Keeps recovery state small because exclusive ownership narrows the paths that may be changed.

**Cons**:

1. Existing version 1 state needs an explicit future migration or recreation.
2. Every refresh performs a complete synchronous filesystem scan of each managed destination.

### Option 2: Add a separate ownership inventory file

Keep the existing source state unchanged and add another tracked file containing the complete path inventory and ownership proof.

**Pros**:

1. Separates source revision state from filesystem ownership state.
2. Allows independent future evolution of the two records.

**Cons**:

1. Adds another tracked control file, parser, consistency relationship, and commit predicate.
2. Does not remove the need to reject or migrate existing incomplete ownership state.

### Option 3: Infer ownership from the newly fetched source tree

Treat every path named by the new source projection as safe and remove other content below the destination during replacement.

**Pros**:

1. Requires little additional durable state.
2. Makes the desired output easy to calculate.

**Cons**:

1. It cannot prove that a path absent from the new source was created by vaultctl and is safe to delete.
2. It risks deleting ignored or untracked operator content before the source projection is known to be safe.

## Decision

**Chosen option**: Option 1: Extend the existing refresh with a complete ownership inventory.

Refresh will treat every configured destination subtree as exclusively owned. Version 2 generated state will record every projected file and materialized directory. The old inventory and committed projection are validated before source fetch, validated again immediately before writing, and used to authorize exact path replacement and stale cleanup.

**Implementation skills**: `golang-testing` (`samber/cc-skills-golang`, `.agents/skills/golang-testing/`) · `golang-safety` (`samber/cc-skills-golang`, `.agents/skills/golang-safety/`)

## Rationale

The complete inventory is the smallest durable proof that supports both strict rejection and stale cleanup. A scan based on the newly fetched source would confuse absence in the new source with permission to delete current vault content. Validating the previous inventory before fetch gives the old state and committed projection authority over what can be replaced.

Extending the existing state file keeps the change inside the established refresh transaction. A separate inventory file would add another control path without improving the ownership proof. Rejecting legacy state is intentionally conservative because root only state cannot distinguish projected content from later operator content. An explicit migration can make that choice with a separate reviewable contract.

## Feature design

**Data model sketch**:

| Entity | Required fields | Relationship and constraints |
|---|---|---|
| Manifest source | `id`, `repository`, `remote`, `reference`, one or more mappings | One source has one or more mappings. Source identifiers are unique. |
| Manifest mapping | `source`, `destination` | Each mapping belongs to one manifest source. Destination roots do not overlap, including case insensitive aliases. |
| State source | `id`, `resolved_commit`, `owned_paths` | One state source corresponds to one manifest source. State identifiers are unique and must be known to the manifest. |
| Owned path | normalized relative path string | Each path belongs to one state source. Paths are sorted and unique and form a valid tree. Directory ancestry is derived from descendants. A path with descendants is a directory, a path without descendants is a file, and a file cannot be an ancestor of another path. The set includes every projected file, every materialized directory, and each destination root. |

The manifest remains version 1 and operator authored. Generated state becomes version 2. `resolved_commit` is a full commit object identifier. A directory that has no projected files is not materialized and is not recorded. Parent directories created to contain projected files are recorded. Existing ancestors above a configured destination are never recorded or claimed. The state file is tracked, deterministic, and unchanged relative to `HEAD` before refresh begins. No content hashes, entry kinds, or mapping root fields are stored in generated state.

**State transitions**:

1. No manifest: normal client sync behavior remains unchanged.
2. Manifest with no state: each destination must be absent. A successful refresh creates version 2 state and one isolated commit.
3. Version 2 state at a recorded commit: the old ownership inventory and committed projection are validated before fetch. Under each destination, the committed Git tree must contain exactly the file paths implied by the inventory, all as regular blobs. Extra or missing committed files fail. The managed destination must be clean in both the worktree and index relative to `HEAD`. The fetched source commit must remain the same or be a descendant under the existing forward check.
4. Version 1 state or invalid version 2 state: refresh stops before source fetch and reports migration or validation failure. It does not repair state.
5. A valid forward source update: refresh computes a new inventory, removes only old paths absent from the new inventory, writes the new projection, and commits the new state.
6. A failed pre commit operation: refresh restores the old inventory and projection while preserving unrelated vault work. An uncertain commit result uses the existing manual recovery path.

**API surface**:

| Endpoint | Method | Key inputs | Key outputs | Auth | Key errors |
|---|---|---|---|---|---|
| `vaultctl sync` | CLI action | Active client configuration, tracked manifest, local vault Git state, source repositories named by the manifest | Ownership validation result, source progress, resolved commits, projection result, refresh commit result, existing sync result | Existing local user with access to the configured vault and source repositories | Legacy state, unexpected path, projection mismatch, missing source mapping, dirty managed path, unsafe boundary, fetch failure, commit failure, manual recovery |

There is no new public refresh command, flag, server action, or migration action in this feature. Refresh remains an internal phase of a new normal client `sync`. Existing commit subject, lock, source fetch, automatic save, and later remote synchronization behavior remain governed by spec 0001.

**Value sourcing**:

| Action | Value produced or displayed | Source |
|---|---|---|
| Load manifest | Source identifiers, source paths, destination roots, remotes, and branch references | Tracked `.vaultctl/external-docs.json` |
| Load prior ownership | State version, source identifier, prior commit, complete owned path set | Tracked `.vaultctl/external-docs-state.json`, unchanged from `HEAD` |
| Validate committed projection | Exact prior file paths, regular blob entries, and absence of extra or missing files | Prior ownership inventory plus the installed Git committed tree at `HEAD`; managed worktree and index cleanliness are compared with `HEAD` |
| Validate current destination | Actual files, directories, ignored files, symlinks, special entries, and relative paths | Recursive filesystem scan below each prior destination root |
| Resolve source content | New commit and selected tree entries | Existing explicit remote and branch fetch, immediate `FETCH_HEAD`, and the resolved commit tree |
| Build next inventory | New files, materialized directories, and destination roots | Selected regular files and directories in the resolved source commit tree |
| Capture refresh baseline | Vault `HEAD`, manifest bytes, state bytes, managed index and worktree state, and prior scan result | In memory values captured before source fetch for this refresh only |
| Authorize replacement | Paths allowed to remove or write | Replacement authorization set, the union of the prior and next ownership inventories, after both validation passes |
| Stage refresh | Exact file additions, modifications, and deletions | Staging set derived from the prepared old and new file inventories plus generated state when it changes |
| Confirm refresh commit | Exact changed file paths and prepared projection bytes | Expected commit changed path set, the exact staging set, captured pre refresh `HEAD`, post commit `HEAD`, and the prepared plan |
| Report violation | Destination, relative path, category, and operation phase | Deterministic sorted filesystem scan and named manifest or state source |

**Key invariants**:

1. The old ownership inventory is authoritative for the pre fetch replacement boundary. The newly fetched source tree cannot authorize deletion of an existing vault path.
2. Actual paths below every existing destination root must exactly equal the prior recorded paths. No unexpected file, ignored file, untracked file, empty directory, symlink, special entry, or missing prior path is tolerated. Destinations and entries use normalized slash paths and bytewise lexical ordering. Only the first violation is reported, using stable categories for unexpected file, unexpected directory, symlink, special entry, missing owned path, committed projection mismatch, and dirty managed path. State and Git failures use their relevant control path or `.`.
3. The owned path set is a valid tree. An owned path with descendants is a directory. An owned path without descendants is a file. Duplicate paths and a file ancestor of another path are invalid. Directory ancestry is not independently persisted in Git.
4. Under every managed destination, the committed Git tree must contain exactly the file paths implied by the prior inventory, all as regular blobs. Extra or missing committed files are an ownership mismatch. The managed destination must be clean in both the worktree and index relative to `HEAD`.
5. Version 1 root only state is not a complete ownership proof. It is rejected before source fetch and is never expanded from the filesystem.
6. First refresh may establish a new source only when every destination root is absent. Existing ancestors above a destination may exist and remain ordinary vault content. Vaultctl creates the destination root and required descendants, and records only the root and materialized paths at or below it. Existing source identifiers retain their destination roots. Source removal and destination root changes remain outside this feature. Empty source directory mappings are unsupported.
7. A missing selected source mapping root fails. A surviving source directory may remove stale owned descendants. A file and directory mapping type change is allowed only when the entire old subtree is proven owned.
8. Replacement is path precise and follows the specified safe mutation sequence. It never recursively removes an arbitrary configured destination root.
9. The complete ownership scan runs synchronously before source fetch and immediately before the first projection write. It has no feature specific entry or byte limit and fails closed on filesystem errors. The immediate second validation compares the in memory baseline values exactly.
10. The replacement authorization set, staging set, and expected commit changed path set are distinct in memory sets. Directories are not staged or expected as Git commit paths. The refresh commit contains only the expected file paths and generated state. Unrelated staged and unstaged vault work remains untouched.
11. The existing lock serializes vaultctl refreshes. The second ownership scan protects the boundary from changes made by other processes between the initial scan and projection. It does not claim to eliminate races that occur after the second scan begins mutation.

**Security model**:

The local operator is the only actor. Ownership is authorized by tracked version 2 state, the committed vault projection, the explicit manifest, and the current filesystem scan. No state or manifest repair, adoption, privilege change, remote change, or source worktree mutation is automatic. Symlinks and special entries are rejected rather than followed. The existing canonical vault, control directory, source root, Git cleanliness, unfinished operation, and lock checks remain in force. This feature introduces no regulated data, credentials, external service account, or new trust boundary.

**Configuration required**:

No new configuration or credentials are required. Existing client `source_root`, explicit manifest remotes and branch references, vault permissions, and source repository permissions remain the only inputs.

**Critical test scenarios**:

1. A version 2 one file projection scans its previous inventory and committed tree before fetch, then refreshes an unchanged source without an extra commit, verifying **AC-1**, **AC-2**, **AC-3**, and **AC-7**.
2. An ignored file, untracked file, unexpected empty directory, symlink, special entry, or missing prior file appears below a managed destination. Refresh reports the first sorted violation and leaves source `FETCH_HEAD`, vault `HEAD`, worktree, and index unchanged, verifying **AC-2**.
3. A previous inventory and committed projection disagree because a prior file was manually committed away. Refresh stops before source fetch and reports the mismatch, verifying **AC-2** and **AC-3**.
4. A source directory loses one projected file and a nested directory becomes empty. Refresh removes only the proven stale paths, records the new inventory, and commits the exact result, verifying **AC-4**, **AC-5**, and **AC-7**.
5. A mapping changes from a file to a directory or a directory to a file. Refresh succeeds only with a fully proven old subtree and never removes a new unexpected path, verifying **AC-5**.
6. Another process changes a managed destination after the first scan. The second scan fails before projection writes, verifying **AC-6**.
7. A projection, staging, or commit precondition fails after writes begin. The validated prior committed projection and ownership inventory restore managed paths, prior state bytes restore generated state, and pre refresh index entries restore only exact refresh files while unrelated staged and worktree changes remain, verifying **AC-7** and **AC-8**.
8. Commit confirmation becomes uncertain. Refresh reports the existing manual recovery information and does not reset `HEAD`, restore paths, or create another commit, verifying **AC-8**.
9. A version 1 root only state file exists. Refresh fails before source fetch with migration required and makes no ownership claim, verifying **AC-3**.
10. A configured source mapping root disappears from the resolved source commit. Refresh fails rather than treating it as an empty projection, verifying **AC-4**.

## Build plan

The repository uses a Tracer Bullet approach. The build should keep one real client sync path through state parsing, old boundary validation, source resolution, exact projection, commit confirmation, and recovery before broadening directory and concurrency cases.

- [x] Introduce version 2 generated state parsing, deterministic serialization, path only valid tree validation, and explicit rejection of version 1 root only state. Add focused table driven parser tests and preserve the existing manifest contract, satisfying **AC-3**.
- [x] Build the thin one file path around a pre fetch ownership scan. Validate the prior inventory against the exact committed tree predicate and filesystem, report deterministic violations, capture the in memory baseline, and keep the no manifest and sync boundary behavior unchanged, satisfying **AC-1**, **AC-2**, and **AC-6**.
- [x] Extend projection planning to derive complete file and directory inventories from the resolved source tree. Implement the replacement authorization set, the required safe mutation order, stale descendant removal, missing source root refusal, and file or directory type changes, satisfying **AC-4** and **AC-5**.
- [x] Add immediate pre write baseline revalidation and keep replacement authorization, staging, and expected commit changed paths as distinct exact sets. Preserve unrelated staged and unstaged work, satisfying **AC-6** and **AC-7**.
- [x] Adapt pre commit restoration and post commit confirmation to the validated prior committed projection, path only inventory, exact staging set, and explicit outcome classification. Add failure injection and manual recovery tests for handled and uncertain outcomes, satisfying **AC-8**.
- [x] Broaden disposable repository integration tests across multiple mappings and sources, ignored content, empty directories, special entries, type changes, legacy state, source failure after earlier fetch, and normal sync continuation and abort boundaries. Run the project Go formatting, test, and vet commands, satisfying **AC-1** through **AC-9**.

## Consequences

**Positive**:

1. Refresh can remove stale projected content without granting the new source tree permission to delete arbitrary current vault content.
2. Unexpected ignored, untracked, empty, linked, and special content fails before source fetch and before projection mutation.
3. Recovery can operate on exact owned paths and continues to preserve unrelated vault work.

**Negative / tradeoffs**:

1. Every refresh performs a complete synchronous scan of every managed destination and keeps an in memory baseline across source fetch.
2. Existing version 1 state cannot refresh until a separate explicit migration or recreation path is designed.
3. Operators cannot place hand maintained files inside a configured projection destination. A different destination is required.

**Neutral**:

1. The manifest remains operator authored and source repositories remain authoritative.
2. No new command, configuration, dependency, server capability, or secret is introduced.

## Follow-up

1. Design a separate explicit migration or adoption workflow for version 1 root only state. It must establish complete ownership without silently claiming operator content.
2. Keep source removal, destination root changes, and repository lifecycle commands in a later decision, as scoped by spec 0001.

## Migration plan

**Strategy**: no automatic migration in this slice

**Phases**:

1. Ship version 2 state and reject version 1 root only state before source fetch with an actionable error.
2. Design and verify a separate migration or recreation workflow before allowing older vaults to refresh under exclusive ownership.

**Rollback**: Revert the implementation change. No legacy state or vault content is rewritten by this feature.

**Risks**: Existing vaults with version 1 state will require operator action before refresh resumes. Any future migration must distinguish projected content from content added after the older refresh implementation.
