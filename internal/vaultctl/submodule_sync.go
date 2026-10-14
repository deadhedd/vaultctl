package vaultctl

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

type submoduleEntry struct {
	name      string
	path      string
	url       string
	branch    string
	branchSet bool
	pinned    bool
}

type projectionProtection struct {
	worktrees []string
	endpoints map[string]struct{}
}

func (a *App) syncSelectedSubmodules(useMerge bool) error {
	entries, gitlinks, err := loadDirectSubmodules(a.git)
	if err != nil {
		return err
	}
	selected, err := activeSubmodules(a.git, entries)
	if err != nil {
		return err
	}
	protection, err := a.loadProjectionProtection()
	if err != nil {
		return err
	}
	root, err := canonicalDirectory(a.cfg.VaultPath)
	if err != nil {
		return fmt.Errorf("resolve client vault path: %w", err)
	}
	for _, entry := range selected {
		if _, ok := gitlinks[entry.path]; !ok {
			return fmt.Errorf("submodule %q has no indexed gitlink", entry.path)
		}
		if err := a.checkSubmodulePath(root, entry.path, protection); err != nil {
			return fmt.Errorf("submodule %q: %w", entry.path, err)
		}
	}
	a.say("Synchronizing selected submodules...")

	tracking := make([]submoduleEntry, 0, len(selected))
	for _, entry := range selected {
		if entry.pinned {
			a.say("Updating pinned submodule %s at its recorded commit...", entry.path)
			if err := a.updateSubmoduleAtGitlink(entry); err != nil {
				return fmt.Errorf("submodule %q pinned checkout failed: %w", entry.path, err)
			}
			continue
		}
		tracking = append(tracking, entry)
	}

	baseline := cloneGitlinks(gitlinks)
	successful := make(map[string]string, len(tracking))
	for _, entry := range tracking {
		head, err := a.syncTrackingSubmodule(entry, baseline[entry.path], useMerge)
		if err != nil {
			return fmt.Errorf("submodule %q: %w", entry.path, err)
		}
		successful[entry.path] = head
	}

	changed, err := a.recordTrackingGitlinks(baseline, successful)
	if err != nil {
		return err
	}
	expectedGitlinks := cloneGitlinks(gitlinks)
	for path, head := range successful {
		expectedGitlinks[path] = head
	}
	if changed {
		if err := a.syncParentFollowup(useMerge); err != nil {
			return fmt.Errorf("parent gitlink follow up failed: %w", err)
		}
	}
	selected, err = a.revalidateFinalSubmoduleCheckout(selected, expectedGitlinks)
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		a.say("No active submodules are selected; skipping recursive checkout.")
		return nil
	}
	a.say("Applying final recursive checkout at recorded gitlinks...")
	args := []string{"submodule", "update", "--init", "--recursive", "--checkout", "--"}
	for _, entry := range selected {
		args = append(args, literalPathspec(entry.path))
	}
	code, err := a.git.stream(args...)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("recursive submodule checkout failed (Git exited with status %d)", code)
	}
	return nil
}

func (a *App) revalidateFinalSubmoduleCheckout(planned []submoduleEntry, expectedGitlinks map[string]string) ([]submoduleEntry, error) {
	entries, gitlinks, err := loadDirectSubmodules(a.git)
	if err != nil {
		return nil, fmt.Errorf("reload parent submodule metadata before final checkout; review .gitmodules and the parent index, then run vaultctl sync again: %w", err)
	}
	selected, err := activeSubmodules(a.git, entries)
	if err != nil {
		return nil, fmt.Errorf("reload active submodules before final checkout; review submodule.active settings, then run vaultctl sync again: %w", err)
	}
	protection, err := a.loadProjectionProtection()
	if err != nil {
		return nil, fmt.Errorf("reload projection source protections before final checkout; review the external documentation manifest and source_root, then run vaultctl sync again: %w", err)
	}
	if !sameSubmoduleEntries(planned, selected) {
		return nil, fmt.Errorf("parent submodule metadata changed during child synchronization; refusing final recursive checkout; review the updated metadata and run vaultctl sync again to process the current selection")
	}
	if err := a.validateRecursiveCheckoutPaths(selected, protection); err != nil {
		return nil, fmt.Errorf("current submodule checkout is unsafe under the updated projection source protections; review the metadata and run vaultctl sync again: %w", err)
	}
	for _, entry := range selected {
		if expectedGitlinks[entry.path] == "" || gitlinks[entry.path] != expectedGitlinks[entry.path] {
			return nil, fmt.Errorf("parent gitlink for selected submodule %q changed during child synchronization; refusing final recursive checkout; review the updated gitlink and run vaultctl sync again", entry.path)
		}
	}
	return selected, nil
}

func sameSubmoduleEntries(left, right []submoduleEntry) bool {
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

func (a *App) syncTrackingSubmodule(entry submoduleEntry, recorded string, useMerge bool) (string, error) {
	child, childRoot, err := a.submoduleApp(entry.path)
	if err != nil {
		return "", err
	}
	if err := a.initializeTrackingSubmodule(entry, childRoot); err != nil {
		return "", err
	}
	child, childRoot, err = a.submoduleApp(entry.path)
	if err != nil {
		return "", err
	}
	validateIdentity := func() error { return a.validateRegisteredSubmoduleIdentity(entry) }
	if err := validateIdentity(); err != nil {
		return "", err
	}
	child.pushGuard = func() error {
		if err := validateIdentity(); err != nil {
			return err
		}
		return a.checkSubmodulePushEndpoints(child)
	}
	operation, err := child.detectOperation()
	if err != nil {
		return "", err
	}
	if operation.any() {
		if operation.rebase || operation.merge {
			return "", fmt.Errorf("an unfinished %s is in progress; use vaultctl sync --continue --submodule %s or vaultctl sync --abort --submodule %s", operation.description(), shellQuote(entry.path), shellQuote(entry.path))
		}
		return "", fmt.Errorf("an unfinished %s is in progress in the submodule; resolve it before syncing", operation.description())
	}
	conflicts, err := child.conflictedFiles()
	if err != nil {
		return "", err
	}
	if len(conflicts) > 0 {
		child.reportConflicts(conflicts)
		return "", &userError{"vaultctl sync cannot start while a submodule index contains unresolved conflicts."}
	}
	branch, err := child.selectedSubmoduleBranch(entry)
	if err != nil {
		return "", err
	}
	attached, err := child.currentBranch()
	if err != nil {
		return "", err
	}
	if attached != "" && attached != branch {
		return "", fmt.Errorf("worktree is attached to branch %q, but .gitmodules selects %q; change the branch explicitly before syncing", attached, branch)
	}

	child.say("Fetching origin branch %s...", branch)
	if err := child.checkedStream("fetch selected submodule branch", "fetch", "--", "origin", "refs/heads/"+branch); err != nil {
		return "", err
	}
	fetchPath, err := child.captureChecked("locate fetched branch result", "rev-parse", "--git-path", "FETCH_HEAD")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(fetchPath) {
		fetchPath = filepath.Join(childRoot, fetchPath)
	}
	target, err := readFetchedCommit(childRoot, fetchPath)
	if err != nil {
		return "", fmt.Errorf("validate fetched branch %q: %w", branch, err)
	}
	if err := requireCommitObject(childRoot, target); err != nil {
		return "", fmt.Errorf("validate fetched branch %q: %w", branch, err)
	}
	if err := child.autosaveSubmoduleWork(func() error {
		if err := validateIdentity(); err != nil {
			return err
		}
		return a.checkSubmodulePushEndpoints(child)
	}); err != nil {
		return "", err
	}

	pushTarget := upstreamInfo{
		branch:   branch,
		name:     "origin/" + branch,
		remote:   "origin",
		mergeRef: "refs/heads/" + branch,
	}
	if err := validateIdentity(); err != nil {
		return "", err
	}
	if err := child.reconcileHistory(target, pushTarget, useMerge, "origin/"+branch); err != nil {
		return "", err
	}
	head, err := child.captureChecked("read synchronized submodule HEAD", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if !isObjectID(head) {
		return "", fmt.Errorf("synchronized HEAD %q is not a valid commit identifier", head)
	}
	if recorded == "" {
		return "", fmt.Errorf("parent index has no recorded gitlink for tracking submodule")
	}
	child.say("Tracking submodule %s is synchronized at %s.", entry.path, head)
	return head, nil
}

func (a *App) autosaveSubmoduleWork(beforeCommit func() error) error {
	status, err := a.captureChecked("inspect submodule changes", "status", "--porcelain")
	if err != nil {
		return err
	}
	if status == "" {
		return nil
	}
	gitlinks, err := a.syncAutosaveGitlinkPaths()
	if err != nil {
		return err
	}
	message, err := a.commitMessage()
	if err != nil {
		return err
	}
	if beforeCommit != nil {
		if err := beforeCommit(); err != nil {
			return err
		}
	}
	a.say("Saving safe local work before reconciling the selected branch...")
	if err := a.stageAndCommitExcluding(message, false, gitlinks); err != nil {
		return err
	}
	remaining, err := a.git.capture("status", "--porcelain=v1", "-z")
	if err != nil {
		return err
	}
	if remaining.exitCode != 0 {
		return fmt.Errorf("verify saved submodule changes failed (Git exited with status %d): %s", remaining.exitCode, strings.TrimSpace(remaining.stderr))
	}
	paths, err := porcelainPaths(remaining.stdout)
	if err != nil {
		return fmt.Errorf("verify saved submodule changes: %w", err)
	}
	if !onlyGitlinkPaths(paths, gitlinks) {
		return fmt.Errorf("submodule changes remain after autosave; refusing to contact the selected branch")
	}
	return nil
}

func (a *App) currentBranch() (string, error) {
	result, err := a.git.capture("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	if result.exitCode == 1 {
		return "", nil
	}
	if result.exitCode != 0 {
		return "", fmt.Errorf("inspect current branch failed (Git exited with status %d): %s", result.exitCode, strings.TrimSpace(result.stderr))
	}
	branch := strings.TrimSpace(result.stdout)
	if branch == "" {
		return "", fmt.Errorf("Git returned an empty current branch name")
	}
	return branch, nil
}

func (a *App) selectedSubmoduleBranch(entry submoduleEntry) (string, error) {
	if entry.branchSet {
		if err := a.validateBranchName(entry.branch); err != nil {
			return "", err
		}
		return entry.branch, nil
	}
	result, err := a.git.capture("ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", err
	}
	if result.exitCode != 0 {
		return "", fmt.Errorf("resolve origin default branch: %s", strings.TrimSpace(result.stderr))
	}
	var symbolic []string
	var headObjects []string
	for _, line := range strings.Split(strings.TrimSuffix(result.stdout, "\n"), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 {
			return "", fmt.Errorf("origin returned malformed symbolic HEAD output")
		}
		if fields[1] == "HEAD" && strings.HasPrefix(fields[0], "ref: ") {
			symbolic = append(symbolic, strings.TrimPrefix(fields[0], "ref: "))
			continue
		}
		if fields[1] == "HEAD" && isObjectID(fields[0]) {
			headObjects = append(headObjects, fields[0])
			continue
		}
		return "", fmt.Errorf("origin returned unexpected symbolic HEAD output")
	}
	if len(symbolic) != 1 || len(headObjects) != 1 || !strings.HasPrefix(symbolic[0], "refs/heads/") {
		return "", fmt.Errorf("origin did not report one unambiguous symbolic default branch")
	}
	branch := strings.TrimPrefix(symbolic[0], "refs/heads/")
	if err := a.validateBranchName(branch); err != nil {
		return "", err
	}
	return branch, nil
}

func (a *App) validateBranchName(branch string) error {
	if branch == "" || branch == "." || strings.HasPrefix(branch, "-") || strings.ContainsRune(branch, '\x00') {
		return fmt.Errorf("branch %q is not a concrete branch name", branch)
	}
	result, err := a.git.capture("check-ref-format", "refs/heads/"+branch)
	if err != nil {
		return err
	}
	if result.exitCode != 0 {
		return fmt.Errorf("branch %q is not a valid Git branch name", branch)
	}
	return nil
}

func (a *App) initializeTrackingSubmodule(entry submoduleEntry, childRoot string) error {
	path := entry.path
	registeredRoot, err := a.registeredSubmodulePath(path)
	if err != nil {
		return err
	}
	if !sameFilesystemPath(registeredRoot, childRoot) {
		return fmt.Errorf("submodule %q does not resolve to its registered path", path)
	}
	if info, err := os.Stat(registeredRoot); err == nil && info.Mode().IsRegular() {
		return fmt.Errorf("submodule path is an existing file")
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect submodule worktree: %w", err)
	}
	if _, err := os.Stat(registeredRoot); os.IsNotExist(err) {
		if err := a.checkedStream("initialize tracking submodule", "submodule", "update", "--init", "--checkout", "--", literalPathspec(path)); err != nil {
			return err
		}
		return a.validateRegisteredSubmoduleIdentity(entry)
	}
	child, _, err := a.submoduleApp(path)
	if err != nil {
		return err
	}
	inside, err := child.git.capture("rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if inside.exitCode == 0 {
		actual, err := canonicalDirectory(strings.TrimSpace(inside.stdout))
		if err != nil {
			return err
		}
		want, err := canonicalPathAllowMissing(childRoot)
		if err != nil {
			return err
		}
		parent, err := canonicalDirectory(a.cfg.VaultPath)
		if err != nil {
			return err
		}
		if sameFilesystemPath(actual, parent) {
			if err := a.checkedStream("initialize tracking submodule", "submodule", "update", "--init", "--checkout", "--", literalPathspec(path)); err != nil {
				return err
			}
			return a.validateRegisteredSubmoduleIdentity(entry)
		}
		if !sameFilesystemPath(actual, want) {
			return fmt.Errorf("submodule worktree resolves to %q, expected %q", actual, want)
		}
		return a.validateRegisteredSubmoduleIdentity(entry)
	}
	if err := a.checkedStream("initialize tracking submodule", "submodule", "update", "--init", "--checkout", "--", literalPathspec(path)); err != nil {
		return err
	}
	return a.validateRegisteredSubmoduleIdentity(entry)
}

func (a *App) registeredSubmodulePath(path string) (string, error) {
	if _, err := normalizeRelativePath(path); err != nil {
		return "", err
	}
	root, err := canonicalDirectory(a.cfg.VaultPath)
	if err != nil {
		return "", fmt.Errorf("resolve client vault path: %w", err)
	}
	registered := filepath.Clean(filepath.Join(root, filepath.FromSlash(path)))
	resolved, err := canonicalPathAllowMissing(registered)
	if err != nil {
		return "", fmt.Errorf("resolve registered submodule path: %w", err)
	}
	if !canonicalPathWithin(root, resolved) {
		return "", fmt.Errorf("worktree path resolves outside the configured vault")
	}
	if !sameFilesystemPath(registered, resolved) {
		return "", fmt.Errorf("registered submodule path %q resolves through a symlink or alias", path)
	}
	return resolved, nil
}

func (a *App) validateRegisteredSubmoduleIdentity(entry submoduleEntry) error {
	registeredRoot, err := a.registeredSubmodulePath(entry.path)
	if err != nil {
		return err
	}
	child, childRoot, err := a.submoduleApp(entry.path)
	if err != nil {
		return err
	}
	if !sameFilesystemPath(childRoot, registeredRoot) {
		return fmt.Errorf("submodule %q does not match its registered Git submodule path", entry.path)
	}
	inside, err := child.captureChecked("inspect registered submodule worktree", "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("submodule %q is not initialized: %w", entry.path, err)
	}
	actualRoot, err := canonicalDirectory(inside)
	if err != nil {
		return err
	}
	wantRoot, err := canonicalDirectory(registeredRoot)
	if err != nil {
		return fmt.Errorf("submodule %q is not initialized: %w", entry.path, err)
	}
	if !sameFilesystemPath(actualRoot, wantRoot) {
		return fmt.Errorf("repository at submodule path %q is not the registered Git submodule", entry.path)
	}

	if _, err := normalizeRelativePath(entry.name); err != nil {
		return fmt.Errorf("registered Git submodule %q has an unsafe module name: %w", entry.path, err)
	}
	modulePath := filepath.ToSlash(filepath.Join("modules", filepath.FromSlash(entry.name)))
	expectedGitDir, err := a.captureChecked("resolve registered submodule Git directory", "rev-parse", "--git-path", modulePath)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(expectedGitDir) {
		root, err := canonicalDirectory(a.cfg.VaultPath)
		if err != nil {
			return fmt.Errorf("resolve client vault path: %w", err)
		}
		expectedGitDir = filepath.Join(root, filepath.FromSlash(expectedGitDir))
	}
	expectedGitDir, err = canonicalDirectory(expectedGitDir)
	if err != nil {
		return fmt.Errorf("registered Git submodule %q has no expected module directory: %w", entry.path, err)
	}
	actualGitDir, err := child.captureChecked("inspect registered submodule Git directory", "rev-parse", "--absolute-git-dir")
	if err != nil {
		return err
	}
	actualGitDir, err = canonicalDirectory(actualGitDir)
	if err != nil {
		return err
	}
	if !sameFilesystemPath(actualGitDir, expectedGitDir) {
		return fmt.Errorf("repository at submodule path %q is not the registered Git submodule", entry.path)
	}

	superproject, err := child.captureChecked("inspect registered submodule parent", "rev-parse", "--show-superproject-working-tree")
	if err != nil {
		return err
	}
	if superproject == "" {
		return fmt.Errorf("repository at submodule path %q is not the registered Git submodule", entry.path)
	}
	superproject, err = canonicalDirectory(superproject)
	if err != nil {
		return fmt.Errorf("inspect registered submodule parent: %w", err)
	}
	parentRoot, err := canonicalDirectory(a.cfg.VaultPath)
	if err != nil {
		return fmt.Errorf("resolve client vault path: %w", err)
	}
	if !sameFilesystemPath(superproject, parentRoot) {
		return fmt.Errorf("repository at submodule path %q is not the registered Git submodule", entry.path)
	}
	return nil
}

func (a *App) validateExistingRegisteredSubmoduleIdentity(entry submoduleEntry) error {
	registeredRoot, err := a.registeredSubmodulePath(entry.path)
	if err != nil {
		return err
	}
	info, err := os.Stat(registeredRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect submodule worktree: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("submodule path is not a directory")
	}
	child, childRoot, err := a.submoduleApp(entry.path)
	if err != nil {
		return err
	}
	inside, err := child.git.capture("rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if inside.exitCode != 0 {
		return nil
	}
	actualRoot, err := canonicalDirectory(strings.TrimSpace(inside.stdout))
	if err != nil {
		return err
	}
	parentRoot, err := canonicalDirectory(a.cfg.VaultPath)
	if err != nil {
		return fmt.Errorf("resolve client vault path: %w", err)
	}
	if sameFilesystemPath(actualRoot, parentRoot) {
		return nil
	}
	if !sameFilesystemPath(actualRoot, childRoot) {
		return fmt.Errorf("submodule worktree resolves to %q, expected %q", actualRoot, childRoot)
	}
	return a.validateRegisteredSubmoduleIdentity(entry)
}

func (a *App) updateSubmoduleAtGitlink(entry submoduleEntry) error {
	if err := a.validateExistingRegisteredSubmoduleIdentity(entry); err != nil {
		return err
	}
	if err := a.checkedStream("checkout pinned submodule", "submodule", "update", "--init", "--checkout", "--", literalPathspec(entry.path)); err != nil {
		return err
	}
	return a.validateRegisteredSubmoduleIdentity(entry)
}

func (a *App) submoduleApp(path string) (*App, string, error) {
	root, err := canonicalDirectory(a.cfg.VaultPath)
	if err != nil {
		return nil, "", fmt.Errorf("resolve client vault path: %w", err)
	}
	childRoot, err := canonicalPathAllowMissing(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return nil, "", err
	}
	if !canonicalPathWithin(root, childRoot) {
		return nil, "", fmt.Errorf("submodule path escapes the configured vault worktree")
	}
	cfg := a.cfg
	cfg.VaultPath = childRoot
	child := *a
	child.cfg = cfg
	child.git = newGitRunner(cfg, a.git.stdin, a.stdout, a.stderr)
	child.git.goos = a.git.goos
	child.repoLabel = "submodule " + path
	child.submoduleRecoveryPath = path
	return &child, childRoot, nil
}

func literalPathspec(path string) string {
	return ":(top,literal)" + path
}

func cloneGitlinks(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for path, object := range input {
		output[path] = object
	}
	return output
}

func loadDirectSubmodules(git *gitRunner) ([]submoduleEntry, map[string]string, error) {
	result, err := git.capture("config", "--null", "--file", ".gitmodules", "--get-regexp", `^submodule\..*\.(path|url|branch|vaultctl-pinned)$`)
	if err != nil {
		return nil, nil, err
	}
	entriesByName := make(map[string]*submoduleEntry)
	fieldCounts := make(map[string]int)
	if result.exitCode != 0 && result.exitCode != 1 {
		return nil, nil, fmt.Errorf("read .gitmodules: %s", strings.TrimSpace(result.stderr))
	}
	if result.exitCode == 0 {
		records, err := nulRecords(result.stdout, "read .gitmodules")
		if err != nil {
			return nil, nil, err
		}
		for _, record := range records {
			separator := strings.IndexByte(record, '\n')
			if separator < 0 {
				return nil, nil, fmt.Errorf("read .gitmodules: malformed Git config output")
			}
			key, value := record[:separator], record[separator+1:]
			if !strings.HasPrefix(key, "submodule.") {
				return nil, nil, fmt.Errorf("read .gitmodules: unexpected config key %q", key)
			}
			lastDot := strings.LastIndexByte(key, '.')
			if lastDot <= len("submodule.") {
				return nil, nil, fmt.Errorf("read .gitmodules: malformed config key %q", key)
			}
			name, field := key[len("submodule."):lastDot], key[lastDot+1:]
			countKey := name + "\x00" + field
			fieldCounts[countKey]++
			if fieldCounts[countKey] > 1 {
				return nil, nil, fmt.Errorf("submodule %q has duplicate %s values", name, field)
			}
			entry := entriesByName[name]
			if entry == nil {
				entry = &submoduleEntry{name: name}
				entriesByName[name] = entry
			}
			switch field {
			case "path":
				entry.path = value
			case "url":
				entry.url = value
			case "branch":
				entry.branch = value
				entry.branchSet = true
			case "vaultctl-pinned":
				if value != "true" {
					return nil, nil, fmt.Errorf("submodule %q vaultctl-pinned must be the literal true", name)
				}
				entry.pinned = true
			}
		}
	}

	entries := make([]submoduleEntry, 0, len(entriesByName))
	seenPaths := make(map[string]struct{}, len(entriesByName))
	for name, entry := range entriesByName {
		if entry.path == "" || entry.url == "" {
			return nil, nil, fmt.Errorf("submodule %q must have exactly one path and URL", name)
		}
		path, err := normalizeRelativePath(entry.path)
		if err != nil {
			return nil, nil, fmt.Errorf("submodule %q path: %w", name, err)
		}
		if _, exists := seenPaths[path]; exists {
			return nil, nil, fmt.Errorf("multiple .gitmodules entries use path %q", path)
		}
		seenPaths[path] = struct{}{}
		entry.path = path
		if entry.branchSet && entry.branch == "." {
			return nil, nil, fmt.Errorf("submodule %q branch = . is not supported", name)
		}
		if entry.branchSet {
			if err := validateGitBranch(git, entry.branch); err != nil {
				return nil, nil, fmt.Errorf("submodule %q: %w", name, err)
			}
		}
		entries = append(entries, *entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })

	gitlinks, err := readIndexGitlinks(git)
	if err != nil {
		return nil, nil, err
	}
	for _, entry := range entries {
		if _, ok := gitlinks[entry.path]; !ok {
			return nil, nil, fmt.Errorf(".gitmodules path %q does not match an indexed gitlink", entry.path)
		}
	}
	for path := range gitlinks {
		if _, ok := seenPaths[path]; !ok {
			return nil, nil, fmt.Errorf("indexed gitlink %q has no matching .gitmodules path", path)
		}
	}
	return entries, gitlinks, nil
}

func loadSubmodulePaths(git *gitRunner) ([]string, error) {
	result, err := git.capture("config", "--null", "--file", ".gitmodules", "--get-regexp", `^submodule\..*\.path$`)
	if err != nil {
		return nil, err
	}
	if result.exitCode != 0 && result.exitCode != 1 {
		return nil, fmt.Errorf("read nested .gitmodules paths: %s", strings.TrimSpace(result.stderr))
	}

	paths := make([]string, 0)
	names := make(map[string]struct{})
	seenPaths := make(map[string]struct{})
	if result.exitCode == 0 {
		records, err := nulRecords(result.stdout, "read nested .gitmodules paths")
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			separator := strings.IndexByte(record, '\n')
			if separator < 0 {
				return nil, fmt.Errorf("read nested .gitmodules paths: malformed Git config output")
			}
			key, value := record[:separator], record[separator+1:]
			if !strings.HasPrefix(key, "submodule.") {
				return nil, fmt.Errorf("read nested .gitmodules paths: unexpected config key %q", key)
			}
			lastDot := strings.LastIndexByte(key, '.')
			if lastDot <= len("submodule.") || key[lastDot+1:] != "path" {
				return nil, fmt.Errorf("read nested .gitmodules paths: malformed config key %q", key)
			}
			name := key[len("submodule."):lastDot]
			if _, duplicate := names[name]; duplicate {
				return nil, fmt.Errorf("nested submodule %q has duplicate path values", name)
			}
			names[name] = struct{}{}
			path, err := normalizeRelativePath(value)
			if err != nil {
				return nil, fmt.Errorf("nested submodule %q path: %w", name, err)
			}
			if _, duplicate := seenPaths[path]; duplicate {
				return nil, fmt.Errorf("multiple nested .gitmodules entries use path %q", path)
			}
			seenPaths[path] = struct{}{}
			paths = append(paths, path)
		}
	}

	gitlinks, err := readIndexGitlinks(git)
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		if _, ok := gitlinks[path]; !ok {
			return nil, fmt.Errorf("nested .gitmodules path %q does not match an indexed gitlink", path)
		}
	}
	for path := range gitlinks {
		if _, ok := seenPaths[path]; !ok {
			return nil, fmt.Errorf("indexed nested gitlink %q has no matching .gitmodules path", path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func validateGitBranch(git *gitRunner, branch string) error {
	if branch == "" || branch == "." || strings.HasPrefix(branch, "-") || strings.ContainsRune(branch, '\x00') {
		return fmt.Errorf("branch %q is not a concrete branch name", branch)
	}
	result, err := git.capture("check-ref-format", "refs/heads/"+branch)
	if err != nil {
		return err
	}
	if result.exitCode != 0 {
		return fmt.Errorf("branch %q is not a valid Git branch name", branch)
	}
	return nil
}

func readIndexGitlinks(git *gitRunner) (map[string]string, error) {
	result, err := git.capture("ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	if result.exitCode != 0 {
		return nil, fmt.Errorf("inspect indexed gitlinks failed (Git exited with status %d): %s", result.exitCode, strings.TrimSpace(result.stderr))
	}
	links := make(map[string]string)
	if result.stdout == "" {
		return links, nil
	}
	records, err := nulRecords(result.stdout, "inspect indexed gitlinks")
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		tab := strings.IndexByte(record, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("inspect indexed gitlinks: malformed index record")
		}
		fields := strings.Fields(record[:tab])
		path := record[tab+1:]
		if len(fields) != 3 || path == "" {
			return nil, fmt.Errorf("inspect indexed gitlinks: malformed index record")
		}
		if fields[0] != "160000" {
			continue
		}
		if fields[2] != "0" {
			return nil, fmt.Errorf("indexed gitlink %q has unresolved stages", path)
		}
		if _, exists := links[path]; exists {
			return nil, fmt.Errorf("indexed gitlink %q has duplicate entries", path)
		}
		if !isObjectID(fields[1]) {
			return nil, fmt.Errorf("indexed gitlink %q has malformed object ID", path)
		}
		links[path] = fields[1]
	}
	return links, nil
}

func activeSubmodules(git *gitRunner, entries []submoduleEntry) ([]submoduleEntry, error) {
	patterns, err := configValues(git, "submodule.active")
	if err != nil {
		return nil, err
	}
	matchedPaths := make(map[string]struct{})
	if len(patterns) > 0 {
		args := []string{"ls-files", "--stage", "-z", "--"}
		hasPositive := false
		for _, pattern := range patterns {
			if !negativePathspec(pattern) {
				hasPositive = true
			}
		}
		if !hasPositive {
			args = append(args, ".")
		}
		args = append(args, patterns...)
		result, err := git.capture(args...)
		if err != nil {
			return nil, err
		}
		if result.exitCode != 0 {
			return nil, fmt.Errorf("resolve submodule.active pathspecs: %s", strings.TrimSpace(result.stderr))
		}
		paths, err := indexPaths(result.stdout)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			matchedPaths[path] = struct{}{}
		}
	}

	selected := make([]submoduleEntry, 0, len(entries))
	for _, entry := range entries {
		activeValue, activeSet, err := configValue(git, "submodule."+entry.name+".active")
		if err != nil {
			return nil, err
		}
		if activeSet {
			active, err := strconv.ParseBool(strings.ToLower(activeValue))
			if err != nil {
				switch strings.ToLower(activeValue) {
				case "yes", "on", "1":
					active = true
				case "no", "off", "0":
					active = false
				default:
					return nil, fmt.Errorf("submodule %q has invalid active setting %q", entry.name, activeValue)
				}
			}
			if active {
				selected = append(selected, entry)
			}
			continue
		}
		if len(patterns) > 0 {
			if _, ok := matchedPaths[entry.path]; ok {
				selected = append(selected, entry)
			}
			continue
		}
		urls, err := configValues(git, "submodule."+entry.name+".url")
		if err != nil {
			return nil, err
		}
		if len(urls) > 0 {
			selected = append(selected, entry)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].path < selected[j].path })
	return selected, nil
}

func negativePathspec(value string) bool {
	if strings.HasPrefix(value, ":!") || strings.HasPrefix(value, ":^") {
		return true
	}
	if !strings.HasPrefix(value, ":(") {
		return false
	}
	close := strings.IndexByte(value, ')')
	if close < 0 {
		return false
	}
	for _, option := range strings.Split(value[2:close], ",") {
		if option == "exclude" {
			return true
		}
	}
	return false
}

func configValues(git *gitRunner, key string) ([]string, error) {
	result, err := git.capture("config", "--null", "--get-all", key)
	if err != nil {
		return nil, err
	}
	if result.exitCode == 1 {
		return nil, nil
	}
	if result.exitCode != 0 {
		return nil, fmt.Errorf("read Git configuration %q: %s", key, strings.TrimSpace(result.stderr))
	}
	return nulRecords(result.stdout, "read Git configuration "+key)
}

func configValue(git *gitRunner, key string) (string, bool, error) {
	result, err := git.capture("config", "--null", "--get", key)
	if err != nil {
		return "", false, err
	}
	if result.exitCode == 1 {
		return "", false, nil
	}
	if result.exitCode != 0 {
		return "", false, fmt.Errorf("read Git configuration %q: %s", key, strings.TrimSpace(result.stderr))
	}
	records, err := nulRecords(result.stdout, "read Git configuration "+key)
	if err != nil {
		return "", false, err
	}
	if len(records) != 1 {
		return "", false, fmt.Errorf("read Git configuration %q: expected one effective value", key)
	}
	return records[0], true, nil
}

func nulRecords(output, action string) ([]string, error) {
	if output == "" {
		return nil, nil
	}
	records := strings.Split(output, "\x00")
	if records[len(records)-1] != "" {
		return nil, fmt.Errorf("%s: malformed NUL-delimited Git output", action)
	}
	for _, record := range records[:len(records)-1] {
		if record == "" {
			return nil, fmt.Errorf("%s: empty record in Git output", action)
		}
	}
	return records[:len(records)-1], nil
}

func gitCommandPathExists(git *gitRunner, args ...string) (bool, error) {
	result, err := git.capture(args...)
	if err != nil {
		return false, err
	}
	if result.exitCode == 0 {
		return true, nil
	}
	if result.exitCode == 1 {
		return false, nil
	}
	return false, fmt.Errorf("Git exited with status %d: %s", result.exitCode, strings.TrimSpace(result.stderr))
}

func gitCommitPathExists(git *gitRunner, path string) (bool, error) {
	result, err := git.capture("ls-tree", "--full-tree", "--name-only", "-r", "-z", "HEAD", "--", literalPathspec(path))
	if err != nil {
		return false, err
	}
	if result.exitCode != 0 {
		return false, fmt.Errorf("Git exited with status %d: %s", result.exitCode, strings.TrimSpace(result.stderr))
	}
	paths, err := nulRecords(result.stdout, "inspect committed path")
	if err != nil {
		return false, err
	}
	for _, committedPath := range paths {
		if committedPath == path {
			return true, nil
		}
	}
	return false, nil
}

func indexPaths(output string) ([]string, error) {
	records, err := nulRecords(output, "read Git index paths")
	if err != nil || len(records) == 0 {
		return nil, err
	}
	paths := make([]string, 0, len(records))
	for _, record := range records {
		tab := strings.IndexByte(record, '\t')
		if tab < 0 || record[tab+1:] == "" {
			return nil, fmt.Errorf("read Git index paths: malformed index record")
		}
		paths = append(paths, record[tab+1:])
	}
	return paths, nil
}

func (a *App) loadProjectionProtection() (projectionProtection, error) {
	protection := projectionProtection{endpoints: make(map[string]struct{})}
	manifestPath := filepath.Join(a.cfg.VaultPath, filepath.FromSlash(externalManifestPath))
	info, err := os.Lstat(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			inIndex, indexErr := gitCommandPathExists(a.git, "ls-files", "--error-unmatch", "--", externalManifestPath)
			if indexErr != nil {
				return projectionProtection{}, fmt.Errorf("inspect tracked external documentation manifest: %w", indexErr)
			}
			inHead, headErr := gitCommitPathExists(a.git, externalManifestPath)
			if headErr != nil {
				return projectionProtection{}, fmt.Errorf("inspect committed external documentation manifest: %w", headErr)
			}
			if inIndex || inHead {
				return projectionProtection{}, fmt.Errorf("external documentation manifest is missing from the worktree or parent index")
			}
			return protection, nil
		}
		return projectionProtection{}, fmt.Errorf("inspect external documentation manifest: %w", err)
	}
	if err := a.requireTrackedAtHead(externalManifestPath); err != nil {
		return projectionProtection{}, fmt.Errorf("external documentation manifest must be tracked and unchanged: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return projectionProtection{}, fmt.Errorf("external documentation manifest must be a regular file")
	}
	if a.cfg.SourceRoot == "" {
		return projectionProtection{}, fmt.Errorf("source_root is required when %s exists", externalManifestPath)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return projectionProtection{}, fmt.Errorf("read external documentation manifest: %w", err)
	}
	manifest, err := parseExternalManifest(data)
	if err != nil {
		return projectionProtection{}, fmt.Errorf("invalid %s: %w", externalManifestPath, err)
	}
	sourceRoot, err := canonicalDirectory(a.cfg.SourceRoot)
	if err != nil {
		return projectionProtection{}, fmt.Errorf("validate source_root: %w", err)
	}
	for _, source := range manifest.Sources {
		repository, err := boundedSourceRepository(sourceRoot, source.Repository)
		if err != nil {
			return projectionProtection{}, fmt.Errorf("source %q repository: %w", source.ID, err)
		}
		if err := validateSourceRepository(repository); err != nil {
			return projectionProtection{}, fmt.Errorf("source %q: %w", source.ID, err)
		}
		if err := validateSourceRemote(repository, source.Remote); err != nil {
			return projectionProtection{}, fmt.Errorf("source %q: %w", source.ID, err)
		}
		canonical, err := canonicalDirectory(repository)
		if err != nil {
			return projectionProtection{}, fmt.Errorf("source %q worktree: %w", source.ID, err)
		}
		protection.worktrees = append(protection.worktrees, canonical)
		for _, push := range []bool{false, true} {
			urls, err := effectiveRemoteURLs(repository, source.Remote, push)
			if err != nil {
				return projectionProtection{}, fmt.Errorf("source %q remote identity: %w", source.ID, err)
			}
			for _, remoteURL := range urls {
				endpoint, err := canonicalGitEndpoint(repository, remoteURL)
				if err != nil {
					return projectionProtection{}, fmt.Errorf("source %q remote identity: %w", source.ID, err)
				}
				protection.endpoints[endpoint] = struct{}{}
			}
		}
	}
	return protection, nil
}

func (a *App) checkSubmodulePath(vaultRoot, path string, protection projectionProtection) error {
	if _, err := normalizeRelativePath(path); err != nil {
		return err
	}
	candidate, err := canonicalPathAllowMissing(filepath.Join(vaultRoot, filepath.FromSlash(path)))
	if err != nil {
		return fmt.Errorf("resolve worktree path: %w", err)
	}
	if !canonicalPathWithin(vaultRoot, candidate) {
		return fmt.Errorf("worktree path resolves outside the configured vault")
	}
	for _, source := range protection.worktrees {
		if canonicalPathsOverlap(candidate, source) {
			return fmt.Errorf("worktree path overlaps external documentation source %q", source)
		}
	}
	return nil
}

func (a *App) checkSubmodulePushEndpoints(child *App) error {
	protection, err := a.loadProjectionProtection()
	if err != nil {
		return fmt.Errorf("reload projection source protections before child commit or push: %w", err)
	}
	urls, err := effectiveRemoteURLs(child.cfg.VaultPath, "origin", true)
	if err != nil {
		return fmt.Errorf("resolve origin push URL: %w", err)
	}
	for _, remoteURL := range urls {
		endpoint, err := canonicalGitEndpoint(child.cfg.VaultPath, remoteURL)
		if err != nil {
			return fmt.Errorf("resolve origin push URL: %w", err)
		}
		if _, overlaps := protection.endpoints[endpoint]; overlaps {
			return fmt.Errorf("origin push URL matches an external documentation source remote; refusing child commit or push")
		}
	}
	return nil
}

func effectiveRemoteURLs(repository, remote string, push bool) ([]string, error) {
	args := []string{"remote", "get-url"}
	if push {
		args = append(args, "--push")
	}
	args = append(args, "--all", "--", remote)
	result, err := externalGitCapture(repository, args...)
	if err != nil {
		return nil, err
	}
	if result.exitCode != 0 {
		return nil, fmt.Errorf("remote %q has no reliable effective URL", remote)
	}
	output := strings.TrimSuffix(result.stdout, "\n")
	if output == "" {
		return nil, fmt.Errorf("remote %q has no effective URL", remote)
	}
	urls := strings.Split(output, "\n")
	for _, value := range urls {
		if value == "" || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("remote %q returned an empty or malformed URL", remote)
		}
	}
	return urls, nil
}

func canonicalGitEndpoint(repository, value string) (string, error) {
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("Git returned an empty or malformed remote URL")
	}
	if filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
		canonical, err := canonicalPathAllowMissing(value)
		if err != nil {
			return "", err
		}
		return "path:" + canonical, nil
	}
	if strings.HasPrefix(value, "file:") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "file" || (parsed.Host != "" && parsed.Host != "localhost") || parsed.Path == "" {
			return "", fmt.Errorf("cannot resolve local file URL %q", value)
		}
		filePath := parsed.Path
		if runtime.GOOS == "windows" && len(filePath) >= 3 && filePath[0] == '/' && filePath[2] == ':' {
			filePath = filePath[1:]
		}
		path, err := canonicalPathAllowMissing(filepath.FromSlash(filePath))
		if err != nil {
			return "", err
		}
		return "path:" + path, nil
	}
	if looksLikeSCPLikeURL(value) {
		return "url:" + value, nil
	}
	if parsed, err := url.Parse(value); err != nil {
		return "", fmt.Errorf("cannot parse remote URL %q: %w", value, err)
	} else if parsed.Scheme != "" {
		return "url:" + value, nil
	}
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(repository, filepath.FromSlash(path))
	}
	canonical, err := canonicalPathAllowMissing(path)
	if err != nil {
		return "", err
	}
	return "path:" + canonical, nil
}

func looksLikeSCPLikeURL(value string) bool {
	if strings.Contains(value, "://") {
		return false
	}
	colon := strings.IndexByte(value, ':')
	if colon <= 0 || colon == len(value)-1 {
		return false
	}
	slash := strings.IndexAny(value, "/\\")
	if slash >= 0 && slash < colon {
		return false
	}
	authority := value[:colon]
	if strings.ContainsAny(authority, " \t\r\n\\") {
		return false
	}
	if strings.Count(authority, "@") == 1 {
		user, host, _ := strings.Cut(authority, "@")
		return user != "" && host != ""
	}
	if strings.Contains(authority, "@") {
		return false
	}
	// Dotted and underscored host names are not valid URL schemes. A single
	// label such as "host:path" is already accepted by net/url as an opaque
	// URL, so it does not need the SCP fallback here.
	return strings.ContainsAny(authority, "._")
}

func canonicalPathAllowMissing(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	current := absolute
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		if _, lstatErr := os.Lstat(current); lstatErr == nil {
			return "", fmt.Errorf("cannot resolve existing path %q: %w", current, err)
		} else if !os.IsNotExist(lstatErr) {
			return "", lstatErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("cannot resolve path %q", path)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func canonicalPathWithin(root, path string) bool {
	if runtime.GOOS == "windows" {
		root = strings.ToLower(filepath.Clean(root))
		path = strings.ToLower(filepath.Clean(path))
		if path == root || strings.HasPrefix(path, strings.TrimRight(root, string(filepath.Separator))+string(filepath.Separator)) {
			return true
		}
		return false
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func sameFilesystemPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func canonicalPathsOverlap(left, right string) bool {
	return canonicalPathWithin(left, right) || canonicalPathWithin(right, left)
}

func (a *App) recordTrackingGitlinks(baseline, successful map[string]string) (bool, error) {
	current, err := readIndexGitlinks(a.git)
	if err != nil {
		return false, err
	}
	if !equalGitlinks(current, baseline) {
		return false, fmt.Errorf("parent index gitlinks changed during child sync; refusing grouped recording")
	}
	staged, err := a.git.capture("diff", "--cached", "--quiet")
	if err != nil {
		return false, err
	}
	if staged.exitCode != 0 {
		return false, fmt.Errorf("parent index has staged changes during child sync; refusing grouped recording")
	}
	var changed []string
	for path, head := range successful {
		if baseline[path] != head {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	if len(changed) == 0 {
		return false, nil
	}
	if err := a.validateTrackingSubmoduleIdentities(changed); err != nil {
		return false, err
	}
	args := []string{"add", "--"}
	for _, path := range changed {
		args = append(args, literalPathspec(path))
	}
	if err := a.checkedStream("stage synchronized gitlinks", args...); err != nil {
		return false, err
	}
	if err := a.verifyOnlyChangedGitlinks(changed, successful); err != nil {
		return false, err
	}
	if err := a.validateTrackingSubmoduleIdentities(changed); err != nil {
		return false, err
	}
	message, err := a.commitMessage()
	if err != nil {
		return false, err
	}
	a.say("Recording synchronized project submodules in one parent commit...")
	if err := a.checkedStream("commit synchronized gitlinks", "commit", "-m", message); err != nil {
		return false, err
	}
	return true, nil
}

func (a *App) validateTrackingSubmoduleIdentities(paths []string) error {
	entries, _, err := loadDirectSubmodules(a.git)
	if err != nil {
		return err
	}
	byPath := make(map[string]submoduleEntry, len(entries))
	for _, entry := range entries {
		byPath[entry.path] = entry
	}
	for _, path := range paths {
		entry, ok := byPath[path]
		if !ok {
			return fmt.Errorf("submodule %q is no longer registered before grouped gitlink recording", path)
		}
		if err := a.validateRegisteredSubmoduleIdentity(entry); err != nil {
			return fmt.Errorf("submodule %q identity changed before grouped gitlink recording: %w", path, err)
		}
	}
	return nil
}

func equalGitlinks(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for path, object := range left {
		if right[path] != object {
			return false
		}
	}
	return true
}

func (a *App) verifyOnlyChangedGitlinks(expected []string, successful map[string]string) error {
	result, err := a.git.capture("diff", "--cached", "--name-only", "-z")
	if err != nil {
		return err
	}
	if result.exitCode != 0 {
		return fmt.Errorf("inspect grouped gitlink changes failed (Git exited with status %d): %s", result.exitCode, strings.TrimSpace(result.stderr))
	}
	actual, err := nulRecords(result.stdout, "inspect grouped gitlink changes")
	if err != nil {
		return err
	}
	if !sameStrings(actual, expected) {
		return fmt.Errorf("unexpected parent index changes appeared during grouped gitlink recording; refusing commit")
	}
	raw, err := a.git.capture("diff", "--cached", "--raw", "-z")
	if err != nil {
		return err
	}
	if raw.exitCode != 0 {
		return fmt.Errorf("inspect grouped gitlink modes failed (Git exited with status %d): %s", raw.exitCode, strings.TrimSpace(raw.stderr))
	}
	if err := verifyRawDiffGitlinks(raw.stdout, expected); err != nil {
		return err
	}
	index, err := readIndexGitlinks(a.git)
	if err != nil {
		return err
	}
	for _, path := range expected {
		if index[path] != successful[path] {
			return fmt.Errorf("staged gitlink %q does not match its synchronized child HEAD; refusing grouped commit", path)
		}
	}
	return nil
}

func verifyRawDiffGitlinks(output string, expected []string) error {
	if output == "" {
		return fmt.Errorf("grouped gitlink changes are empty")
	}
	records := strings.Split(output, "\x00")
	if records[len(records)-1] != "" {
		return fmt.Errorf("inspect grouped gitlink modes: malformed raw diff output")
	}
	actual := make([]string, 0, len(expected))
	for index := 0; index < len(records)-1; {
		metadata := records[index]
		index++
		if !strings.HasPrefix(metadata, ":") {
			return fmt.Errorf("inspect grouped gitlink modes: malformed raw diff record")
		}
		fields := strings.Fields(metadata[1:])
		if len(fields) != 5 || fields[0] != "160000" || fields[1] != "160000" {
			return fmt.Errorf("grouped recording contains a non gitlink change; refusing commit")
		}
		if fields[4][0] == 'R' || fields[4][0] == 'C' {
			return fmt.Errorf("grouped recording unexpectedly contains a renamed gitlink")
		}
		if index >= len(records)-1 || records[index] == "" {
			return fmt.Errorf("inspect grouped gitlink modes: incomplete raw diff record")
		}
		actual = append(actual, records[index])
		index++
	}
	if !sameStrings(actual, expected) {
		return fmt.Errorf("grouped gitlink paths differ from the validated child results")
	}
	return nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy := append([]string(nil), left...)
	rightCopy := append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] {
			return false
		}
	}
	return true
}

func (a *App) syncParentFollowup(useMerge bool) error {
	a.say("Synchronizing the grouped parent gitlink commit...")
	upstream, err := a.upstream()
	if err != nil {
		return err
	}
	if err := a.checkedStream("fetch parent upstream", "fetch", "--", upstream.remote); err != nil {
		return err
	}
	return a.reconcileHistory(upstream.name, upstream, useMerge, "parent upstream")
}

func (a *App) validateRecursiveCheckoutPaths(selected []submoduleEntry, protection projectionProtection) error {
	root, err := canonicalDirectory(a.cfg.VaultPath)
	if err != nil {
		return err
	}
	visited := make(map[string]struct{})
	for _, entry := range selected {
		if err := a.checkSubmodulePath(root, entry.path, protection); err != nil {
			return fmt.Errorf("submodule %q recursive checkout: %w", entry.path, err)
		}
		if err := a.validateExistingRegisteredSubmoduleIdentity(entry); err != nil {
			return fmt.Errorf("submodule %q recursive checkout: %w", entry.path, err)
		}
		child, childRoot, err := a.submoduleApp(entry.path)
		if err != nil {
			return fmt.Errorf("submodule %q recursive checkout: %w", entry.path, err)
		}
		if err := a.validateInitializedSubmoduleTree(root, child, childRoot, protection, visited); err != nil {
			return fmt.Errorf("submodule %q recursive checkout: %w", entry.path, err)
		}
	}
	return nil
}

func (a *App) validateInitializedSubmoduleTree(vaultRoot string, parent *App, parentRoot string, protection projectionProtection, visited map[string]struct{}) error {
	canonicalParent, err := canonicalDirectory(parentRoot)
	if err != nil {
		return err
	}
	if _, seen := visited[canonicalParent]; seen {
		return nil
	}
	visited[canonicalParent] = struct{}{}
	inside, err := parent.git.capture("rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if inside.exitCode != 0 {
		return fmt.Errorf("selected submodule worktree is not initialized")
	}
	actual, err := canonicalDirectory(strings.TrimSpace(inside.stdout))
	if err != nil {
		return err
	}
	if actual != canonicalParent {
		return fmt.Errorf("submodule worktree resolves to %q, expected %q", actual, canonicalParent)
	}
	paths, err := loadSubmodulePaths(parent.git)
	if err != nil {
		return fmt.Errorf("inspect nested submodules: %w", err)
	}
	// The final recursive --init checkout can initialize nested entries that
	// have no local URL registration yet, so validate every nested gitlink.
	for _, path := range paths {
		child, childRoot, err := parent.submoduleApp(path)
		if err != nil {
			return fmt.Errorf("nested submodule %q: %w", path, err)
		}
		relative, err := filepath.Rel(vaultRoot, childRoot)
		if err != nil {
			return fmt.Errorf("resolve nested submodule %q path: %w", path, err)
		}
		if err := a.checkSubmodulePath(vaultRoot, filepath.ToSlash(relative), protection); err != nil {
			return fmt.Errorf("nested submodule %q: %w", path, err)
		}
		info, err := os.Stat(childRoot)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("inspect nested submodule %q: %w", path, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("nested submodule %q worktree path is not a directory", path)
		}
		inside, err := child.git.capture("rev-parse", "--show-toplevel")
		if err != nil {
			return err
		}
		if inside.exitCode != 0 {
			continue
		}
		actual, err := canonicalDirectory(strings.TrimSpace(inside.stdout))
		if err != nil {
			return err
		}
		if actual == canonicalParent {
			continue
		}
		if actual != childRoot {
			return fmt.Errorf("nested submodule %q worktree resolves to %q, expected %q", path, actual, childRoot)
		}
		if err := a.validateInitializedSubmoduleTree(vaultRoot, child, childRoot, protection, visited); err != nil {
			return fmt.Errorf("nested submodule %q: %w", path, err)
		}
	}
	return nil
}

func (a *App) continueOrAbortSubmodule(path, action string) error {
	if err := a.ensureClientWorktree(); err != nil {
		return err
	}
	entries, _, err := loadDirectSubmodules(a.git)
	if err != nil {
		return err
	}
	var entry *submoduleEntry
	for index := range entries {
		if entries[index].path == path {
			entry = &entries[index]
			break
		}
	}
	if entry == nil {
		return fmt.Errorf("%q is not a registered direct submodule path", path)
	}
	child, childRoot, err := a.submoduleApp(path)
	if err != nil {
		return err
	}
	inside, err := child.captureChecked("inspect submodule worktree", "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("submodule %q is not initialized: %w", path, err)
	}
	actual, err := canonicalDirectory(inside)
	if err != nil || actual != childRoot {
		return fmt.Errorf("submodule %q worktree does not resolve to its registered path", path)
	}
	protection, err := a.loadProjectionProtection()
	if err != nil {
		return err
	}
	root, err := canonicalDirectory(a.cfg.VaultPath)
	if err != nil {
		return err
	}
	if err := a.checkSubmodulePath(root, path, protection); err != nil {
		return err
	}
	validateIdentity := func() error { return a.validateRegisteredSubmoduleIdentity(*entry) }
	if err := validateIdentity(); err != nil {
		return err
	}
	operation, err := child.detectOperation()
	if err != nil {
		return err
	}
	if !operation.rebase && !operation.merge {
		return fmt.Errorf("submodule %q has no rebase or merge in progress", path)
	}
	if action == "abort" {
		if err := validateIdentity(); err != nil {
			return err
		}
		if operation.rebase {
			child.say("Aborting submodule rebase...")
			return child.checkedStream("abort submodule rebase", "rebase", "--abort")
		}
		child.say("Aborting submodule merge...")
		return child.checkedStream("abort submodule merge", "merge", "--abort")
	}
	if action != "continue" {
		return fmt.Errorf("internal error: unsupported submodule recovery action %q", action)
	}
	if err := validateIdentity(); err != nil {
		return err
	}
	if err := a.checkSubmodulePushEndpoints(child); err != nil {
		return err
	}
	child.say("Staging resolved submodule files...")
	if err := child.checkedStream("stage resolved submodule files", "add", "-A"); err != nil {
		return err
	}
	conflicts, err := child.conflictedFiles()
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		child.reportConflicts(conflicts)
		return &userError{"vaultctl sync cannot continue while submodule conflicts remain unresolved."}
	}
	if operation.rebase {
		if err := validateIdentity(); err != nil {
			return err
		}
		child.say("Continuing submodule rebase...")
		return child.runSyncCommand("continue submodule rebase", "rebase", "--continue")
	}
	if err := validateIdentity(); err != nil {
		return err
	}
	child.say("Completing submodule merge...")
	return child.runSyncCommand("complete submodule merge", "commit", "--no-edit")
}
