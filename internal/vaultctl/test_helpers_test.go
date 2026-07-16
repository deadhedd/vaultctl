package vaultctl

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", fullArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return string(output)
}

func runGitFailure(t testing.TB, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", fullArgs...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("git %v unexpectedly succeeded\n%s", args, output)
	}
	return string(output)
}

func writeTestFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func configureIdentity(t testing.TB, repo string) {
	t.Helper()
	runGit(t, repo, "config", "user.name", "Vaultctl Test")
	runGit(t, repo, "config", "user.email", "vaultctl@example.invalid")
}

func initTestRepo(t testing.TB) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-b", "main", ".")
	configureIdentity(t, repo)
	writeTestFile(t, filepath.Join(repo, "note.md"), "base\n")
	runGit(t, repo, "add", "note.md")
	runGit(t, repo, "commit", "-m", "base")
	return repo
}

func commitTestFile(t testing.TB, repo, name, content, message string) {
	t.Helper()
	writeTestFile(t, filepath.Join(repo, name), content)
	runGit(t, repo, "add", "--", name)
	runGit(t, repo, "commit", "-m", message)
}

func testApp(cfg Config) (*App, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	return newApp(cfg, nil, &stdout, &stderr), &stdout, &stderr
}

func setupRemoteFixture(t testing.TB) (remote, client, peer string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	client = filepath.Join(root, "client")
	peer = filepath.Join(root, "peer")
	for _, dir := range []string{remote, client} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, remote, "init", "--bare", "-b", "main", ".")
	runGit(t, client, "init", "-b", "main", ".")
	configureIdentity(t, client)
	commitTestFile(t, client, "note.md", "base\n", "base")
	runGit(t, client, "remote", "add", "origin", remote)
	runGit(t, client, "push", "-u", "origin", "main")
	runGit(t, root, "clone", "--branch", "main", remote, peer)
	configureIdentity(t, peer)
	return remote, client, peer
}
