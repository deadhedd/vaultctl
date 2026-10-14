package vaultctl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type syncOutputMutationWriter struct {
	bytes.Buffer
	onWrite func(string)
}

func (w *syncOutputMutationWriter) Write(data []byte) (int, error) {
	n, err := w.Buffer.Write(data)
	if err == nil && w.onWrite != nil {
		w.onWrite(string(data))
	}
	return n, err
}

func setupTrackingSubmoduleFixture(t *testing.T, explicitBranch bool) (parentRemote, client, childRemote, childPath string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	parentRemote, client, _ = setupRemoteFixture(t)

	child := initTestRepo(t)
	childRemote = filepath.Join(t.TempDir(), "child.git")
	if err := os.MkdirAll(childRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, childRemote, "init", "--bare", "-b", "main", ".")
	runGit(t, child, "remote", "add", "origin", childRemote)
	runGit(t, child, "push", "-u", "origin", "main")
	runGit(t, client, "config", "protocol.file.allow", "always")
	runGit(t, client, "submodule", "add", childRemote, "Projects/child")
	childPath = filepath.Join(client, "Projects", "child")
	if explicitBranch {
		runGit(t, client, "config", "--file", ".gitmodules", "submodule.Projects/child.branch", "main")
	}
	runGit(t, client, "add", "--", ".gitmodules")
	runGit(t, client, "commit", "-m", "add tracking child")
	runGit(t, client, "push", "origin", "main")
	configureIdentity(t, childPath)
	return parentRemote, client, childRemote, childPath
}

func setupStandaloneRepositoryAtPath(t testing.TB, path string) (remote string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatalf("remove registered submodule worktree: %v", err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("create standalone repository path: %v", err)
	}
	runGit(t, path, "init", "-b", "main", ".")
	configureIdentity(t, path)
	writeTestFile(t, filepath.Join(path, "shared.md"), "base version\n")
	runGit(t, path, "add", "shared.md")
	runGit(t, path, "commit", "-m", "standalone base")
	remote = filepath.Join(t.TempDir(), "standalone.git")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatalf("create standalone remote: %v", err)
	}
	runGit(t, remote, "init", "--bare", "-b", "main", ".")
	runGit(t, path, "remote", "add", "origin", remote)
	runGit(t, path, "push", "-u", "origin", "main")
	return remote
}

func startStandaloneRebaseConflict(t testing.TB, path, remote string) {
	t.Helper()
	commitTestFile(t, path, "shared.md", "local version\n", "standalone local change")
	peer := filepath.Join(t.TempDir(), "standalone-peer")
	runGit(t, filepath.Dir(peer), "clone", "--branch", "main", remote, peer)
	configureIdentity(t, peer)
	commitTestFile(t, peer, "shared.md", "remote version\n", "standalone remote change")
	runGit(t, peer, "push", "origin", "main")
	runGit(t, path, "fetch", "origin")
	output := runGitFailure(t, path, "rebase", "origin/main")
	if !strings.Contains(output, "CONFLICT") {
		t.Fatalf("standalone rebase output = %q, want a conflict", output)
	}
}

// covers: AC-5, AC-6
func TestValidateRegisteredSubmoduleIdentityAcceptsInitializedChild(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	_, client, _, childPath := setupTrackingSubmoduleFixture(t, true)
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client})
	entries, _, err := loadDirectSubmodules(app.git)
	if err != nil {
		t.Fatalf("load registered submodule: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("registered submodules = %d, want 1", len(entries))
	}
	if err := app.validateRegisteredSubmoduleIdentity(entries[0]); err != nil {
		t.Fatalf("validate initialized child: %v", err)
	}

	override := filepath.Join(t.TempDir(), "local-origin.git")
	if err := os.MkdirAll(override, 0o755); err != nil {
		t.Fatalf("create local origin override: %v", err)
	}
	runGit(t, override, "init", "--bare", "-b", "main", ".")
	runGit(t, childPath, "remote", "set-url", "origin", override)
	if err := app.validateRegisteredSubmoduleIdentity(entries[0]); err != nil {
		t.Fatalf("validate child with local origin override: %v", err)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "remote", "get-url", "origin")); got != override {
		t.Fatalf("child origin after identity validation = %q, want local override %q", got, override)
	}
}

// covers: AC-5, AC-6, AC-8
func TestSyncRejectsStandaloneRepositoryAtRegisteredSubmodulePath(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	_, client, _, childPath := setupTrackingSubmoduleFixture(t, true)
	remote := setupStandaloneRepositoryAtPath(t, childPath)
	writeTestFile(t, filepath.Join(childPath, "local.md"), "do not autosave this repository\n")
	headBefore := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	remoteBefore := strings.TrimSpace(runGit(t, remote, "rev-parse", "refs/heads/main"))
	statusBefore := strings.TrimSpace(runGit(t, childPath, "status", "--porcelain"))

	app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	err := app.sync(nil)
	if err == nil || !strings.Contains(err.Error(), "registered Git submodule") {
		t.Fatalf("sync error = %v, want registered Git submodule identity refusal\nstderr:\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD")); got != headBefore {
		t.Fatalf("standalone repository HEAD changed: %s -> %s", headBefore, got)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "status", "--porcelain")); got != statusBefore {
		t.Fatalf("standalone repository status changed: %q -> %q", statusBefore, got)
	}
	if got := strings.TrimSpace(runGit(t, remote, "rev-parse", "refs/heads/main")); got != remoteBefore {
		t.Fatalf("standalone remote changed: %s -> %s", remoteBefore, got)
	}
}

// covers: AC-8
func TestSyncSubmoduleRecoveryRejectsUnrelatedRepositoryIdentity(t *testing.T) {
	for _, tt := range []struct {
		name   string
		action string
	}{
		{name: "abort", action: "abort"},
		{name: "continue", action: "continue"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
			t.Setenv("GIT_ALLOW_PROTOCOL", "file")
			t.Setenv("GIT_EDITOR", "true")
			_, client, _, childPath := setupTrackingSubmoduleFixture(t, true)
			remote := setupStandaloneRepositoryAtPath(t, childPath)
			startStandaloneRebaseConflict(t, childPath, remote)
			if tt.action == "continue" {
				writeTestFile(t, filepath.Join(childPath, "shared.md"), "resolved outside the registered submodule\n")
			}
			headBefore := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
			statusBefore := strings.TrimSpace(runGit(t, childPath, "status", "--porcelain"))
			remoteBefore := strings.TrimSpace(runGit(t, remote, "rev-parse", "refs/heads/main"))
			app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
			err := app.sync([]string{"--" + tt.action, "--submodule", "Projects/child"})
			if err == nil || !strings.Contains(err.Error(), "registered Git submodule") {
				t.Fatalf("scoped %s error = %v, want registered Git submodule identity refusal\nstderr:\n%s", tt.action, err, stderr.String())
			}
			child, _, err := app.submoduleApp("Projects/child")
			if err != nil {
				t.Fatalf("inspect standalone repository after refusal: %v", err)
			}
			operation, err := child.detectOperation()
			if err != nil || !operation.rebase {
				t.Fatalf("standalone rebase after refusal = %#v, err = %v, want rebase in progress", operation, err)
			}
			if got := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD")); got != headBefore {
				t.Fatalf("standalone repository HEAD changed: %s -> %s", headBefore, got)
			}
			if got := strings.TrimSpace(runGit(t, childPath, "status", "--porcelain")); got != statusBefore {
				t.Fatalf("standalone repository status changed: %q -> %q", statusBefore, got)
			}
			if got := strings.TrimSpace(runGit(t, remote, "rev-parse", "refs/heads/main")); got != remoteBefore {
				t.Fatalf("standalone remote changed: %s -> %s", remoteBefore, got)
			}
		})
	}
}

func commitProjectionManifest(t *testing.T, vault, repository string) string {
	t.Helper()
	control := filepath.Join(vault, filepath.FromSlash(filepath.Dir(externalManifestPath)))
	if err := os.MkdirAll(control, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{"version":1,"sources":[{"id":"source","repository":%q,"remote":"origin","reference":"main","mappings":[{"source":"note.md","destination":"external/source/note.md"}]}]}`+"\n", repository)
	writeTestFile(t, filepath.Join(vault, filepath.FromSlash(externalManifestPath)), manifest)
	runGit(t, vault, "add", "--", filepath.FromSlash(externalManifestPath))
	runGit(t, vault, "commit", "-m", "configure projection source")
	runGit(t, vault, "push", "origin", "main")
	return manifest
}

func testFileURL(t *testing.T, path string) string {
	t.Helper()
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolve file URL path: %v", err)
	}
	urlPath := filepath.ToSlash(absolute)
	if filepath.VolumeName(absolute) != "" {
		urlPath = "/" + urlPath
	}
	return (&url.URL{Scheme: "file", Path: urlPath}).String()
}

func prepareParentFollowupCommit(t *testing.T, parentRemote string, prepare func(string)) string {
	t.Helper()
	peer := filepath.Join(t.TempDir(), "parent-peer")
	runGit(t, filepath.Dir(peer), "clone", "--branch", "main", parentRemote, peer)
	configureIdentity(t, peer)
	prepare(peer)
	runGit(t, peer, "commit", "-m", "concurrent parent metadata")
	commit := strings.TrimSpace(runGit(t, peer, "rev-parse", "HEAD"))
	runGit(t, peer, "push", "origin", "HEAD:refs/heads/vaultctl-followup")
	return commit
}

// covers: AC-2, AC-10, AC-14
func TestSyncRevalidatesNewlyActiveSubmoduleAfterParentFollowup(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	parentRemote, client, _, childPath := setupTrackingSubmoduleFixture(t, true)
	newChild := initTestRepo(t)
	newChildRemote := filepath.Join(t.TempDir(), "new-child.git")
	if err := os.MkdirAll(newChildRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, newChildRemote, "init", "--bare", "-b", "main", ".")
	runGit(t, newChild, "remote", "add", "origin", newChildRemote)
	runGit(t, newChild, "push", "-u", "origin", "main")
	newChildPath := "Projects/new-child"
	advance := prepareParentFollowupCommit(t, parentRemote, func(peer string) {
		runGit(t, peer, "config", "protocol.file.allow", "always")
		runGit(t, peer, "submodule", "add", newChildRemote, newChildPath)
		runGit(t, peer, "config", "--file", ".gitmodules", "submodule."+newChildPath+".branch", "main")
		runGit(t, peer, "add", "--", ".gitmodules")
	})
	runGit(t, client, "config", "--add", "submodule.active", "Projects/child")
	runGit(t, client, "config", "--add", "submodule.active", newChildPath)
	writeTestFile(t, filepath.Join(childPath, "completed.md"), "completed child work\n")
	installGitParentFollowupAdvance(t, client, parentRemote, advance)

	app, stdout, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	err := app.sync(nil)
	entries, _, loadErr := loadDirectSubmodules(app.git)
	if loadErr != nil {
		t.Fatalf("load parent submodules after followup: %v", loadErr)
	}
	selectedAfterFollowup, selectErr := activeSubmodules(app.git, entries)
	if selectErr != nil {
		t.Fatalf("select parent submodules after followup: %v", selectErr)
	}
	if len(selectedAfterFollowup) != 2 || selectedAfterFollowup[1].path != newChildPath {
		t.Fatalf("post followup active submodules = %#v, want Projects/child and %s", selectedAfterFollowup, newChildPath)
	}
	if err == nil || !strings.Contains(err.Error(), "submodule metadata changed during child synchronization") {
		t.Fatalf("first sync error = %v, want stale selection refusal\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(err.Error(), "run vaultctl sync again") {
		t.Fatalf("stale selection error is not actionable: %v", err)
	}
	newChildApp, newChildRoot, err := app.submoduleApp(newChildPath)
	if err != nil {
		t.Fatalf("resolve newly active child: %v", err)
	}
	inside, err := newChildApp.git.capture("rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("inspect newly active child worktree: %v", err)
	}
	if inside.exitCode == 0 {
		actual, err := canonicalDirectory(strings.TrimSpace(inside.stdout))
		if err != nil {
			t.Fatalf("resolve newly active child worktree: %v", err)
		}
		if actual == newChildRoot {
			t.Fatalf("newly active child was initialized before its synchronization")
		}
	}
	completedHead := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	if got := strings.TrimSpace(runGit(t, childPath, "show", "HEAD:completed.md")); got != "completed child work" {
		t.Fatalf("successfully synchronized child work = %q, want it preserved at %s", got, completedHead)
	}
	if got := strings.TrimSpace(runGit(t, parentRemote, "rev-parse", "refs/heads/main")); got != strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")) {
		t.Fatalf("parent followup did not complete before refusal: remote %s, client %s", got, strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")))
	}
	if got := gitlinkCommit(t, client, "Projects/child"); got != completedHead {
		t.Fatalf("completed child gitlink = %s, want %s", got, completedHead)
	}

	if err := app.sync(nil); err != nil {
		t.Fatalf("normal sync retry: %v\nstderr:\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(runGit(t, filepath.Join(client, filepath.FromSlash(newChildPath)), "rev-parse", "HEAD")); got != strings.TrimSpace(runGit(t, newChildRemote, "rev-parse", "refs/heads/main")) {
		t.Fatalf("retry left newly active child at %s, want origin/main", got)
	}
	if !strings.Contains(stdout.String(), "Synchronization completed successfully.") {
		t.Fatalf("normal sync retry did not report completion:\n%s", stdout.String())
	}
}

// covers: AC-13, AC-14
func TestSyncReloadsProjectionProtectionAfterParentFollowup(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	parentRemote, client, _, childPath := setupTrackingSubmoduleFixture(t, true)
	pinnedSource := initTestRepo(t)
	pinnedRemote := filepath.Join(t.TempDir(), "pinned.git")
	if err := os.MkdirAll(pinnedRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, pinnedRemote, "init", "--bare", "-b", "main", ".")
	runGit(t, pinnedSource, "remote", "add", "origin", pinnedRemote)
	runGit(t, pinnedSource, "push", "-u", "origin", "main")
	pinnedPath := "Projects/pinned-source"
	runGit(t, client, "config", "protocol.file.allow", "always")
	runGit(t, client, "submodule", "add", pinnedRemote, pinnedPath)
	runGit(t, client, "config", "--file", ".gitmodules", "submodule."+pinnedPath+".vaultctl-pinned", "true")
	runGit(t, client, "add", "--", ".gitmodules")
	runGit(t, client, "commit", "-m", "add pinned source candidate")
	runGit(t, client, "push", "origin", "main")
	peer := filepath.Join(t.TempDir(), "pinned-peer")
	runGit(t, filepath.Dir(peer), "clone", "--branch", "main", parentRemote, peer)
	runGit(t, peer, "config", "protocol.file.allow", "always")
	runGit(t, peer, "submodule", "update", "--init", "--", pinnedPath)
	pinnedPeer := filepath.Join(peer, filepath.FromSlash(pinnedPath))
	pinnedBase := strings.TrimSpace(runGit(t, pinnedPeer, "rev-parse", "HEAD"))
	newPinnedHead := commitTestFileAndHash(t, pinnedSource, "later.md", "later pinned source\n", "advance pinned source")
	runGit(t, pinnedSource, "push", "origin", "main")
	manifest := fmt.Sprintf(`{"version":1,"sources":[{"id":"pinned-source","repository":%q,"remote":"origin","reference":"main","mappings":[{"source":"note.md","destination":"external/pinned/note.md"}]}]}`+"\n", pinnedPath)
	advance := prepareParentFollowupCommit(t, parentRemote, func(parentPeer string) {
		runGit(t, parentPeer, "config", "protocol.file.allow", "always")
		runGit(t, parentPeer, "submodule", "update", "--init", "--", pinnedPath)
		if got := strings.TrimSpace(runGit(t, filepath.Join(parentPeer, filepath.FromSlash(pinnedPath)), "rev-parse", "HEAD")); got != pinnedBase {
			t.Fatalf("concurrent parent child HEAD = %s, want pinned baseline %s", got, pinnedBase)
		}
		runGit(t, filepath.Join(parentPeer, filepath.FromSlash(pinnedPath)), "fetch", "origin")
		runGit(t, filepath.Join(parentPeer, filepath.FromSlash(pinnedPath)), "checkout", newPinnedHead)
		runGit(t, parentPeer, "add", "--", pinnedPath)
		if err := os.MkdirAll(filepath.Join(parentPeer, filepath.FromSlash(filepath.Dir(externalManifestPath))), 0o755); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(parentPeer, filepath.FromSlash(externalManifestPath)), manifest)
		runGit(t, parentPeer, "add", "--", filepath.FromSlash(externalManifestPath))
	})
	writeTestFile(t, filepath.Join(childPath, "completed.md"), "completed child work\n")
	installGitParentFollowupAdvance(t, client, parentRemote, advance)

	app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: client})
	err := app.sync(nil)
	if err == nil || !strings.Contains(err.Error(), "overlaps external documentation source") {
		t.Fatalf("sync error = %v, want fresh projection protection refusal\nstderr:\n%s", err, stderr.String())
	}
	if !strings.Contains(err.Error(), "run vaultctl sync again") {
		t.Fatalf("projection protection error is not actionable: %v", err)
	}
	if got := strings.TrimSpace(runGit(t, filepath.Join(client, filepath.FromSlash(pinnedPath)), "rev-parse", "HEAD")); got != pinnedBase {
		t.Fatalf("final checkout changed protected source worktree: got %s, want %s", got, pinnedBase)
	}
	if got := gitlinkCommit(t, client, pinnedPath); got != newPinnedHead {
		t.Fatalf("parent followup gitlink = %s, want concurrent revision %s", got, newPinnedHead)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "show", "HEAD:completed.md")); got != "completed child work" {
		t.Fatalf("tracking child work was lost after checkout refusal: %q", got)
	}
}

// covers: AC-5, AC-7, AC-11, AC-14
func TestSyncTracksExplicitBranchAndRecordsGroupedGitlink(t *testing.T) {
	parentRemote, client, childRemote, childPath := setupTrackingSubmoduleFixture(t, true)
	branchBefore := strings.TrimSpace(runGit(t, childPath, "symbolic-ref", "--quiet", "--short", "HEAD"))
	writeTestFile(t, filepath.Join(childPath, "local.md"), "safe local work\n")
	app, stdout, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	app.hostname = func() (string, error) { return "tracking-test", nil }
	if err := app.sync(nil); err != nil {
		t.Fatalf("sync: %v\nstderr:\n%s", err, stderr.String())
	}

	childHead := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	if remoteHead := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main")); remoteHead != childHead {
		t.Fatalf("child remote main = %s, want pushed child HEAD %s", remoteHead, childHead)
	}
	parentLink := gitlinkCommit(t, client, "Projects/child")
	if parentLink != childHead {
		t.Fatalf("parent gitlink = %s, want synchronized child HEAD %s", parentLink, childHead)
	}
	if remoteParentHead := strings.TrimSpace(runGit(t, parentRemote, "rev-parse", "refs/heads/main")); remoteParentHead != strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")) {
		t.Fatalf("parent remote main = %s, want client HEAD %s", remoteParentHead, strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")))
	}
	if got := strings.TrimSpace(runGit(t, childPath, "status", "--porcelain")); got != "" {
		t.Fatalf("child worktree status = %q, want clean", got)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "symbolic-ref", "--quiet", "--short", "HEAD")); got != branchBefore {
		t.Fatalf("tracking sync changed child branch from %q to %q", branchBefore, got)
	}
	output := stdout.String()
	for _, required := range []string{
		"Parent vault synchronization succeeded.",
		"Saving safe local work before reconciling the selected branch...",
		"Staging all submodule Projects/child changes...",
		"Creating submodule Projects/child commit...",
		"Submodule Projects/child changes saved.",
		"Pushing main to origin/main...",
		"Recording synchronized project submodules in one parent commit...",
		"Synchronizing the grouped parent gitlink commit...",
		"Synchronizing selected submodules...",
		"Synchronization completed successfully.",
	} {
		if !strings.Contains(output, required) {
			t.Fatalf("sync output missing %q:\n%s", required, output)
		}
	}
	if strings.TrimSpace(runGit(t, client, "rev-list", "--count", "HEAD")) != "3" {
		t.Fatalf("parent commit count = %s, want add-child plus grouped-link commits after base", strings.TrimSpace(runGit(t, client, "rev-list", "--count", "HEAD")))
	}
}

// covers: AC-10
func TestSyncProcessesTrackingSubmodulesInAscendingPathOrder(t *testing.T) {
	tests := []struct {
		name              string
		registrationOrder []string
	}{
		{
			name:              "completes in path order after reverse registration",
			registrationOrder: []string{"Projects/z-last", "Projects/a-first"},
		},
		{
			name:              "completes in path order after sorted registration",
			registrationOrder: []string{"Projects/a-first", "Projects/z-last"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client, _, firstChildPath := setupTrackingSubmoduleFixture(t, true)
			addTrackingSubmodule := func(path string) string {
				t.Helper()
				source := initTestRepo(t)
				remote := filepath.Join(t.TempDir(), "child.git")
				if err := os.MkdirAll(remote, 0o755); err != nil {
					t.Fatal(err)
				}
				runGit(t, remote, "init", "--bare", "-b", "main", ".")
				runGit(t, source, "remote", "add", "origin", remote)
				runGit(t, source, "push", "-u", "origin", "main")
				runGit(t, client, "submodule", "add", remote, path)
				runGit(t, client, "config", "--file", ".gitmodules", "submodule."+path+".branch", "main")
				runGit(t, client, "add", "--", ".gitmodules", path)
				runGit(t, client, "commit", "-m", "add "+path)
				runGit(t, client, "push", "origin", "main")
				childPath := filepath.Join(client, filepath.FromSlash(path))
				configureIdentity(t, childPath)
				return childPath
			}

			childPaths := map[string]string{"Projects/child": firstChildPath}
			for _, path := range tt.registrationOrder {
				childPaths[path] = addTrackingSubmodule(path)
			}
			for index, path := range []string{"Projects/child", "Projects/a-first", "Projects/z-last"} {
				writeTestFile(t, filepath.Join(childPaths[path], fmt.Sprintf("local-%d.md", index)), "safe local work\n")
			}

			app, stdout, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
			if err := app.sync(nil); err != nil {
				t.Fatalf("sync: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			}

			output := stdout.String()
			previous := -1
			for _, path := range []string{"Projects/a-first", "Projects/child", "Projects/z-last"} {
				position := strings.Index(output, "Tracking submodule "+path+" is synchronized at ")
				if position < 0 {
					t.Fatalf("sync output does not report completion for %s:\n%s", path, output)
				}
				if position <= previous {
					t.Fatalf("tracking submodules did not complete in ascending path order; %s completed out of order:\n%s", path, output)
				}
				previous = position
			}
		})
	}
}

// covers: AC-4, AC-5, AC-6, AC-7, AC-11, AC-14
func TestSyncRebasesTrackingChildOntoSelectedBranch(t *testing.T) {
	parentRemote, client, childRemote, childPath := setupTrackingSubmoduleFixture(t, true)
	peerRoot := t.TempDir()
	peer := filepath.Join(peerRoot, "child-peer")
	runGit(t, peerRoot, "clone", "--branch", "main", childRemote, peer)
	configureIdentity(t, peer)
	remoteTarget := commitTestFileAndHash(t, peer, "remote.md", "remote branch work\n", "remote branch work")
	runGit(t, peer, "push", "origin", "main")
	branchBefore := strings.TrimSpace(runGit(t, childPath, "symbolic-ref", "--quiet", "--short", "HEAD"))
	writeTestFile(t, filepath.Join(childPath, "local.md"), "safe local work\n")
	app, stdout, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	app.hostname = func() (string, error) { return "rebase-test", nil }
	if err := app.sync(nil); err != nil {
		t.Fatalf("sync: %v\nstderr:\n%s", err, stderr.String())
	}

	childHead := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	parents := strings.Fields(runGit(t, childPath, "rev-list", "--parents", "-n", "1", "HEAD"))
	if len(parents) != 2 || parents[1] != remoteTarget {
		t.Fatalf("rebased child commit parents = %v, want fetched target %s as its sole parent", parents, remoteTarget)
	}
	if got := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main")); got != childHead {
		t.Fatalf("child remote main = %s, want rebased child HEAD %s", got, childHead)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "symbolic-ref", "--quiet", "--short", "HEAD")); got != branchBefore {
		t.Fatalf("tracking sync changed child branch from %q to %q", branchBefore, got)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "config", "--get", "remote.origin.url")); got != childRemote {
		t.Fatalf("child origin URL = %q, want initialized URL %q", got, childRemote)
	}
	if got := gitlinkCommit(t, client, "Projects/child"); got != childHead {
		t.Fatalf("parent gitlink = %s, want rebased child HEAD %s", got, childHead)
	}
	if got := strings.TrimSpace(runGit(t, parentRemote, "rev-parse", "refs/heads/main")); got != strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")) {
		t.Fatalf("parent remote main = %s, want client HEAD", got)
	}
	output := stdout.String()
	fetch := strings.Index(output, "Fetching origin branch main...")
	save := strings.Index(output, "Saving safe local work before reconciling the selected branch...")
	rebase := strings.Index(output, "Histories diverged; rebasing local commits onto origin/main...")
	push := strings.Index(output, "Pushing main to origin/main...")
	if fetch < 0 || save <= fetch || rebase <= save || push <= rebase {
		t.Fatalf("child fetch, autosave, rebase, or push phases are missing or out of order:\n%s", output)
	}
}

// covers: AC-4, AC-5
func TestSyncUsesRemoteSymbolicDefaultBranch(t *testing.T) {
	_, client, childRemote, childPath := setupTrackingSubmoduleFixture(t, false)
	writeTestFile(t, filepath.Join(childPath, "local.md"), "default branch work\n")
	app, stdout, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	if err := app.sync(nil); err != nil {
		t.Fatalf("sync: %v\nstderr:\n%s", err, stderr.String())
	}
	childHead := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	if got := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main")); got != childHead {
		t.Fatalf("child remote main = %s, want child HEAD %s", got, childHead)
	}
	if !strings.Contains(stdout.String(), "Pushing main to origin/main...") {
		t.Fatalf("sync did not use the symbolic default branch:\n%s", stdout.String())
	}
}

// covers: AC-5, AC-11
func TestSyncMergeReconcilesTrackingChild(t *testing.T) {
	t.Setenv("GIT_EDITOR", "true")
	parentRemote, client, childRemote, childPath := setupTrackingSubmoduleFixture(t, true)
	peer := filepath.Join(t.TempDir(), "child-peer")
	runGit(t, childRemote, "clone", "--branch", "main", childRemote, peer)
	configureIdentity(t, peer)
	commitTestFile(t, peer, "remote.md", "remote child work\n", "remote child work")
	runGit(t, peer, "push", "origin", "main")
	commitTestFile(t, childPath, "local.md", "local child work\n", "local child work")
	app, stdout, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	if err := app.sync([]string{"--merge"}); err != nil {
		t.Fatalf("sync --merge: %v\nstderr:\n%s", err, stderr.String())
	}
	head := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	if got := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main")); got != head {
		t.Fatalf("child remote main = %s, want merged child HEAD %s", got, head)
	}
	parents := strings.Fields(runGit(t, childPath, "rev-list", "--parents", "-n", "1", "HEAD"))
	if len(parents) != 3 {
		t.Fatalf("merged child commit has %d parents, want 2: %v", len(parents)-1, parents)
	}
	if got := gitlinkCommit(t, client, "Projects/child"); got != head {
		t.Fatalf("parent gitlink = %s, want merged child HEAD %s", got, head)
	}
	if got := strings.TrimSpace(runGit(t, parentRemote, "rev-parse", "refs/heads/main")); got != strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")) {
		t.Fatalf("parent remote main = %s, want client HEAD", got)
	}
	if !strings.Contains(stdout.String(), "Histories diverged; merging origin/main...") {
		t.Fatalf("child did not use the merge strategy:\n%s", stdout.String())
	}
}

// covers: AC-2, AC-4
func TestSyncInitializesMissingActiveTrackingSubmodule(t *testing.T) {
	parentRemote, client, childRemote, _ := setupTrackingSubmoduleFixture(t, false)
	fresh := filepath.Join(t.TempDir(), "fresh-client")
	runGit(t, client, "clone", parentRemote, fresh)
	runGit(t, fresh, "config", "submodule.active", "Projects/child")
	childPath := filepath.Join(fresh, "Projects", "child")
	if got := strings.TrimSpace(runGit(t, childPath, "rev-parse", "--show-toplevel")); got != fresh {
		t.Fatalf("fresh clone child resolves to %q before sync, want parent worktree %q", got, fresh)
	}
	app, stdout, stderr := testApp(Config{Mode: ModeClient, VaultPath: fresh})
	if err := app.sync(nil); err != nil {
		t.Fatalf("sync: %v\nstderr:\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD")); got != strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main")) {
		t.Fatalf("fresh tracking child HEAD = %s, want origin/main", got)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "config", "--get", "remote.origin.url")); got != childRemote {
		t.Fatalf("initialized child origin URL = %q, want %q", got, childRemote)
	}
	if !strings.Contains(stdout.String(), "Fetching origin branch main...") {
		t.Fatalf("fresh tracking child was not synchronized after initialization:\n%s", stdout.String())
	}
}

// covers: AC-2
func TestSyncDoesNotCheckoutInactiveSubmodule(t *testing.T) {
	_, client, childRemote, childPath := setupTrackingSubmoduleFixture(t, true)
	baseline := gitlinkCommit(t, client, "Projects/child")
	commitTestFile(t, childPath, "local.md", "preserve inactive child head\n", "local inactive work")
	localHead := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	remoteHead := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main"))
	runGit(t, client, "config", "submodule.Projects/child.active", "false")
	app, stdout, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	if err := app.sync(nil); err != nil {
		t.Fatalf("sync: %v\nstderr:\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD")); got != localHead {
		t.Fatalf("inactive child was checked out from %s to %s", localHead, got)
	}
	if got := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main")); got != remoteHead {
		t.Fatalf("inactive child remote changed from %s to %s", remoteHead, got)
	}
	if got := gitlinkCommit(t, client, "Projects/child"); got != baseline {
		t.Fatalf("inactive parent gitlink = %s, want baseline %s", got, baseline)
	}
	if !strings.Contains(stdout.String(), "No active submodules are selected; skipping recursive checkout.") {
		t.Fatalf("inactive selection was not reported:\n%s", stdout.String())
	}
}

// covers: AC-4, AC-14
func TestSyncRejectsUnresolvedRemoteDefaultBeforeChildAutosave(t *testing.T) {
	_, client, childRemote, childPath := setupTrackingSubmoduleFixture(t, false)
	runGit(t, childRemote, "symbolic-ref", "HEAD", "refs/heads/missing")
	writeTestFile(t, filepath.Join(childPath, "local.md"), "must remain uncommitted\n")
	beforeHead := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	err := app.sync(nil)
	if err == nil || !strings.Contains(err.Error(), "unambiguous symbolic default branch") {
		t.Fatalf("sync error = %v, want unresolved symbolic default refusal", err)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD")); got != beforeHead {
		t.Fatalf("child HEAD changed before default branch validation: %s -> %s", beforeHead, got)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "status", "--porcelain")); !strings.Contains(got, "local.md") {
		t.Fatalf("local child work was autosaved before branch validation: %q", got)
	}
	if !strings.Contains(stderr.String(), "Parent synchronization succeeded") {
		t.Fatalf("child failure did not identify the completed parent phase:\n%s", stderr.String())
	}
}

// covers: AC-4
func TestSyncRejectsMissingExplicitBranchBeforeChildAutosave(t *testing.T) {
	_, client, childRemote, childPath := setupTrackingSubmoduleFixture(t, true)
	runGit(t, client, "config", "--file", ".gitmodules", "submodule.Projects/child.branch", "missing")
	runGit(t, client, "add", "--", ".gitmodules")
	runGit(t, client, "commit", "-m", "select missing child branch")
	runGit(t, client, "push", "origin", "main")
	writeTestFile(t, filepath.Join(childPath, "local.md"), "must remain uncommitted\n")
	beforeHead := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	remoteBefore := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main"))
	app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	err := app.sync(nil)
	if err == nil || !strings.Contains(err.Error(), "submodule \"Projects/child\"") {
		t.Fatalf("sync error = %v, want path-qualified missing branch refusal", err)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD")); got != beforeHead {
		t.Fatalf("child HEAD changed before missing branch refusal: %s -> %s", beforeHead, got)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "status", "--porcelain")); !strings.Contains(got, "local.md") {
		t.Fatalf("local child work was autosaved before missing branch refusal: %q", got)
	}
	if got := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main")); got != remoteBefore {
		t.Fatalf("child remote changed before missing branch refusal: %s -> %s", remoteBefore, got)
	}
	if !strings.Contains(stderr.String(), "child synchronization or parent link recording is incomplete") {
		t.Fatalf("missing branch refusal did not report incomplete child phase:\n%s", stderr.String())
	}
}

// covers: AC-5
func TestSyncRejectsAttachedChildOnDifferentBranchBeforeMutation(t *testing.T) {
	_, client, childRemote, childPath := setupTrackingSubmoduleFixture(t, true)
	runGit(t, childPath, "switch", "-c", "local-work")
	writeTestFile(t, filepath.Join(childPath, "local.md"), "preserve this work\n")
	beforeHead := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	remoteBefore := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main"))
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client})
	err := app.sync(nil)
	if err == nil || !strings.Contains(err.Error(), `attached to branch "local-work"`) {
		t.Fatalf("sync error = %v, want attached branch refusal", err)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD")); got != beforeHead {
		t.Fatalf("child HEAD changed before branch mismatch refusal: %s -> %s", beforeHead, got)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "status", "--porcelain")); !strings.Contains(got, "local.md") {
		t.Fatalf("local child work was autosaved before branch mismatch refusal: %q", got)
	}
	if got := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main")); got != remoteBefore {
		t.Fatalf("child remote changed before branch mismatch refusal: %s -> %s", remoteBefore, got)
	}
}

// covers: AC-11
func TestSyncSkipsGroupedParentFollowupWhenChildLinksDoNotChange(t *testing.T) {
	_, client, _, _ := setupTrackingSubmoduleFixture(t, true)
	app, stdout, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	if err := app.sync(nil); err != nil {
		t.Fatalf("sync: %v\nstderr:\n%s", err, stderr.String())
	}
	if strings.Contains(stdout.String(), "Recording synchronized project submodules") || strings.Contains(stdout.String(), "Synchronizing the grouped parent gitlink commit") {
		t.Fatalf("sync ran a grouped parent follow up although no gitlink changed:\n%s", stdout.String())
	}
}

// covers: AC-11, AC-14
func TestSyncRecoversGroupedParentCommitAfterFollowupPushFailure(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	parentRemote, client, firstChildRemote, firstChildPath := setupTrackingSubmoduleFixture(t, true)

	secondChild := initTestRepo(t)
	secondChildRemote := filepath.Join(t.TempDir(), "second-child.git")
	if err := os.MkdirAll(secondChildRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, secondChildRemote, "init", "--bare", "-b", "main", ".")
	runGit(t, secondChild, "remote", "add", "origin", secondChildRemote)
	runGit(t, secondChild, "push", "-u", "origin", "main")
	secondChildPath := "Projects/second"
	runGit(t, client, "submodule", "add", secondChildRemote, secondChildPath)
	runGit(t, client, "config", "--file", ".gitmodules", "submodule."+secondChildPath+".branch", "main")
	runGit(t, client, "add", "--", ".gitmodules")
	runGit(t, client, "commit", "-m", "add second tracking child")
	runGit(t, client, "push", "origin", "main")
	secondChildPath = filepath.Join(client, filepath.FromSlash(secondChildPath))
	configureIdentity(t, secondChildPath)

	commitTestFile(t, client, "previously-synced.md", "preserve parent work\n", "previously synchronized parent work")
	runGit(t, client, "push", "origin", "main")
	parentBeforeSync := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))
	children := []struct {
		path   string
		remote string
		file   string
	}{
		{path: firstChildPath, remote: firstChildRemote, file: "first-local.md"},
		{path: secondChildPath, remote: secondChildRemote, file: "second-local.md"},
	}
	commitCount := func(remote string) int {
		t.Helper()
		value := strings.TrimSpace(runGit(t, remote, "rev-list", "--count", "refs/heads/main"))
		count, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("parse commit count %q for %s: %v", value, remote, err)
		}
		return count
	}
	childCountsBefore := make(map[string]int, len(children))
	for _, child := range children {
		childCountsBefore[child.path] = commitCount(child.remote)
		writeTestFile(t, filepath.Join(child.path, child.file), "preserve pushed child work\n")
	}

	failedPushURL := filepath.Join(t.TempDir(), "unavailable-parent-push.git")
	runGit(t, client, "config", "remote.origin.pushurl", failedPushURL)
	app, stdout, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	err := app.sync(nil)
	if err == nil || !strings.Contains(err.Error(), "parent gitlink follow up failed") {
		t.Fatalf("first sync error = %v, want parent follow up push failure\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "Applying final recursive checkout") || strings.Contains(stdout.String(), "Synchronization completed successfully.") {
		t.Fatalf("failed parent follow up reported final checkout or success:\n%s", stdout.String())
	}

	childHeadsAfterFailure := make(map[string]string, len(children))
	for _, child := range children {
		head := strings.TrimSpace(runGit(t, child.path, "rev-parse", "HEAD"))
		childHeadsAfterFailure[child.path] = head
		if got := strings.TrimSpace(runGit(t, child.remote, "rev-parse", "refs/heads/main")); got != head {
			t.Fatalf("child %s remote main = %s, want successfully pushed HEAD %s", child.path, got, head)
		}
		count := commitCount(child.remote)
		if want := childCountsBefore[child.path] + 1; count != want {
			t.Fatalf("child %s commit count after first sync = %d, want %d", child.path, count, want)
		}
		if got := strings.TrimSpace(runGit(t, child.path, "show", "HEAD:"+child.file)); got != "preserve pushed child work" {
			t.Fatalf("child %s work after parent follow up failure = %q, want it preserved", child.path, got)
		}
	}
	if got := strings.TrimSpace(runGit(t, parentRemote, "rev-parse", "refs/heads/main")); got != parentBeforeSync {
		t.Fatalf("parent remote changed after rejected follow up push: got %s, want %s", got, parentBeforeSync)
	}
	groupedParentHead := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))
	if groupedParentHead == parentBeforeSync {
		t.Fatal("grouped parent gitlink commit was not retained after follow up push failure")
	}
	parents := strings.Fields(runGit(t, client, "rev-list", "--parents", "-n", "1", groupedParentHead))
	if len(parents) != 2 || parents[1] != parentBeforeSync {
		t.Fatalf("retained grouped commit parents = %v, want previous synchronized parent %s", parents, parentBeforeSync)
	}
	changedPaths := strings.Fields(runGit(t, client, "diff-tree", "--no-commit-id", "--name-only", "-r", groupedParentHead))
	if len(changedPaths) != 2 || changedPaths[0] != "Projects/child" || changedPaths[1] != "Projects/second" {
		t.Fatalf("retained grouped commit paths = %v, want only both tracking gitlinks", changedPaths)
	}
	for _, child := range children {
		if got := gitlinkCommit(t, client, filepath.ToSlash(strings.TrimPrefix(child.path, client+string(filepath.Separator)))); got != childHeadsAfterFailure[child.path] {
			t.Fatalf("retained parent gitlink for %s = %s, want pushed child HEAD %s", child.path, got, childHeadsAfterFailure[child.path])
		}
	}
	if got := strings.TrimSpace(runGit(t, client, "show", groupedParentHead+":previously-synced.md")); got != "preserve parent work" {
		t.Fatalf("previously synchronized parent work after follow up failure = %q, want it preserved", got)
	}
	if got := strings.Count(stdout.String(), "Recording synchronized project submodules in one parent commit..."); got != 1 {
		t.Fatalf("grouped parent commit attempts before recovery = %d, want 1", got)
	}

	runGit(t, client, "config", "--unset-all", "remote.origin.pushurl")
	if err := app.sync(nil); err != nil {
		t.Fatalf("normal sync recovery: %v\nstderr:\n%s", err, stderr.String())
	}
	if got := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")); got != groupedParentHead {
		t.Fatalf("parent HEAD after recovery = %s, want retained grouped commit %s", got, groupedParentHead)
	}
	if got := strings.TrimSpace(runGit(t, parentRemote, "rev-parse", "refs/heads/main")); got != groupedParentHead {
		t.Fatalf("parent remote after recovery = %s, want retained grouped commit %s", got, groupedParentHead)
	}
	for _, child := range children {
		if got := strings.TrimSpace(runGit(t, child.remote, "rev-parse", "refs/heads/main")); got != childHeadsAfterFailure[child.path] {
			t.Fatalf("child %s remote changed during recovery: got %s, want original pushed commit %s", child.path, got, childHeadsAfterFailure[child.path])
		}
		count := commitCount(child.remote)
		if want := childCountsBefore[child.path] + 1; count != want {
			t.Fatalf("child %s commit count after recovery = %d, want %d", child.path, count, want)
		}
	}
	if got := strings.Count(stdout.String(), "Recording synchronized project submodules in one parent commit..."); got != 1 {
		t.Fatalf("grouped parent commits after recovery = %d, want the original commit only", got)
	}
	if !strings.Contains(stdout.String(), "Synchronization completed successfully.") {
		t.Fatalf("normal sync recovery did not report completion:\n%s", stdout.String())
	}
	if got := strings.TrimSpace(runGit(t, client, "show", "HEAD:previously-synced.md")); got != "preserve parent work" {
		t.Fatalf("previously synchronized parent work after recovery = %q, want it preserved", got)
	}
	if got := strings.TrimSpace(runGit(t, client, "status", "--porcelain")); got != "" {
		t.Fatalf("parent status after recovery = %q, want clean", got)
	}
}

// covers: AC-7
func TestRecordTrackingGitlinksRejectsUnexpectedStagedChanges(t *testing.T) {
	_, client, _, childPath := setupTrackingSubmoduleFixture(t, true)
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client})
	baseline, err := readIndexGitlinks(app.git)
	if err != nil {
		t.Fatalf("read parent index gitlinks: %v", err)
	}
	commitTestFile(t, childPath, "next.md", "next\n", "next child commit")
	newHead := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	writeTestFile(t, filepath.Join(client, "unexpected.md"), "staged during child sync\n")
	runGit(t, client, "add", "--", "unexpected.md")
	beforeParent := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))
	if _, err := app.recordTrackingGitlinks(baseline, map[string]string{"Projects/child": newHead}); err == nil || !strings.Contains(err.Error(), "staged changes") {
		t.Fatalf("recordTrackingGitlinks error = %v, want staged parent change refusal", err)
	}
	if got := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")); got != beforeParent {
		t.Fatalf("unexpected parent commit was created: %s -> %s", beforeParent, got)
	}
}

// covers: AC-7, AC-11
func TestRecordTrackingGitlinksRejectsUnrelatedSubmoduleIdentityBeforeStaging(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	_, client, _, childPath := setupTrackingSubmoduleFixture(t, true)
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client})
	baseline, err := readIndexGitlinks(app.git)
	if err != nil {
		t.Fatalf("read parent index gitlinks: %v", err)
	}
	parentBefore := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))
	remote := setupStandaloneRepositoryAtPath(t, childPath)
	standaloneHead := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	remoteBefore := strings.TrimSpace(runGit(t, remote, "rev-parse", "refs/heads/main"))

	if _, err := app.recordTrackingGitlinks(baseline, map[string]string{"Projects/child": standaloneHead}); err == nil || !strings.Contains(err.Error(), "registered Git submodule") {
		t.Fatalf("recordTrackingGitlinks error = %v, want registered Git submodule identity refusal", err)
	}
	if got := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")); got != parentBefore {
		t.Fatalf("parent HEAD changed: %s -> %s", parentBefore, got)
	}
	current, err := readIndexGitlinks(app.git)
	if err != nil {
		t.Fatalf("read parent index after refusal: %v", err)
	}
	if !equalGitlinks(current, baseline) {
		t.Fatalf("parent index gitlinks after refusal = %#v, want baseline %#v", current, baseline)
	}
	runGit(t, client, "diff", "--cached", "--quiet")
	if got := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD")); got != standaloneHead {
		t.Fatalf("standalone repository HEAD changed: %s -> %s", standaloneHead, got)
	}
	if got := strings.TrimSpace(runGit(t, remote, "rev-parse", "refs/heads/main")); got != remoteBefore {
		t.Fatalf("standalone repository remote changed: %s -> %s", remoteBefore, got)
	}
}

// covers: AC-7, AC-11
func TestRecordTrackingGitlinksRejectsChildHeadChangeAfterSnapshot(t *testing.T) {
	_, client, _, childPath := setupTrackingSubmoduleFixture(t, true)
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client})
	baseline, err := readIndexGitlinks(app.git)
	if err != nil {
		t.Fatalf("read parent index gitlinks: %v", err)
	}
	commitTestFile(t, childPath, "first.md", "first\n", "first child commit")
	synchronizedHead := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	commitTestFile(t, childPath, "second.md", "second\n", "second child commit")
	beforeParent := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))
	if _, err := app.recordTrackingGitlinks(baseline, map[string]string{"Projects/child": synchronizedHead}); err == nil || !strings.Contains(err.Error(), "does not match its synchronized child HEAD") {
		t.Fatalf("recordTrackingGitlinks error = %v, want changed child HEAD refusal", err)
	}
	if got := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")); got != beforeParent {
		t.Fatalf("parent commit was created from a different child HEAD: %s -> %s", beforeParent, got)
	}
}

// covers: AC-2
func TestActiveSubmoduleUsesLastEffectiveBoolean(t *testing.T) {
	_, client, _, _ := setupTrackingSubmoduleFixture(t, true)
	runGit(t, client, "config", "--add", "submodule.Projects/child.active", "false")
	runGit(t, client, "config", "--add", "submodule.Projects/child.active", "true")
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client})
	entries, _, err := loadDirectSubmodules(app.git)
	if err != nil {
		t.Fatalf("loadDirectSubmodules: %v", err)
	}
	selected, err := activeSubmodules(app.git, entries)
	if err != nil {
		t.Fatalf("activeSubmodules: %v", err)
	}
	if len(selected) != 1 || selected[0].path != "Projects/child" {
		t.Fatalf("selected active submodules = %#v, want Projects/child", selected)
	}
}

// covers: AC-2
func TestNegativeSubmodulePathspecDetection(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		{name: "short exclude bang", pattern: ":!Projects/skip", want: true},
		{name: "short exclude caret", pattern: ":^Projects/skip", want: true},
		{name: "exclude magic", pattern: ":(exclude)Projects/skip", want: true},
		{name: "exclude with top magic", pattern: ":(glob,exclude,top)Projects/*", want: true},
		{name: "positive glob", pattern: ":(glob)Projects/*", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := negativePathspec(tt.pattern); got != tt.want {
				t.Fatalf("negativePathspec(%q) = %t, want %t", tt.pattern, got, tt.want)
			}
		})
	}
}

// covers: AC-13
func TestLoadProjectionProtectionRejectsMissingTrackedManifest(t *testing.T) {
	_, client, _, _ := setupTrackingSubmoduleFixture(t, true)
	commitProjectionManifest(t, client, "source")
	if err := os.Remove(filepath.Join(client, filepath.FromSlash(externalManifestPath))); err != nil {
		t.Fatal(err)
	}
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client})
	if _, err := app.loadProjectionProtection(); err == nil || !strings.Contains(err.Error(), "manifest is missing") {
		t.Fatalf("loadProjectionProtection error = %v, want missing tracked manifest refusal", err)
	}
}

// covers: AC-13
func TestSyncBlocksChildAutosaveWhenPushTargetsProjectionSource(t *testing.T) {
	parentRemote, client, childRemote, childPath := setupTrackingSubmoduleFixture(t, true)
	sourceRoot := t.TempDir()
	sourceRepo := filepath.Join(sourceRoot, "source")
	runGit(t, sourceRoot, "clone", childRemote, sourceRepo)
	sourceBefore := strings.TrimSpace(runGit(t, sourceRepo, "rev-parse", "HEAD"))
	commitProjectionManifest(t, client, "source")
	writeTestFile(t, filepath.Join(childPath, "local.md"), "should not commit to protected remote\n")
	childBefore := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	remoteBefore := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main"))
	app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	err := app.sync(nil)
	if err == nil || !strings.Contains(err.Error(), "matches an external documentation source remote") {
		t.Fatalf("sync error = %v, want protected source remote refusal", err)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD")); got != childBefore {
		t.Fatalf("child work was committed before source remote check: %s -> %s", childBefore, got)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "status", "--porcelain")); !strings.Contains(got, "local.md") {
		t.Fatalf("local child work was staged or committed before source remote check: %q", got)
	}
	if got := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main")); got != remoteBefore {
		t.Fatalf("protected child remote changed: %s -> %s", remoteBefore, got)
	}
	if got := strings.TrimSpace(runGit(t, sourceRepo, "rev-parse", "HEAD")); got != sourceBefore {
		t.Fatalf("projection source worktree HEAD changed: %s -> %s", sourceBefore, got)
	}
	if got := strings.TrimSpace(runGit(t, sourceRepo, "status", "--porcelain")); got != "" {
		t.Fatalf("projection source worktree changed: %q", got)
	}
	if !strings.Contains(stderr.String(), "child synchronization or parent link recording is incomplete") {
		t.Fatalf("protected source refusal lacks phase status:\n%s", stderr.String())
	}
	if got := strings.TrimSpace(runGit(t, parentRemote, "rev-parse", "refs/heads/main")); got != strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")) {
		t.Fatalf("parent projection sync did not complete before child refusal: remote %s, client %s", got, strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")))
	}
}

// covers: AC-13
func TestRecursiveCheckoutRejectsNestedProjectionSourceSymlink(t *testing.T) {
	_, client, _, childPath := setupTrackingSubmoduleFixture(t, true)
	sourceWorktree := filepath.Join(client, "ProjectionSource")
	if err := os.MkdirAll(sourceWorktree, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, sourceWorktree, "init", "-b", "main", ".")
	configureIdentity(t, sourceWorktree)
	commitTestFile(t, sourceWorktree, "note.md", "protected source\n", "source base")
	sourceHead := strings.TrimSpace(runGit(t, sourceWorktree, "rev-parse", "HEAD"))

	runGit(t, childPath, "submodule", "add", sourceWorktree, "nested")
	runGit(t, childPath, "commit", "-m", "add nested submodule")
	nestedWorktree := filepath.Join(childPath, "nested")
	if err := os.RemoveAll(nestedWorktree); err != nil {
		t.Fatalf("remove cloned nested worktree: %v", err)
	}
	if err := os.Symlink(sourceWorktree, nestedWorktree); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client})
	protection := projectionProtection{worktrees: []string{sourceWorktree}}
	if err := app.checkSubmodulePath(client, "Projects/child/nested", protection); err == nil || !strings.Contains(err.Error(), "overlaps external documentation source") {
		t.Fatalf("checkSubmodulePath error = %v, want nested source overlap refusal", err)
	}
	err := app.validateRecursiveCheckoutPaths([]submoduleEntry{{name: "Projects/child", path: "Projects/child"}}, protection)
	if err == nil || !strings.Contains(err.Error(), "escapes the configured vault worktree") {
		t.Fatalf("validateRecursiveCheckoutPaths error = %v, want nested worktree containment refusal", err)
	}
	if got := strings.TrimSpace(runGit(t, sourceWorktree, "rev-parse", "HEAD")); got != sourceHead {
		t.Fatalf("protected source HEAD changed: got %s, want %s", got, sourceHead)
	}
}

// covers: AC-13
func TestSyncRejectsProjectionSourceWorktreeOverlapBeforeChildUpdate(t *testing.T) {
	_, client, _, childPath := setupTrackingSubmoduleFixture(t, true)
	commitProjectionManifest(t, client, "Projects/child")
	writeTestFile(t, filepath.Join(childPath, "local.md"), "keep the source worktree unchanged\n")
	beforeHead := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: client})
	err := app.sync(nil)
	if err == nil || !strings.Contains(err.Error(), "overlaps external documentation source") {
		t.Fatalf("sync error = %v, want source worktree overlap refusal", err)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD")); got != beforeHead {
		t.Fatalf("overlapping source worktree changed: %s -> %s", beforeHead, got)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "status", "--porcelain")); !strings.Contains(got, "local.md") {
		t.Fatalf("overlapping source worktree was autosaved: %q", got)
	}
	if !strings.Contains(stderr.String(), "child synchronization or parent link recording is incomplete") {
		t.Fatalf("source overlap refusal lacks phase status:\n%s", stderr.String())
	}
}

// covers: AC-13
func TestChildPushProtectionUsesEffectiveRewrittenAndMultipleURLs(t *testing.T) {
	_, client, childRemote, childPath := setupTrackingSubmoduleFixture(t, true)
	sourceRoot := t.TempDir()
	sourceRepo := filepath.Join(sourceRoot, "source")
	runGit(t, sourceRoot, "clone", childRemote, sourceRepo)
	secondPush := filepath.Join(t.TempDir(), "source-push.git")
	if err := os.MkdirAll(secondPush, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, secondPush, "init", "--bare", "-b", "main", ".")
	runGit(t, sourceRepo, "remote", "set-url", "--push", "origin", secondPush)
	commitProjectionManifest(t, client, "source")
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	child, _, err := app.submoduleApp("Projects/child")
	if err != nil {
		t.Fatalf("submoduleApp: %v", err)
	}
	if _, err := canonicalGitEndpoint(childPath, childRemote); err != nil {
		t.Fatalf("canonical child endpoint: %v", err)
	}
	runGit(t, childPath, "config", "url."+childRemote+".insteadOf", "fixture:")
	runGit(t, childPath, "remote", "set-url", "--push", "origin", "fixture:")
	runGit(t, childPath, "remote", "set-url", "--add", "--push", "origin", secondPush)
	urls, err := effectiveRemoteURLs(childPath, "origin", true)
	if err != nil {
		t.Fatalf("effectiveRemoteURLs: %v", err)
	}
	if len(urls) != 2 || urls[0] != childRemote || urls[1] != secondPush {
		t.Fatalf("effective origin push URLs = %#v, want rewritten and second push URLs", urls)
	}
	if err := app.checkSubmodulePushEndpoints(child); err == nil || !strings.Contains(err.Error(), "matches an external documentation source remote") {
		t.Fatalf("checkSubmodulePushEndpoints error = %v, want effective URL overlap", err)
	}
}

// covers: AC-13
func TestChildPushProtectionCanonicalizesFileURLSourceAliases(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	tests := []struct {
		name        string
		symlinkPath bool
	}{
		{name: "file source URL matches local child push path"},
		{name: "file source URL symlink alias matches local child push path", symlinkPath: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client, childRemote, _ := setupTrackingSubmoduleFixture(t, true)
			target := childRemote
			if tt.symlinkPath {
				alias := filepath.Join(t.TempDir(), "child-alias.git")
				if err := os.Symlink(childRemote, alias); err != nil {
					t.Skipf("create repository URL symlink: %v", err)
				}
				target = alias
			}
			sourceRoot := t.TempDir()
			sourceRepo := filepath.Join(sourceRoot, "source")
			runGit(t, sourceRoot, "clone", childRemote, sourceRepo)
			runGit(t, sourceRepo, "remote", "set-url", "origin", testFileURL(t, target))
			commitProjectionManifest(t, client, "source")

			app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
			child, _, err := app.submoduleApp("Projects/child")
			if err != nil {
				t.Fatalf("submoduleApp: %v", err)
			}
			if err := app.checkSubmodulePushEndpoints(child); err == nil || !strings.Contains(err.Error(), "matches an external documentation source remote") {
				t.Fatalf("checkSubmodulePushEndpoints error = %v, want file URL endpoint overlap", err)
			}
		})
	}
}

// covers: AC-13
func TestSyncReloadsProjectionProtectionBeforeChildPush(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	_, client, childRemote, childPath := setupTrackingSubmoduleFixture(t, true)

	sourceRepo := initTestRepo(t)
	sourceRoot := filepath.Dir(sourceRepo)
	sourceRemote := filepath.Join(sourceRoot, "source.git")
	if err := os.MkdirAll(sourceRemote, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, sourceRemote, "init", "--bare", "-b", "main", ".")
	runGit(t, sourceRepo, "remote", "add", "origin", sourceRemote)
	runGit(t, sourceRepo, "push", "-u", "origin", "main")
	commitProjectionManifest(t, client, "repo")

	writeTestFile(t, filepath.Join(childPath, "local.md"), "saved locally before the protected push\n")
	childRemoteBefore := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main"))
	mutatedSource := false
	stdout := &syncOutputMutationWriter{onWrite: func(message string) {
		if !mutatedSource && strings.Contains(message, "Submodule") && strings.Contains(message, "changes saved.") {
			runGit(t, sourceRepo, "remote", "set-url", "origin", childRemote)
			mutatedSource = true
		}
	}}
	var stderr bytes.Buffer
	app := newApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot}, nil, stdout, &stderr)
	err := app.sync(nil)
	if !mutatedSource {
		t.Fatal("test did not change the source endpoint after the child commit")
	}
	if err == nil || !strings.Contains(err.Error(), "matches an external documentation source remote") {
		t.Fatalf("sync error = %v, want refreshed source endpoint refusal\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if got := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main")); got != childRemoteBefore {
		t.Fatalf("protected child remote changed from %s to %s", childRemoteBefore, got)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "show", "HEAD:local.md")); got != "saved locally before the protected push" {
		t.Fatalf("saved child work after refused push = %q, want local commit preserved", got)
	}
}

// covers: AC-3, AC-4
func TestLoadDirectSubmodulesRejectsDuplicatePinAndBranchSettings(t *testing.T) {
	tests := []struct {
		name       string
		field      string
		first      string
		second     string
		wantReason string
	}{
		{name: "duplicate pin", field: "vaultctl-pinned", first: "true", second: "true", wantReason: "duplicate vaultctl-pinned"},
		{name: "duplicate branch", field: "branch", first: "main", second: "release", wantReason: "duplicate branch"},
		{name: "nonliteral pin", field: "vaultctl-pinned", first: "yes", wantReason: "literal true"},
		{name: "dot branch", field: "branch", first: ".", wantReason: "branch = ."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client, _, _ := setupTrackingSubmoduleFixture(t, false)
			runGit(t, client, "config", "--file", ".gitmodules", "--add", "submodule.Projects/child."+tt.field, tt.first)
			if tt.second != "" {
				runGit(t, client, "config", "--file", ".gitmodules", "--add", "submodule.Projects/child."+tt.field, tt.second)
			}
			app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client})
			_, _, err := loadDirectSubmodules(app.git)
			if err == nil || !strings.Contains(err.Error(), tt.wantReason) {
				t.Fatalf("loadDirectSubmodules error = %v, want %q", err, tt.wantReason)
			}
		})
	}
}

// covers: AC-8, AC-9
func TestSyncSubmoduleConflictRecoveryDoesNotPush(t *testing.T) {
	t.Setenv("GIT_EDITOR", "true")
	parentRemote, client, childRemote, childPath := setupTrackingSubmoduleFixture(t, true)
	configPath := filepath.Join(t.TempDir(), "client.json")
	config, err := json.Marshal(Config{Mode: ModeClient, VaultPath: client})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	runCLI := func(args ...string) (int, string, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		cliArgs := append([]string{"--config", configPath}, args...)
		code := RunCLI(cliArgs, nil, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	base := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	parentBefore := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))
	parentRemoteBefore := strings.TrimSpace(runGit(t, parentRemote, "rev-parse", "refs/heads/main"))
	commitTestFile(t, childPath, "shared.md", "remote version\n", "remote version")
	runGit(t, childPath, "push", "origin", "HEAD:refs/heads/main")
	runGit(t, childPath, "reset", "--hard", base)
	writeTestFile(t, filepath.Join(childPath, "shared.md"), "local version\n")
	code, stdout, stderr := runCLI("sync")
	if code != 1 {
		t.Fatalf("sync exit status = %d, want child failure status 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "sync --continue --submodule 'Projects/child'") {
		t.Fatalf("child conflict recovery command missing:\n%s", stderr)
	}
	if !strings.Contains(stderr, "child synchronization or parent link recording is incomplete") {
		t.Fatalf("child failure did not report the incomplete phase:\n%s", stderr)
	}
	remoteBefore := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main"))
	writeTestFile(t, filepath.Join(childPath, "shared.md"), "resolved version\n")
	continueCode, continuedOutput, continueStderr := runCLI("sync", "--continue", "--submodule", "Projects/child")
	if continueCode != 0 {
		t.Fatalf("child continuation exit status = %d\nstderr:\n%s", continueCode, continueStderr)
	}
	if got := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main")); got != remoteBefore {
		t.Fatalf("child continuation pushed remote from %s to %s", remoteBefore, got)
	}
	if !strings.Contains(continuedOutput, "Continuing submodule rebase...") {
		t.Fatalf("child continuation was not reported:\n%s", continuedOutput)
	}
	for _, unexpected := range []string{
		"Parent vault synchronization succeeded.",
		"Synchronizing selected submodules...",
		"Synchronization completed successfully.",
	} {
		if strings.Contains(continuedOutput, unexpected) {
			t.Fatalf("scoped continuation ran parent or normal child sync (%q):\n%s", unexpected, continuedOutput)
		}
	}
	if got := strings.TrimSpace(runGit(t, childPath, "show", "HEAD:shared.md")); got != "resolved version" {
		t.Fatalf("continued child HEAD content = %q, want staged resolution", got)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "status", "--porcelain")); got != "" {
		t.Fatalf("child worktree after continuation = %q, want clean", got)
	}
	if got := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")); got != parentBefore {
		t.Fatalf("parent HEAD changed during scoped continuation: %s -> %s", parentBefore, got)
	}
	if got := strings.TrimSpace(runGit(t, parentRemote, "rev-parse", "refs/heads/main")); got != parentRemoteBefore {
		t.Fatalf("parent remote changed during scoped continuation: %s -> %s", parentRemoteBefore, got)
	}
	code, _, stderr = runCLI("sync")
	if code != 0 {
		t.Fatalf("normal sync after child continuation exit status = %d\nstderr:\n%s", code, stderr)
	}
	if got := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main")); got == remoteBefore {
		t.Fatal("normal sync did not push the resolved child commit")
	}
	if got := gitlinkCommit(t, client, "Projects/child"); got != strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD")) {
		t.Fatalf("normal sync parent gitlink = %s, want resolved child HEAD", got)
	}
}

// covers: AC-8, AC-9
func TestSyncSubmoduleMergeConflictRecoveryDoesNotPush(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	parentRemote, client, childRemote, childPath := setupTrackingSubmoduleFixture(t, true)
	base := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	parentBefore := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))
	parentRemoteBefore := strings.TrimSpace(runGit(t, parentRemote, "rev-parse", "refs/heads/main"))

	commitTestFile(t, childPath, "shared.md", "remote version\n", "remote version")
	runGit(t, childPath, "push", "origin", "HEAD:refs/heads/main")
	runGit(t, childPath, "reset", "--hard", base)
	writeTestFile(t, filepath.Join(childPath, "shared.md"), "local version\n")

	app, stdout, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	err := app.sync([]string{"--merge"})
	if err == nil {
		t.Fatalf("merge sync unexpectedly succeeded despite a child conflict\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	child, _, err := app.submoduleApp("Projects/child")
	if err != nil {
		t.Fatalf("open child app: %v", err)
	}
	operation, err := child.detectOperation()
	if err != nil {
		t.Fatalf("inspect child operation: %v", err)
	}
	if !operation.merge || operation.rebase {
		t.Fatalf("child operation = %#v, want merge", operation)
	}
	if !strings.Contains(stderr.String(), "sync --continue --submodule 'Projects/child'") {
		t.Fatalf("child merge recovery command missing:\n%s", stderr.String())
	}
	childLocalCommit := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	childRemoteBeforeContinue := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main"))
	writeTestFile(t, filepath.Join(childPath, "shared.md"), "resolved merge version\n")
	if err := app.sync([]string{"--continue", "--submodule", "Projects/child"}); err != nil {
		t.Fatalf("continue child merge: %v\nstderr:\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Completing submodule merge...") {
		t.Fatalf("child merge completion was not reported:\n%s", stdout.String())
	}
	if got := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main")); got != childRemoteBeforeContinue {
		t.Fatalf("scoped merge continuation pushed child remote from %s to %s", childRemoteBeforeContinue, got)
	}
	if got := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")); got != parentBefore {
		t.Fatalf("parent HEAD changed during scoped merge continuation: %s -> %s", parentBefore, got)
	}
	if got := strings.TrimSpace(runGit(t, parentRemote, "rev-parse", "refs/heads/main")); got != parentRemoteBefore {
		t.Fatalf("parent remote changed during scoped merge continuation: %s -> %s", parentRemoteBefore, got)
	}
	mergeParents := strings.Fields(runGit(t, childPath, "rev-list", "--parents", "-n", "1", "HEAD"))
	if len(mergeParents) != 3 || mergeParents[1] != childLocalCommit || mergeParents[2] != childRemoteBeforeContinue {
		t.Fatalf("continued child merge parent fields = %v, want local %s and remote %s", mergeParents, childLocalCommit, childRemoteBeforeContinue)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "show", "HEAD:shared.md")); got != "resolved merge version" {
		t.Fatalf("continued child merge content = %q, want resolved value", got)
	}
	if got := strings.TrimSpace(runGit(t, childPath, "status", "--porcelain")); got != "" {
		t.Fatalf("child worktree after merge continuation = %q, want clean", got)
	}
	operation, err = child.detectOperation()
	if err != nil || operation.any() {
		t.Fatalf("child merge operation after continuation = %#v, err = %v, want no operation", operation, err)
	}

	if err := app.sync(nil); err != nil {
		t.Fatalf("normal sync after child merge continuation: %v\nstderr:\n%s", err, stderr.String())
	}
	childHead := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
	if got := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main")); got != childHead {
		t.Fatalf("child remote after normal sync = %s, want merged child HEAD %s", got, childHead)
	}
	if got := gitlinkCommit(t, client, "Projects/child"); got != childHead {
		t.Fatalf("parent gitlink after normal sync = %s, want merged child HEAD %s", got, childHead)
	}
	parentHead := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))
	if got := strings.TrimSpace(runGit(t, parentRemote, "rev-parse", "refs/heads/main")); got != parentHead {
		t.Fatalf("parent remote after normal sync = %s, want parent HEAD %s", got, parentHead)
	}
	if !strings.Contains(stdout.String(), "Synchronization completed successfully.") {
		t.Fatalf("normal sync after merge recovery did not complete:\n%s", stdout.String())
	}
}

// covers: AC-8, AC-9
func TestSyncAbortsSelectedSubmoduleOperationWithoutChangingParentOrRemote(t *testing.T) {
	tests := []struct {
		name       string
		syncArgs   []string
		wantRebase bool
	}{
		{name: "rebase abort", wantRebase: true},
		{name: "merge abort", syncArgs: []string{"--merge"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GIT_EDITOR", "true")
			parentRemote, client, childRemote, childPath := setupTrackingSubmoduleFixture(t, true)
			base := strings.TrimSpace(runGit(t, childPath, "rev-parse", "HEAD"))
			parentBefore := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD"))
			parentRemoteBefore := strings.TrimSpace(runGit(t, parentRemote, "rev-parse", "refs/heads/main"))

			commitTestFile(t, childPath, "shared.md", "remote version\n", "remote version")
			runGit(t, childPath, "push", "origin", "HEAD:refs/heads/main")
			runGit(t, childPath, "reset", "--hard", base)
			writeTestFile(t, filepath.Join(childPath, "shared.md"), "local version\n")
			app, stdout, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
			if err := app.sync(tt.syncArgs); err == nil {
				t.Fatalf("sync unexpectedly succeeded despite a child conflict\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
			}
			child, _, err := app.submoduleApp("Projects/child")
			if err != nil {
				t.Fatalf("open child app: %v", err)
			}
			operation, err := child.detectOperation()
			if err != nil {
				t.Fatalf("inspect child operation: %v", err)
			}
			if operation.rebase != tt.wantRebase || operation.merge == tt.wantRebase {
				t.Fatalf("child operation = %#v, want rebase %t", operation, tt.wantRebase)
			}
			childRemoteBefore := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main"))

			if err := app.sync([]string{"--abort", "--submodule", "Projects/child"}); err != nil {
				t.Fatalf("abort child operation: %v\nstderr:\n%s", err, stderr.String())
			}
			operation, err = child.detectOperation()
			if err != nil {
				t.Fatalf("inspect child after abort: %v", err)
			}
			if operation.rebase || operation.merge {
				t.Fatalf("child operation after abort = %#v, want no rebase or merge", operation)
			}
			if got := strings.TrimSpace(runGit(t, client, "rev-parse", "HEAD")); got != parentBefore {
				t.Fatalf("parent HEAD changed during child abort: %s -> %s", parentBefore, got)
			}
			if got := strings.TrimSpace(runGit(t, parentRemote, "rev-parse", "refs/heads/main")); got != parentRemoteBefore {
				t.Fatalf("parent remote changed during child abort: %s -> %s", parentRemoteBefore, got)
			}
			if got := strings.TrimSpace(runGit(t, childRemote, "rev-parse", "refs/heads/main")); got != childRemoteBefore {
				t.Fatalf("child remote changed during abort: %s -> %s", childRemoteBefore, got)
			}
			if got := strings.TrimSpace(runGit(t, childPath, "show", "HEAD:shared.md")); got != "local version" {
				t.Fatalf("child HEAD content after abort = %q, want the autosaved local version", got)
			}
			if strings.Contains(stdout.String(), "Synchronization completed successfully.") {
				t.Fatalf("scoped abort ran normal synchronization:\n%s", stdout.String())
			}
		})
	}
}

// covers: AC-8
func TestSyncSubmoduleRecoveryRejectsUnregisteredPath(t *testing.T) {
	_, client, _, _ := setupTrackingSubmoduleFixture(t, true)
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client})
	err := app.sync([]string{"--abort", "--submodule", "../outside"})
	if err == nil || !strings.Contains(err.Error(), "not a registered direct submodule path") {
		t.Fatalf("recovery error = %v, want unregistered path refusal", err)
	}
}

// covers: AC-8
func TestSyncSubmoduleRecoveryRejectsSymlinkEscape(t *testing.T) {
	_, client, _, childPath := setupTrackingSubmoduleFixture(t, true)
	externalRepo := initTestRepo(t)
	externalHead := strings.TrimSpace(runGit(t, externalRepo, "rev-parse", "HEAD"))
	if err := os.RemoveAll(childPath); err != nil {
		t.Fatalf("remove child worktree fixture: %v", err)
	}
	if err := os.Symlink(externalRepo, childPath); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client})
	err := app.sync([]string{"--abort", "--submodule", "Projects/child"})
	if err == nil || !strings.Contains(err.Error(), "escapes the configured vault worktree") {
		t.Fatalf("recovery error = %v, want symlink escape refusal", err)
	}
	if got := strings.TrimSpace(runGit(t, externalRepo, "rev-parse", "HEAD")); got != externalHead {
		t.Fatalf("external repository HEAD changed: %s -> %s", externalHead, got)
	}
	if got := strings.TrimSpace(runGit(t, externalRepo, "status", "--porcelain")); got != "" {
		t.Fatalf("external repository changed during rejected recovery: %q", got)
	}
}

// covers: AC-8
func TestSyncSubmoduleRecoveryRejectsInVaultSymlinkAlias(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	_, client, _, childPath := setupTrackingSubmoduleFixture(t, true)
	aliasPath := filepath.Join(client, "Projects", "unrelated")
	remote := setupStandaloneRepositoryAtPath(t, aliasPath)
	startStandaloneRebaseConflict(t, aliasPath, remote)
	headBefore := strings.TrimSpace(runGit(t, aliasPath, "rev-parse", "HEAD"))
	statusBefore := strings.TrimSpace(runGit(t, aliasPath, "status", "--porcelain"))
	remoteBefore := strings.TrimSpace(runGit(t, remote, "rev-parse", "refs/heads/main"))
	if err := os.RemoveAll(childPath); err != nil {
		t.Fatalf("remove registered child worktree: %v", err)
	}
	if err := os.Symlink(aliasPath, childPath); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	app, _, stderr := testApp(Config{Mode: ModeClient, VaultPath: client})
	err := app.sync([]string{"--abort", "--submodule", "Projects/child"})
	if err == nil || !strings.Contains(err.Error(), "registered submodule path") {
		t.Fatalf("scoped abort error = %v, want registered path alias refusal\nstderr:\n%s", err, stderr.String())
	}
	child, _, err := app.submoduleApp("Projects/child")
	if err != nil {
		t.Fatalf("inspect aliased repository after refusal: %v", err)
	}
	operation, err := child.detectOperation()
	if err != nil || !operation.rebase {
		t.Fatalf("aliased repository rebase after refusal = %#v, err = %v, want rebase in progress", operation, err)
	}
	if got := strings.TrimSpace(runGit(t, aliasPath, "rev-parse", "HEAD")); got != headBefore {
		t.Fatalf("aliased repository HEAD changed: %s -> %s", headBefore, got)
	}
	if got := strings.TrimSpace(runGit(t, aliasPath, "status", "--porcelain")); got != statusBefore {
		t.Fatalf("aliased repository status changed: %q -> %q", statusBefore, got)
	}
	if got := strings.TrimSpace(runGit(t, remote, "rev-parse", "refs/heads/main")); got != remoteBefore {
		t.Fatalf("aliased repository remote changed: %s -> %s", remoteBefore, got)
	}
}

// covers: AC-13
func TestCanonicalGitEndpointResolvesLocalAliases(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "bare.git")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := canonicalGitEndpoint(root, "./bare.git")
	if err != nil {
		t.Fatalf("canonicalGitEndpoint: %v", err)
	}
	want, err := canonicalGitEndpoint(root, remote)
	if err != nil {
		t.Fatalf("canonicalGitEndpoint target: %v", err)
	}
	if got != want {
		t.Fatalf("canonical endpoints differ: %q and %q", got, want)
	}
}

// covers: AC-13
func TestSubmodulePushEndpointAllowsSCPStyleRemoteAndStillProtectsSources(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	_, client, _, childPath := setupTrackingSubmoduleFixture(t, true)
	scpRemote := "git@example.test:org/project.git"
	for _, malformed := range []string{"http://[", "git@@example.test:org/project.git", "git@example.test:"} {
		if _, err := canonicalGitEndpoint(childPath, malformed); err == nil {
			t.Errorf("malformed endpoint %q unexpectedly passed canonicalization", malformed)
		}
	}
	runGit(t, childPath, "remote", "set-url", "--push", "origin", scpRemote)

	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client})
	child, _, err := app.submoduleApp("Projects/child")
	if err != nil {
		t.Fatalf("open child app: %v", err)
	}
	if err := app.checkSubmodulePushEndpoints(child); err != nil {
		t.Fatalf("checkSubmodulePushEndpoints rejected SCP-style origin without a protected source: %v", err)
	}

	sourceRepo := initTestRepo(t)
	sourceRoot := filepath.Dir(sourceRepo)
	runGit(t, sourceRepo, "remote", "add", "origin", scpRemote)
	commitProjectionManifest(t, client, "repo")
	app.cfg.SourceRoot = sourceRoot
	if err := app.checkSubmodulePushEndpoints(child); err == nil || !strings.Contains(err.Error(), "matches an external documentation source remote") {
		t.Fatalf("checkSubmodulePushEndpoints error = %v, want matching SCP-style source URL refusal", err)
	}
}

// covers: AC-7
func TestValidateRawDiffGitlinksRejectsUnexpectedChanges(t *testing.T) {
	if err := verifyRawDiffGitlinks(":100644 100644 aa bb M\tfile.txt\x00", []string{"file.txt"}); err == nil {
		t.Fatal("non gitlink raw diff unexpectedly passed validation")
	}
}
