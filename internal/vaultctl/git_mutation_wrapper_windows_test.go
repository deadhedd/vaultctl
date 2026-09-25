package vaultctl

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func installGitFetchMutation(t testing.TB, _ string, mutationPath, mutationContent string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	marker := filepath.Join(binDir, "mutation.done")
	buildGitMutationWrapper(t, binDir)
	t.Setenv("VAULTCTL_GIT_WRAPPER_MODE", "fetch")
	t.Setenv("VAULTCTL_REAL_GIT", realGit)
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
	buildGitMutationWrapper(t, binDir)
	t.Setenv("VAULTCTL_GIT_WRAPPER_MODE", "managed-status")
	t.Setenv("VAULTCTL_REAL_GIT", realGit)
	t.Setenv("VAULTCTL_VAULT_ROOT", vaultRoot)
	t.Setenv("VAULTCTL_STATUS_COUNT", countPath)
	t.Setenv("VAULTCTL_MUTATION_PATH", mutationPath)
	t.Setenv("VAULTCTL_MUTATION_CONTENT", mutationContent)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func buildGitMutationWrapper(t testing.TB, binDir string) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed to locate Git mutation wrapper source")
	}
	wrapper := filepath.Join(binDir, "git.exe")
	source := filepath.Join(filepath.Dir(currentFile), "testdata", "git_mutation_wrapper.go")
	command := exec.Command("go", "build", "-o", wrapper, source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build Git mutation wrapper: %v\n%s", err, output)
	}
}
