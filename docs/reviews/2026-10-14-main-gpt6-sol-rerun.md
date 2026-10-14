# Review, main, 2026-10-14

**Reviewed by**: GPT-6 Sol (author on GPT-6 Luna)
**Scope**: 13 files, uncommitted
**Verdict**: Blocked

## Summary

The change extends client sync to track direct submodules, preserve pinned behavior, record changed gitlinks together, and protect documentation projection sources. The child reconciliation and conflict recovery paths are well covered, but the final recursive checkout can act on parent metadata that changed during the follow up while using an earlier protection and selection snapshot.

## Blockers

### 🔴 Revalidate parent metadata before final checkout, `internal/vaultctl/submodule_sync.go:86`

**Problem**: `syncSelectedSubmodules` loads `selected` and `protection` once, then `syncParentFollowup` fetches and may rebase or merge a concurrent parent commit. The final `validateRecursiveCheckoutPaths(selected, protection)` and `git submodule update` still use the old entries and source protection. For example, a concurrent parent commit can add a projection source that points at a selected pinned submodule and change its gitlink. The follow up can incorporate both changes without a conflict; the final checkout then updates that now protected source worktree because the earlier `protection.worktrees` did not include it. A newly added active submodule can also be skipped while overall success is reported.

**Why it matters**: The projection source read only guarantee in AC-13 can be broken, and AC-14 can report full success without processing the final parent tree's selected children.

**Suggested fix**: After parent follow up, reload the committed/indexed submodule metadata, active selection, and projection source identities; validate them against the final parent tree before recursive checkout. If the follow up introduced paths or gitlink changes that this sync has not processed safely, fail with an incomplete phase result and let normal sync retry. Add a disposable test where the parent remote advances during follow up with a new projection source or submodule.

## Strengths

- Child branch selection fetches one explicit ref and validates `FETCH_HEAD` before autosaving local work.
- Scoped child continuation completes the Git operation without pushing, and the existing tests check that normal sync later performs the push and parent recording.

## Test coverage

The tests cover explicit and default branches, pin validation, child conflicts, projection endpoint aliases, and grouped gitlink safeguards. They do not cover a parent follow up that incorporates changed `.gitmodules`, gitlinks, or projection manifest data before the final recursive checkout. This is the gap behind the blocker above. Review only; tests were not rerun.
