package vaultctl

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	externalManifestPath  = ".vaultctl/external-docs.json"
	externalStatePath     = ".vaultctl/external-docs-state.json"
	externalLockPath      = ".vaultctl/external-docs.lock"
	externalCommitSubject = "Refresh external documentation"
	externalStateVersion  = 2
)

type externalMapping struct {
	Source      string
	Destination string
}

type externalSource struct {
	ID         string
	Repository string
	Remote     string
	Reference  string
	Mappings   []externalMapping
}

type externalManifest struct {
	Sources []externalSource
}

type externalStateSource struct {
	ID             string
	ResolvedCommit string
	OwnedPaths     []string
}

type externalState struct {
	Version int
	Sources []externalStateSource
}

type externalProjection struct {
	Source     externalSource
	Repository string
	Commit     string
	Mappings   []externalProjectionMapping
	OwnedPaths []string
}

type externalProjectionMapping struct {
	Source      string
	Destination string
	Directory   bool
	Files       map[string][]byte
	OwnedPaths  []string
}

type externalTreeEntry struct {
	Mode   string
	Type   string
	Object string
	Path   string
}

type externalPathSnapshot struct {
	mode    os.FileMode
	content []byte
	link    string
}

type externalProjectionSnapshot struct {
	paths map[string]*externalPathSnapshot
}

type externalScannedPath struct {
	Path    string
	Kind    string
	Mode    os.FileMode
	Content []byte
	Link    string
}

type externalOwnershipScan struct {
	Paths []externalScannedPath
}

type externalRefreshBaseline struct {
	Head            string
	Manifest        []byte
	State           []byte
	Ownership       externalOwnershipScan
	ManagedGitState string
}

type externalLock struct {
	path string
	file *os.File
}

func (l *externalLock) close() error {
	if l == nil {
		return nil
	}
	var closeErr error
	if l.file != nil {
		closeErr = l.file.Close()
	}
	removeErr := os.Remove(l.path)
	if removeErr != nil && !os.IsNotExist(removeErr) {
		return errors.Join(closeErr, fmt.Errorf("remove external documentation lock: %w", removeErr))
	}
	return closeErr
}

func (a *App) refreshExternalDocs() (retErr error) {
	manifestFile := filepath.Join(a.cfg.VaultPath, filepath.FromSlash(externalManifestPath))
	info, err := os.Lstat(manifestFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect external documentation manifest: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("external documentation manifest must be a regular file")
	}
	if a.cfg.SourceRoot == "" {
		return fmt.Errorf("source_root is required when %s exists", externalManifestPath)
	}

	root, err := canonicalDirectory(a.cfg.VaultPath)
	if err != nil {
		return fmt.Errorf("validate vault path for external documentation: %w", err)
	}
	control, err := validateControlDirectory(root)
	if err != nil {
		return err
	}
	lock, err := acquireExternalLock(control)
	if err != nil {
		return err
	}
	defer func() {
		if err := lock.close(); err != nil {
			retErr = errors.Join(retErr, err)
		}
	}()

	manifestBytes, err := os.ReadFile(manifestFile)
	if err != nil {
		return fmt.Errorf("read external documentation manifest: %w", err)
	}
	manifest, err := parseExternalManifest(manifestBytes)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", externalManifestPath, err)
	}
	if err := a.requireTrackedAtHead(externalManifestPath); err != nil {
		return fmt.Errorf("external documentation manifest must be tracked and unchanged: %w", err)
	}

	state, stateBytes, err := a.loadExternalState(control)
	if err != nil {
		return err
	}
	if err := validateExternalOwnership(manifest, state); err != nil {
		return err
	}
	ownership, err := a.validateExternalVaultState(root, manifest, state)
	if err != nil {
		return err
	}
	if err := a.validateCommittedExternalProjection(manifest, state); err != nil {
		return err
	}

	sourceRoot, err := canonicalDirectory(a.cfg.SourceRoot)
	if err != nil {
		return fmt.Errorf("validate source_root: %w", err)
	}
	preHead, err := a.captureChecked("capture pre refresh HEAD", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	baseline, err := a.captureExternalRefreshBaseline(preHead, manifestBytes, stateBytes, ownership, manifest)
	if err != nil {
		return err
	}

	plans, err := a.prepareExternalProjections(sourceRoot, manifest, state)
	if err != nil {
		return err
	}
	desiredState := externalState{Sources: make([]externalStateSource, 0, len(plans))}
	for _, plan := range plans {
		desiredState.Sources = append(desiredState.Sources, externalStateSource{
			ID:             plan.Source.ID,
			ResolvedCommit: plan.Commit,
			OwnedPaths:     append([]string(nil), plan.OwnedPaths...),
		})
	}
	desiredStateBytes, err := marshalExternalState(desiredState)
	if err != nil {
		return err
	}
	changed, err := externalProjectionChanged(root, plans, stateBytes, desiredStateBytes)
	if err != nil {
		return err
	}
	if !changed {
		a.say("External documentation is already current.")
		return nil
	}

	if err := a.revalidateExternalRefreshBaseline(root, manifest, state, baseline); err != nil {
		return err
	}

	replacementPaths := externalReplacementPaths(plans, state)
	refreshFilePaths := externalRefreshFilePaths(plans, state)
	snapshot, err := snapshotExternalProjection(root, replacementPaths)
	if err != nil {
		return fmt.Errorf("snapshot external documentation paths: %w", err)
	}
	if err := a.applyExternalProjection(root, plans, state, desiredStateBytes); err != nil {
		return a.restoreExternalProjection(root, replacementPaths, refreshFilePaths, snapshot, err)
	}
	refreshPaths, err := a.stageExternalProjection(refreshFilePaths)
	if err != nil {
		return a.restoreExternalProjection(root, replacementPaths, refreshFilePaths, snapshot, err)
	}

	a.say("Creating external documentation refresh commit...")
	commitResult, err := a.git.capture(commitArgs(externalCommitSubject, refreshPaths)...)
	if err != nil {
		return a.restoreExternalProjection(root, replacementPaths, refreshFilePaths, snapshot, err)
	}
	currentHead, headErr := a.captureChecked("inspect refresh HEAD", "rev-parse", "HEAD")
	if headErr != nil {
		return a.terminalExternalRecovery(preHead, "unknown", refreshPaths, fmt.Errorf("inspect post commit HEAD: %w", headErr))
	}
	if currentHead == preHead {
		reason := fmt.Errorf("refresh commit did not advance HEAD")
		if commitResult.exitCode != 0 && strings.TrimSpace(commitResult.stderr) != "" {
			reason = fmt.Errorf("%w: %s", reason, strings.TrimSpace(commitResult.stderr))
		}
		return a.restoreExternalProjection(root, replacementPaths, refreshFilePaths, snapshot, reason)
	}

	if err := a.confirmExternalCommit(preHead, currentHead, plans, desiredStateBytes, refreshPaths); err != nil {
		return a.terminalExternalRecovery(preHead, currentHead, refreshPaths, err)
	}
	a.say("External documentation refresh committed.")
	return nil
}

func (a *App) loadExternalState(control string) (externalState, []byte, error) {
	path := filepath.Join(control, "external-docs-state.json")
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := a.rejectUntrackedPath(externalStatePath); err != nil {
				return externalState{}, nil, err
			}
			return externalState{}, nil, nil
		}
		return externalState{}, nil, fmt.Errorf("inspect generated external documentation state: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return externalState{}, nil, fmt.Errorf("generated external documentation state must be a regular file")
	}
	if err := a.requireTrackedAtHead(externalStatePath); err != nil {
		return externalState{}, nil, fmt.Errorf("generated external documentation state must be tracked and unchanged: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return externalState{}, nil, fmt.Errorf("read generated external documentation state: %w", err)
	}
	state, err := parseExternalState(data)
	if err != nil {
		return externalState{}, nil, fmt.Errorf("invalid %s: %w", externalStatePath, err)
	}
	canonical, err := marshalExternalState(state)
	if err != nil {
		return externalState{}, nil, err
	}
	if !bytes.Equal(data, canonical) {
		return externalState{}, nil, fmt.Errorf("invalid %s: generated state is not in deterministic form", externalStatePath)
	}
	return state, data, nil
}

func (a *App) requireTrackedAtHead(path string) error {
	if _, err := a.captureChecked("inspect tracked path", "ls-files", "--error-unmatch", "--", path); err != nil {
		return err
	}
	if _, err := a.captureChecked("inspect HEAD path", "cat-file", "-e", "HEAD:"+path); err != nil {
		return err
	}
	result, err := a.git.capture("diff", "--quiet", "HEAD", "--", path)
	if err != nil {
		return err
	}
	if result.exitCode != 0 {
		return fmt.Errorf("path %q differs from HEAD", path)
	}
	return nil
}

func (a *App) rejectUntrackedPath(path string) error {
	result, err := a.git.capture("status", "--porcelain", "--untracked-files=all", "--", path)
	if err != nil {
		return err
	}
	if result.exitCode != 0 {
		return fmt.Errorf("inspect path %q status failed (Git exited with status %d)", path, result.exitCode)
	}
	if strings.TrimSpace(result.stdout) != "" {
		return fmt.Errorf("path %q has uncommitted changes", path)
	}
	return nil
}

func (a *App) validateExternalVaultState(root string, manifest externalManifest, state externalState) (externalOwnershipScan, error) {
	if operation, err := a.detectOperation(); err != nil {
		return externalOwnershipScan{}, err
	} else if operation.any() {
		return externalOwnershipScan{}, fmt.Errorf("cannot refresh external documentation during an unfinished %s", operation.description())
	}
	conflicts, err := a.conflictedFiles()
	if err != nil {
		return externalOwnershipScan{}, err
	}
	if len(conflicts) > 0 {
		return externalOwnershipScan{}, &userError{"external documentation refresh cannot start while the index contains unresolved conflicts."}
	}

	destinations := uniqueSortedPaths(externalManifestPaths(manifest))
	for _, path := range destinations {
		if err := validateDestinationPath(root, path); err != nil {
			return externalOwnershipScan{}, err
		}
	}
	if err := validateControlFile(root, externalStatePath); err != nil {
		return externalOwnershipScan{}, err
	}
	if err := a.rejectUntrackedPath(externalStatePath); err != nil {
		return externalOwnershipScan{}, err
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(externalStatePath))); err == nil {
		result, err := a.git.capture("diff", "HEAD", "--quiet", "--", externalStatePath)
		if err != nil {
			return externalOwnershipScan{}, err
		}
		if result.exitCode != 0 {
			return externalOwnershipScan{}, fmt.Errorf("dirty managed path %q: path has changes relative to HEAD", externalStatePath)
		}
	} else if !os.IsNotExist(err) {
		return externalOwnershipScan{}, fmt.Errorf("inspect generated external documentation state: %w", err)
	}

	expected := externalOwnershipByDestination(manifest, state)
	scan := externalOwnershipScan{Paths: make([]externalScannedPath, 0)}
	for _, destination := range uniqueSortedPaths(destinations) {
		paths, err := scanExternalDestination(root, destination)
		if err != nil {
			return externalOwnershipScan{}, err
		}
		scan.Paths = append(scan.Paths, paths...)
		if err := validateScannedDestination(destination, paths, expected[destination]); err != nil {
			return externalOwnershipScan{}, err
		}
		if statePaths := expected[destination]; len(statePaths) > 0 {
			if err := a.validateManagedDestinationGitState(destination); err != nil {
				return externalOwnershipScan{}, err
			}
		}
	}
	return scan, nil
}

func (a *App) validateManagedDestinationGitState(destination string) error {
	if err := a.rejectUntrackedPath(destination); err != nil {
		return fmt.Errorf("dirty managed path %q: %w", destination, err)
	}
	result, err := a.git.capture("diff", "HEAD", "--quiet", "--", destination)
	if err != nil {
		return err
	}
	if result.exitCode != 0 {
		return fmt.Errorf("dirty managed path %q: path has changes relative to HEAD", destination)
	}
	return nil
}

func externalOwnershipByDestination(manifest externalManifest, state externalState) map[string][]string {
	owned := make(map[string][]string)
	for _, source := range manifest.Sources {
		entry, ok := stateSource(state, source.ID)
		for _, mapping := range source.Mappings {
			if ok {
				for _, path := range entry.OwnedPaths {
					if path == mapping.Destination || strings.HasPrefix(path, mapping.Destination+"/") {
						owned[mapping.Destination] = append(owned[mapping.Destination], path)
					}
				}
			}
		}
	}
	return owned
}

func scanExternalDestination(root, destination string) ([]externalScannedPath, error) {
	path := filepath.Join(root, filepath.FromSlash(destination))
	_, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []externalScannedPath{{Path: destination, Kind: "absent"}}, nil
		}
		return nil, fmt.Errorf("inspect destination %q: %w", destination, err)
	}
	paths := make([]externalScannedPath, 0)
	if err := scanExternalPath(path, destination, &paths); err != nil {
		return nil, fmt.Errorf("scan destination %q: %w", destination, err)
	}
	sort.Slice(paths, func(i, j int) bool { return paths[i].Path < paths[j].Path })
	return paths, nil
}

func scanExternalPath(path, relative string, paths *[]externalScannedPath) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	entry := externalScannedPath{Path: relative, Mode: info.Mode(), Kind: externalEntryKind(info.Mode())}
	switch entry.Kind {
	case "file":
		entry.Content, err = os.ReadFile(path)
		if err != nil {
			return err
		}
	case "symlink":
		entry.Link, err = os.Readlink(path)
		if err != nil {
			return err
		}
	case "directory":
		children, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		*paths = append(*paths, entry)
		for _, child := range children {
			childPath := filepath.Join(path, child.Name())
			childRelative := relative + "/" + child.Name()
			if err := scanExternalPath(childPath, childRelative, paths); err != nil {
				return err
			}
		}
		return nil
	}
	*paths = append(*paths, entry)
	return nil
}

func externalEntryKind(mode os.FileMode) string {
	switch {
	case mode&os.ModeSymlink != 0:
		return "symlink"
	case mode.IsDir():
		return "directory"
	case mode.IsRegular():
		return "file"
	default:
		return "special"
	}
}

func validateScannedDestination(destination string, actual []externalScannedPath, expected []string) error {
	expectedSet := make(map[string]bool, len(expected))
	for _, path := range expected {
		expectedSet[path] = true
	}
	actualByPath := make(map[string]externalScannedPath, len(actual))
	for _, entry := range actual {
		actualByPath[entry.Path] = entry
	}
	paths := make([]string, 0, len(actualByPath)+len(expectedSet))
	seen := make(map[string]bool)
	for path := range actualByPath {
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	for path := range expectedSet {
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		entry, exists := actualByPath[path]
		if !exists {
			return externalOwnershipViolation(destination, relativeDestinationPath(destination, path), "missing owned path")
		}
		if entry.Kind == "absent" {
			if len(expectedSet) > 0 {
				return externalOwnershipViolation(destination, ".", "missing owned path")
			}
			continue
		}
		if entry.Kind == "symlink" {
			return externalOwnershipViolation(destination, relativeDestinationPath(destination, path), "symlink")
		}
		if entry.Kind == "special" {
			return externalOwnershipViolation(destination, relativeDestinationPath(destination, path), "special entry")
		}
		if !expectedSet[path] {
			category := "unexpected file"
			if entry.Kind == "directory" {
				category = "unexpected directory"
			}
			return externalOwnershipViolation(destination, relativeDestinationPath(destination, path), category)
		}
		expectedDirectory := ownedPathHasDescendant(expected, path)
		if expectedDirectory && entry.Kind != "directory" {
			return externalOwnershipViolation(destination, relativeDestinationPath(destination, path), "ownership mismatch")
		}
		if !expectedDirectory && entry.Kind != "file" {
			return externalOwnershipViolation(destination, relativeDestinationPath(destination, path), "ownership mismatch")
		}
	}
	return nil
}

func ownedPathHasDescendant(paths []string, path string) bool {
	prefix := path + "/"
	for _, candidate := range paths {
		if strings.HasPrefix(candidate, prefix) {
			return true
		}
	}
	return false
}

func relativeDestinationPath(destination, path string) string {
	if path == destination {
		return "."
	}
	return strings.TrimPrefix(path, destination+"/")
}

func externalOwnershipViolation(destination, relative, category string) error {
	return fmt.Errorf("external destination %q relative path %q: %s", destination, relative, category)
}

func (a *App) validateCommittedExternalProjection(manifest externalManifest, state externalState) error {
	for _, source := range manifest.Sources {
		entry, ok := stateSource(state, source.ID)
		if !ok {
			continue
		}
		for _, mapping := range source.Mappings {
			expected := make(map[string]bool)
			for _, path := range entry.OwnedPaths {
				if (path == mapping.Destination || strings.HasPrefix(path, mapping.Destination+"/")) && !ownedPathHasDescendant(entry.OwnedPaths, path) {
					expected[path] = true
				}
			}
			actual, err := externalTreeEntries(a.cfg.VaultPath, "HEAD", mapping.Destination)
			if err != nil {
				return fmt.Errorf("validate committed projection at %q: %w", mapping.Destination, err)
			}
			actualPaths := make(map[string]bool, len(actual))
			for _, treeEntry := range actual {
				if treeEntry.Type != "blob" || (treeEntry.Mode != "100644" && treeEntry.Mode != "100755") {
					return externalOwnershipViolation(mapping.Destination, relativeDestinationPath(mapping.Destination, treeEntry.Path), "committed projection mismatch")
				}
				actualPaths[treeEntry.Path] = true
			}
			paths := make([]string, 0, len(expected)+len(actualPaths))
			seen := make(map[string]bool)
			for path := range expected {
				seen[path] = true
				paths = append(paths, path)
			}
			for path := range actualPaths {
				if !seen[path] {
					seen[path] = true
					paths = append(paths, path)
				}
			}
			sort.Strings(paths)
			for _, path := range paths {
				if expected[path] != actualPaths[path] {
					return externalOwnershipViolation(mapping.Destination, relativeDestinationPath(mapping.Destination, path), "committed projection mismatch")
				}
			}
		}
	}
	return nil
}

func (a *App) captureExternalRefreshBaseline(head string, manifest, state []byte, ownership externalOwnershipScan, manifestModel externalManifest) (externalRefreshBaseline, error) {
	managed, err := a.captureExternalManagedGitState(manifestModel)
	if err != nil {
		return externalRefreshBaseline{}, err
	}
	return externalRefreshBaseline{
		Head:            head,
		Manifest:        append([]byte(nil), manifest...),
		State:           append([]byte(nil), state...),
		Ownership:       ownership,
		ManagedGitState: managed,
	}, nil
}

func (a *App) revalidateExternalRefreshBaseline(root string, manifest externalManifest, state externalState, baseline externalRefreshBaseline) error {
	head, err := a.captureChecked("revalidate refresh HEAD", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if head != baseline.Head {
		return fmt.Errorf("external documentation refresh baseline changed: vault HEAD advanced from %s to %s", baseline.Head, head)
	}
	manifestPath := filepath.Join(root, filepath.FromSlash(externalManifestPath))
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("revalidate external documentation manifest: %w", err)
	}
	if !bytes.Equal(manifestBytes, baseline.Manifest) {
		return fmt.Errorf("external documentation refresh baseline changed: manifest bytes changed")
	}
	statePath := filepath.Join(root, filepath.FromSlash(externalStatePath))
	stateBytes, err := os.ReadFile(statePath)
	if os.IsNotExist(err) {
		stateBytes = nil
	} else if err != nil {
		return fmt.Errorf("revalidate generated external documentation state: %w", err)
	}
	if !bytes.Equal(stateBytes, baseline.State) {
		return fmt.Errorf("external documentation refresh baseline changed: state bytes changed")
	}
	current, err := a.validateExternalVaultState(root, manifest, state)
	if err != nil {
		return err
	}
	if !equalExternalOwnershipScan(current, baseline.Ownership) {
		return fmt.Errorf("external documentation refresh baseline changed: ownership scan changed")
	}
	managed, err := a.captureExternalManagedGitState(manifest)
	if err != nil {
		return err
	}
	if managed != baseline.ManagedGitState {
		return fmt.Errorf("external documentation refresh baseline changed: managed Git state changed")
	}
	return nil
}

func (a *App) captureExternalManagedGitState(manifest externalManifest) (string, error) {
	paths := externalManifestPaths(manifest)
	args := []string{"status", "--porcelain=v2", "-z", "--untracked-files=all", "--"}
	args = append(args, uniqueSortedPaths(paths)...)
	result, err := a.git.capture(args...)
	if err != nil {
		return "", err
	}
	if result.exitCode != 0 {
		return "", fmt.Errorf("inspect managed Git state failed (Git exited with status %d)", result.exitCode)
	}
	return result.stdout, nil
}

func equalExternalOwnershipScan(left, right externalOwnershipScan) bool {
	if len(left.Paths) != len(right.Paths) {
		return false
	}
	for index := range left.Paths {
		leftPath, rightPath := left.Paths[index], right.Paths[index]
		if leftPath.Path != rightPath.Path || leftPath.Kind != rightPath.Kind || leftPath.Mode != rightPath.Mode || leftPath.Link != rightPath.Link || !bytes.Equal(leftPath.Content, rightPath.Content) {
			return false
		}
	}
	return true
}

func (a *App) prepareExternalProjections(sourceRoot string, manifest externalManifest, state externalState) ([]externalProjection, error) {
	plans := make([]externalProjection, 0, len(manifest.Sources))
	for _, source := range manifest.Sources {
		repository, err := boundedSourceRepository(sourceRoot, source.Repository)
		if err != nil {
			return nil, fmt.Errorf("source %q repository: %w", source.ID, err)
		}
		if err := validateSourceRepository(repository); err != nil {
			return nil, fmt.Errorf("source %q: %w", source.ID, err)
		}
		if err := validateSourceRemote(repository, source.Remote); err != nil {
			return nil, fmt.Errorf("source %q: %w", source.ID, err)
		}
		if err := validateSourceReference(repository, source.Reference); err != nil {
			return nil, fmt.Errorf("source %q: %w", source.ID, err)
		}
		fetchHead, err := externalGitPath(repository, "FETCH_HEAD")
		if err != nil {
			return nil, fmt.Errorf("source %q locate FETCH_HEAD: %w", source.ID, err)
		}
		if err := ensureSourceOperationAbsent(repository); err != nil {
			return nil, fmt.Errorf("source %q: %w", source.ID, err)
		}
		a.say("Refreshing external source %s...", source.ID)
		if err := externalGitChecked(repository, externalFetchArgs(source.Remote, source.Reference)...); err != nil {
			return nil, fmt.Errorf("source %q fetch failed: %w", source.ID, err)
		}
		resolved, err := readFetchedCommit(repository, fetchHead)
		if err != nil {
			return nil, fmt.Errorf("source %q fetched result: %w", source.ID, err)
		}
		if err := requireCommitObject(repository, resolved); err != nil {
			return nil, fmt.Errorf("source %q resolved object: %w", source.ID, err)
		}
		if previous, ok := stateSource(state, source.ID); ok {
			if err := ensureSourceAncestor(repository, previous.ResolvedCommit, resolved); err != nil {
				return nil, fmt.Errorf("source %q forward check failed: %w", source.ID, err)
			}
		}
		plan, err := buildExternalProjection(repository, source, resolved)
		if err != nil {
			return nil, fmt.Errorf("source %q projection: %w", source.ID, err)
		}
		plans = append(plans, plan)
		a.say("External source %s resolved to %s.", source.ID, resolved)
	}
	return plans, nil
}

func (a *App) applyExternalProjection(root string, plans []externalProjection, state externalState, stateBytes []byte) error {
	oldKinds := externalOwnedPathKinds(state)
	newKinds := externalPlanPathKinds(plans)
	for _, path := range deepestFirstExternalPaths(externalPathsOfKind(oldKinds, "file")) {
		if newKinds[path] != "file" {
			if err := removeExternalFile(filepath.Join(root, filepath.FromSlash(path))); err != nil {
				return fmt.Errorf("remove stale projected file %q: %w", path, err)
			}
		}
	}
	for _, path := range deepestFirstExternalPaths(externalPathsOfKind(oldKinds, "directory")) {
		if newKinds[path] != "directory" {
			if err := removeExternalDirectory(filepath.Join(root, filepath.FromSlash(path))); err != nil {
				return fmt.Errorf("remove stale projected directory %q: %w", path, err)
			}
		}
	}
	for _, path := range shallowFirstExternalPaths(newKinds, "directory") {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(path)), 0o755); err != nil {
			return fmt.Errorf("create projected directory %q: %w", path, err)
		}
	}
	for _, plan := range plans {
		for _, mapping := range plan.Mappings {
			paths := make([]string, 0, len(mapping.Files))
			for path := range mapping.Files {
				paths = append(paths, path)
			}
			sort.Strings(paths)
			for _, path := range paths {
				if err := writeExternalFile(root, path, mapping.Files[path]); err != nil {
					return err
				}
			}
		}
	}
	statePath := filepath.Join(root, filepath.FromSlash(externalStatePath))
	if err := os.WriteFile(statePath, stateBytes, 0o644); err != nil {
		return fmt.Errorf("write generated external documentation state: %w", err)
	}
	return nil
}

func externalOwnedPathKinds(state externalState) map[string]string {
	kinds := make(map[string]string)
	for _, source := range state.Sources {
		for _, path := range source.OwnedPaths {
			kind := "file"
			if ownedPathHasDescendant(source.OwnedPaths, path) {
				kind = "directory"
			}
			kinds[path] = kind
		}
	}
	return kinds
}

func externalPlanPathKinds(plans []externalProjection) map[string]string {
	kinds := make(map[string]string)
	for _, plan := range plans {
		for _, mapping := range plan.Mappings {
			for _, path := range mapping.OwnedPaths {
				kind := "file"
				if ownedPathHasDescendant(mapping.OwnedPaths, path) {
					kind = "directory"
				}
				kinds[path] = kind
			}
		}
	}
	return kinds
}

func externalPathsOfKind(kinds map[string]string, wanted string) []string {
	paths := make([]string, 0)
	for path, kind := range kinds {
		if kind == wanted {
			paths = append(paths, path)
		}
	}
	return paths
}

func deepestFirstExternalPaths(paths []string) []string {
	sort.Slice(paths, func(i, j int) bool {
		leftDepth := strings.Count(paths[i], "/")
		rightDepth := strings.Count(paths[j], "/")
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return paths[i] < paths[j]
	})
	return paths
}

func shallowFirstExternalPaths(kinds map[string]string, wanted string) []string {
	paths := externalPathsOfKind(kinds, wanted)
	return shallowFirstExternalPathList(paths)
}

func shallowFirstExternalPathsForPaths(paths []string) []string {
	return shallowFirstExternalPathList(append([]string(nil), paths...))
}

func shallowFirstExternalPathList(paths []string) []string {
	sort.Slice(paths, func(i, j int) bool {
		leftDepth := strings.Count(paths[i], "/")
		rightDepth := strings.Count(paths[j], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return paths[i] < paths[j]
	})
	return paths
}

func removeExternalFile(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file")
	}
	return os.Remove(path)
}

func removeExternalDirectory(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory")
	}
	return os.Remove(path)
}

func (a *App) stageExternalProjection(refreshFilePaths []string) ([]string, error) {
	before, err := a.cachedPaths()
	if err != nil {
		return nil, err
	}
	args := []string{"add", "-A", "--"}
	args = append(args, refreshFilePaths...)
	if err := a.checkedStream("stage external documentation", args...); err != nil {
		return nil, err
	}
	after, err := a.cachedPaths()
	if err != nil {
		return nil, err
	}
	changed := make([]string, 0)
	for path := range after {
		if !before[path] && !containsPath(refreshFilePaths, path) {
			return nil, fmt.Errorf("refresh staging changed unrelated path %q", path)
		}
		if !before[path] {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	return changed, nil
}

func (a *App) cachedPaths() (map[string]bool, error) {
	result, err := a.git.capture("diff", "--cached", "--name-only", "-z")
	if err != nil {
		return nil, err
	}
	if result.exitCode != 0 {
		return nil, fmt.Errorf("inspect staged paths failed (Git exited with status %d)", result.exitCode)
	}
	paths := make(map[string]bool)
	for _, path := range strings.Split(result.stdout, "\x00") {
		if path != "" {
			paths[path] = true
		}
	}
	return paths, nil
}

func (a *App) confirmExternalCommit(preHead, currentHead string, plans []externalProjection, stateBytes []byte, expectedPaths []string) error {
	parents, err := a.captureChecked("inspect refresh commit parents", "rev-list", "--parents", "-n", "1", currentHead)
	if err != nil {
		return err
	}
	fields := strings.Fields(parents)
	if len(fields) != 2 || fields[0] != currentHead || fields[1] != preHead {
		return fmt.Errorf("refresh commit parent predicate failed: got %q, want %s with parent %s", parents, currentHead, preHead)
	}
	changed, err := a.captureChecked("inspect refresh commit paths", "diff-tree", "--no-commit-id", "--name-only", "-r", "-z", currentHead)
	if err != nil {
		return err
	}
	actualPaths := make([]string, 0)
	for _, path := range strings.Split(changed, "\x00") {
		if path != "" {
			actualPaths = append(actualPaths, path)
		}
	}
	sort.Strings(actualPaths)
	if !slicesEqual(actualPaths, expectedPaths) {
		return fmt.Errorf("refresh commit changed paths %v, want %v", actualPaths, expectedPaths)
	}
	for _, plan := range plans {
		for _, mapping := range plan.Mappings {
			entries, err := externalTreeEntries(a.cfg.VaultPath, currentHead, mapping.Destination)
			if err != nil {
				return err
			}
			expected := mapping.Files
			if len(entries) != len(expected) {
				return fmt.Errorf("refresh commit content at %q does not match the prepared projection", mapping.Destination)
			}
			for _, entry := range entries {
				content, ok := expected[entry.Path]
				if !ok || entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") {
					return fmt.Errorf("refresh commit contains unexpected entry %q", entry.Path)
				}
				result, err := a.git.capture("show", currentHead+":"+entry.Path)
				if err != nil {
					return err
				}
				if result.exitCode != 0 || !bytes.Equal([]byte(result.stdout), content) {
					return fmt.Errorf("refresh commit content at %q differs from the prepared projection", entry.Path)
				}
			}
		}
	}
	stateResult, err := a.git.capture("show", currentHead+":"+externalStatePath)
	if err != nil {
		return err
	}
	if stateResult.exitCode != 0 || stateResult.stdout != string(stateBytes) {
		return fmt.Errorf("refresh commit generated state differs from the prepared state")
	}
	return nil
}

func snapshotExternalProjection(root string, replacementPaths []string) (externalProjectionSnapshot, error) {
	paths := uniqueSortedPaths(replacementPaths)
	snapshot := externalProjectionSnapshot{paths: make(map[string]*externalPathSnapshot, len(paths))}
	for _, path := range paths {
		entry, err := snapshotExternalPath(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return externalProjectionSnapshot{}, fmt.Errorf("snapshot managed path %q: %w", path, err)
		}
		snapshot.paths[path] = entry
	}
	return snapshot, nil
}

func snapshotExternalPath(path string) (*externalPathSnapshot, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	entry := &externalPathSnapshot{mode: info.Mode()}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		entry.link, err = os.Readlink(path)
		if err != nil {
			return nil, err
		}
	case info.IsDir():
	case info.Mode().IsRegular():
		entry.content, err = os.ReadFile(path)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("contains unsupported entry")
	}
	return entry, nil
}

func (a *App) restoreExternalProjection(root string, replacementPaths, refreshFilePaths []string, snapshot externalProjectionSnapshot, cause error) error {
	removePaths := make([]string, 0, len(replacementPaths))
	restorePaths := make([]string, 0, len(replacementPaths))
	for _, path := range uniqueSortedPaths(replacementPaths) {
		if snapshot.paths[path] == nil {
			removePaths = append(removePaths, path)
		} else {
			restorePaths = append(restorePaths, path)
		}
	}
	for _, path := range deepestFirstExternalPaths(removePaths) {
		if err := restoreExternalPath(filepath.Join(root, filepath.FromSlash(path)), nil); err != nil {
			return errors.Join(cause, fmt.Errorf("restore managed path %q: %w", path, err))
		}
	}
	for _, path := range shallowFirstExternalPathsForPaths(restorePaths) {
		if err := restoreExternalPath(filepath.Join(root, filepath.FromSlash(path)), snapshot.paths[path]); err != nil {
			return errors.Join(cause, fmt.Errorf("restore managed path %q: %w", path, err))
		}
	}

	staged, err := a.cachedPaths()
	if err != nil {
		return errors.Join(cause, fmt.Errorf("inspect refresh index for restoration: %w", err))
	}
	stagedManaged := make([]string, 0)
	for path := range staged {
		if containsPath(refreshFilePaths, path) {
			stagedManaged = append(stagedManaged, path)
		}
	}
	sort.Strings(stagedManaged)
	if len(stagedManaged) > 0 {
		args := []string{"reset", "HEAD", "--"}
		args = append(args, stagedManaged...)
		if err := a.checkedStream("reset refresh index", args...); err != nil {
			return errors.Join(cause, err)
		}
	}

	tracked, err := a.trackedExternalPaths(refreshFilePaths)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("inspect tracked refresh paths for restoration: %w", err))
	}
	if len(tracked) > 0 {
		args := []string{"restore", "--source=HEAD", "--worktree", "--"}
		args = append(args, tracked...)
		if err := a.checkedStream("restore external documentation changes", args...); err != nil {
			return errors.Join(cause, err)
		}
	}
	return cause
}

func restoreExternalPath(path string, snapshot *externalPathSnapshot) error {
	if snapshot == nil {
		return removeExternalPath(path)
	}
	info, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if os.IsNotExist(err) || (info.Mode()&os.ModeType) != (snapshot.mode&os.ModeType) {
		if err := removeExternalPath(path); err != nil {
			return err
		}
		return materializeExternalPath(path, snapshot)
	}

	switch {
	case snapshot.mode&os.ModeSymlink != 0:
		link, err := os.Readlink(path)
		if err != nil {
			return err
		}
		if link != snapshot.link {
			if err := removeExternalPath(path); err != nil {
				return err
			}
			return materializeExternalPath(path, snapshot)
		}
	case snapshot.mode.IsDir():
		return os.Chmod(path, snapshot.mode.Perm())
	case snapshot.mode.IsRegular():
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(content, snapshot.content) {
			if err := os.WriteFile(path, snapshot.content, snapshot.mode.Perm()); err != nil {
				return err
			}
		}
		if err := os.Chmod(path, snapshot.mode.Perm()); err != nil {
			return err
		}
	}
	return nil
}

func materializeExternalPath(path string, snapshot *externalPathSnapshot) error {
	switch {
	case snapshot.mode&os.ModeSymlink != 0:
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.Symlink(snapshot.link, path)
	case snapshot.mode.IsDir():
		if err := os.MkdirAll(path, snapshot.mode.Perm()); err != nil {
			return err
		}
		return os.Chmod(path, snapshot.mode.Perm())
	case snapshot.mode.IsRegular():
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, snapshot.content, snapshot.mode.Perm()); err != nil {
			return err
		}
		return os.Chmod(path, snapshot.mode.Perm())
	default:
		return fmt.Errorf("unsupported snapshot entry")
	}
}

func (a *App) trackedExternalPaths(refreshFilePaths []string) ([]string, error) {
	args := []string{"ls-tree", "-r", "-z", "--name-only", "HEAD", "--"}
	args = append(args, refreshFilePaths...)
	result, err := a.git.capture(args...)
	if err != nil {
		return nil, err
	}
	if result.exitCode != 0 {
		return nil, fmt.Errorf("inspect tracked refresh paths failed (Git exited with status %d): %s", result.exitCode, strings.TrimSpace(result.stderr))
	}
	paths := make([]string, 0)
	for _, path := range strings.Split(result.stdout, "\x00") {
		if path != "" {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func (a *App) terminalExternalRecovery(preHead, currentHead string, refreshPaths []string, cause error) error {
	return fmt.Errorf("external documentation refresh entered manual recovery: captured pre refresh HEAD %s, current HEAD %s, expected refresh paths %s, reason: %w", preHead, currentHead, strings.Join(refreshPaths, ", "), cause)
}

func commitArgs(subject string, paths []string) []string {
	args := []string{"commit", "--only", "-m", subject, "--"}
	return append(args, paths...)
}

func parseExternalManifest(data []byte) (externalManifest, error) {
	value, err := parseStrictJSON(data)
	if err != nil {
		return externalManifest{}, err
	}
	object, err := jsonObject(value)
	if err != nil {
		return externalManifest{}, err
	}
	if err := requireKeys(object, "version", "sources"); err != nil {
		return externalManifest{}, err
	}
	if err := requireVersion(object["version"]); err != nil {
		return externalManifest{}, err
	}
	values, err := jsonArray(object["sources"])
	if err != nil || len(values) == 0 {
		return externalManifest{}, fmt.Errorf("sources must be a nonempty array")
	}
	manifest := externalManifest{Sources: make([]externalSource, 0, len(values))}
	seenIDs := make(map[string]bool)
	allDestinations := make([]string, 0)
	for index, value := range values {
		entry, err := jsonObject(value)
		if err != nil {
			return externalManifest{}, fmt.Errorf("sources[%d]: %w", index, err)
		}
		if err := requireKeys(entry, "id", "repository", "remote", "reference", "mappings"); err != nil {
			return externalManifest{}, fmt.Errorf("sources[%d]: %w", index, err)
		}
		id, err := jsonString(entry["id"])
		if err != nil {
			return externalManifest{}, fmt.Errorf("sources[%d] id: %w", index, err)
		}
		if err := validateIdentifier(id); err != nil {
			return externalManifest{}, fmt.Errorf("sources[%d] id: %w", index, err)
		}
		if seenIDs[id] {
			return externalManifest{}, fmt.Errorf("duplicate source identifier %q", id)
		}
		seenIDs[id] = true
		repository, err := jsonString(entry["repository"])
		if err != nil {
			return externalManifest{}, fmt.Errorf("source %q repository: %w", id, err)
		}
		repository, err = normalizeRelativePath(repository)
		if err != nil {
			return externalManifest{}, fmt.Errorf("source %q repository: %w", id, err)
		}
		remote, err := jsonString(entry["remote"])
		if err != nil || remote == "" {
			return externalManifest{}, fmt.Errorf("source %q remote must be a nonempty string", id)
		}
		reference, err := jsonString(entry["reference"])
		if err != nil || reference == "" {
			return externalManifest{}, fmt.Errorf("source %q reference must be a nonempty string", id)
		}
		mappingValues, err := jsonArray(entry["mappings"])
		if err != nil || len(mappingValues) == 0 {
			return externalManifest{}, fmt.Errorf("source %q mappings must be a nonempty array", id)
		}
		source := externalSource{ID: id, Repository: repository, Remote: remote, Reference: reference, Mappings: make([]externalMapping, 0, len(mappingValues))}
		seenMappings := make(map[string]bool)
		for mappingIndex, mappingValue := range mappingValues {
			mappingObject, err := jsonObject(mappingValue)
			if err != nil {
				return externalManifest{}, fmt.Errorf("source %q mappings[%d]: %w", id, mappingIndex, err)
			}
			if err := requireKeys(mappingObject, "source", "destination"); err != nil {
				return externalManifest{}, fmt.Errorf("source %q mappings[%d]: %w", id, mappingIndex, err)
			}
			sourcePath, err := jsonString(mappingObject["source"])
			if err != nil {
				return externalManifest{}, fmt.Errorf("source %q mapping source: %w", id, err)
			}
			sourcePath, err = normalizeRelativePath(sourcePath)
			if err != nil {
				return externalManifest{}, fmt.Errorf("source %q mapping source: %w", id, err)
			}
			destination, err := jsonString(mappingObject["destination"])
			if err != nil {
				return externalManifest{}, fmt.Errorf("source %q mapping destination: %w", id, err)
			}
			destination, err = normalizeRelativePath(destination)
			if err != nil {
				return externalManifest{}, fmt.Errorf("source %q mapping destination: %w", id, err)
			}
			key := sourcePath + "\x00" + destination
			if seenMappings[key] {
				return externalManifest{}, fmt.Errorf("source %q contains duplicate mapping %q to %q", id, sourcePath, destination)
			}
			seenMappings[key] = true
			source.Mappings = append(source.Mappings, externalMapping{Source: sourcePath, Destination: destination})
			allDestinations = append(allDestinations, destination)
		}
		manifest.Sources = append(manifest.Sources, source)
	}
	if err := validatePathOverlaps(allDestinations); err != nil {
		return externalManifest{}, err
	}
	return manifest, nil
}

func parseExternalState(data []byte) (externalState, error) {
	value, err := parseStrictJSON(data)
	if err != nil {
		return externalState{}, err
	}
	object, err := jsonObject(value)
	if err != nil {
		return externalState{}, err
	}
	if err := requireKeys(object, "version", "sources"); err != nil {
		return externalState{}, err
	}
	version, err := externalStateVersionValue(object["version"])
	if err != nil {
		return externalState{}, err
	}
	if version == 1 {
		return externalState{}, fmt.Errorf("generated external documentation state version 1 requires migration")
	}
	values, err := jsonArray(object["sources"])
	if err != nil || len(values) == 0 {
		return externalState{}, fmt.Errorf("sources must be a nonempty array")
	}
	state := externalState{Version: version, Sources: make([]externalStateSource, 0, len(values))}
	seenIDs := make(map[string]bool)
	previousID := ""
	for index, value := range values {
		entry, err := jsonObject(value)
		if err != nil {
			return externalState{}, fmt.Errorf("sources[%d]: %w", index, err)
		}
		if err := requireKeys(entry, "id", "resolved_commit", "owned_paths"); err != nil {
			return externalState{}, fmt.Errorf("sources[%d]: %w", index, err)
		}
		id, err := jsonString(entry["id"])
		if err != nil {
			return externalState{}, fmt.Errorf("sources[%d] id: %w", index, err)
		}
		if err := validateIdentifier(id); err != nil {
			return externalState{}, fmt.Errorf("sources[%d] id: %w", index, err)
		}
		if seenIDs[id] {
			return externalState{}, fmt.Errorf("duplicate state source identifier %q", id)
		}
		if previousID != "" && id <= previousID {
			return externalState{}, fmt.Errorf("state sources must be sorted by identifier")
		}
		previousID = id
		seenIDs[id] = true
		commit, err := jsonString(entry["resolved_commit"])
		if err != nil || !isObjectID(commit) {
			return externalState{}, fmt.Errorf("state source %q resolved_commit must be a full object identifier", id)
		}
		paths, err := jsonArray(entry["owned_paths"])
		if err != nil || len(paths) == 0 {
			return externalState{}, fmt.Errorf("state source %q owned_paths must be a nonempty array", id)
		}
		owned := make([]string, 0, len(paths))
		seenPaths := make(map[string]bool)
		previousPath := ""
		for pathIndex, pathValue := range paths {
			rawPath, err := jsonString(pathValue)
			if err != nil {
				return externalState{}, fmt.Errorf("state source %q owned_paths[%d]: %w", id, pathIndex, err)
			}
			path, err := normalizeRelativePath(rawPath)
			if err != nil {
				return externalState{}, fmt.Errorf("state source %q owned path: %w", id, err)
			}
			if rawPath != path || (previousPath != "" && path <= previousPath) {
				return externalState{}, fmt.Errorf("state source %q owned paths must be sorted and normalized", id)
			}
			previousPath = path
			if seenPaths[path] {
				return externalState{}, fmt.Errorf("state source %q contains duplicate owned path %q", id, path)
			}
			seenPaths[path] = true
			owned = append(owned, path)
		}
		if err := validateOwnedPathTree(owned); err != nil {
			return externalState{}, fmt.Errorf("state source %q owned paths: %w", id, err)
		}
		state.Sources = append(state.Sources, externalStateSource{ID: id, ResolvedCommit: commit, OwnedPaths: owned})
	}
	return state, nil
}

func marshalExternalState(state externalState) ([]byte, error) {
	if state.Version != 0 && state.Version != externalStateVersion {
		return nil, fmt.Errorf("generated external documentation state must use version %d", externalStateVersion)
	}
	copyState := externalState{Sources: append([]externalStateSource(nil), state.Sources...)}
	sort.Slice(copyState.Sources, func(i, j int) bool { return copyState.Sources[i].ID < copyState.Sources[j].ID })
	type outputSource struct {
		ID             string   `json:"id"`
		ResolvedCommit string   `json:"resolved_commit"`
		OwnedPaths     []string `json:"owned_paths"`
	}
	type outputState struct {
		Version int            `json:"version"`
		Sources []outputSource `json:"sources"`
	}
	output := outputState{Version: externalStateVersion, Sources: make([]outputSource, 0, len(copyState.Sources))}
	for _, source := range copyState.Sources {
		paths := append([]string(nil), source.OwnedPaths...)
		sort.Strings(paths)
		if err := validateOwnedPathTree(paths); err != nil {
			return nil, fmt.Errorf("state source %q owned paths: %w", source.ID, err)
		}
		output.Sources = append(output.Sources, outputSource{ID: source.ID, ResolvedCommit: source.ResolvedCommit, OwnedPaths: paths})
	}
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal generated external documentation state: %w", err)
	}
	return append(data, '\n'), nil
}

func externalStateVersionValue(value any) (int, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, fmt.Errorf("version must be integer %d", externalStateVersion)
	}
	switch number.String() {
	case "1":
		return 1, nil
	case "2":
		return externalStateVersion, nil
	default:
		return 0, fmt.Errorf("version must be integer %d", externalStateVersion)
	}
}

func validateOwnedPathTree(paths []string) error {
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		normalized, err := normalizeRelativePath(path)
		if err != nil || normalized != path {
			return fmt.Errorf("contains non-normalized owned path %q", path)
		}
		if seen[path] {
			return fmt.Errorf("contains duplicate owned path %q", path)
		}
		seen[path] = true
		parts := strings.Split(path, "/")
		nearestAncestor := 0
		for index := 1; index < len(parts); index++ {
			if seen[strings.Join(parts[:index], "/")] {
				nearestAncestor = index
			}
		}
		if nearestAncestor == 0 {
			continue
		}
		for index := nearestAncestor + 1; index < len(parts); index++ {
			ancestor := strings.Join(parts[:index], "/")
			if !seen[ancestor] {
				return fmt.Errorf("is missing directory ancestor %q", ancestor)
			}
		}
	}
	return nil
}

func parseStrictJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := readStrictJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("more than one JSON value")
		}
		return nil, err
	}
	return value, nil
}

func readStrictJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := make(map[string]any)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("object member name is not a string")
				}
				if _, exists := object[key]; exists {
					return nil, fmt.Errorf("duplicate object member %q", key)
				}
				member, err := readStrictJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				object[key] = member
			}
			_, err := decoder.Token()
			return object, err
		case '[':
			array := make([]any, 0)
			for decoder.More() {
				member, err := readStrictJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, member)
			}
			_, err := decoder.Token()
			return array, err
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter %q", value)
		}
	default:
		return value, nil
	}
}

func jsonObject(value any) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected object")
	}
	return object, nil
}

func jsonArray(value any) ([]any, error) {
	array, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("expected array")
	}
	return array, nil
}

func jsonString(value any) (string, error) {
	stringValue, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("expected string")
	}
	return stringValue, nil
}

func requireKeys(object map[string]any, keys ...string) error {
	expected := make(map[string]bool, len(keys))
	for _, key := range keys {
		expected[key] = true
		if _, ok := object[key]; !ok {
			return fmt.Errorf("missing required field %q", key)
		}
	}
	for key := range object {
		if !expected[key] {
			return fmt.Errorf("unknown field %q", key)
		}
	}
	return nil
}

func requireVersion(value any) error {
	number, ok := value.(json.Number)
	if !ok || number.String() != "1" {
		return fmt.Errorf("version must be integer 1")
	}
	return nil
}

func validateIdentifier(value string) error {
	if value == "" {
		return fmt.Errorf("must be nonempty")
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("must not contain control characters")
		}
	}
	return nil
}

func normalizeRelativePath(value string) (string, error) {
	if value == "" || strings.ContainsRune(value, '\x00') || strings.ContainsRune(value, '\\') {
		return "", fmt.Errorf("path %q is not a safe relative path", value)
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || (len(value) >= 2 && value[1] == ':') {
		return "", fmt.Errorf("path %q is not a safe relative path", value)
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("path %q is not a normalized relative path", value)
		}
		for _, character := range part {
			if character < 0x20 || character == 0x7f {
				return "", fmt.Errorf("path %q contains a control character", value)
			}
		}
	}
	return strings.Join(parts, "/"), nil
}

func validatePathOverlaps(paths []string) error {
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		if seen[path] {
			return fmt.Errorf("destination path %q is duplicated", path)
		}
		seen[path] = true
	}
	normalized := uniqueSortedPaths(paths)
	for index := range normalized {
		for other := index + 1; other < len(normalized); other++ {
			if pathsOverlap(normalized[index], normalized[other]) {
				return fmt.Errorf("destination paths %q and %q overlap", normalized[index], normalized[other])
			}
		}
	}
	return nil
}

func pathsOverlap(left, right string) bool {
	leftParts := strings.Split(left, "/")
	rightParts := strings.Split(right, "/")
	if len(leftParts) > len(rightParts) {
		leftParts, rightParts = rightParts, leftParts
	}
	for index := range leftParts {
		if !strings.EqualFold(leftParts[index], rightParts[index]) {
			return false
		}
	}
	return true
}

func uniqueSortedPaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if !seen[path] {
			seen[path] = true
			result = append(result, path)
		}
	}
	sort.Strings(result)
	return result
}

func validateControlDirectory(root string) (string, error) {
	path := filepath.Join(root, ".vaultctl")
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect .vaultctl: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf(".vaultctl must be a real directory")
	}
	return path, nil
}

func validateDestinationPath(root, path string) error {
	if err := validatePathNotControl(path); err != nil {
		return err
	}
	current := root
	parts := strings.Split(path, "/")
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("inspect destination ancestry %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination path %q contains a symlink", path)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("destination path %q has a non-directory ancestor", path)
		}
	}
	return nil
}

func validateControlFile(root, path string) error {
	current := root
	for _, part := range strings.Split(path, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("inspect control path %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("control path %q contains a symlink", path)
		}
	}
	return nil
}

func validatePathNotControl(path string) error {
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if strings.EqualFold(part, ".vaultctl") {
			return fmt.Errorf("destination path %q is inside .vaultctl", path)
		}
	}
	return nil
}

func canonicalDirectory(path string) (string, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", path)
	}
	return canonical, nil
}

func boundedSourceRepository(root, relative string) (string, error) {
	candidate := filepath.Join(root, filepath.FromSlash(relative))
	canonical, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	relativeCanonical, err := filepath.Rel(root, canonical)
	if err != nil || relativeCanonical == ".." || strings.HasPrefix(relativeCanonical, ".."+string(filepath.Separator)) || filepath.IsAbs(relativeCanonical) {
		return "", fmt.Errorf("repository path escapes source_root")
	}
	return canonical, nil
}

func acquireExternalLock(control string) (*externalLock, error) {
	path := filepath.Join(control, "external-docs.lock")
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("external documentation lock path is unsafe")
		}
		return nil, fmt.Errorf("external documentation refresh is already active")
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect external documentation lock: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("external documentation refresh is already active")
		}
		return nil, fmt.Errorf("acquire external documentation lock: %w", err)
	}
	return &externalLock{path: path, file: file}, nil
}

func externalGitCommand(dir string, args ...string) (*exec.Cmd, error) {
	command := exec.Command("git", args...)
	command.Dir = dir
	return command, nil
}

func externalGitCapture(dir string, args ...string) (commandResult, error) {
	command, err := externalGitCommand(dir, args...)
	if err != nil {
		return commandResult{}, err
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	result := commandResult{stdout: stdout.String(), stderr: stderr.String()}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.exitCode = exitErr.ExitCode()
		return result, nil
	}
	return commandResult{}, fmt.Errorf("start Git: %w", err)
}

func externalGitChecked(dir string, args ...string) error {
	result, err := externalGitCapture(dir, args...)
	if err != nil {
		return err
	}
	if result.exitCode != 0 {
		detail := strings.TrimSpace(result.stderr)
		if detail != "" {
			return fmt.Errorf("Git failed: %s", detail)
		}
		return fmt.Errorf("Git exited with status %d", result.exitCode)
	}
	return nil
}

func externalFetchArgs(remote, reference string) []string {
	return []string{"-c", "core.hooksPath=", "fetch", "--no-tags", "--", remote, "refs/heads/" + reference}
}

func externalGitPath(dir, name string) (string, error) {
	result, err := externalGitCapture(dir, "rev-parse", "--git-path", name)
	if err != nil {
		return "", err
	}
	if result.exitCode != 0 {
		return "", fmt.Errorf("locate Git state %s failed: %s", name, strings.TrimSpace(result.stderr))
	}
	path := strings.TrimSpace(result.stdout)
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return filepath.Clean(path), nil
}

func validateSourceRepository(repository string) error {
	result, err := externalGitCapture(repository, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return err
	}
	if result.exitCode != 0 || strings.TrimSpace(result.stdout) != "true" {
		return fmt.Errorf("repository is not a Git worktree")
	}
	return ensureSourceOperationAbsent(repository)
}

func ensureSourceOperationAbsent(repository string) error {
	for _, name := range unfinishedGitOperationPaths {
		path, err := externalGitPath(repository, name)
		if err != nil {
			return err
		}
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("source repository has an unfinished Git operation")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect source Git operation %s: %w", name, err)
		}
	}
	return nil
}

func validateSourceRemote(repository, remote string) error {
	if err := validateIdentifier(remote); err != nil {
		return fmt.Errorf("remote name: %w", err)
	}
	result, err := externalGitCapture(repository, "remote", "get-url", "--", remote)
	if err != nil {
		return err
	}
	if result.exitCode != 0 || strings.TrimSpace(result.stdout) == "" {
		return fmt.Errorf("remote %q does not exist", remote)
	}
	return nil
}

func validateSourceReference(repository, reference string) error {
	if strings.HasPrefix(reference, "-") || strings.ContainsRune(reference, '\x00') {
		return fmt.Errorf("reference %q is not a valid branch name", reference)
	}
	result, err := externalGitCapture(repository, "check-ref-format", "--branch", reference)
	if err != nil {
		return err
	}
	if result.exitCode != 0 {
		return fmt.Errorf("reference %q is not a valid branch name", reference)
	}
	return nil
}

func readFetchedCommit(repository, fetchHead string) (string, error) {
	data, err := os.ReadFile(fetchHead)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	var objectIDs []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 || !isObjectID(fields[0]) {
			return "", fmt.Errorf("FETCH_HEAD contains an invalid result")
		}
		objectIDs = append(objectIDs, fields[0])
	}
	if len(objectIDs) != 1 {
		return "", fmt.Errorf("FETCH_HEAD contains %d results, want exactly one", len(objectIDs))
	}
	return objectIDs[0], nil
}

func isObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func requireCommitObject(repository, objectID string) error {
	result, err := externalGitCapture(repository, "cat-file", "-t", objectID)
	if err != nil {
		return err
	}
	if result.exitCode != 0 || strings.TrimSpace(result.stdout) != "commit" {
		return fmt.Errorf("object %s is not a commit", objectID)
	}
	return nil
}

func ensureSourceAncestor(repository, previous, resolved string) error {
	if !isObjectID(previous) {
		return fmt.Errorf("recorded commit %q is malformed", previous)
	}
	if err := requireCommitObject(repository, previous); err != nil {
		return err
	}
	result, err := externalGitCapture(repository, "merge-base", "--is-ancestor", previous, resolved)
	if err != nil {
		return err
	}
	if result.exitCode == 1 {
		return fmt.Errorf("resolved commit %s diverges from recorded commit %s", resolved, previous)
	}
	if result.exitCode != 0 {
		return fmt.Errorf("cannot establish ancestry from %s to %s", previous, resolved)
	}
	return nil
}

func buildExternalProjection(repository string, source externalSource, commit string) (externalProjection, error) {
	plan := externalProjection{Source: source, Repository: repository, Commit: commit, Mappings: make([]externalProjectionMapping, 0, len(source.Mappings)), OwnedPaths: make([]string, 0, len(source.Mappings))}
	for _, mapping := range source.Mappings {
		entries, err := readSourceTree(repository, commit, mapping.Source)
		if err != nil {
			return externalProjection{}, fmt.Errorf("mapping %q: %w", mapping.Source, err)
		}
		projection := externalProjectionMapping{Source: mapping.Source, Destination: mapping.Destination, Files: make(map[string][]byte), OwnedPaths: []string{mapping.Destination}}
		for index, entry := range entries {
			if entry.Type == "tree" {
				if index == 0 {
					projection.Directory = true
				}
				path := mapping.Destination
				if entry.Path != mapping.Source {
					relative := strings.TrimPrefix(entry.Path, mapping.Source+"/")
					path = mapping.Destination + "/" + relative
				}
				projection.OwnedPaths = append(projection.OwnedPaths, path)
				continue
			}
			if entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") {
				return externalProjection{}, fmt.Errorf("selected tree entry %q is unsupported", entry.Path)
			}
			path := mapping.Destination
			if projection.Directory {
				relative := strings.TrimPrefix(entry.Path, mapping.Source+"/")
				path = mapping.Destination + "/" + relative
			}
			content, err := externalGitCapture(repository, "cat-file", "blob", entry.Object)
			if err != nil || content.exitCode != 0 {
				if err != nil {
					return externalProjection{}, err
				}
				return externalProjection{}, fmt.Errorf("read selected entry %q failed: %s", entry.Path, strings.TrimSpace(content.stderr))
			}
			projection.Files[path] = []byte(content.stdout)
			projection.OwnedPaths = append(projection.OwnedPaths, path)
		}
		if !projection.Directory && len(entries) != 1 {
			return externalProjection{}, fmt.Errorf("mapping %q has unexpected tree entries", mapping.Source)
		}
		if projection.Directory && len(projection.Files) == 0 {
			return externalProjection{}, fmt.Errorf("mapping %q selects an empty directory", mapping.Source)
		}
		projection.OwnedPaths = uniqueSortedPaths(projection.OwnedPaths)
		plan.Mappings = append(plan.Mappings, projection)
		plan.OwnedPaths = append(plan.OwnedPaths, projection.OwnedPaths...)
	}
	plan.OwnedPaths = uniqueSortedPaths(plan.OwnedPaths)
	return plan, nil
}

func readSourceTree(repository, commit, source string) ([]externalTreeEntry, error) {
	entries, err := readTreeCommand(repository, "ls-tree", "-z", commit, "--", source)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("selected path %q does not exist", source)
	}
	if len(entries) != 1 {
		return nil, fmt.Errorf("selected path %q is ambiguous", source)
	}
	if entries[0].Type != "tree" {
		return entries, nil
	}
	return readTreeCommand(repository, "ls-tree", "-r", "-t", "-z", commit, "--", source)
}

func readTreeCommand(repository string, args ...string) ([]externalTreeEntry, error) {
	result, err := externalGitCapture(repository, args...)
	if err != nil {
		return nil, err
	}
	if result.exitCode != 0 {
		return nil, fmt.Errorf("ls-tree failed: %s", strings.TrimSpace(result.stderr))
	}
	entries := make([]externalTreeEntry, 0)
	for _, record := range bytes.Split([]byte(result.stdout), []byte{0}) {
		if len(record) == 0 {
			continue
		}
		parts := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("malformed Git tree entry")
		}
		fields := strings.Fields(string(parts[0]))
		if len(fields) != 3 {
			return nil, fmt.Errorf("malformed Git tree entry")
		}
		entries = append(entries, externalTreeEntry{Mode: fields[0], Type: fields[1], Object: fields[2], Path: string(parts[1])})
	}
	return entries, nil
}

func externalProjectionChanged(root string, plans []externalProjection, previousState, desiredState []byte) (bool, error) {
	if !bytes.Equal(previousState, desiredState) {
		return true, nil
	}
	for _, plan := range plans {
		for _, mapping := range plan.Mappings {
			if !externalMappingMatches(root, mapping) {
				return true, nil
			}
		}
	}
	return false, nil
}

func externalMappingMatches(root string, mapping externalProjectionMapping) bool {
	path := filepath.Join(root, filepath.FromSlash(mapping.Destination))
	if !mapping.Directory {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false
		}
		content, err := os.ReadFile(path)
		return err == nil && bytes.Equal(content, mapping.Files[mapping.Destination])
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return len(mapping.Files) == 0 && os.IsNotExist(err)
	}
	actual := make(map[string][]byte)
	actualDirectories := make(map[string]bool)
	valid := true
	err = filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			valid = false
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			valid = false
			return filepath.SkipDir
		}
		if entry.IsDir() {
			relative, err := filepath.Rel(root, current)
			if err != nil {
				valid = false
				return nil
			}
			if relative != filepath.FromSlash(mapping.Destination) {
				actualDirectories[filepath.ToSlash(relative)] = true
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			valid = false
			return nil
		}
		content, err := os.ReadFile(current)
		if err != nil {
			valid = false
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			valid = false
			return nil
		}
		actual[filepath.ToSlash(relative)] = content
		return nil
	})
	expectedDirectories := make(map[string]bool)
	for path := range mapping.Files {
		directory := filepath.ToSlash(filepath.Dir(path))
		for directory != "." && directory != "" && directory != mapping.Destination {
			expectedDirectories[directory] = true
			directory = filepath.ToSlash(filepath.Dir(directory))
		}
	}
	if err != nil || !valid || len(actual) != len(mapping.Files) || len(actualDirectories) != len(expectedDirectories) {
		return false
	}
	for directory := range expectedDirectories {
		if !actualDirectories[directory] {
			return false
		}
	}
	for path, expected := range mapping.Files {
		if !bytes.Equal(actual[path], expected) {
			return false
		}
	}
	return true
}

func writeExternalFile(root, path string, content []byte) error {
	file := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return fmt.Errorf("create projection parent for %q: %w", path, err)
	}
	if err := os.WriteFile(file, content, 0o644); err != nil {
		return fmt.Errorf("write projection file %q: %w", path, err)
	}
	return nil
}

func removeExternalPath(path string) error {
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.Remove(path)
}

func externalReplacementPaths(plans []externalProjection, state externalState) []string {
	paths := []string{externalStatePath}
	for _, source := range state.Sources {
		paths = append(paths, source.OwnedPaths...)
	}
	for _, plan := range plans {
		paths = append(paths, plan.OwnedPaths...)
	}
	return uniqueSortedPaths(paths)
}

func externalRefreshFilePaths(plans []externalProjection, state externalState) []string {
	paths := []string{externalStatePath}
	for path, kind := range externalOwnedPathKinds(state) {
		if kind == "file" {
			paths = append(paths, path)
		}
	}
	for path, kind := range externalPlanPathKinds(plans) {
		if kind == "file" {
			paths = append(paths, path)
		}
	}
	return uniqueSortedPaths(paths)
}

func externalManifestPaths(manifest externalManifest) []string {
	paths := make([]string, 0)
	for _, source := range manifest.Sources {
		for _, mapping := range source.Mappings {
			paths = append(paths, mapping.Destination)
		}
	}
	return paths
}

func validateExternalOwnership(manifest externalManifest, state externalState) error {
	manifestSources := make(map[string]externalSource, len(manifest.Sources))
	for _, source := range manifest.Sources {
		manifestSources[source.ID] = source
	}
	for _, entry := range state.Sources {
		source, ok := manifestSources[entry.ID]
		if !ok {
			return fmt.Errorf("generated state contains unknown source %q", entry.ID)
		}
		owned := make([]string, 0, len(source.Mappings))
		for _, mapping := range source.Mappings {
			owned = append(owned, mapping.Destination)
		}
		sort.Strings(owned)
		for _, destination := range owned {
			if !containsPath(entry.OwnedPaths, destination) {
				return fmt.Errorf("source %q changed its owned destination paths", entry.ID)
			}
		}
		for _, path := range entry.OwnedPaths {
			belongs := false
			for _, destination := range owned {
				if path == destination || strings.HasPrefix(path, destination+"/") {
					belongs = true
					break
				}
			}
			if !belongs {
				return fmt.Errorf("source %q contains owned path outside its destinations: %q", entry.ID, path)
			}
		}
		if err := validateOwnedPathTree(entry.OwnedPaths); err != nil {
			return fmt.Errorf("source %q owned paths: %w", entry.ID, err)
		}
		if len(entry.OwnedPaths) < len(owned) {
			return fmt.Errorf("source %q changed its owned destination paths", entry.ID)
		}
	}
	return nil
}

func stateSource(state externalState, id string) (externalStateSource, bool) {
	for _, source := range state.Sources {
		if source.ID == id {
			return source, true
		}
	}
	return externalStateSource{}, false
}

func containsPath(paths []string, wanted string) bool {
	for _, path := range paths {
		if path == wanted {
			return true
		}
	}
	return false
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func externalTreeEntries(vault, commit, path string) ([]externalTreeEntry, error) {
	return readTreeCommand(vault, "ls-tree", "-r", "-z", commit, "--", path)
}
