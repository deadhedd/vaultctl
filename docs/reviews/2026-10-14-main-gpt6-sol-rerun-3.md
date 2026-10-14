# Review, main, 2026-10-14

**Reviewed by**: GPT-6 Sol (author on GPT-6 Luna)
**Scope**: 16 files, uncommitted
**Verdict**: Changes requested

## Summary

The change adds tracking by default for direct submodules, scoped conflict recovery, grouped parent gitlink commits, and projection source guards. The normal and retry paths have substantial repository level coverage. The source remote guard can use stale projection endpoints at the moment of a child push, and two new recovery and URL branches lack focused tests.

## Major

### 🟠 Reload projection source endpoints before a child push, `internal/vaultctl/submodule_sync.go:164`
**Problem**: The child `pushGuard` closes over `protection`, which was loaded once before any pinned checkout or child operation. `push()` rechecks the child's effective push URLs, but compares them with that old set of source endpoints. A child commit hook, an earlier child operation, or another local process can change a configured projection source remote between the initial load and the push.
**Why it matters**: A child push can target an endpoint that has since become a projection source remote, violating AC-13's read only source boundary. The initial URL comparison still passes because it uses the former source configuration.
**Suggested fix**: Rebuild the source protection set when guarding each child commit and push, and compare the current effective URLs on both sides immediately before those operations. Add a disposable repository case that changes a source remote after the initial protection load and confirms the push is refused.

### 🟠 Exercise merge continuation through scoped recovery, `internal/vaultctl/submodule_sync.go:1449`
**Problem**: The new merge continuation path calls `git commit --no-edit`, but the added end to end recovery test continues only a rebase. The merge case tests abort, and the successful merge test has no conflict.
**Why it matters**: AC-8 requires resolved merge conflicts to complete through `sync --continue --submodule PATH` without a push. This distinct branch can fail because of Git merge state, staging, or commit behavior without any configured test detecting it.
**Suggested fix**: Add a disposable child merge conflict case that resolves the conflict, invokes scoped continuation, checks the resulting merge commit, and confirms neither the child remote nor parent changed until a later normal sync.

### 🟠 Cover file URL source aliases in the protection tests, `internal/vaultctl/submodule_sync.go:1006`
**Problem**: The new `file:` URL parsing and canonicalization branch has no test. The endpoint tests cover a plain relative path, effective URL rewriting, multiple push URLs, and SCP syntax, but never compare a `file://` URL with an equivalent local path or symlink.
**Why it matters**: AC-13 explicitly includes `file://` and symlink aliases in the read only boundary. An error in this branch could let a child push to a projection source remote while all current endpoint tests pass.
**Suggested fix**: Add named cases for equivalent `file://` and local path endpoints, including a symlink alias, and verify that the child push guard refuses them.

## Strengths

- The sync phase retains successful child work across a later failure and records changed gitlinks only after every selected tracking child succeeds.
- The tests use disposable repositories to check parent follow-up revalidation, initialized child URL preservation, and retry after a failed parent push.

## Test coverage

The added tests cover normal tracking, branch selection failures, active selection, pins, partial progress, grouped commits, rebase continuation, and several source protection cases. Scoped merge continuation and `file://` source alias matching remain untested. This review inspected source and tests only; it did not run tests.
