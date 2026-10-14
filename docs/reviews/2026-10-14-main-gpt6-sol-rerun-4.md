# Review, main, 2026-10-14

**Reviewed by**: GPT-6 Sol (author on GPT-6 Luna)
**Scope**: 16 files, uncommitted
**Verdict**: Blocked

## Summary

The change adds tracking for direct submodules, scoped child conflict recovery, grouped parent gitlink recording, and projection source guards. The main flows have substantial disposable repository coverage. One repository identity gap allows child sync and recovery to operate on an unrelated Git worktree placed at a registered submodule path.

## Blockers

### 🔴 Verify the child is the registered Git checkout, `internal/vaultctl/submodule_sync.go:375`

**Problem**: `initializeTrackingSubmodule` accepts any Git worktree whose reported top level equals the resolved child path. A standalone clone placed at that path passes this check. A symlink from the registered path to another repository inside the vault also passes because `submoduleApp` resolves the symlink before comparing paths. Scoped recovery uses the same top level check at line 1401. Neither path verifies that the worktree belongs to the parent's registered gitlink and Git submodule metadata.

**Why it matters**: Normal sync can autosave, reconcile, and push the unrelated repository's `origin`. Scoped continuation or abort can alter an unrelated rebase or merge. These mutations occur before the final recursive checkout can report a problem. This violates the direct registered child boundary and can change or publish the wrong repository.

**Suggested fix**: Verify the child checkout's Git identity against the registered parent submodule, including its expected module gitdir and path, before child mutation or scoped recovery. Reject aliases and standalone repositories at the registered path. Add disposable tests for a standalone repository at that path and an in vault symlink to another repository, asserting that neither repository nor its remote changes.

## Strengths

* Child continuation completes a conflict without pushing and leaves normal sync to select the current target again.
* The grouped parent commit checks staged paths, modes, gitlink objects, and the index baseline before recording child results.

## Test coverage

The new tests cover branch selection, pinning, conflict recovery, grouped gitlinks, partial progress, active selection, and several projection overlap cases. A symlink escape test exists, but it does not exercise an in vault alias or a standalone repository at a registered child path. Tests were not run during this review.
