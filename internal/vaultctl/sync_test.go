package vaultctl

import (
	"bytes"
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
	if !strings.Contains(stdout.String(), "parent vault history is already synchronized") {
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

// covers: AC-2, AC-14
func TestSyncSubmodulePhaseAcrossParentSyncPaths(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		setup func(*testing.T, string, string)
	}{
		{name: "equal"},
		{name: "merge flag on equal history", args: []string{"--merge"}},
		{
			name: "fast forward",
			setup: func(t *testing.T, _ string, peer string) {
				commitTestFile(t, peer, "peer.md", "peer\n", "peer")
				runGit(t, peer, "push", "origin", "main")
			},
		},
		{
			name: "push",
			setup: func(t *testing.T, client string, _ string) {
				commitTestFile(t, client, "local.md", "local\n", "local")
			},
		},
		{
			name: "rebase and push",
			setup: func(t *testing.T, client, peer string) {
				commitTestFile(t, client, "local.md", "local\n", "local")
				commitTestFile(t, peer, "peer.md", "peer\n", "peer")
				runGit(t, peer, "push", "origin", "main")
			},
		},
		{
			name: "merge and push",
			args: []string{"--merge"},
			setup: func(t *testing.T, client, peer string) {
				commitTestFile(t, client, "local.md", "local\n", "local")
				commitTestFile(t, peer, "peer.md", "peer\n", "peer")
				runGit(t, peer, "push", "origin", "main")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client, peer, _ := setupSubmoduleRemoteFixture(t)
			runGit(t, client, "submodule", "deinit", "-f", "--", "project")
			runGit(t, client, "config", "submodule.active", ".")
			if tt.setup != nil {
				tt.setup(t, client, peer)
			}
			app, stdout, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
			if err := app.sync(tt.args); err != nil {
				t.Fatalf("sync: %v\nstderr:\n%s", err, stderr.String())
			}
			wantCommit := gitlinkCommit(t, client, "project")
			if got := strings.TrimSpace(runGit(t, filepath.Join(client, "project"), "rev-parse", "HEAD")); got != wantCommit {
				t.Fatalf("project HEAD = %s, want parent gitlink %s", got, wantCommit)
			}
			output := stdout.String()
			parent := strings.Index(output, "Parent vault synchronization succeeded.")
			update := strings.Index(output, "Synchronizing selected submodules...")
			success := strings.Index(output, "Submodule initialization and update succeeded.")
			if parent < 0 || update <= parent || success <= update {
				t.Fatalf("sync phases are missing or out of order:\n%s", output)
			}
		})
	}
}

// covers: AC-7
func TestSyncAutosavePreservesGitlinkDecisions(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(*testing.T, string, string) (string, bool)
		secondAutosave bool
	}{
		{
			name: "unstaged submodule checkout drift",
			setup: func(t *testing.T, client, project string) (string, bool) {
				runGit(t, client, "config", "--file", ".gitmodules", "submodule.project.vaultctl-pinned", "true")
				pinned := gitlinkCommit(t, client, "project")
				commitTestFile(t, project, "next.md", "next\n", "next project commit")
				target := strings.TrimSpace(runGit(t, project, "rev-parse", "HEAD"))
				projectPath := filepath.Join(client, "project")
				runGit(t, projectPath, "fetch", "origin")
				runGit(t, projectPath, "checkout", target)
				return pinned, true
			},
		},
		{
			name: "staged gitlink commit change",
			setup: func(t *testing.T, client, project string) (string, bool) {
				commitTestFile(t, project, "next.md", "next\n", "next project commit")
				target := strings.TrimSpace(runGit(t, project, "rev-parse", "HEAD"))
				projectPath := filepath.Join(client, "project")
				runGit(t, projectPath, "fetch", "origin")
				runGit(t, projectPath, "checkout", target)
				runGit(t, client, "add", "--", "project")
				return target, true
			},
		},
		{
			name: "staged removal with worktree left behind",
			setup: func(t *testing.T, client, _ string) (string, bool) {
				runGit(t, client, "rm", "--cached", "--", "project")
				runGit(t, client, "config", "--file", ".gitmodules", "--remove-section", "submodule.project")
				runGit(t, client, "add", "--", ".gitmodules")
				return "", false
			},
			secondAutosave: true,
		},
		{
			name: "staged submodule addition",
			setup: func(t *testing.T, client, _ string) (string, bool) {
				additional := initTestRepo(t)
				runGit(t, client, "submodule", "add", additional, "additional")
				return strings.TrimSpace(runGit(t, additional, "rev-parse", "HEAD")), true
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client, _, project := setupSubmoduleRemoteFixture(t)
			target, hasTarget := tt.setup(t, client, project)
			writeTestFile(t, filepath.Join(client, "local.md"), "autosaved\n")
			app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
			if err := app.sync(nil); err != nil {
				t.Fatalf("sync: %v\nstderr:\n%s", err, stderr.String())
			}
			if hasTarget {
				path := "project"
				if tt.name == "staged submodule addition" {
					path = "additional"
				}
				if got := gitlinkCommit(t, client, path); got != target {
					t.Fatalf("committed %s gitlink = %s, want %s", path, got, target)
				}
			} else if got := strings.TrimSpace(runGit(t, client, "ls-tree", "HEAD", "project")); got != "" {
				t.Fatalf("staged removal was re-added to the autosave commit: %s", got)
			}
			if _, err := os.Stat(filepath.Join(client, "project", "note.md")); err != nil {
				t.Fatalf("sync removed the existing project worktree content: %v", err)
			}
			if got := strings.TrimSpace(runGit(t, client, "show", "HEAD:local.md")); got != "autosaved" {
				t.Fatalf("ordinary autosave content = %q, want autosaved", got)
			}
			if tt.secondAutosave {
				writeTestFile(t, filepath.Join(client, "second.md"), "second autosave\n")
				if err := app.sync(nil); err != nil {
					t.Fatalf("second sync: %v\nstderr:\n%s", err, stderr.String())
				}
				if got := strings.TrimSpace(runGit(t, client, "ls-tree", "HEAD", "project")); got != "" {
					t.Fatalf("second sync readded removed project gitlink: %s", got)
				}
				if got := strings.TrimSpace(runGit(t, client, "show", "HEAD:second.md")); got != "second autosave" {
					t.Fatalf("second autosave content = %q, want second autosave", got)
				}
				if _, err := os.Stat(filepath.Join(client, "project", "note.md")); err != nil {
					t.Fatalf("second sync removed the existing project worktree content: %v", err)
				}
			}
		})
	}
}

// covers: AC-7
func TestGitlinkPathsFromUntracked(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    []string
		wantErr bool
	}{
		{name: "returns no paths for empty output"},
		{
			name:   "keeps embedded repositories and ignores ordinary files",
			output: "project/\x00nested project/\x00notes.md\x00",
			want:   []string{"nested project", "project"},
		},
		{name: "rejects output without a final separator", output: "project/", wantErr: true},
		{name: "rejects an empty path record", output: "\x00", wantErr: true},
		{name: "rejects a repository with an empty path", output: "/\x00", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gitlinkPathsFromUntracked(tt.output)
			if (err != nil) != tt.wantErr {
				t.Fatalf("gitlinkPathsFromUntracked error = %v, want error %t", err, tt.wantErr)
			}
			if !tt.wantErr {
				assertGitlinkPaths(t, got, tt.want)
			}
		})
	}
}

// covers: AC-7
func TestGitlinkPathsFromIndex(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    []string
		wantErr bool
	}{
		{name: "returns no paths for empty output"},
		{
			name:   "keeps indexed gitlinks and ignores ordinary files",
			output: "100644 abcdef 0\treadme.md\x00160000 123456 0\tproject with space\x00",
			want:   []string{"project with space"},
		},
		{name: "rejects a record without a path separator", output: "malformed\x00", wantErr: true},
		{name: "rejects malformed index metadata", output: "160000 abcdef\tproject\x00", wantErr: true},
		{name: "rejects an empty indexed path", output: "160000 abcdef 0\t\x00", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gitlinkPathsFromIndex(tt.output)
			if (err != nil) != tt.wantErr {
				t.Fatalf("gitlinkPathsFromIndex error = %v, want error %t", err, tt.wantErr)
			}
			if !tt.wantErr {
				assertGitlinkPaths(t, got, tt.want)
			}
		})
	}
}

// covers: AC-7
func TestGitlinkPathsFromRawDiff(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    []string
		wantErr bool
	}{
		{name: "returns no paths for empty output"},
		{
			name: "keeps gitlink additions and removals",
			output: ":000000 160000 0000000 1234567 A\x00new project\x00" +
				":160000 000000 1234567 0000000 D\x00removed project\x00",
			want: []string{"new project", "removed project"},
		},
		{
			name:   "keeps both paths in a gitlink rename",
			output: ":160000 160000 1234567 89abcde R100\x00old project\x00new project\x00",
			want:   []string{"new project", "old project"},
		},
		{name: "rejects output without a final separator", output: ":160000 000000 1234567 0000000 D\x00project", wantErr: true},
		{name: "rejects malformed metadata", output: "not metadata\x00project\x00", wantErr: true},
		{name: "rejects incomplete rename records", output: ":160000 160000 1234567 89abcde R100\x00old project\x00", wantErr: true},
		{name: "rejects an empty gitlink path", output: ":000000 160000 0000000 1234567 A\x00\x00", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gitlinkPathsFromRawDiff(tt.output)
			if (err != nil) != tt.wantErr {
				t.Fatalf("gitlinkPathsFromRawDiff error = %v, want error %t", err, tt.wantErr)
			}
			if !tt.wantErr {
				assertGitlinkPaths(t, got, tt.want)
			}
		})
	}
}

// covers: AC-7
func TestPorcelainPaths(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    []string
		wantErr bool
	}{
		{name: "returns no paths for empty output"},
		{
			name:   "keeps ordinary paths including spaces",
			output: "M  staged.md\x00?? new note.md\x00",
			want:   []string{"staged.md", "new note.md"},
		},
		{
			name:   "keeps both paths in a rename",
			output: "R  new note.md\x00old note.md\x00",
			want:   []string{"new note.md", "old note.md"},
		},
		{name: "rejects output without a final separator", output: "M  note.md", wantErr: true},
		{name: "rejects a short status record", output: "M \x00", wantErr: true},
		{name: "rejects a status record without its separator", output: "M Xnote.md\x00", wantErr: true},
		{name: "rejects an incomplete rename", output: "R  new note.md\x00", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := porcelainPaths(tt.output)
			if (err != nil) != tt.wantErr {
				t.Fatalf("porcelainPaths error = %v, want error %t", err, tt.wantErr)
			}
			if !tt.wantErr && len(got) != len(tt.want) {
				t.Fatalf("porcelainPaths = %q, want %q", got, tt.want)
			}
			if !tt.wantErr {
				for i := range tt.want {
					if got[i] != tt.want[i] {
						t.Fatalf("porcelainPaths = %q, want %q", got, tt.want)
					}
				}
			}
		})
	}
}

// covers: AC-7
func TestOnlyGitlinkPaths(t *testing.T) {
	tests := []struct {
		name    string
		paths   []string
		gitlink []string
		want    bool
	}{
		{name: "allows no remaining paths", want: true},
		{name: "allows the gitlink path", paths: []string{"project"}, gitlink: []string{"project"}, want: true},
		{name: "allows paths below a gitlink", paths: []string{"project/note.md"}, gitlink: []string{"project"}, want: true},
		{name: "rejects unrelated paths", paths: []string{"note.md"}, gitlink: []string{"project"}},
		{name: "rejects a sibling with the same prefix", paths: []string{"project-copy/note.md"}, gitlink: []string{"project"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitlinks := make(map[string]struct{}, len(tt.gitlink))
			for _, path := range tt.gitlink {
				gitlinks[path] = struct{}{}
			}
			if got := onlyGitlinkPaths(tt.paths, gitlinks); got != tt.want {
				t.Fatalf("onlyGitlinkPaths(%q, %q) = %t, want %t", tt.paths, tt.gitlink, got, tt.want)
			}
		})
	}
}

// covers: AC-7
func TestSortedGitlinkPaths(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{name: "sorts paths for deterministic staging", input: []string{"z-project", "a-project", "middle project"}, want: []string{"a-project", "middle project", "z-project"}},
		{name: "returns an empty list for no paths"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths := make(map[string]struct{}, len(tt.input))
			for _, path := range tt.input {
				paths[path] = struct{}{}
			}
			got := sortedGitlinkPaths(paths)
			if len(got) != len(tt.want) {
				t.Fatalf("sortedGitlinkPaths = %q, want %q", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("sortedGitlinkPaths = %q, want %q", got, tt.want)
				}
			}
		})
	}
}

func assertGitlinkPaths(t *testing.T, got map[string]struct{}, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("gitlink paths = %v, want %v", sortedGitlinkPaths(got), want)
	}
	for _, path := range want {
		if _, ok := got[path]; !ok {
			t.Fatalf("gitlink paths = %v, want %v", sortedGitlinkPaths(got), want)
		}
	}
}

func gitlinkCommit(t testing.TB, repo, path string) string {
	t.Helper()
	fields := strings.Fields(runGit(t, repo, "ls-tree", "HEAD", "--", path))
	if len(fields) != 4 || fields[0] != "160000" || fields[1] != "commit" {
		t.Fatalf("gitlink at %q = %v, want a committed submodule", path, fields)
	}
	return fields[2]
}

// covers: AC-2
func TestSyncSubmoduleUpdateUsesGitActiveSelection(t *testing.T) {
	_, client, _, _ := setupSubmoduleRemoteFixture(t)
	additional := initTestRepo(t)
	runGit(t, client, "submodule", "add", additional, "additional")
	runGit(t, client, "commit", "-m", "add another project")
	runGit(t, client, "push", "origin", "main")
	runGit(t, client, "submodule", "deinit", "-f", "--", "project")
	runGit(t, client, "submodule", "deinit", "-f", "--", "additional")
	runGit(t, client, "config", "--replace-all", "submodule.active", "project")

	app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	if err := app.sync(nil); err != nil {
		t.Fatalf("sync: %v\nstderr:\n%s", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(client, "project", "note.md")); err != nil {
		t.Fatalf("active project submodule was not initialized: %v", err)
	}
	if _, err := os.Stat(filepath.Join(client, "additional", "note.md")); err == nil {
		t.Fatal("inactive submodule was initialized")
	}
}

// covers: AC-3, AC-12
func TestSyncChecksOutNestedGitlinkWithBranchDotAtParentPin(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	_, client, peer := setupRemoteFixture(t)
	project := initTestRepo(t)
	nested := initTestRepo(t)
	runGit(t, project, "submodule", "add", nested, "nested")
	runGit(t, project, "config", "--file", ".gitmodules", "submodule.nested.branch", ".")
	runGit(t, project, "add", "--", ".gitmodules")
	runGit(t, project, "commit", "-m", "add nested project")
	nestedPin := strings.TrimSpace(runGit(t, nested, "rev-parse", "HEAD"))
	runGit(t, client, "submodule", "add", project, "project")
	runGit(t, client, "config", "--file", ".gitmodules", "submodule.project.vaultctl-pinned", "true")
	runGit(t, client, "commit", "-m", "add project submodule")
	runGit(t, client, "push", "origin", "main")
	runGit(t, peer, "pull", "--ff-only")
	runGit(t, peer, "config", "protocol.file.allow", "always")

	projectTarget := commitTestFileAndHash(t, project, "remote.md", "remote revision\n", "remote project revision")
	runGit(t, peer, "submodule", "update", "--init", "--recursive", "--", "project")
	peerProject := filepath.Join(peer, "project")
	runGit(t, peerProject, "fetch", "origin")
	runGit(t, peerProject, "checkout", projectTarget)
	runGit(t, peer, "add", "--", "project")
	runGit(t, peer, "commit", "-m", "select next project revision")
	runGit(t, peer, "push", "origin", "main")

	clientProject := filepath.Join(client, "project")
	configureIdentity(t, clientProject)
	runGit(t, clientProject, "fetch", "origin")
	runGit(t, clientProject, "switch", "-c", "local-work")
	commitTestFile(t, clientProject, "local.md", "local project work\n", "local project work")
	runGit(t, client, "config", "submodule.project.update", "merge")
	override := filepath.Join(t.TempDir(), "project-override.git")
	runGit(t, t.TempDir(), "clone", "--bare", project, override)
	runGit(t, client, "config", "submodule.project.url", override)

	app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	if err := app.sync(nil); err != nil {
		t.Fatalf("sync: %v\nstderr:\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(runGit(t, clientProject, "rev-parse", "HEAD")); got != projectTarget {
		t.Fatalf("project HEAD = %s, want parent gitlink %s despite local merge strategy", got, projectTarget)
	}
	if got := strings.TrimSpace(runGit(t, filepath.Join(clientProject, "nested"), "rev-parse", "HEAD")); got != nestedPin {
		t.Fatalf("nested project HEAD = %s, want parent gitlink %s", got, nestedPin)
	}
	if got := strings.TrimSpace(runGit(t, client, "config", "--get", "submodule.project.url")); got != override {
		t.Fatalf("initialized submodule URL = %q, want local override %q", got, override)
	}
}

// covers: AC-2, AC-6
func TestSyncPreservesInitializedSubmoduleRemoteOriginURL(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	_, client, peer, project := setupSubmoduleRemoteFixture(t)
	clientProject := filepath.Join(client, "project")
	peerProject := filepath.Join(peer, "project")

	override := filepath.Join(t.TempDir(), "project-override.git")
	runGit(t, t.TempDir(), "clone", "--bare", project, override)
	overrideWork := filepath.Join(t.TempDir(), "override-work")
	runGit(t, t.TempDir(), "clone", override, overrideWork)
	configureIdentity(t, overrideWork)
	target := commitTestFileAndHash(t, overrideWork, "remote.md", "override project revision\n", "override project revision")
	runGit(t, overrideWork, "push", "origin", "main")

	runGit(t, peer, "submodule", "update", "--init", "--", "project")
	runGit(t, peerProject, "fetch", override, "main")
	runGit(t, peerProject, "checkout", target)
	runGit(t, peer, "add", "--", "project")
	runGit(t, peer, "commit", "-m", "select override project revision")
	runGit(t, peer, "push", "origin", "main")

	runGit(t, clientProject, "config", "remote.origin.url", override)
	if got := strings.TrimSpace(runGit(t, client, "config", "--file", filepath.Join(client, ".gitmodules"), "--get", "submodule.project.url")); got != project {
		t.Fatalf(".gitmodules project URL = %q, want original URL %q", got, project)
	}
	for _, repo := range []string{clientProject, project} {
		runGitFailure(t, repo, "cat-file", "-e", target+"^{commit}")
	}

	app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	if err := app.sync(nil); err != nil {
		t.Fatalf("sync: %v\nstderr:\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(runGit(t, clientProject, "rev-parse", "HEAD")); got != target {
		t.Fatalf("project HEAD = %s, want recorded gitlink commit %s", got, target)
	}
	if got := strings.TrimSpace(runGit(t, clientProject, "config", "--get", "remote.origin.url")); got != override {
		t.Fatalf("initialized submodule remote.origin.url = %q, want local URL %q", got, override)
	}
}

// covers: AC-2, AC-12, AC-14
func TestSyncSubmoduleFailurePreservesDirtyWorkAndCanRetry(t *testing.T) {
	_, client, peer, project := setupSubmoduleRemoteFixture(t)
	runGit(t, client, "config", "--file", ".gitmodules", "submodule.project.vaultctl-pinned", "true")
	runGit(t, client, "add", "--", ".gitmodules")
	runGit(t, client, "commit", "-m", "pin project submodule")
	runGit(t, client, "push", "origin", "main")
	runGit(t, peer, "pull", "--ff-only")
	runGit(t, peer, "submodule", "update", "--init", "--", "project")
	target := commitTestFileAndHash(t, project, "note.md", "upstream project version\n", "upstream project version")
	peerProject := filepath.Join(peer, "project")
	runGit(t, peerProject, "fetch", "origin")
	runGit(t, peerProject, "checkout", target)
	runGit(t, peer, "add", "--", "project")
	runGit(t, peer, "commit", "-m", "select upstream project version")
	runGit(t, peer, "push", "origin", "main")

	clientProject := filepath.Join(client, "project")
	writeTestFile(t, filepath.Join(clientProject, "note.md"), "local uncommitted work\n")
	app, stdout, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	err := app.sync(nil)
	if err == nil || !strings.Contains(err.Error(), "pinned checkout failed") {
		t.Fatalf("sync error = %v, want a pinned checkout failure", err)
	}
	if !strings.Contains(stderr.String(), "local changes") || !strings.Contains(stderr.String(), "child synchronization or parent link recording is incomplete") {
		t.Fatalf("submodule failure output did not preserve Git diagnostics and phase state:\n%s", stderr.String())
	}
	if strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")) != strings.TrimSpace(runGit(t, peer, "rev-parse", "HEAD")) {
		t.Fatal("parent synchronization did not finish before the submodule update failure")
	}
	got, err := os.ReadFile(filepath.Join(clientProject, "note.md"))
	if err != nil {
		t.Fatalf("read dirty submodule content: %v", err)
	}
	if string(got) != "local uncommitted work\n" {
		t.Fatalf("dirty submodule content = %q, want it preserved", got)
	}
	if parent, update := strings.Index(stdout.String(), "Parent vault synchronization succeeded."), strings.Index(stdout.String(), "Synchronizing selected submodules"); parent < 0 || update <= parent {
		t.Fatalf("parent success was not reported before the submodule phase:\n%s", stdout.String())
	}

	runGit(t, clientProject, "restore", "--", "note.md")
	if err := app.sync(nil); err != nil {
		t.Fatalf("retry sync: %v\nstderr:\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(runGit(t, clientProject, "rev-parse", "HEAD")); got != target {
		t.Fatalf("project HEAD after retry = %s, want %s", got, target)
	}
}

// covers: AC-2, AC-4, AC-10, AC-11, AC-14
func TestSyncSubmoduleFailureKeepsCompletedUpdatesAndCanRetry(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	_, client, peer := setupRemoteFixture(t)
	good := initTestRepo(t)
	bad := initTestRepo(t)
	goodBase := strings.TrimSpace(runGit(t, good, "rev-parse", "HEAD"))
	goodTarget := commitTestFileAndHash(t, good, "next.md", "next good revision\n", "next good revision")
	badTarget := commitTestFileAndHash(t, bad, "next.md", "next bad revision\n", "next bad revision")
	runGit(t, client, "submodule", "add", good, "a-good")
	gitmodules := filepath.Join(client, ".gitmodules")
	contents, err := os.ReadFile(gitmodules)
	if err != nil {
		t.Fatal(err)
	}
	badURL := "https://example.invalid/missing.git"
	contents = append(contents, []byte("\n[submodule \"z-bad\"]\n\tpath = z-bad\n\turl = "+badURL+"\n")...)
	if err := os.WriteFile(gitmodules, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	badBase := strings.TrimSpace(runGit(t, bad, "rev-parse", "HEAD~1"))
	runGit(t, client, "update-index", "--add", "--cacheinfo", "160000,"+badBase+",z-bad")
	runGit(t, client, "commit", "-m", "record project submodules")
	runGit(t, client, "push", "origin", "main")
	runGit(t, peer, "pull", "--ff-only")

	runGit(t, peer, "update-index", "--add", "--cacheinfo", "160000,"+goodBase+",a-good")
	runGit(t, peer, "update-index", "--add", "--cacheinfo", "160000,"+badTarget+",z-bad")
	runGit(t, peer, "commit", "-m", "select newer project revisions")
	runGit(t, peer, "push", "origin", "main")
	runGit(t, client, "config", "submodule.fetchJobs", "1")
	runGit(t, client, "config", "submodule.active", ".")

	app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	err = app.sync(nil)
	if err == nil || !strings.Contains(stderr.String(), "child synchronization or parent link recording is incomplete") {
		t.Fatalf("sync error = %v, stderr:\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(runGit(t, filepath.Join(client, "a-good"), "rev-parse", "HEAD")); got != goodTarget {
		t.Fatalf("completed project update was rolled back or missed: got %s, want %s", got, goodTarget)
	}
	if got := gitlinkCommit(t, client, "a-good"); got != goodBase {
		t.Fatalf("failed group unexpectedly recorded a-good: got %s, want %s", got, goodBase)
	}
	if _, err := os.Stat(filepath.Join(client, "z-bad", "note.md")); err == nil {
		t.Fatal("submodule with the unavailable URL unexpectedly initialized")
	}

	badURL = filepath.ToSlash(bad)
	sectionStart := bytes.LastIndex(contents, []byte("\n[submodule \"z-bad\"]"))
	if sectionStart < 0 {
		t.Fatal("could not locate test submodule URL section")
	}
	updated := append(append([]byte(nil), contents[:sectionStart]...), []byte("\n[submodule \"z-bad\"]\n\tpath = z-bad\n\turl = "+badURL+"\n")...)
	if err := os.WriteFile(gitmodules, updated, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, client, "config", "submodule.z-bad.url", badURL)
	if err := app.sync(nil); err != nil {
		t.Fatalf("retry after URL repair: %v\nstderr:\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(runGit(t, filepath.Join(client, "z-bad"), "rev-parse", "HEAD")); got != badTarget {
		t.Fatalf("recovered project HEAD = %s, want %s", got, badTarget)
	}
}

// covers: AC-2, AC-14
func TestFailedParentSyncDoesNotStartSubmodulePhase(t *testing.T) {
	_, client, _, _ := setupSubmoduleRemoteFixture(t)
	runGit(t, client, "submodule", "deinit", "-f", "--", "project")
	runGit(t, client, "remote", "set-url", "origin", "https://example.invalid/missing.git")
	app, stdout, _ := testApp(Config{Mode: ModeClient, VaultPath: client})
	if err := app.sync(nil); err == nil {
		t.Fatal("sync unexpectedly succeeded with a missing parent remote")
	}
	if strings.Contains(stdout.String(), "Parent vault synchronization succeeded.") || strings.Contains(stdout.String(), "Synchronizing selected submodules") {
		t.Fatalf("submodule phase started after parent sync failure:\n%s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(client, "project", "note.md")); err == nil {
		t.Fatal("submodule was initialized even though parent synchronization failed")
	}
}

// covers: AC-2
func TestSyncKeepsWorktreeAfterParentRemovesGitlink(t *testing.T) {
	_, client, peer, _ := setupSubmoduleRemoteFixture(t)
	runGit(t, peer, "submodule", "update", "--init", "--recursive", "--", "project")
	project := filepath.Join(peer, "project")
	pinned := strings.TrimSpace(runGit(t, project, "rev-parse", "HEAD"))

	runGit(t, client, "rm", "--", "project")
	runGit(t, client, "commit", "-m", "remove project submodule")
	runGit(t, client, "push", "origin", "main")

	app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: peer})
	if err := app.sync(nil); err != nil {
		t.Fatalf("sync after parent removed the gitlink: %v\nstderr:\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(runGit(t, peer, "ls-tree", "HEAD", "--", "project")); got != "" {
		t.Fatalf("parent still records the removed project gitlink: %s", got)
	}
	if got := strings.TrimSpace(runGit(t, project, "rev-parse", "HEAD")); got != pinned {
		t.Fatalf("retained project HEAD = %s, want its original %s", got, pinned)
	}
	if _, err := os.Stat(filepath.Join(project, "note.md")); err != nil {
		t.Fatalf("normal sync removed the unrecorded project worktree: %v", err)
	}
}

func commitTestFileAndHash(t testing.TB, repo, name, content, message string) string {
	t.Helper()
	commitTestFile(t, repo, name, content, message)
	return strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
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

// covers: AC-14
func TestSyncRefusesServerMode(t *testing.T) {
	cfg := Config{Mode: ModeServer, BareRepo: "/unused/repo", Worktree: "/unused/tree"}
	tests := []struct {
		name string
		args []string
	}{
		{name: "normal sync is refused", args: nil},
		{name: "parent continuation is refused", args: []string{"--continue"}},
		{name: "parent abort is refused", args: []string{"--abort"}},
		{name: "merge sync is refused", args: []string{"--merge"}},
		{name: "scoped child continuation is refused", args: []string{"--continue", "--submodule", "Projects/child"}},
		{name: "scoped child abort is refused", args: []string{"--abort", "--submodule", "Projects/child"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, _, _ := testApp(cfg)
			err := app.sync(tt.args)
			var displayed *userError
			if !errors.As(err, &displayed) || displayed.Error() != serverSyncMessage {
				t.Errorf("sync(%v) error = %v, want server refusal", tt.args, err)
			}
		})
	}
}

// covers: AC-8, AC-9
func TestReportConflictsQuotesSubmoduleRecoveryPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "ordinary path", path: "Projects/child", want: "'Projects/child'"},
		{name: "path with spaces and apostrophe", path: "Projects/O'Brien notes", want: "'Projects/O'\\''Brien notes'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			app := newApp(Config{}, nil, &bytes.Buffer{}, &stderr)
			app.submoduleRecoveryPath = tt.path

			app.reportConflicts(nil)

			for _, action := range []string{"continue", "abort"} {
				want := "vaultctl sync --" + action + " --submodule " + tt.want
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("conflict recovery guidance missing %q:\n%s", want, stderr.String())
				}
			}
		})
	}
}

func TestConflictDetectionAndAbort(t *testing.T) {
	repo := initTestRepo(t)
	runGit(t, repo, "switch", "-c", "topic")
	commitTestFile(t, repo, "note.md", "topic\n", "topic")
	runGit(t, repo, "switch", "main")
	commitTestFile(t, repo, "note.md", "main\n", "main")
	runGitFailure(t, repo, "merge", "topic")

	app, stdout, _ := testApp(Config{Mode: ModeClient, VaultPath: repo})
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
	if strings.Contains(stdout.String(), "Synchronizing selected submodules") {
		t.Fatalf("sync --abort started the normal submodule phase:\n%s", stdout.String())
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

func TestSyncAbortHandlesRevertAndUnknownSequencer(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*testing.T, string)
		wantError string
	}{
		{
			name: "revert",
			setup: func(t *testing.T, repo string) {
				runGit(t, repo, "switch", "-c", "topic")
				commitTestFile(t, repo, "note.md", "topic\n", "topic")
				topicCommit := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
				runGit(t, repo, "switch", "main")
				commitTestFile(t, repo, "note.md", "main\n", "main")
				runGitFailure(t, repo, "revert", "--no-edit", topicCommit)
			},
		},
		{
			name: "unknown sequencer",
			setup: func(t *testing.T, repo string) {
				path := strings.TrimSpace(runGit(t, repo, "rev-parse", "--git-path", "sequencer"))
				if !filepath.IsAbs(path) {
					path = filepath.Join(repo, path)
				}
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatalf("create sequencer state: %v", err)
				}
			},
			wantError: "an unfinished Git sequencer is in progress",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := initTestRepo(t)
			tt.setup(t, repo)
			app, stdout, _ := testApp(Config{Mode: ModeClient, VaultPath: repo})

			err := app.sync([]string{"--abort"})
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("abort error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("abort revert: %v\noutput:\n%s", err, stdout.String())
			}
			if !strings.Contains(stdout.String(), "Aborting revert") {
				t.Fatalf("abort output = %q, want revert message", stdout.String())
			}
			operation, err := app.detectOperation()
			if err != nil {
				t.Fatal(err)
			}
			if operation.any() {
				t.Fatalf("operation remains after abort: %#v", operation)
			}
		})
	}
}

func TestSyncContinuationAndAbortSkipExternalDocumentationRefresh(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{name: "continue", args: []string{"--continue"}, want: "no rebase or merge is in progress"},
		{name: "abort", args: []string{"--abort"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, client, _ := setupRemoteFixture(t)
			writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)
			app, stdout, _ := testApp(Config{Mode: ModeClient, VaultPath: client})

			err := app.sync(tt.args)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("sync(%v): %v", tt.args, err)
				}
				if !strings.Contains(stdout.String(), "nothing to abort") {
					t.Fatalf("sync(%v) output = %q, want abort message", tt.args, stdout.String())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("sync(%v) error = %v, want %q", tt.args, err, tt.want)
			}
		})
	}
}
