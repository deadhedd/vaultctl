package vaultctl

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// covers: AC-1
func TestGitEscapeHatchRunsNativeSubmoduleLifecycle(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	vault := initTestRepo(t)
	project := initTestRepo(t)
	runGit(t, vault, "config", "protocol.file.allow", "always")

	configPath := filepath.Join(t.TempDir(), "config.json")
	config, err := json.Marshal(Config{Mode: ModeClient, VaultPath: vault})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) (int, string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		cliArgs := append([]string{"--config", configPath, "git", "--"}, args...)
		code := RunCLI(cliArgs, nil, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}

	code, output, stderr := run("submodule", "add", project, "project")
	if code != 0 {
		t.Fatalf("vaultctl git -- submodule add failed: %s", stderr)
	}
	if !strings.Contains(output+stderr, "Cloning into") {
		t.Fatalf("submodule add did not preserve native Git output:\nstdout:\n%s\nstderr:\n%s", output, stderr)
	}
	if fields := strings.Fields(runGit(t, vault, "ls-files", "--stage", "--", "project")); len(fields) != 4 || fields[0] != "160000" {
		t.Fatalf("added project index entry = %v, want a gitlink", fields)
	}
	runGit(t, vault, "commit", "-m", "add project submodule")

	if code, _, stderr := run("submodule", "deinit", "-f", "--", "project"); code != 0 {
		t.Fatalf("vaultctl git -- submodule deinit failed: %s", stderr)
	}
	if code, _, stderr := run("submodule", "update", "--init", "--recursive", "--", "project"); code != 0 {
		t.Fatalf("vaultctl git -- submodule update failed: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(vault, "project", "note.md")); err != nil {
		t.Fatalf("native update did not initialize the project worktree: %v", err)
	}

	if code, _, stderr := run("rm", "--", "project"); code != 0 {
		t.Fatalf("vaultctl git -- rm failed: %s", stderr)
	}
	if output := strings.TrimSpace(runGit(t, vault, "ls-files", "--stage", "--", "project")); output != "" {
		t.Fatalf("removed project remains in the index: %s", output)
	}
	if _, err := os.Stat(filepath.Join(vault, ".vaultctl")); !os.IsNotExist(err) {
		t.Fatalf("git escape hatch created vaultctl submodule state, stat error = %v", err)
	}

	var stdout, commandStderr bytes.Buffer
	if code := RunCLI([]string{"--config", configPath, "submodule", "list"}, nil, &stdout, &commandStderr); code != 2 {
		t.Fatalf("dedicated submodule command exit = %d, want usage error 2", code)
	}
	if !strings.Contains(commandStderr.String(), "unknown command: submodule") {
		t.Fatalf("unexpected dedicated submodule command error:\n%s", commandStderr.String())
	}
}
