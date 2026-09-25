//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package vaultctl

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// AC-2: special filesystem entries fail before source fetch or projection mutation.
func TestRefreshExternalDocsRejectsSpecialEntryBeforeSourceFetch(t *testing.T) {
	_, client, _ := setupRemoteFixture(t)
	sourceRoot, sourceRepo, _ := setupExternalSource(t)
	writeExternalManifest(t, client, `[{"source":"docs","destination":"external/project"}]`)
	app, _, _ := testApp(Config{Mode: ModeClient, VaultPath: client, SourceRoot: sourceRoot})
	if err := app.refreshExternalDocs(); err != nil {
		t.Fatalf("initial refreshExternalDocs: %v", err)
	}

	special := filepath.Join(client, "external", "project", "special")
	if err := syscall.Mkfifo(special, 0o644); err != nil {
		t.Fatal(err)
	}
	fetchHead := strings.TrimSpace(runGit(t, sourceRepo, "rev-parse", "--git-path", "FETCH_HEAD"))
	if !filepath.IsAbs(fetchHead) {
		fetchHead = filepath.Join(sourceRepo, fetchHead)
	}
	beforeFetch, err := os.ReadFile(fetchHead)
	if err != nil {
		t.Fatal(err)
	}

	err = app.refreshExternalDocs()
	if err == nil || !strings.Contains(err.Error(), `relative path "special"`) || !strings.Contains(err.Error(), "special entry") {
		t.Fatalf("refresh error = %v, want special entry refusal", err)
	}
	afterFetch, err := os.ReadFile(fetchHead)
	if err != nil || string(afterFetch) != string(beforeFetch) {
		t.Fatalf("source FETCH_HEAD changed before special entry refusal")
	}
	if _, err := os.Stat(special); err != nil {
		t.Fatalf("special entry was removed after ownership refusal: %v", err)
	}
}
