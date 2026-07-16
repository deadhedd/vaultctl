# vaultctl

`vaultctl` is a standalone command-line tool for Git-backed Obsidian vaults.
It keeps Git as the source of truth while making routine saves and
synchronization explicit, conservative, and consistent across machines.

The implementation is written in Go, uses only the standard library, and calls
the installed `git` executable with argument arrays. It does not embed or
reimplement Git.

## Client mode and server mode

The two modes represent different repository layouts and intentionally have
different capabilities.

Client mode operates on a normal Git clone. The clone has a current branch and
an explicitly configured upstream tracking branch. Client mode supports all
commands, including `sync`.

Server mode operates on a split bare-repository/worktree layout, such as:

```text
Git directory: /home/git/vaults/Main.git
Worktree:      /home/obsidian/vaults/Main
```

On OpenBSD, a configured `run_as_user` makes every Git invocation run as that
service user through `doas`. Server mode does not need a remote or upstream
branch because the bare repository is already the canonical repository.
Consequently, `sync` is client-only and refuses to run in server mode. Server
mode never fetches, pushes, rebases, or merges except when the user explicitly
requests such an operation through the raw `git` escape hatch.

## Build

Go 1.26 or newer is recommended:

```sh
go test ./...
go build -o vaultctl ./cmd/vaultctl
```

Install the resulting binary somewhere in `PATH`. For example:

```sh
install -m 0755 vaultctl "$HOME/bin/vaultctl"
```

The project does not install into a production account automatically.

## Configuration

The configuration format is JSON so the first version needs no parsing
dependency. By default, `vaultctl` reads:

```text
Linux/OpenBSD: ~/.config/vaultctl/config.json
Windows:       %APPDATA%\vaultctl\config.json
```

Select another file before the command with `--config`:

```sh
vaultctl --config /path/to/config.json status
```

Unknown fields and mode-inappropriate fields are rejected rather than ignored.

### Client configuration

```json
{
  "mode": "client",
  "vault_path": "/home/cw/Documents/vaults/Main"
}
```

Windows paths use normal JSON escaping:

```json
{
  "mode": "client",
  "vault_path": "C:\\Users\\CW\\Documents\\vaults\\Main"
}
```

### Server configuration

```json
{
  "mode": "server",
  "bare_repo": "/home/git/vaults/Main.git",
  "worktree": "/home/obsidian/vaults/Main",
  "run_as_user": "obsidian"
}
```

`run_as_user` is optional. When present in server mode on OpenBSD, commands
take this form:

```sh
doas -u obsidian git \
  --git-dir=/home/git/vaults/Main.git \
  --work-tree=/home/obsidian/vaults/Main \
  <args...>
```

The invoking account needs an appropriate `doas.conf` rule. `vaultctl` never
changes privileges, ownership, Git configuration, remotes, or `doas.conf` by
itself.

Ready-to-copy examples are in [`examples/`](examples/).

## Commands

Shared client/server commands:

```sh
vaultctl status
vaultctl diff
vaultctl log
vaultctl save
vaultctl doctor
vaultctl git -- status --short
```

Client-only synchronization commands:

```sh
vaultctl sync
vaultctl sync --continue
vaultctl sync --abort
vaultctl sync --merge
```

### status, diff, and log

`status` shows the normal Git status. `diff` shows unstaged changes. `log`
runs:

```sh
git log --oneline --graph --decorate -n 30
```

The Git invocation automatically uses either the client working directory or
the server's configured Git directory and worktree.

### save

`save` stages all changes with `git add -A`. If the index is unchanged, it
prints `Nothing to save.` and exits successfully. Otherwise it creates:

```text
Vault update from <hostname> - YYYY-MM-DD HH:MM:SS
```

`save` performs no network or history-integration operations.

### git

The `git` command is an explicit escape hatch:

```sh
vaultctl git -- branch
vaultctl git -- show HEAD
vaultctl git -- status --short
vaultctl git -- ls-files
vaultctl git -- diff --stat
```

The `--` separator is optional for compatibility but recommended for clarity.
Arguments are passed directly to Git without shell interpolation. In server
mode this escape hatch is the only way to request fetch, push, rebase, or merge.

### sync

A normal client sync follows a state machine:

1. Verify that the configured path is a normal Git worktree.
2. Refuse to start during a merge, rebase, or cherry-pick.
3. Require the current branch to have an explicit upstream.
4. Stage and commit local changes before contacting the remote.
5. Fetch the remote named by the branch's upstream configuration.
6. Compare `HEAD` and `@{u}` with `git rev-list --left-right --count`.
7. Act on the relationship:
   - equal: report that the vault is already synced;
   - behind: fast-forward with `git merge --ff-only`;
   - ahead: push to the configured upstream branch;
   - diverged: rebase onto upstream and push.

`sync --merge` changes only the diverged case: it merges upstream instead of
rebasing, then pushes. It has no effect on equal, ahead-only, or behind-only
histories.

Pushes target the configured branch remote and merge ref explicitly. The tool
does not guess `origin`, `main`, or `master`.

### Conflict recovery

When Git reports a rebase or merge conflict, `vaultctl` stops, lists unresolved
files, and prints:

```sh
vaultctl status
vaultctl sync --continue
vaultctl sync --abort
```

Resolve files manually, then use `sync --continue`. It stages resolved files,
verifies that no unmerged index entries remain, continues the rebase or commits
the merge, and pushes only after Git completes successfully.

`sync --abort` aborts an in-progress rebase, merge, or cherry-pick. If no such
operation is active, it prints a mild `nothing to abort` message and exits
successfully.

A cherry-pick can be aborted by `sync --abort`; continuing a cherry-pick is
left to the explicit escape hatch:

```sh
vaultctl git -- cherry-pick --continue
```

### doctor

`doctor` checks that the required executable is available and that the
configured worktree is accessible. In client mode it also verifies that an
upstream tracking branch exists. In server mode it explicitly confirms that no
remote or upstream is required.

## Safety policy

`vaultctl` is deliberately conservative:

- It never force-pushes.
- It never resolves conflicts automatically with `ours` or `theirs`.
- It never creates or guesses a remote or upstream.
- It never silently discards local changes.
- It does not continue after a failed Git command.
- It exits nonzero for unsafe, incomplete, or manually resolvable states.
- It keeps server-side `save` separate from client-side `sync`.

The test suite creates disposable repositories under the test process's
temporary directory. It does not operate on the configured or production
vault.
