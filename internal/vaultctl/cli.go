package vaultctl

import (
	"fmt"
	"strings"
)

type invocation struct {
	configPath string
	command    string
	args       []string
}

type usageError struct {
	message string
}

func (e *usageError) Error() string { return e.message }

func parseCLI(args []string) (invocation, error) {
	inv := invocation{}
	for len(args) > 0 {
		arg := args[0]
		switch {
		case arg == "--config":
			if len(args) < 2 || args[1] == "" {
				return invocation{}, &usageError{"--config requires a path"}
			}
			inv.configPath = args[1]
			args = args[2:]
		case strings.HasPrefix(arg, "--config="):
			inv.configPath = strings.TrimPrefix(arg, "--config=")
			if inv.configPath == "" {
				return invocation{}, &usageError{"--config requires a path"}
			}
			args = args[1:]
		case arg == "-h" || arg == "--help":
			if len(args) != 1 {
				return invocation{}, &usageError{fmt.Sprintf("%s does not accept arguments", arg)}
			}
			inv.command = "help"
			return inv, nil
		case strings.HasPrefix(arg, "-"):
			return invocation{}, &usageError{fmt.Sprintf("unknown global option: %s", arg)}
		default:
			inv.command = arg
			inv.args = append([]string(nil), args[1:]...)
			return inv, nil
		}
	}
	inv.command = "help"
	return inv, nil
}

func validateInvocation(inv invocation) error {
	switch inv.command {
	case "help", "status", "diff", "log", "save", "doctor":
		if len(inv.args) != 0 {
			return &usageError{fmt.Sprintf("%s does not accept arguments", inv.command)}
		}
	case "sync":
		if len(inv.args) > 1 {
			return &usageError{"sync accepts at most one of --continue, --abort, or --merge"}
		}
		if len(inv.args) == 1 {
			switch inv.args[0] {
			case "--continue", "--abort", "--merge":
			default:
				return &usageError{fmt.Sprintf("unknown sync option: %s", inv.args[0])}
			}
		}
	case "git":
		args := inv.args
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		if len(args) == 0 {
			return &usageError{"git requires arguments after an optional -- separator"}
		}
	default:
		return &usageError{fmt.Sprintf("unknown command: %s", inv.command)}
	}
	return nil
}

const helpText = `vaultctl safely manages a Git-backed Obsidian vault.

Usage:
  vaultctl [--config PATH] status
  vaultctl [--config PATH] diff
  vaultctl [--config PATH] log
  vaultctl [--config PATH] save
  vaultctl [--config PATH] sync
  vaultctl [--config PATH] sync --continue
  vaultctl [--config PATH] sync --abort
  vaultctl [--config PATH] sync --merge
  vaultctl [--config PATH] doctor
  vaultctl [--config PATH] git -- <args...>
  vaultctl help

Commands:
  status          show Git status for the configured vault
  diff            show unstaged changes
  log             show the latest 30 commits as a graph
  save            stage and commit all vault changes
  sync            conservatively synchronize a client clone
  sync --continue continue a conflicted rebase or merge, then push
  sync --abort    abort an in-progress rebase, merge, or cherry-pick
  sync --merge    merge instead of rebasing when histories diverge
  doctor          check configuration and repository basics
  git -- ARGS     run Git directly in the configured vault context
  help            show this help

sync is client-only. Server mode provides safe access to the split bare
repository/worktree layout and never fetches, rebases, merges, or pushes.
`
