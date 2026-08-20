# Changelog

All notable changes to **vaultctl** are documented here.

This changelog was reconstructed from the repository's Git history. The project currently has a compact history, so entries are grouped by the dates of substantive changes rather than assigning version numbers that were not used in the repository.

## Unreleased

No notable changes recorded yet.

## 2026-07-16 — Initial implementation

### Added

- Added `vaultctl`, a standalone Go command-line tool for managing Git-backed Obsidian vaults.
- Added separate **client** and **server** operating modes for normal Git clones and split bare-repository/worktree layouts.
- Added JSON configuration with platform-appropriate default paths and strict validation of unknown or mode-inappropriate fields.
- Added `status`, `diff`, and `log` commands for routine repository inspection.
- Added `save` to stage and commit local vault changes without performing network or history-integration operations.
- Added client-side `sync` with explicit upstream handling, fetch, fast-forward, push, and divergence resolution.
- Added `sync --merge` as an alternative to rebasing diverged histories.
- Added `sync --continue` and `sync --abort` for explicit conflict recovery.
- Added a raw `git` escape hatch that passes arguments directly to the installed Git executable without shell interpolation.
- Added `doctor` for configuration, executable, repository, and upstream validation.
- Added optional OpenBSD `doas` support for running server-mode Git operations as a configured service user.
- Added example client and server configuration files.
- Added automated tests using disposable temporary repositories.

### Safety

- Refuses to force-push.
- Refuses to guess or create remotes or upstream branches.
- Does not automatically choose conflict resolutions such as `ours` or `theirs`.
- Does not silently discard local changes.
- Stops on failed Git operations and returns nonzero for unsafe or incomplete states.
- Keeps server-side save operations separate from client-side synchronization.
