package vaultctl

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const serverSyncMessage = "vaultctl sync is a client-side command. This server vault uses save/status/log/git commands instead."

type userError struct {
	message string
}

func (e *userError) Error() string { return e.message }

type App struct {
	cfg      Config
	git      *gitRunner
	stdout   io.Writer
	stderr   io.Writer
	now      func() time.Time
	hostname func() (string, error)
}

func newApp(cfg Config, stdin io.Reader, stdout, stderr io.Writer) *App {
	return &App{
		cfg:      cfg,
		git:      newGitRunner(cfg, stdin, stdout, stderr),
		stdout:   stdout,
		stderr:   stderr,
		now:      time.Now,
		hostname: os.Hostname,
	}
}

func RunCLI(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	inv, err := parseCLI(args)
	if err != nil {
		fmt.Fprintf(stderr, "vaultctl: %v\nTry 'vaultctl help' for usage.\n", err)
		return 2
	}
	if err := validateInvocation(inv); err != nil {
		fmt.Fprintf(stderr, "vaultctl: %v\nTry 'vaultctl help' for usage.\n", err)
		return 2
	}
	if inv.command == "help" {
		fmt.Fprint(stdout, helpText)
		return 0
	}

	cfg, _, err := loadConfig(inv.configPath)
	if err != nil {
		fmt.Fprintf(stderr, "vaultctl: %v\n", err)
		return 1
	}
	app := newApp(cfg, stdin, stdout, stderr)
	if err := app.execute(inv.command, inv.args); err != nil {
		var displayed *userError
		if errors.As(err, &displayed) {
			fmt.Fprintln(stderr, displayed.message)
		} else {
			fmt.Fprintf(stderr, "vaultctl: %v\n", err)
		}
		return 1
	}
	return 0
}

func (a *App) execute(command string, args []string) error {
	switch command {
	case "status":
		return a.checkedStream("status", "status")
	case "diff":
		return a.checkedStream("diff", "diff")
	case "log":
		return a.checkedStream("log", "log", "--oneline", "--graph", "--decorate", "-n", "30")
	case "save":
		return a.save()
	case "sync":
		return a.sync(args)
	case "doctor":
		return a.doctor()
	case "git":
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		return a.checkedStream("git", args...)
	default:
		return fmt.Errorf("internal error: unhandled command %q", command)
	}
}

func (a *App) say(format string, args ...any) {
	fmt.Fprintf(a.stdout, "[vaultctl] "+format+"\n", args...)
}

func (a *App) checkedStream(action string, args ...string) error {
	code, err := a.git.stream(args...)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s failed (Git exited with status %d)", action, code)
	}
	return nil
}

func (a *App) captureChecked(action string, args ...string) (string, error) {
	result, err := a.git.capture(args...)
	if err != nil {
		return "", err
	}
	if result.exitCode != 0 {
		detail := strings.TrimSpace(result.stderr)
		if detail != "" {
			return "", fmt.Errorf("%s failed: %s", action, detail)
		}
		return "", fmt.Errorf("%s failed (Git exited with status %d)", action, result.exitCode)
	}
	return strings.TrimSpace(result.stdout), nil
}

func (a *App) commitMessage() (string, error) {
	host, err := a.hostname()
	if err != nil {
		return "", fmt.Errorf("determine hostname: %w", err)
	}
	return fmt.Sprintf("Vault update from %s - %s", host, a.now().Format("2006-01-02 15:04:05")), nil
}

func (a *App) save() error {
	message, err := a.commitMessage()
	if err != nil {
		return err
	}
	return a.stageAndCommit(message, true)
}

func (a *App) stageAndCommit(message string, announceNothing bool) error {
	a.say("Staging all vault changes...")
	if err := a.checkedStream("stage vault changes", "add", "-A"); err != nil {
		return err
	}

	result, err := a.git.capture("diff", "--cached", "--quiet")
	if err != nil {
		return err
	}
	switch result.exitCode {
	case 0:
		if announceNothing {
			a.say("Nothing to save.")
		}
		return nil
	case 1:
		// Git uses status 1 to report that the index differs from HEAD.
	default:
		return fmt.Errorf("inspect staged changes failed (Git exited with status %d): %s", result.exitCode, strings.TrimSpace(result.stderr))
	}

	a.say("Creating vault commit...")
	if err := a.checkedStream("commit vault changes", "commit", "-m", message); err != nil {
		return err
	}
	a.say("Vault changes saved.")
	return nil
}

func (a *App) doctor() error {
	a.say("Mode: %s", a.cfg.Mode)
	spec, err := buildGitCommand(a.cfg, []string{"--version"}, a.git.goos)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath(spec.path); err != nil {
		return fmt.Errorf("required executable %q was not found in PATH", spec.path)
	}
	a.say("Executable available: %s", spec.path)

	inside, err := a.captureChecked("inspect repository", "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return err
	}
	if inside != "true" {
		return fmt.Errorf("configured vault is not a Git worktree")
	}
	a.say("Git worktree is accessible.")

	if a.cfg.Mode == ModeClient {
		if _, err := a.upstream(); err != nil {
			return err
		}
		a.say("Upstream tracking branch is configured.")
	} else {
		a.say("Server mode does not require a remote or upstream branch.")
	}
	a.say("Doctor checks passed.")
	return nil
}
