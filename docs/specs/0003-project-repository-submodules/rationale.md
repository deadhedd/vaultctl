# Rationale: Project repository submodules · spec 0003

## Context

The vault is a Git superproject (the parent repository that records other repositories) with an existing client sync flow. That flow saves local vault changes with `git add -A`, fetches and reconciles the configured upstream, and pushes when needed. Git records submodule URLs in `.gitmodules` and each selected submodule commit as a gitlink (a commit reference stored in the parent tree). The automatic save must not turn an unstaged submodule checkout change into a new parent gitlink commit.

After a vault sync, a submodule may still be absent or checked out at a commit other than the one recorded by the vault. Operators need the project files present at the exact commits selected by the parent repository. A dirty submodule worktree must keep its local edits safe, and a partial recursive update must remain clear and retryable.

This feature is separate from external documentation projection. That feature continues to reject submodules inside selected source trees. Server mode continues to reject sync as it does today.

## Options considered

### Option 1: Use native Git for lifecycle and client sync

Keep explicit add, update, and removal in the existing Git escape hatch. After a successful normal client sync, run one recursive Git update at the commits recorded by the parent repositories.

**Pros**:

* Reuses Git's submodule format, checkout rules, URL handling, and worktree safety.
* Keeps the new vaultctl behavior inside the existing client sync path.
* Requires no application state, rollback protocol, or public command surface.

**Cons**:

* Git's diagnostics and partial progress are visible to operators, and a failed update may follow a successful parent push.
* Git's native active-submodule selection and partial recursive progress determine which worktrees are updated before an error.

### Option 2: Add dedicated vaultctl lifecycle commands

Add vaultctl commands for add, initialize, update, and remove, with custom validation around Git operations.

**Pros**:

* The CLI could give lifecycle actions a consistent help surface and tailored messages.
* It could enforce additional path and repository rules before invoking Git.

**Cons**:

* It duplicates operations already available through the Git escape hatch.
* It creates new command behavior and validation rules without a requirement that Git cannot meet.

### Option 3: Keep all submodule updates manual

Document native Git lifecycle operations, but leave submodule initialization to operators after each vault sync.

**Pros**:

* Adds no behavior to the existing synchronization flow.
* Leaves all update timing under direct operator control.

**Cons**:

* A normal vault sync can finish while project worktrees remain absent or at a different commit.
* Operators must remember and repeat a second command after synchronization.

## Rationale

The existing Git escape hatch already exposes the native operations needed to add, initialize, update, and remove submodules. Git's `.gitmodules` file and gitlinks already provide the tracked configuration and exact target commits. Adding a vaultctl model or lifecycle commands would create a second source of truth for behavior that Git owns. The project rule against automatic Git configuration changes also favors preserving local URL overrides. A broken local URL should fail visibly instead of being silently rewritten.

Normal sync must update submodules only after the parent repository reaches its final synchronized commit, including after a required push. Its autosave must preserve index decisions for explicitly staged gitlinks and must not stage an unstaged worktree gitlink change. The explicit checkout mode selects recorded commits even when local update strategies request merge or rebase, while omitting force lets Git protect local work. If Git returns an error after making partial progress, those completed updates remain and vaultctl reports the parent success and incomplete submodule phase so the operator can retry.
