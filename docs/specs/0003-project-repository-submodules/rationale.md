# Rationale: Project repository submodules · spec 0003

## Context

The vault already synchronizes a parent Git repository and can initialize selected submodules at the commits recorded by their parents. The parent autosave has a narrow rule for gitlinks (the commit pointers stored for submodules): it preserves staged operator choices and does not turn unstaged child checkout movement into a parent commit.

The extension makes direct project submodules track by default. Each child may have safe local work, an upstream branch, a conflict, or a push failure. A child can finish before a later child fails, so the result needs to remain recoverable across another parent sync. The parent also needs to record child commits without replacing the existing gitlink safeguards.

The external documentation feature reads selected source commit trees and fetches source Git metadata. Its source worktrees and upstreams must remain read only. Server mode does not synchronize and remains outside this feature.

## Options considered

### Option 1: Extend the current client sync and recovery flow

Reuse the existing Git runner and the parent's autosave, fetch, history classification, rebase or merge, push, and conflict recovery functions. Apply them to each direct tracking child, then record all changed child gitlinks together after every selected child succeeds. Store the tracking default and pin opt out in `.gitmodules`. Scoped child continue completes Git's operation but leaves pushing to the next normal sync, which reselects and validates its target. (basis: `internal/vaultctl/sync.go`, spec 0003, `internal/vaultctl/AGENTS.md`)

**Pros**:

* Reuses existing safety checks and user recovery patterns.
* Keeps tracked state in Git's existing parent metadata without a separate vaultctl manifest.
* Makes child successes durable before moving to the next child, while keeping parent recording atomic across the selected set.

**Cons**:

* A child can push successfully before the parent records its gitlink.
* A second parent reconciliation is needed after the grouped link commit.
* Direct child path recovery adds syntax to the existing sync command.

### Option 2: Use only native Git remote submodule updates

Ask Git to update all selected children from remote branches and retain native checkout behavior for pins. (basis: spec 0003, Git's existing submodule interface)

**Pros**:

* Git owns branch lookup and recursive update selection.
* The vaultctl code adds fewer child history operations.

**Cons**:

* It does not reuse vaultctl's safe child autosave, history classification, push, and conflict recovery behavior.
* Native checkout alone does not provide the required grouped parent recording and retry contract.

### Option 3: Add a separate vaultctl submodule manifest and recovery ledger

Store mode, branch, and progress for each child in a new manifest and persist unfinished recovery information outside Git's operation state. (basis: existing external documentation manifest and state model in specs 0001 and 0002)

**Pros**:

* The feature could express per child mode and progress without relying on `.gitmodules`.
* A ledger could make every partial phase explicit.

**Cons**:

* It duplicates paths and branch data already represented by Git.
* It adds migrations and another state format to validate and recover.
* Git already keeps the rebase and merge state needed for targeted child recovery.

## Rationale

The current sync code already supplies the decisions that matter for each repository: it checks for unfinished operations and conflicts, saves safe work, fetches, classifies history, rebases by default or merges on request, pushes without force, and exposes manual recovery. Reusing that path for a child keeps the extension close to the accepted Feature 7 behavior and avoids a second reconciliation implementation. (basis: `internal/vaultctl/sync.go`, `internal/vaultctl/app.go`, `internal/vaultctl/AGENTS.md`)

The tracked `.gitmodules` entry is the smallest durable mode source. Tracking is the default as requested. A concrete `branch` value names the intended branch; otherwise Git's remote symbolic `HEAD` supplies the default. The explicit `vaultctl-pinned = true` key is only an opt out. This avoids both guessed branch names and a second manifest. (basis: `docs/scope/scope.md`, spec 0003, `AGENTS.md`)

Pushing each completed child before continuing makes partial progress durable. Waiting until every selected child succeeds before creating the parent link commit keeps the parent's new links together. The retry compares every successful child `HEAD` with the parent index gitlink after initial parent sync, so earlier successful children are still recorded if unchanged during retry. A retry must leave initialized tracking worktrees at their current commit and fetch them again, while the existing autosave continues to protect intentional gitlink changes. (basis: spec 0003, `internal/vaultctl/AGENTS.md`, the project rule to stop on unsafe or incomplete repository states)

Scoped continuation deliberately does not push. Git keeps the operation state needed to complete the conflict, but it does not durably identify the original branch and push destination. A normal sync afterward resolves and fetches the currently selected target before it pushes. This avoids a separate recovery ledger while keeping branch selection and push validation in the normal sync path. (basis: existing parent `sync --continue` recovery shape and the engineer's recovery decision)

Projection remains a separate read only operation. Its fetch and tree read path is not reused for child sync. Canonical source worktree path overlap blocks checkouts and child recovery. A tracking push target that matches a configured projection source remote is rejected before the child autosave or continuation can create a commit, and again before a push. Git URL rewrites are resolved and local endpoints are canonicalized before comparison. (basis: specs 0001 and 0002, the engineer's projection boundary decision)

## References

**Project sources**:

* `AGENTS.md` and `internal/vaultctl/AGENTS.md`, repository safety and sync boundaries
* `docs/scope/scope.md`, Feature 7 extension intent
* `docs/specs/0001-external-documentation-refresh.md` and `docs/specs/0002-exclusive-ownership-projected-destinations.md`, projection source behavior
* `docs/specs/0003-project-repository-submodules/index.md`, existing lifecycle, gitlink, and recovery behavior
* `internal/vaultctl/sync.go`, existing synchronization and recovery flow
* `golang-testing` and `golang-safety`, project implementation skills

**Practices & standards**:

* Fail closed when a Git target or repository state cannot be identified reliably.
* Preserve local work and make partial synchronization retryable.
