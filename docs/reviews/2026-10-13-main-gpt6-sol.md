# Review, main, 2026-10-13

**Reviewed by**: GPT-6 Sol (author on GPT-6 Luna)
**Scope**: 12 files, uncommitted
**Verdict**: Approve with nits

## Summary

The change adds native recursive submodule checkout after successful client parent synchronization and preserves staged gitlink decisions during sync autosave. The implementation follows the accepted phase ordering, keeps server and recovery behavior separate, and has substantial disposable repository coverage. One URL override test checks a different configuration value from the one required by the spec's verification scenario.

## Minor

### 🟡 Exercise the initialized submodule's URL override, `internal/vaultctl/sync_test.go:582`

**Problem**: This test sets and later checks `submodule.project.url` in the parent repository. The accepted verification scenario calls for changing `remote.origin.url` in the initialized `client/project` repository and confirming that normal sync retains and uses it.
**Why it matters**: The current assertion can pass even if a future sync change rewrites an initialized submodule's own remote URL or fetches from the wrong location. The parent setting and the initialized repository's remote are different Git configuration values.
**Suggested fix**: Add a disposable repository case that sets `remote.origin.url` inside the initialized project to a distinct usable fixture URL, requires a fetch during the update, and checks that the URL remains unchanged afterward.

## Strengths

- The normal sync paths call one common submodule phase only after the required parent fetch, integration, and push have succeeded.
- The autosave tests cover staged additions, removals, commit changes, unstaged checkout drift, and a second sync after a removed worktree remains on disk.

## Test coverage

The tests exercise parent history paths, active selection, recursive pins, explicit lifecycle operations, dirty checkout failure, partial update and retry, and server and recovery boundaries with disposable Git repositories. The initialized submodule's own URL override is the one remaining local coverage gap. Tests were not run during this review.
