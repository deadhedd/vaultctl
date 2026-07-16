package vaultctl

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyCounts(t *testing.T) {
	tests := []struct {
		input string
		want  syncState
	}{
		{"0\t0\n", syncEqual},
		{"2 0", syncAhead},
		{"0 3", syncBehind},
		{"4 5", syncDiverged},
	}
	for _, tt := range tests {
		got, err := classifyCounts(tt.input)
		if err != nil {
			t.Fatalf("classifyCounts(%q): %v", tt.input, err)
		}
		if got != tt.want {
			t.Errorf("classifyCounts(%q) = %s, want %s", tt.input, got, tt.want)
		}
	}
	if _, err := classifyCounts("bad"); err == nil {
		t.Fatal("classifyCounts accepted malformed output")
	}
}

func TestClassifyRepositoryHistories(t *testing.T) {
	tests := []struct {
		name string
		want syncState
		set  func(*testing.T, string)
	}{
		{name: "equal", want: syncEqual},
		{
			name: "ahead",
			want: syncAhead,
			set: func(t *testing.T, repo string) {
				commitTestFile(t, repo, "local.md", "local\n", "local")
			},
		},
		{
			name: "behind",
			want: syncBehind,
			set: func(t *testing.T, repo string) {
				runGit(t, repo, "switch", "upstream")
				commitTestFile(t, repo, "remote.md", "remote\n", "remote")
				runGit(t, repo, "switch", "main")
			},
		},
		{
			name: "diverged",
			want: syncDiverged,
			set: func(t *testing.T, repo string) {
				commitTestFile(t, repo, "local.md", "local\n", "local")
				runGit(t, repo, "switch", "upstream")
				commitTestFile(t, repo, "remote.md", "remote\n", "remote")
				runGit(t, repo, "switch", "main")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := initTestRepo(t)
			runGit(t, repo, "branch", "upstream")
			if tt.set != nil {
				tt.set(t, repo)
			}
			runGit(t, repo, "config", "branch.main.remote", ".")
			runGit(t, repo, "config", "branch.main.merge", "refs/heads/upstream")
			app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: repo})
			got, err := app.currentSyncState()
			if err != nil {
				t.Fatalf("currentSyncState: %v", err)
			}
			if got != tt.want {
				t.Fatalf("state = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSyncLifecycle(t *testing.T) {
	remote, client, peer := setupRemoteFixture(t)
	app, stdout, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	app.hostname = func() (string, error) { return "client", nil }

	if err := app.sync(nil); err != nil {
		t.Fatalf("equal sync: %v\nstderr:\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "already synced") {
		t.Fatalf("equal sync output:\n%s", stdout.String())
	}

	writeTestFile(t, filepath.Join(client, "local.md"), "saved by sync\n")
	if err := app.sync(nil); err != nil {
		t.Fatalf("ahead sync: %v\nstderr:\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(runGit(t, remote, "rev-list", "--count", "main")); got != "2" {
		t.Fatalf("remote count after ahead sync = %s, want 2", got)
	}

	runGit(t, peer, "pull", "--ff-only")
	commitTestFile(t, peer, "peer.md", "peer\n", "peer")
	runGit(t, peer, "push", "origin", "main")
	if err := app.sync(nil); err != nil {
		t.Fatalf("behind sync: %v\nstderr:\n%s", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(client, "peer.md")); err != nil {
		t.Fatalf("behind sync did not fast-forward peer.md: %v", err)
	}

	commitTestFile(t, client, "client-diverged.md", "client\n", "client diverged")
	commitTestFile(t, peer, "peer-diverged.md", "peer\n", "peer diverged")
	runGit(t, peer, "push", "origin", "main")
	if err := app.sync(nil); err != nil {
		t.Fatalf("diverged rebase sync: %v\nstderr:\n%s", err, stderr.String())
	}
	state, err := app.currentSyncState()
	if err != nil {
		t.Fatal(err)
	}
	if state != syncEqual {
		t.Fatalf("state after diverged sync = %s, want equal", state)
	}
	if got := strings.TrimSpace(runGit(t, remote, "rev-list", "--count", "main")); got != "5" {
		t.Fatalf("remote count after rebase sync = %s, want 5", got)
	}
}

func TestSyncMergeDivergence(t *testing.T) {
	_, client, peer := setupRemoteFixture(t)
	commitTestFile(t, client, "client.md", "client\n", "client")
	commitTestFile(t, peer, "peer.md", "peer\n", "peer")
	runGit(t, peer, "push", "origin", "main")

	app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	if err := app.sync([]string{"--merge"}); err != nil {
		t.Fatalf("merge sync: %v\nstderr:\n%s", err, stderr.String())
	}
	parents := strings.Fields(runGit(t, client, "rev-list", "--parents", "-n", "1", "HEAD"))
	if len(parents) != 3 {
		t.Fatalf("merge commit parent fields = %v, want commit plus two parents", parents)
	}
	state, err := app.currentSyncState()
	if err != nil {
		t.Fatal(err)
	}
	if state != syncEqual {
		t.Fatalf("state after merge sync = %s, want equal", state)
	}
}

func TestMissingUpstreamStopsBeforeSavingChanges(t *testing.T) {
	repo := initTestRepo(t)
	writeTestFile(t, filepath.Join(repo, "note.md"), "unsaved\n")
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: repo})
	err := app.sync(nil)
	if err == nil || !strings.Contains(err.Error(), "no upstream tracking branch") {
		t.Fatalf("sync error = %v, want missing upstream", err)
	}
	if got := strings.TrimSpace(runGit(t, repo, "rev-list", "--count", "HEAD")); got != "1" {
		t.Fatalf("sync committed before checking upstream; count = %s", got)
	}
	status := runGit(t, repo, "status", "--porcelain")
	if !strings.Contains(status, "note.md") {
		t.Fatalf("local change disappeared:\n%s", status)
	}
}

func TestSyncRefusesServerMode(t *testing.T) {
	cfg := Config{Mode: ModeServer, BareRepo: "/unused/repo", Worktree: "/unused/tree"}
	for _, args := range [][]string{nil, {"--continue"}, {"--abort"}, {"--merge"}} {
		app, _, _ := testApp(cfg)
		err := app.sync(args)
		var displayed *userError
		if !errors.As(err, &displayed) || displayed.Error() != serverSyncMessage {
			t.Errorf("sync(%v) error = %v, want server refusal", args, err)
		}
	}
}

func TestConflictDetectionAndAbort(t *testing.T) {
	repo := initTestRepo(t)
	runGit(t, repo, "switch", "-c", "topic")
	commitTestFile(t, repo, "note.md", "topic\n", "topic")
	runGit(t, repo, "switch", "main")
	commitTestFile(t, repo, "note.md", "main\n", "main")
	runGitFailure(t, repo, "merge", "topic")

	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: repo})
	files, err := app.conflictedFiles()
	if err != nil {
		t.Fatalf("conflictedFiles: %v", err)
	}
	if len(files) != 1 || files[0] != "note.md" {
		t.Fatalf("conflicted files = %v, want [note.md]", files)
	}
	operation, err := app.detectOperation()
	if err != nil {
		t.Fatal(err)
	}
	if !operation.merge {
		t.Fatalf("operation = %#v, want merge", operation)
	}
	if err := app.sync(nil); err == nil || !strings.Contains(err.Error(), "unfinished merge") {
		t.Fatalf("sync error = %v, want unfinished merge refusal", err)
	}
	if err := app.sync([]string{"--abort"}); err != nil {
		t.Fatalf("abort merge: %v", err)
	}
	operation, err = app.detectOperation()
	if err != nil {
		t.Fatal(err)
	}
	if operation.any() {
		t.Fatalf("operation remains after abort: %#v", operation)
	}
}

func TestSyncAbortWithNoOperationSucceeds(t *testing.T) {
	repo := initTestRepo(t)
	app, stdout, _ := testApp(Config{Mode: ModeClient, VaultPath: repo})
	if err := app.sync([]string{"--abort"}); err != nil {
		t.Fatalf("abort without operation: %v", err)
	}
	if !strings.Contains(stdout.String(), "nothing to abort") {
		t.Fatalf("unexpected output:\n%s", stdout.String())
	}
}
