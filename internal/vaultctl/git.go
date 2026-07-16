package vaultctl

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"runtime"
)

type commandSpec struct {
	path string
	args []string
	dir  string
}

func buildGitCommand(cfg Config, gitArgs []string, goos string) (commandSpec, error) {
	args := append([]string(nil), gitArgs...)
	switch cfg.Mode {
	case ModeClient:
		return commandSpec{path: "git", args: args, dir: cfg.VaultPath}, nil
	case ModeServer:
		serverArgs := []string{
			"--git-dir=" + cfg.BareRepo,
			"--work-tree=" + cfg.Worktree,
		}
		serverArgs = append(serverArgs, args...)
		if cfg.RunAsUser == "" {
			return commandSpec{path: "git", args: serverArgs, dir: cfg.Worktree}, nil
		}
		if goos != "openbsd" {
			return commandSpec{}, fmt.Errorf("run_as_user is supported only for OpenBSD server mode")
		}
		doasArgs := []string{"-u", cfg.RunAsUser, "git"}
		doasArgs = append(doasArgs, serverArgs...)
		return commandSpec{path: "doas", args: doasArgs, dir: cfg.Worktree}, nil
	default:
		return commandSpec{}, fmt.Errorf("unknown mode %q", cfg.Mode)
	}
}

type commandResult struct {
	stdout   string
	stderr   string
	exitCode int
}

type gitRunner struct {
	cfg    Config
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	goos   string
}

func newGitRunner(cfg Config, stdin io.Reader, stdout, stderr io.Writer) *gitRunner {
	return &gitRunner{
		cfg: cfg, stdin: stdin, stdout: stdout, stderr: stderr, goos: runtime.GOOS,
	}
}

func (g *gitRunner) command(args ...string) (*exec.Cmd, error) {
	spec, err := buildGitCommand(g.cfg, args, g.goos)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(spec.path, spec.args...)
	cmd.Dir = spec.dir
	cmd.Stdin = g.stdin
	return cmd, nil
}

func (g *gitRunner) stream(args ...string) (int, error) {
	cmd, err := g.command(args...)
	if err != nil {
		return -1, err
	}
	cmd.Stdout = g.stdout
	cmd.Stderr = g.stderr
	err = cmd.Run()
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), nil
	}
	return -1, fmt.Errorf("start Git: %w", err)
}

func (g *gitRunner) capture(args ...string) (commandResult, error) {
	cmd, err := g.command(args...)
	if err != nil {
		return commandResult{}, err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	result := commandResult{stdout: stdout.String(), stderr: stderr.String()}
	if err == nil {
		return result, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.exitCode = exitErr.ExitCode()
		return result, nil
	}
	return commandResult{}, fmt.Errorf("start Git: %w", err)
}
