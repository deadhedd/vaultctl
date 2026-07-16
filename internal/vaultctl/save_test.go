package vaultctl

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveWithNoChanges(t *testing.T) {
	repo := initTestRepo(t)
	app, stdout, _ := testApp(Config{Mode: ModeClient, VaultPath: repo})
	app.hostname = func() (string, error) { return "testhost", nil }
	app.now = func() time.Time {
		return time.Date(2026, 7, 14, 9, 8, 7, 0, time.Local)
	}

	if err := app.save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !strings.Contains(stdout.String(), "Nothing to save.") {
		t.Fatalf("unexpected output:\n%s", stdout.String())
	}
	if got := strings.TrimSpace(runGit(t, repo, "rev-list", "--count", "HEAD")); got != "1" {
		t.Fatalf("commit count = %s, want 1", got)
	}
}

func TestSaveCommitsAllChanges(t *testing.T) {
	repo := initTestRepo(t)
	writeTestFile(t, filepath.Join(repo, "note.md"), "changed\n")
	writeTestFile(t, filepath.Join(repo, "new note.md"), "new\n")

	app, stdout, _ := testApp(Config{Mode: ModeClient, VaultPath: repo})
	app.hostname = func() (string, error) { return "testhost", nil }
	app.now = func() time.Time {
		return time.Date(2026, 7, 14, 9, 8, 7, 0, time.Local)
	}
	if err := app.save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	if !strings.Contains(stdout.String(), "Vault changes saved.") {
		t.Fatalf("unexpected output:\n%s", stdout.String())
	}
	subject := strings.TrimSpace(runGit(t, repo, "log", "-1", "--format=%s"))
	want := "Vault update from testhost - 2026-07-14 09:08:07"
	if subject != want {
		t.Fatalf("subject = %q, want %q", subject, want)
	}
	if status := strings.TrimSpace(runGit(t, repo, "status", "--porcelain")); status != "" {
		t.Fatalf("worktree is not clean after save:\n%s", status)
	}
}

func TestSaveInServerMode(t *testing.T) {
	repo := initTestRepo(t)
	writeTestFile(t, filepath.Join(repo, "server.md"), "server\n")

	cfg := Config{
		Mode:     ModeServer,
		BareRepo: filepath.Join(repo, ".git"),
		Worktree: repo,
	}
	app, _, _ := testApp(cfg)
	app.hostname = func() (string, error) { return "daemon", nil }
	app.now = func() time.Time {
		return time.Date(2026, 7, 14, 10, 11, 12, 0, time.Local)
	}
	if err := app.save(); err != nil {
		t.Fatalf("server save: %v", err)
	}

	subject := strings.TrimSpace(runGit(t, repo, "log", "-1", "--format=%s"))
	want := "Vault update from daemon - 2026-07-14 10:11:12"
	if subject != want {
		t.Fatalf("subject = %q, want %q", subject, want)
	}
}
