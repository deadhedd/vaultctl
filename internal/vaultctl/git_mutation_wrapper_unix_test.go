//go:build !windows

package vaultctl

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func installGitFetchMutation(t testing.TB, sourceRepo, mutationPath, mutationContent string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	marker := filepath.Join(binDir, "mutation.done")
	wrapper := filepath.Join(binDir, "git")
	writeTestFile(t, wrapper, `#!/bin/sh
"$VAULTCTL_REAL_GIT" "$@"
status=$?
is_fetch=false
for arg in "$@"; do
	if [ "$arg" = "fetch" ]; then
		is_fetch=true
	fi
done
if [ "$status" -eq 0 ] && [ "$PWD" = "$VAULTCTL_SOURCE_REPO" ] && [ "$is_fetch" = true ] && [ ! -e "$VAULTCTL_MUTATION_MARKER" ]; then
	printf '%s' "$VAULTCTL_MUTATION_CONTENT" > "$VAULTCTL_MUTATION_PATH"
	touch "$VAULTCTL_MUTATION_MARKER"
fi
exit "$status"
`)
	if err := os.Chmod(wrapper, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VAULTCTL_REAL_GIT", realGit)
	t.Setenv("VAULTCTL_SOURCE_REPO", sourceRepo)
	t.Setenv("VAULTCTL_MUTATION_PATH", mutationPath)
	t.Setenv("VAULTCTL_MUTATION_CONTENT", mutationContent)
	t.Setenv("VAULTCTL_MUTATION_MARKER", marker)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func installGitManagedStateMutation(t testing.TB, vaultRoot, mutationPath, mutationContent string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	countPath := filepath.Join(binDir, "managed-status.count")
	wrapper := filepath.Join(binDir, "git")
	writeTestFile(t, wrapper, `#!/bin/sh
"$VAULTCTL_REAL_GIT" "$@"
status=$?
is_managed_status=false
for arg in "$@"; do
	if [ "$arg" = "--porcelain=v2" ]; then
		is_managed_status=true
	fi
done
if [ "$status" -eq 0 ] && [ "$PWD" = "$VAULTCTL_VAULT_ROOT" ] && [ "$is_managed_status" = true ]; then
	count=0
	if [ -f "$VAULTCTL_STATUS_COUNT" ]; then
		count=$(cat "$VAULTCTL_STATUS_COUNT")
	fi
	count=$((count + 1))
	printf '%s' "$count" > "$VAULTCTL_STATUS_COUNT"
	if [ "$count" -eq 2 ]; then
		printf '%s' "$VAULTCTL_MUTATION_CONTENT" > "$VAULTCTL_MUTATION_PATH"
	fi
fi
exit "$status"
`)
	if err := os.Chmod(wrapper, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VAULTCTL_REAL_GIT", realGit)
	t.Setenv("VAULTCTL_VAULT_ROOT", vaultRoot)
	t.Setenv("VAULTCTL_STATUS_COUNT", countPath)
	t.Setenv("VAULTCTL_MUTATION_PATH", mutationPath)
	t.Setenv("VAULTCTL_MUTATION_CONTENT", mutationContent)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
