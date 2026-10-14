# Review, main, 2026-10-14

**Reviewed by**: GPT-6 Sol (author on GPT-6 Luna)
**Scope**: 18 files, uncommitted
**Verdict**: Changes requested

## Summary

The change adds tracking submodule synchronization, pinned checkout, grouped parent gitlink recording, scoped child recovery, and projection source guards. The implementation addresses the earlier final checkout, SCP remote, and nested metadata findings. One critical failure and recovery path remains untested.

## Major

### 🟠 Cover failed parent follow up after child pushes, `internal/vaultctl/submodule_sync.go:85`

**Problem**: The new grouped commit path calls `syncParentFollowup` after child pushes, but no test makes that follow up fail during fetch, reconciliation, or push. The concurrent parent tests exercise a successful follow up followed by final checkout refusal. The explicit branch test exercises a successful grouped push.

**Why it matters**: AC-11 requires the grouped commit and completed child pushes to survive a parent follow up failure and remain recoverable through the existing parent flow. This is a new error boundary after multiple durable mutations, and its retention and retry behavior has no direct test.

**Suggested fix**: In disposable repositories, complete a child push and grouped parent commit, make the parent follow up fail or conflict, then assert that both commits remain, no final checkout or overall success is reported, and parent recovery or a later normal sync completes without losing or repeating child work.

## Strengths

- Child sync validates one fetched branch commit before autosaving and uses an explicit nonforce push target.
- Final checkout reloads parent metadata and projection protections, then checks the selected paths and expected gitlinks before recursive Git checkout.

## Test coverage

The tests cover tracking and pinned paths, default branch selection, child rebase and merge, grouped gitlink safeguards, partial child progress, scoped child recovery, source path and remote protection, and concurrent parent metadata changes. The post-child parent follow up failure and recovery boundary remains uncovered. Review only; tests were not rerun.
