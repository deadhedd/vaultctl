package vaultctl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupExternalSource(t testing.TB) (sourceRoot, sourceRepo, sourceRemote string) {
	t.Helper()
	root := t.TempDir()
	sourceRoot = filepath.Join(root, "sources")
	sourceRemote = filepath.Join(root, "source.git")
	sourceRepo = filepath.Join(sourceRoot, "project")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--bare", "-b", "main", "source.git")
	if err := os.MkdirAll(sourceRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, sourceRepo, "init", "-b", "main", ".")
	configureIdentity(t, sourceRepo)
	if err := os.MkdirAll(filepath.Join(sourceRepo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(sourceRepo, "docs", "guide.md"), "Guide one\n")
	writeTestFile(t, filepath.Join(sourceRepo, "unrelated.md"), "not selected\n")
	runGit(t, sourceRepo, "add", ".")
	runGit(t, sourceRepo, "commit", "-m", "source base")
	runGit(t, sourceRepo, "remote", "add", "origin", sourceRemote)
	runGit(t, sourceRepo, "push", "-u", "origin", "main")
	return sourceRoot, sourceRepo, sourceRemote
}

func writeExternalManifest(t testing.TB, vault string, mappings string) {
	t.Helper()
	control := filepath.Join(vault, ".vaultctl")
	if err := os.MkdirAll(control, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"version":1,"sources":[{"id":"docs","repository":"project","remote":"origin","reference":"main","mappings":` + mappings + `}]}`
	writeTestFile(t, filepath.Join(control, "external-docs.json"), manifest+"\n")
	runGit(t, vault, "add", ".vaultctl/external-docs.json")
	runGit(t, vault, "commit", "-m", "configure external documentation")
}

func TestRefreshExternalDocsThinPath(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, _, _ := setupExternalSource(t)
	writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)

	app, stdout, stderr := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	if err := app.refreshExternalDocs(); err != nil {
		t.Fatalf("refreshExternalDocs: %v\nstderr:\n%s", err, stderr.String())
	}

	content, err := os.ReadFile(filepath.Join(client, "external", "project", "guide.md"))
	if err != nil {
		t.Fatalf("read projected file: %v", err)
	}
	if string(content) != "Guide one\n" {
		t.Fatalf("projected content = %q, want source content", content)
	}
	state := filepath.Join(client, ".vaultctl", "external-docs-state.json")
	stateContent, err := os.ReadFile(state)
	if err != nil {
		t.Fatalf("read generated state: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(stateContent, &decoded); err != nil {
		t.Fatalf("state is not JSON: %v", err)
	}
	if !strings.HasSuffix(string(stateContent), "\n") {
		t.Fatal("generated state does not end with a newline")
	}
	if got := strings.TrimSpace(runGit(t, client, "log", "-1", "--format=%s")); got != externalCommitSubject {
		t.Fatalf("refresh subject = %q, want %q", got, externalCommitSubject)
	}
	if !strings.Contains(stdout.String(), "External source docs resolved") {
		t.Fatalf("refresh output does not report source progress:\n%s", stdout.String())
	}
	if status := strings.TrimSpace(runGit(t, client, "status", "--porcelain")); status != "" {
		t.Fatalf("refresh left worktree changes:\n%s", status)
	}
	if _, err := os.Stat(filepath.Join(client, filepath.FromSlash(externalLockPath))); !os.IsNotExist(err) {
		t.Fatalf("refresh lock remains after success, stat error = %v", err)
	}
	if got := strings.TrimSpace(runGit(t, client, "show", "--format=", "--name-only", "HEAD")); got != ".vaultctl/external-docs-state.json\nexternal/project/guide.md" {
		t.Fatalf("refresh commit paths = %q, want only managed paths", got)
	}
}

// AC-1: without a manifest, refresh preserves the existing client path and
// does not require the optional source_root configuration.
func TestRefreshExternalDocsWithoutManifestDoesNotRequireSourceRoot(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	app, stdout, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})

	if err := app.refreshExternalDocs(); err != nil {
		t.Fatalf("refreshExternalDocs without manifest: %v\nstderr:\n%s", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("refresh output without manifest = %q, want empty", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(client, filepath.FromSlash(externalManifestPath))); !os.IsNotExist(err) {
		t.Fatalf("refresh created a manifest, stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(client, filepath.FromSlash(externalStatePath))); !os.IsNotExist(err) {
		t.Fatalf("refresh created generated state, stat error = %v", err)
	}
}

func TestRefreshExternalDocsRestoresPrecommitFailure(t *testing.T) {
	tests := []struct {
		name   string
		exists bool
	}{
		{name: "new projection", exists: false},
		{name: "existing projection", exists: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client, _ := setupRemoteFixture(t)
			sourceRoot, sourceRepo, _ := setupExternalSource(t)
			writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)
			app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})

			if tt.exists {
				if err := app.refreshExternalDocs(); err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, filepath.Join(sourceRepo, "docs", "guide.md"), "Guide two\n")
				runGit(t, sourceRepo, "add", "docs/guide.md")
				runGit(t, sourceRepo, "commit", "-m", "source update")
				runGit(t, sourceRepo, "push", "origin", "main")
			}

			writeTestFile(t, filepath.Join(client, "staged.md"), "keep staged\n")
			runGit(t, client, "add", "staged.md")
			writeTestFile(t, filepath.Join(client, "unstaged.md"), "keep unstaged\n")
			beforeHead := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))
			beforeGuide, beforeGuideErr := os.ReadFile(filepath.Join(client, "external", "project", "guide.md"))
			beforeState, beforeStateErr := os.ReadFile(filepath.Join(client, ".vaultctl", "external-docs-state.json"))
			if tt.exists {
				if beforeGuideErr != nil || string(beforeGuide) != "Guide one\n" {
					t.Fatalf("before guide = %q, err = %v", beforeGuide, beforeGuideErr)
				}
				if beforeStateErr != nil {
					t.Fatalf("read before state: %v", beforeStateErr)
				}
			} else if !os.IsNotExist(beforeGuideErr) || !os.IsNotExist(beforeStateErr) {
				t.Fatalf("new projection exists before refresh failure, guide error = %v, state error = %v", beforeGuideErr, beforeStateErr)
			}

			hook := filepath.Join(client, ".git", "hooks", "pre-commit")
			writeTestFile(t, hook, "#!/bin/sh\nprintf '%s\\n' 'simulated refresh commit failure' >&2\nexit 42\n")
			if err := os.Chmod(hook, 0o755); err != nil {
				t.Fatal(err)
			}

			err := app.refreshExternalDocs()
			if err == nil || !strings.Contains(err.Error(), "simulated refresh commit failure") {
				t.Fatalf("refresh error = %v, want simulated commit failure", err)
			}

			if got := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")); got != beforeHead {
				t.Fatalf("HEAD changed from %s to %s after precommit failure", beforeHead, got)
			}
			afterGuide, afterGuideErr := os.ReadFile(filepath.Join(client, "external", "project", "guide.md"))
			afterState, afterStateErr := os.ReadFile(filepath.Join(client, ".vaultctl", "external-docs-state.json"))
			if tt.exists {
				if afterGuideErr != nil || string(afterGuide) != string(beforeGuide) {
					t.Fatalf("guide after rollback = %q, err = %v, want %q", afterGuide, afterGuideErr, beforeGuide)
				}
				if afterStateErr != nil || string(afterState) != string(beforeState) {
					t.Fatalf("state after rollback = %q, err = %v, want %q", afterState, afterStateErr, beforeState)
				}
			} else if !os.IsNotExist(afterGuideErr) || !os.IsNotExist(afterStateErr) {
				t.Fatalf("new projection remains after rollback, guide error = %v, state error = %v", afterGuideErr, afterStateErr)
			}
			if got := strings.TrimSpace(runGit(t, client, "diff", "--cached", "--name-only")); got != "staged.md" {
				t.Fatalf("staged paths after rollback = %q, want unrelated staged.md only", got)
			}
			if got := strings.TrimSpace(runGit(t, client, "status", "--porcelain")); got != "A  staged.md\n?? unstaged.md" {
				t.Fatalf("status after rollback = %q, want unrelated staged and unstaged work", got)
			}
		})
	}
}

// AC-9: an uncertain post commit inspection enters manual recovery and keeps the refresh commit.
func TestRefreshExternalDocsReportsManualRecoveryWhenPostCommitInspectionFails(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, _, _ := setupExternalSource(t)
	writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)
	hook := filepath.Join(client, ".git", "hooks", "post-commit")
	writeTestFile(t, hook, "#!/bin/sh\nmv \"$PWD/.git/HEAD\" \"$PWD/.git/HEAD.moved\"\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}

	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	err := app.refreshExternalDocs()
	if err == nil || !strings.Contains(err.Error(), "entered manual recovery") {
		t.Fatalf("refresh error = %v, want manual recovery", err)
	}
	if !strings.Contains(err.Error(), "current HEAD unknown") || !strings.Contains(err.Error(), "expected refresh paths") {
		t.Fatalf("manual recovery error = %v, want captured recovery details", err)
	}

	headPath := filepath.Join(client, ".git", "HEAD")
	movedHeadPath := filepath.Join(client, ".git", "HEAD.moved")
	if _, statErr := os.Stat(headPath); !os.IsNotExist(statErr) {
		t.Fatalf("post commit hook did not remove HEAD, stat error = %v", statErr)
	}
	if err := os.Rename(movedHeadPath, headPath); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGit(t, client, "log", "-1", "--format=%s")); got != externalCommitSubject {
		t.Fatalf("HEAD after manual recovery = %q, want refresh commit", got)
	}
	if _, err := os.Stat(filepath.Join(client, "external", "project", "guide.md")); err != nil {
		t.Fatalf("projection was restored after uncertain commit: %v", err)
	}
}

func TestRefreshExternalDocsRollbackPreservesIgnoredAndEmptyContent(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, _, _ := setupExternalSource(t)
	writeExternalManifest(t, client, `[{"source":"docs","destination":"external/project"}]`)
	writeTestFile(t, filepath.Join(client, ".gitignore"), "external/project/ignored.md\n")
	runGit(t, client, "add", ".gitignore")
	runGit(t, client, "commit", "-m", "ignore local projection content")
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	if err := app.refreshExternalDocs(); err != nil {
		t.Fatalf("initial refreshExternalDocs: %v", err)
	}
	managed := filepath.Join(client, "external", "project")
	writeTestFile(t, filepath.Join(managed, "ignored.md"), "keep ignored content\n")
	if err := os.Mkdir(filepath.Join(managed, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	hook := filepath.Join(client, ".git", "hooks", "pre-commit")
	writeTestFile(t, hook, "#!/bin/sh\nprintf '%s\\n' 'simulated refresh commit failure' >&2\nexit 42\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}

	err := app.refreshExternalDocs()
	if err == nil || !strings.Contains(err.Error(), "simulated refresh commit failure") {
		t.Fatalf("refresh error = %v, want simulated commit failure", err)
	}
	content, err := os.ReadFile(filepath.Join(managed, "ignored.md"))
	if err != nil || string(content) != "keep ignored content\n" {
		t.Fatalf("ignored content after rollback = %q, err = %v", content, err)
	}
	if info, err := os.Stat(filepath.Join(managed, "empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty directory after rollback, info = %#v, err = %v", info, err)
	}
	guide, err := os.ReadFile(filepath.Join(managed, "guide.md"))
	if err != nil || string(guide) != "Guide one\n" {
		t.Fatalf("projected guide after rollback = %q, err = %v", guide, err)
	}
}

func TestRefreshExternalDocsRepeatDoesNotCommit(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, _, _ := setupExternalSource(t)
	writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	if err := app.refreshExternalDocs(); err != nil {
		t.Fatal(err)
	}
	before := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))
	if err := app.refreshExternalDocs(); err != nil {
		t.Fatal(err)
	}
	after := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))
	if after != before {
		t.Fatalf("repeat refresh moved HEAD from %s to %s", before, after)
	}
}

func TestSyncRunsExternalRefreshBeforeNormalSaveAndPush(t *testing.T) {
	remote, client, _ := setupRemoteFixture(t)
	sourceRoot, _, _ := setupExternalSource(t)
	writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)

	app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	if err := app.sync(nil); err != nil {
		t.Fatalf("sync: %v\nstderr:\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(runGit(t, remote, "rev-list", "--count", "main")); got != "3" {
		t.Fatalf("remote commit count = %s, want manifest and refresh commits", got)
	}
	if got := strings.TrimSpace(runGit(t, client, "log", "-2", "--format=%s")); !strings.Contains(got, externalCommitSubject) {
		t.Fatalf("recent commits do not include refresh commit:\n%s", got)
	}
}

func TestSyncKeepsRefreshCommitWhenLaterVaultFetchFails(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, _, _ := setupExternalSource(t)
	writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)
	runGit(t, client, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))

	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	err := app.sync(nil)
	if err == nil || !strings.Contains(err.Error(), "fetch upstream") {
		t.Fatalf("sync error = %v, want later upstream fetch failure", err)
	}
	if got := strings.TrimSpace(runGit(t, client, "log", "-1", "--format=%s")); got != externalCommitSubject {
		t.Fatalf("refresh commit was not kept after later failure, HEAD subject = %q", got)
	}
}

func TestRefreshExternalDocsProjectsDirectoryAndPrunesOwnedFiles(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, sourceRepo, _ := setupExternalSource(t)
	writeTestFile(t, filepath.Join(sourceRepo, "docs", "old.md"), "old\n")
	runGit(t, sourceRepo, "add", "docs/old.md")
	runGit(t, sourceRepo, "commit", "-m", "add old")
	runGit(t, sourceRepo, "push", "origin", "main")
	writeExternalManifest(t, client, `[{"source":"docs","destination":"external/project"}]`)
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	if err := app.refreshExternalDocs(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(client, "external", "project", "guide.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(client, "external", "project", "old.md")); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(sourceRepo, "docs", "old.md")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(sourceRepo, "docs", "guide.md"), "Guide two\n")
	runGit(t, sourceRepo, "add", "docs")
	runGit(t, sourceRepo, "commit", "-m", "remove old")
	runGit(t, sourceRepo, "push", "origin", "main")
	if err := app.refreshExternalDocs(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(client, "external", "project", "old.md")); !os.IsNotExist(err) {
		t.Fatalf("old projected file still exists, stat error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(client, "external", "project", "guide.md"))
	if err != nil || string(content) != "Guide two\n" {
		t.Fatalf("updated projected guide = %q, err = %v", content, err)
	}
}

func TestRefreshExternalDocsPreservesUnrelatedStagedChanges(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, _, _ := setupExternalSource(t)
	writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)
	writeTestFile(t, filepath.Join(client, "staged.md"), "keep staged\n")
	runGit(t, client, "add", "staged.md")

	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	if err := app.refreshExternalDocs(); err != nil {
		t.Fatal(err)
	}
	staged := runGit(t, client, "diff", "--cached", "--name-only")
	if !strings.Contains(staged, "staged.md") {
		t.Fatalf("unrelated staged change was not preserved:\n%s", staged)
	}
	if strings.Contains(staged, "external/project") || strings.Contains(staged, externalStatePath) {
		t.Fatalf("refresh paths remain staged after commit:\n%s", staged)
	}
	if got := strings.TrimSpace(runGit(t, client, "show", "--format=%s", "--no-patch", "HEAD")); got != externalCommitSubject {
		t.Fatalf("HEAD subject = %q, want refresh subject", got)
	}
}

// AC-2: an untracked manifest fails before any source fetch or projection.
func TestRefreshExternalDocsRejectsUntrackedManifestBeforeSourceFetch(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, sourceRepo, _ := setupExternalSource(t)
	control := filepath.Join(client, ".vaultctl")
	if err := os.MkdirAll(control, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(control, "external-docs.json"), `{"version":1,"sources":[{"id":"docs","repository":"project","remote":"origin","reference":"main","mappings":[{"source":"docs/guide.md","destination":"external/project/guide.md"}]}]}`+"\n")
	beforeHead := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))
	fetchHead := strings.TrimSpace(runGit(t, sourceRepo, "rev-parse", "--git-path", "FETCH_HEAD"))
	if !filepath.IsAbs(fetchHead) {
		fetchHead = filepath.Join(sourceRepo, fetchHead)
	}
	_, beforeFetchErr := os.Stat(fetchHead)

	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	err := app.refreshExternalDocs()
	if err == nil || !strings.Contains(err.Error(), "tracked and unchanged") {
		t.Fatalf("refresh error = %v, want untracked manifest refusal", err)
	}
	if afterHead := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")); afterHead != beforeHead {
		t.Fatalf("HEAD changed from %s to %s after untracked manifest refusal", beforeHead, afterHead)
	}
	if _, err := os.Stat(filepath.Join(client, "external", "project", "guide.md")); !os.IsNotExist(err) {
		t.Fatalf("projection exists after untracked manifest refusal, stat error = %v", err)
	}
	_, afterFetchErr := os.Stat(fetchHead)
	if (beforeFetchErr == nil) != (afterFetchErr == nil) {
		t.Fatalf("source FETCH_HEAD existence changed before manifest validation")
	}
}

// AC-5: removing a source identifier from the manifest cannot silently delete its owned paths.
func TestRefreshExternalDocsRejectsRemovedSourceFromState(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, _, _ := setupExternalSource(t)
	control := filepath.Join(client, ".vaultctl")
	if err := os.MkdirAll(control, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"version":1,"sources":[{"id":"api","repository":"project","remote":"origin","reference":"main","mappings":[{"source":"docs/guide.md","destination":"external/project/api-guide.md"}]},{"id":"docs","repository":"project","remote":"origin","reference":"main","mappings":[{"source":"docs/guide.md","destination":"external/project/guide.md"}]}]}` + "\n"
	manifestPath := filepath.Join(control, "external-docs.json")
	writeTestFile(t, manifestPath, manifest)
	runGit(t, client, "add", ".vaultctl/external-docs.json")
	runGit(t, client, "commit", "-m", "configure external documentation")

	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	if err := app.refreshExternalDocs(); err != nil {
		t.Fatalf("initial refreshExternalDocs: %v", err)
	}
	writeTestFile(t, manifestPath, `{"version":1,"sources":[{"id":"docs","repository":"project","remote":"origin","reference":"main","mappings":[{"source":"docs/guide.md","destination":"external/project/guide.md"}]}]}`+"\n")
	runGit(t, client, "add", ".vaultctl/external-docs.json")
	runGit(t, client, "commit", "-m", "remove external source")
	manifestHead := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))

	err := app.refreshExternalDocs()
	if err == nil || !strings.Contains(err.Error(), `generated state contains unknown source "api"`) {
		t.Fatalf("refresh error = %v, want removed source refusal", err)
	}
	if afterHead := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")); afterHead != manifestHead {
		t.Fatalf("refresh created a commit after ownership refusal: before=%s after=%s", manifestHead, afterHead)
	}
	content, err := os.ReadFile(filepath.Join(client, "external", "project", "api-guide.md"))
	if err != nil || string(content) != "Guide one\n" {
		t.Fatalf("removed source projection = %q, err = %v, want preserved content", content, err)
	}
}

// AC-7: a source changing from a directory to a file cannot replace an existing directory destination.
func TestRefreshExternalDocsRejectsDestinationTypeChange(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, sourceRepo, _ := setupExternalSource(t)
	writeExternalManifest(t, client, `[{"source":"docs","destination":"external/project"}]`)
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	if err := app.refreshExternalDocs(); err != nil {
		t.Fatalf("initial refreshExternalDocs: %v", err)
	}
	beforeHead := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))
	if err := os.RemoveAll(filepath.Join(sourceRepo, "docs")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(sourceRepo, "docs"), "the source path is now a file\n")
	runGit(t, sourceRepo, "add", "--all")
	runGit(t, sourceRepo, "commit", "-m", "replace source directory")
	runGit(t, sourceRepo, "push", "origin", "main")

	err := app.refreshExternalDocs()
	if err == nil || !strings.Contains(err.Error(), "file mapping destination") {
		t.Fatalf("refresh error = %v, want destination type refusal", err)
	}
	if afterHead := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")); afterHead != beforeHead {
		t.Fatalf("HEAD changed from %s to %s after destination type refusal", beforeHead, afterHead)
	}
	content, err := os.ReadFile(filepath.Join(client, "external", "project", "guide.md"))
	if err != nil || string(content) != "Guide one\n" {
		t.Fatalf("existing projection = %q, err = %v, want unchanged content", content, err)
	}
}

func TestParseExternalManifestRejectsDuplicateMembersAndUnsafePaths(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "duplicate member", data: `{"version":1,"version":1,"sources":[]}`, want: "duplicate object member"},
		{name: "unknown member", data: `{"version":1,"sources":[],"extra":true}`, want: "unknown field"},
		{name: "unsafe destination", data: `{"version":1,"sources":[{"id":"docs","repository":"project","remote":"origin","reference":"main","mappings":[{"source":"docs","destination":"../outside"}]}]}`, want: "relative path"},
		{name: "overlapping destinations", data: `{"version":1,"sources":[{"id":"docs","repository":"project","remote":"origin","reference":"main","mappings":[{"source":"a","destination":"external"},{"source":"b","destination":"external/nested"}]}]}`, want: "overlap"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseExternalManifest([]byte(tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseExternalManifest error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

// AC-2 and AC-4: refresh rejects an edited tracked manifest before source resolution.
func TestRefreshExternalDocsRejectsEditedManifestBeforeSourceFetch(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, sourceRepo, _ := setupExternalSource(t)
	writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)

	fetchHead := strings.TrimSpace(runGit(t, sourceRepo, "rev-parse", "--git-path", "FETCH_HEAD"))
	if !filepath.IsAbs(fetchHead) {
		fetchHead = filepath.Join(sourceRepo, fetchHead)
	}
	beforeFetch, beforeErr := os.ReadFile(fetchHead)
	writeTestFile(t, filepath.Join(client, filepath.FromSlash(externalManifestPath)), `{"version":1,"sources":[{"id":"docs","repository":"project","remote":"origin","reference":"main","mappings":[{"source":"docs/guide.md","destination":"changed.md"}]}]}`+"\n")

	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	err := app.refreshExternalDocs()
	if err == nil || !strings.Contains(err.Error(), "tracked and unchanged") {
		t.Fatalf("refresh error = %v, want tracked manifest refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(client, "external", "project", "guide.md")); !os.IsNotExist(statErr) {
		t.Fatalf("projection exists after manifest refusal, stat error = %v", statErr)
	}
	afterFetch, afterErr := os.ReadFile(fetchHead)
	if (beforeErr == nil) != (afterErr == nil) || string(beforeFetch) != string(afterFetch) {
		t.Fatalf("source FETCH_HEAD changed before manifest validation")
	}
}

// AC-3 and AC-4: source worktree content is not changed by refresh.
func TestRefreshExternalDocsPreservesSourceWorktreeChanges(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, sourceRepo, _ := setupExternalSource(t)
	writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)
	writeTestFile(t, filepath.Join(sourceRepo, "local-notes.md"), "keep source worktree change\n")
	before := runGit(t, sourceRepo, "status", "--porcelain")

	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	if err := app.refreshExternalDocs(); err != nil {
		t.Fatalf("refreshExternalDocs: %v", err)
	}
	if after := runGit(t, sourceRepo, "status", "--porcelain"); after != before {
		t.Fatalf("source worktree status changed from %q to %q", before, after)
	}
}

func TestRefreshExternalDocsDisablesSourceFetchHooks(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, sourceRepo, sourceRemote := setupExternalSource(t)
	writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)
	fetchHead := strings.TrimSpace(runGit(t, sourceRepo, "rev-parse", "--git-path", "FETCH_HEAD"))
	if !filepath.IsAbs(fetchHead) {
		fetchHead = filepath.Join(sourceRepo, fetchHead)
	}
	hookMutation := filepath.Join(sourceRepo, "post-fetch-mutated.md")
	hook := filepath.Join(sourceRepo, ".git", "hooks", "post-fetch")
	writeTestFile(t, hook, "#!/bin/sh\nprintf '%s\\n' 'mutated by post-fetch' > "+hookMutation+"\nprintf '%s\\n' 'not a fetch result' > "+fetchHead+"\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}

	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	if err := app.refreshExternalDocs(); err != nil {
		t.Fatalf("refreshExternalDocs: %v", err)
	}
	if _, err := os.Stat(hookMutation); !os.IsNotExist(err) {
		t.Fatalf("source post-fetch hook mutated worktree, stat error = %v", err)
	}
	wantCommit := strings.TrimSpace(runGit(t, sourceRemote, "rev-parse", "main"))
	gotCommit, err := readFetchedCommit(sourceRepo, fetchHead)
	if err != nil || gotCommit != wantCommit {
		t.Fatalf("FETCH_HEAD result = %q, err = %v, want %q", gotCommit, err, wantCommit)
	}
}

func TestExternalFetchArgsDisableHooks(t *testing.T) {
	got := externalFetchArgs("origin", "main")
	want := []string{"-c", "core.hooksPath=", "fetch", "--no-tags", "--", "origin", "refs/heads/main"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("externalFetchArgs = %#v, want %#v", got, want)
	}
}

func TestRefreshExternalDocsRejectsUnfinishedSourceOperations(t *testing.T) {
	for _, tt := range []struct {
		name string
		path string
	}{
		{name: "revert", path: "REVERT_HEAD"},
		{name: "sequencer", path: "sequencer"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, client, _ := setupRemoteFixture(t)
			sourceRoot, sourceRepo, _ := setupExternalSource(t)
			writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)
			operationPath := strings.TrimSpace(runGit(t, sourceRepo, "rev-parse", "--git-path", tt.path))
			if !filepath.IsAbs(operationPath) {
				operationPath = filepath.Join(sourceRepo, operationPath)
			}
			if tt.path == "sequencer" {
				if err := os.MkdirAll(operationPath, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				writeTestFile(t, operationPath, strings.Repeat("0", 40)+"\n")
			}
			app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
			err := app.refreshExternalDocs()
			if err == nil || !strings.Contains(err.Error(), "unfinished Git operation") {
				t.Fatalf("refresh error = %v, want unfinished operation refusal", err)
			}
			if _, err := os.Stat(filepath.Join(client, "external", "project", "guide.md")); !os.IsNotExist(err) {
				t.Fatalf("projection exists after source operation refusal, stat error = %v", err)
			}
		})
	}
}

func TestRefreshExternalDocsRejectsUnfinishedVaultOperations(t *testing.T) {
	for _, tt := range []struct {
		name string
		path string
		want string
	}{
		{name: "revert", path: "REVERT_HEAD", want: "unfinished revert"},
		{name: "sequencer", path: "sequencer", want: "unfinished sequencer"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, client, _ := setupRemoteFixture(t)
			sourceRoot, _, _ := setupExternalSource(t)
			writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)
			operationPath := strings.TrimSpace(runGit(t, client, "rev-parse", "--git-path", tt.path))
			if !filepath.IsAbs(operationPath) {
				operationPath = filepath.Join(client, operationPath)
			}
			if tt.path == "sequencer" {
				if err := os.MkdirAll(operationPath, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				writeTestFile(t, operationPath, strings.Repeat("0", 40)+"\n")
			}
			app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
			err := app.refreshExternalDocs()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("refresh error = %v, want %q", err, tt.want)
			}
		})
	}
}

// AC-4 and AC-11: an existing lock prevents any refresh mutation.
func TestRefreshExternalDocsRejectsActiveLock(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, _, _ := setupExternalSource(t)
	writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)
	writeTestFile(t, filepath.Join(client, filepath.FromSlash(externalLockPath)), "active\n")
	beforeHead := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))

	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	err := app.refreshExternalDocs()
	if err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("refresh error = %v, want active lock refusal", err)
	}
	if afterHead := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")); afterHead != beforeHead {
		t.Fatalf("HEAD changed from %s to %s while lock was active", beforeHead, afterHead)
	}
	if _, err := os.Stat(filepath.Join(client, filepath.FromSlash(externalLockPath))); err != nil {
		t.Fatalf("active lock was removed: %v", err)
	}
}

// AC-4: destination ancestry symlinks are rejected without following them.
func TestRefreshExternalDocsRejectsSymlinkedDestinationAncestry(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, _, _ := setupExternalSource(t)
	writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(outside, "sentinel.md"), "untouched\n")
	if err := os.Symlink(outside, filepath.Join(client, "external")); err != nil {
		t.Fatal(err)
	}

	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	err := app.refreshExternalDocs()
	if err == nil || !strings.Contains(err.Error(), "contains a symlink") {
		t.Fatalf("refresh error = %v, want symlink refusal", err)
	}
	content, readErr := os.ReadFile(filepath.Join(outside, "sentinel.md"))
	if readErr != nil || string(content) != "untouched\n" {
		t.Fatalf("outside sentinel changed to %q, err = %v", content, readErr)
	}
}

// AC-3 and AC-4: unsupported source links are rejected before projection.
func TestRefreshExternalDocsRejectsSelectedSourceSymlink(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, sourceRepo, _ := setupExternalSource(t)
	if err := os.Remove(filepath.Join(sourceRepo, "docs", "guide.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../unrelated.md", filepath.Join(sourceRepo, "docs", "guide.md")); err != nil {
		t.Fatal(err)
	}
	runGit(t, sourceRepo, "add", "--all")
	runGit(t, sourceRepo, "commit", "-m", "make selected path a symlink")
	runGit(t, sourceRepo, "push", "origin", "main")
	writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)
	beforeHead := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))

	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	err := app.refreshExternalDocs()
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("refresh error = %v, want unsupported source entry", err)
	}
	if afterHead := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")); afterHead != beforeHead {
		t.Fatalf("HEAD changed from %s to %s after source symlink refusal", beforeHead, afterHead)
	}
	if _, err := os.Stat(filepath.Join(client, "external", "project", "guide.md")); !os.IsNotExist(err) {
		t.Fatalf("projection exists after source symlink refusal, stat error = %v", err)
	}
}

// AC-2 and AC-8: generated state identifiers use the same strict identifier contract.
func TestParseExternalStateRejectsInvalidIdentifiers(t *testing.T) {
	for _, tt := range []struct {
		name   string
		idJSON string
	}{
		{name: "empty", idJSON: `""`},
		{name: "control character", idJSON: `"\u0001"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := `{"version":1,"sources":[{"id":` + tt.idJSON + `,"resolved_commit":"` + strings.Repeat("0", 40) + `","owned_paths":["docs"]}]}`
			if _, err := parseExternalState([]byte(data)); err == nil {
				t.Fatalf("parseExternalState accepted invalid identifier %s", tt.idJSON)
			}
		})
	}
}

func TestParseExternalManifestRejectsInvalidStructure(t *testing.T) {
	validSource := `{"id":"docs","repository":"project","remote":"origin","reference":"main","mappings":[{"source":"docs/guide.md","destination":"external/guide.md"}]}`
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "wrong version", data: `{"version":2,"sources":[` + validSource + `]}`, want: "version must be integer 1"},
		{name: "empty sources", data: `{"version":1,"sources":[]}`, want: "sources must be a nonempty array"},
		{name: "duplicate source identifier", data: `{"version":1,"sources":[` + validSource + `,` + validSource + `]}`, want: "duplicate source identifier"},
		{name: "duplicate mapping", data: `{"version":1,"sources":[{"id":"docs","repository":"project","remote":"origin","reference":"main","mappings":[{"source":"docs/guide.md","destination":"external/guide.md"},{"source":"docs/guide.md","destination":"external/guide.md"}]}]}`, want: "duplicate mapping"},
		{name: "case insensitive overlap", data: `{"version":1,"sources":[{"id":"docs","repository":"project","remote":"origin","reference":"main","mappings":[{"source":"a","destination":"External/guide"},{"source":"b","destination":"external/guide/nested"}]}]}`, want: "overlap"},
		{name: "trailing value", data: `{"version":1,"sources":[` + validSource + `]} true`, want: "more than one JSON value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseExternalManifest([]byte(tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseExternalManifest error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestParseExternalStateRequiresCanonicalOrdering(t *testing.T) {
	commit := strings.Repeat("a", 40)
	stateSource := func(id string, paths string) string {
		return `{"id":"` + id + `","resolved_commit":"` + commit + `","owned_paths":` + paths + `}`
	}
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "source identifiers are sorted",
			data: `{"version":1,"sources":[` + stateSource("z", `["external/z"]`) + `,` + stateSource("a", `["external/a"]`) + `]}`,
			want: "sorted by identifier",
		},
		{
			name: "owned paths are sorted",
			data: `{"version":1,"sources":[` + stateSource("docs", `["external/z","external/a"]`) + `]}`,
			want: "sorted and normalized",
		},
		{
			name: "owned paths are unique",
			data: `{"version":1,"sources":[` + stateSource("docs", `["external/a","external/a"]`) + `]}`,
			want: "sorted and normalized",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseExternalState([]byte(tt.data))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseExternalState error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestReadFetchedCommitRequiresOneValidResult(t *testing.T) {
	commit := strings.Repeat("a", 40)
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "one commit", data: commit + "\t\tnot-for-merge\n", want: ""},
		{name: "empty file", data: "", want: "exactly one"},
		{name: "multiple results", data: commit + "\t\tfirst\n" + strings.Repeat("b", 40) + "\t\tsecond\n", want: "exactly one"},
		{name: "invalid object identifier", data: "not-an-object\t\tresult\n", want: "invalid result"},
		{name: "malformed record", data: commit + "\n", want: "invalid result"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "FETCH_HEAD")
			writeTestFile(t, path, tt.data)
			got, err := readFetchedCommit("unused", path)
			if tt.want == "" {
				if err != nil || got != commit {
					t.Fatalf("readFetchedCommit = %q, %v, want %q", got, err, commit)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("readFetchedCommit error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestRefreshExternalDocsRejectsDivergedSourceReference(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, sourceRepo, _ := setupExternalSource(t)
	writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	if err := app.refreshExternalDocs(); err != nil {
		t.Fatal(err)
	}

	runGit(t, sourceRepo, "switch", "--orphan", "diverged")
	if err := os.RemoveAll(filepath.Join(sourceRepo, "docs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(sourceRepo, "unrelated.md")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sourceRepo, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	commitTestFile(t, sourceRepo, "docs/guide.md", "Diverged\n", "diverged source")
	runGit(t, sourceRepo, "push", "--force", "origin", "diverged:main")
	runGit(t, sourceRepo, "switch", "main")
	beforeHead := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))

	err := app.refreshExternalDocs()
	if err == nil || !strings.Contains(err.Error(), "diverges from recorded commit") {
		t.Fatalf("refresh error = %v, want source divergence refusal", err)
	}
	if afterHead := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")); afterHead != beforeHead {
		t.Fatalf("HEAD changed from %s to %s after source divergence refusal", beforeHead, afterHead)
	}
	content, err := os.ReadFile(filepath.Join(client, "external", "project", "guide.md"))
	if err != nil || string(content) != "Guide one\n" {
		t.Fatalf("projected guide after divergence = %q, err = %v", content, err)
	}
}

func TestRefreshExternalDocsRejectsChangedOwnership(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, _, _ := setupExternalSource(t)
	writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	if err := app.refreshExternalDocs(); err != nil {
		t.Fatal(err)
	}

	writeTestFile(t, filepath.Join(client, filepath.FromSlash(externalManifestPath)), `{"version":1,"sources":[{"id":"docs","repository":"project","remote":"origin","reference":"main","mappings":[{"source":"docs/guide.md","destination":"external/renamed/guide.md"}]}]}`+"\n")
	runGit(t, client, "add", filepath.FromSlash(externalManifestPath))
	runGit(t, client, "commit", "-m", "change external ownership")
	beforeHead := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))

	err := app.refreshExternalDocs()
	if err == nil || !strings.Contains(err.Error(), "changed its owned destination paths") {
		t.Fatalf("refresh error = %v, want ownership refusal", err)
	}
	if afterHead := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")); afterHead != beforeHead {
		t.Fatalf("HEAD changed from %s to %s after ownership refusal", beforeHead, afterHead)
	}
	if _, err := os.Stat(filepath.Join(client, "external", "renamed")); !os.IsNotExist(err) {
		t.Fatalf("new ownership path exists after refusal, stat error = %v", err)
	}
}

func TestRefreshExternalDocsRejectsDirtyManagedDestination(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, sourceRepo, _ := setupExternalSource(t)
	writeExternalManifest(t, client, `[{"source":"docs/guide.md","destination":"external/project/guide.md"}]`)
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	if err := app.refreshExternalDocs(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(client, "external", "project", "guide.md"), "local edit\n")
	fetchHead := strings.TrimSpace(runGit(t, sourceRepo, "rev-parse", "--git-path", "FETCH_HEAD"))
	if !filepath.IsAbs(fetchHead) {
		fetchHead = filepath.Join(sourceRepo, fetchHead)
	}
	beforeFetch, err := os.ReadFile(fetchHead)
	if err != nil {
		t.Fatal(err)
	}

	err = app.refreshExternalDocs()
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("refresh error = %v, want dirty managed path refusal", err)
	}
	afterFetch, err := os.ReadFile(fetchHead)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterFetch) != string(beforeFetch) {
		t.Fatal("source FETCH_HEAD changed before dirty destination validation")
	}
}
