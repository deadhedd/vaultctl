# vaultctl implementation

## Overview

This package contains the command line behavior for vaultctl. It parses commands, validates JSON configuration, runs Git in client or server mode, saves changes, and synchronizes client histories. Its tests create temporary Git repositories to exercise observable command behavior and recovery paths.

## Key files

| File | Owns |
|---|---|
| `app.go` | Command dispatch, save behavior, doctor checks, and user facing errors |
| `cli.go` | Global option parsing, command validation, and help text |
| `config.go` | Platform config paths, strict JSON decoding, and mode validation |
| `git.go` | The external Git and OpenBSD `doas` process boundary |
| `sync.go` | Upstream checks, history classification, synchronization, and conflict recovery |
| `*_test.go` | Parser, configuration, process boundary, save, sync, and fixture tests |

## Conventions

- Pass Git arguments as slices to `exec.Command`. Do not build shell command strings.
- Client mode uses the configured vault as the process directory. Server mode supplies explicit bare repository and worktree arguments.
- Check the current repository state before changing it. A sync first checks the worktree, unfinished operations, conflicts, and upstream configuration.
- Keep synchronization outcomes explicit: equal, ahead, behind, or diverged. Diverged histories use rebase by default or merge only with `--merge`.
- Report conflicts and require the user to resolve them. Continue only after unresolved index entries are gone.

## Gotchas

- Configuration rejects unknown fields and fields belonging to the other mode.
- `sync` is client only. Server mode supports save, status, diff, log, doctor, and the raw Git escape hatch.
- `run_as_user` is supported only for OpenBSD server mode and uses `doas`.
- A cherry pick can be aborted with `sync --abort`, but it must be continued through `vaultctl git -- cherry-pick --continue`.
- Tests depend on an installed Git executable and use temporary repositories, remote fixtures, and disposable identities.

## Agent skills

- [golang-testing](../../.agents/skills/golang-testing/): `samber/cc-skills-golang`, local test work in this package
- [golang-safety](../../.agents/skills/golang-safety/): `samber/cc-skills-golang`, defensive correctness in this package

_Drafted by /audit from the repo, worth a quick human pass. Edit freely: once a line stops matching this draft, later runs treat it as curated and will flag rather than overwrite it._
