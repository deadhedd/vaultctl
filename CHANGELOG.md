# Changelog

All notable changes to **vaultctl** are documented here.

This changelog was reconstructed from the repository's Git history. The project currently has a compact history, so entries are grouped by the dates of substantive changes rather than assigning version numbers that were not used in the repository.

## Unreleased

### Added

- Added client sync refresh for selected documentation paths from named external Git repositories, using a tracked `.vaultctl/external-docs.json` manifest and generated commit state.
- Added bounded projection validation, source reference checks, ownership tracking, isolated refresh commits, handled recovery, and an atomic refresh lock.

### Changed

- Changed normal client sync to refresh external documentation before saving unrelated vault changes and continuing normal upstream synchronization. Server mode and sync continuation or abort do not run the refresh.
- Expanded `sync --abort` to abort an unfinished revert and to report the raw Git recovery command for an unsupported sequencer state.
- Changed external documentation refresh to require a version 2 ownership inventory, reject legacy state and unexpected destination content before source fetch, and remove stale paths only from proven ownership records (see spec 0002).
- Changed refresh staging and recovery to use exact projected file paths, preserving unrelated staged and unstaged vault work and reporting manual recovery when commit confirmation is uncertain (see spec 0002).

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
