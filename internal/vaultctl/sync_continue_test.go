package vaultctl

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncContinueAfterRebaseConflict(t *testing.T) {
	_, client, peer := setupRemoteFixture(t)
	commitTestFile(t, client, "note.md", "client\n", "client note")
	commitTestFile(t, peer, "note.md", "peer\n", "peer note")
	runGit(t, peer, "push", "origin", "main")

	app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	err := app.sync(nil)
	if err == nil {
		t.Fatal("diverged rebase unexpectedly succeeded despite conflict")
	}
	operation, inspectErr := app.detectOperation()
	if inspectErr != nil {
		t.Fatal(inspectErr)
	}
	if !operation.rebase {
		t.Fatalf("operation = %#v, want rebase", operation)
	}
	if !strings.Contains(stderr.String(), "note.md") ||
		!strings.Contains(stderr.String(), "vaultctl sync --continue") {
		t.Fatalf("conflict guidance is incomplete:\n%s", stderr.String())
	}

	writeTestFile(t, filepath.Join(client, "note.md"), "resolved\n")
	t.Setenv("GIT_EDITOR", "true")
	if err := app.sync([]string{"--continue"}); err != nil {
		t.Fatalf("continue rebase: %v\nstderr:\n%s", err, stderr.String())
	}
	assertSyncEqual(t, app)
	if got := strings.TrimSpace(runGit(t, client, "show", "HEAD:note.md")); got != "resolved" {
		t.Fatalf("resolved content = %q, want resolved", got)
	}
}

func TestSyncContinueAfterMergeConflict(t *testing.T) {
	_, client, peer := setupRemoteFixture(t)
	commitTestFile(t, client, "note.md", "client\n", "client note")
	commitTestFile(t, peer, "note.md", "peer\n", "peer note")
	runGit(t, peer, "push", "origin", "main")

	app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	err := app.sync([]string{"--merge"})
	if err == nil {
		t.Fatal("diverged merge unexpectedly succeeded despite conflict")
	}
	operation, inspectErr := app.detectOperation()
	if inspectErr != nil {
		t.Fatal(inspectErr)
	}
	if !operation.merge {
		t.Fatalf("operation = %#v, want merge", operation)
	}
	if !strings.Contains(stderr.String(), "note.md") ||
		!strings.Contains(stderr.String(), "vaultctl sync --abort") {
		t.Fatalf("conflict guidance is incomplete:\n%s", stderr.String())
	}

	writeTestFile(t, filepath.Join(client, "note.md"), "merged\n")
	if err := app.sync([]string{"--continue"}); err != nil {
		t.Fatalf("continue merge: %v\nstderr:\n%s", err, stderr.String())
	}
	assertSyncEqual(t, app)
	parents := strings.Fields(runGit(t, client, "rev-list", "--parents", "-n", "1", "HEAD"))
	if len(parents) != 3 {
		t.Fatalf("continued merge parent fields = %v, want two parents", parents)
	}
}

func assertSyncEqual(t testing.TB, app *App) {
	t.Helper()
	state, err := app.currentSyncState()
	if err != nil {
		t.Fatal(err)
	}
	if state != syncEqual {
		t.Fatalf("sync state = %s, want equal", state)
	}
	operation, err := app.detectOperation()
	if err != nil {
		t.Fatal(err)
	}
	if operation.any() {
		t.Fatalf("operation remains after continuation: %#v", operation)
	}
}
