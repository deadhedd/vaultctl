package vaultctl

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type syncState int

const (
	syncEqual syncState = iota
	syncAhead
	syncBehind
	syncDiverged
)

func (s syncState) String() string {
	switch s {
	case syncEqual:
		return "equal"
	case syncAhead:
		return "ahead"
	case syncBehind:
		return "behind"
	case syncDiverged:
		return "diverged"
	default:
		return "unknown"
	}
}

func classifyCounts(output string) (syncState, error) {
	fields := strings.Fields(output)
	if len(fields) != 2 {
		return 0, fmt.Errorf("expected two revision counts, got %q", output)
	}
	ahead, err := strconv.Atoi(fields[0])
	if err != nil || ahead < 0 {
		return 0, fmt.Errorf("invalid local revision count %q", fields[0])
	}
	behind, err := strconv.Atoi(fields[1])
	if err != nil || behind < 0 {
		return 0, fmt.Errorf("invalid upstream revision count %q", fields[1])
	}
	switch {
	case ahead == 0 && behind == 0:
		return syncEqual, nil
	case ahead > 0 && behind == 0:
		return syncAhead, nil
	case ahead == 0 && behind > 0:
		return syncBehind, nil
	default:
		return syncDiverged, nil
	}
}

type upstreamInfo struct {
	branch   string
	name     string
	remote   string
	mergeRef string
}

type operationState struct {
	rebase     bool
	merge      bool
	cherryPick bool
	revert     bool
	sequencer  bool
}

var unfinishedGitOperationPaths = []string{
	"rebase-merge",
	"rebase-apply",
	"MERGE_HEAD",
	"CHERRY_PICK_HEAD",
	"REVERT_HEAD",
	"sequencer",
}

func (o operationState) any() bool {
	return o.rebase || o.merge || o.cherryPick || o.revert || o.sequencer
}

func (o operationState) description() string {
	var names []string
	if o.rebase {
		names = append(names, "rebase")
	}
	if o.merge {
		names = append(names, "merge")
	}
	if o.cherryPick {
		names = append(names, "cherry-pick")
	}
	if o.revert {
		names = append(names, "revert")
	}
	if o.sequencer {
		names = append(names, "sequencer")
	}
	return strings.Join(names, ", ")
}

func (a *App) sync(args []string) error {
	if a.cfg.Mode != ModeClient {
		return &userError{serverSyncMessage}
	}
	if len(args) == 0 {
		return a.syncStart(false)
	}
	switch args[0] {
	case "--continue":
		return a.syncContinue()
	case "--abort":
		return a.syncAbort()
	case "--merge":
		return a.syncStart(true)
	default:
		return fmt.Errorf("internal error: unsupported sync option %q", args[0])
	}
}

func (a *App) ensureClientWorktree() error {
	inside, err := a.captureChecked("inspect client worktree", "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return err
	}
	if inside != "true" {
		return fmt.Errorf("configured client vault is not a normal Git worktree")
	}
	return nil
}

func (a *App) upstream() (upstreamInfo, error) {
	branchResult, err := a.git.capture("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return upstreamInfo{}, err
	}
	if branchResult.exitCode != 0 {
		return upstreamInfo{}, fmt.Errorf("the client vault is on a detached HEAD; check out a branch with an upstream before syncing")
	}
	branch := strings.TrimSpace(branchResult.stdout)

	upstreamResult, err := a.git.capture("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		return upstreamInfo{}, err
	}
	if upstreamResult.exitCode != 0 {
		return upstreamInfo{}, fmt.Errorf("current branch %q has no upstream tracking branch; configure one explicitly before syncing", branch)
	}
	name := strings.TrimSpace(upstreamResult.stdout)

	remoteResult, err := a.git.capture("config", "--get", "branch."+branch+".remote")
	if err != nil {
		return upstreamInfo{}, err
	}
	mergeResult, err := a.git.capture("config", "--get", "branch."+branch+".merge")
	if err != nil {
		return upstreamInfo{}, err
	}
	if remoteResult.exitCode != 0 || strings.TrimSpace(remoteResult.stdout) == "" ||
		mergeResult.exitCode != 0 || strings.TrimSpace(mergeResult.stdout) == "" {
		return upstreamInfo{}, fmt.Errorf("upstream configuration for branch %q is incomplete; configure it explicitly before syncing", branch)
	}
	return upstreamInfo{
		branch:   branch,
		name:     name,
		remote:   strings.TrimSpace(remoteResult.stdout),
		mergeRef: strings.TrimSpace(mergeResult.stdout),
	}, nil
}

func (a *App) syncStart(useMerge bool) error {
	if err := a.ensureClientWorktree(); err != nil {
		return err
	}
	operation, err := a.detectOperation()
	if err != nil {
		return err
	}
	if operation.any() {
		return fmt.Errorf("an unfinished %s is already in progress; use vaultctl status, vaultctl sync --continue, or vaultctl sync --abort", operation.description())
	}
	conflicts, err := a.conflictedFiles()
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		a.reportConflicts(conflicts)
		return &userError{"vaultctl sync cannot start while the index contains unresolved conflicts."}
	}

	upstream, err := a.upstream()
	if err != nil {
		return err
	}
	a.say("Upstream: %s", upstream.name)
	if err := a.refreshExternalDocs(); err != nil {
		return err
	}

	status, err := a.captureChecked("inspect local changes", "status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" {
		gitlinks, err := a.syncAutosaveGitlinkPaths()
		if err != nil {
			return err
		}
		message, err := a.commitMessage()
		if err != nil {
			return err
		}
		a.say("Saving local changes before contacting the remote...")
		if err := a.stageAndCommitExcluding(message, false, gitlinks); err != nil {
			return err
		}
		remaining, err := a.git.capture("status", "--porcelain=v1", "-z")
		if err != nil {
			return err
		}
		if remaining.exitCode != 0 {
			return fmt.Errorf("verify saved local changes failed (Git exited with status %d): %s", remaining.exitCode, strings.TrimSpace(remaining.stderr))
		}
		remainingPaths, err := porcelainPaths(remaining.stdout)
		if err != nil {
			return fmt.Errorf("verify saved local changes: %w", err)
		}
		if !onlyGitlinkPaths(remainingPaths, gitlinks) {
			return fmt.Errorf("local changes remain after staging and committing; refusing to contact the remote")
		}
	}

	a.say("Fetching from %s...", upstream.remote)
	if err := a.checkedStream("fetch upstream", "fetch", "--", upstream.remote); err != nil {
		return err
	}

	state, err := a.currentSyncState()
	if err != nil {
		return err
	}
	a.say("Local and upstream histories are %s.", state)
	switch state {
	case syncEqual:
		a.say("The parent vault history is already synchronized.")
		return a.finishNormalSync()
	case syncBehind:
		a.say("Fast-forwarding to %s...", upstream.name)
		if err := a.checkedStream("fast-forward", "merge", "--ff-only", upstream.name); err != nil {
			return err
		}
		return a.finishNormalSync()
	case syncAhead:
		if err := a.push(upstream); err != nil {
			return err
		}
		return a.finishNormalSync()
	case syncDiverged:
		if useMerge {
			a.say("Histories diverged; merging %s...", upstream.name)
			if err := a.runSyncCommand("merge upstream", "merge", upstream.name); err != nil {
				return err
			}
		} else {
			a.say("Histories diverged; rebasing local commits onto %s...", upstream.name)
			if err := a.runSyncCommand("rebase onto upstream", "rebase", upstream.name); err != nil {
				return err
			}
		}
		if err := a.push(upstream); err != nil {
			return err
		}
		return a.finishNormalSync()
	default:
		return fmt.Errorf("internal error: unknown synchronization state")
	}
}

func (a *App) syncAutosaveGitlinkPaths() (map[string]struct{}, error) {
	index, err := a.git.capture("ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	if index.exitCode != 0 {
		return nil, fmt.Errorf("inspect indexed gitlinks failed (Git exited with status %d): %s", index.exitCode, strings.TrimSpace(index.stderr))
	}
	paths, err := gitlinkPathsFromIndex(index.stdout)
	if err != nil {
		return nil, err
	}

	staged, err := a.git.capture("diff", "--cached", "--raw", "-z")
	if err != nil {
		return nil, err
	}
	if staged.exitCode != 0 {
		return nil, fmt.Errorf("inspect staged gitlinks failed (Git exited with status %d): %s", staged.exitCode, strings.TrimSpace(staged.stderr))
	}
	changedPaths, err := gitlinkPathsFromRawDiff(staged.stdout)
	if err != nil {
		return nil, err
	}
	for path := range changedPaths {
		paths[path] = struct{}{}
	}

	untracked, err := a.git.capture("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	if untracked.exitCode != 0 {
		return nil, fmt.Errorf("inspect untracked repositories failed (Git exited with status %d): %s", untracked.exitCode, strings.TrimSpace(untracked.stderr))
	}
	untrackedPaths, err := gitlinkPathsFromUntracked(untracked.stdout)
	if err != nil {
		return nil, err
	}
	for path := range untrackedPaths {
		paths[path] = struct{}{}
	}
	return paths, nil
}

// Git reports an untracked embedded repository as a directory ending in "/".
// Excluding that directory prevents autosave from recording it as a gitlink.
func gitlinkPathsFromUntracked(output string) (map[string]struct{}, error) {
	paths := make(map[string]struct{})
	if output == "" {
		return paths, nil
	}
	records := strings.Split(output, "\x00")
	if records[len(records)-1] != "" {
		return nil, fmt.Errorf("inspect untracked repositories: malformed `git ls-files --others -z` output")
	}
	for _, record := range records[:len(records)-1] {
		if record == "" {
			return nil, fmt.Errorf("inspect untracked repositories: empty path in `git ls-files --others -z` output")
		}
		if strings.HasSuffix(record, "/") {
			path := strings.TrimSuffix(record, "/")
			if path == "" {
				return nil, fmt.Errorf("inspect untracked repositories: empty repository path in `git ls-files --others -z` output")
			}
			paths[path] = struct{}{}
		}
	}
	return paths, nil
}

func gitlinkPathsFromIndex(output string) (map[string]struct{}, error) {
	paths := make(map[string]struct{})
	if output == "" {
		return paths, nil
	}
	records := strings.Split(output, "\x00")
	for i, record := range records {
		if record == "" && i == len(records)-1 {
			continue
		}
		tab := strings.IndexByte(record, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("inspect indexed gitlinks: malformed `git ls-files --stage -z` output")
		}
		fields := strings.Fields(record[:tab])
		path := record[tab+1:]
		if len(fields) != 3 || path == "" {
			return nil, fmt.Errorf("inspect indexed gitlinks: malformed `git ls-files --stage -z` record")
		}
		if fields[0] == "160000" {
			paths[path] = struct{}{}
		}
	}
	return paths, nil
}

func gitlinkPathsFromRawDiff(output string) (map[string]struct{}, error) {
	paths := make(map[string]struct{})
	if output == "" {
		return paths, nil
	}
	records := strings.Split(output, "\x00")
	if records[len(records)-1] != "" {
		return nil, fmt.Errorf("inspect staged gitlinks: malformed `git diff --cached --raw -z` output")
	}
	for i := 0; i < len(records)-1; {
		metadata := records[i]
		i++
		if !strings.HasPrefix(metadata, ":") {
			return nil, fmt.Errorf("inspect staged gitlinks: malformed `git diff --cached --raw -z` record")
		}
		fields := strings.Fields(metadata[1:])
		if len(fields) != 5 || fields[4] == "" {
			return nil, fmt.Errorf("inspect staged gitlinks: malformed `git diff --cached --raw -z` metadata")
		}
		pathCount := 1
		if fields[4][0] == 'R' || fields[4][0] == 'C' {
			pathCount = 2
		}
		if i+pathCount > len(records)-1 {
			return nil, fmt.Errorf("inspect staged gitlinks: incomplete `git diff --cached --raw -z` record")
		}
		if fields[0] == "160000" || fields[1] == "160000" {
			for _, path := range records[i : i+pathCount] {
				if path == "" {
					return nil, fmt.Errorf("inspect staged gitlinks: empty path in `git diff --cached --raw -z` record")
				}
				paths[path] = struct{}{}
			}
		}
		i += pathCount
	}
	return paths, nil
}

func porcelainPaths(output string) ([]string, error) {
	if output == "" {
		return nil, nil
	}
	records := strings.Split(output, "\x00")
	if records[len(records)-1] != "" {
		return nil, fmt.Errorf("malformed `git status --porcelain=v1 -z` output")
	}
	var paths []string
	for i := 0; i < len(records)-1; {
		record := records[i]
		i++
		if len(record) < 4 || record[2] != ' ' {
			return nil, fmt.Errorf("malformed `git status --porcelain=v1 -z` record")
		}
		paths = append(paths, record[3:])
		if strings.ContainsAny(record[:2], "RC") {
			if i >= len(records)-1 {
				return nil, fmt.Errorf("incomplete rename in `git status --porcelain=v1 -z` output")
			}
			paths = append(paths, records[i])
			i++
		}
	}
	return paths, nil
}

func onlyGitlinkPaths(paths []string, gitlinks map[string]struct{}) bool {
	for _, path := range paths {
		if _, ok := gitlinks[path]; ok {
			continue
		}
		contained := false
		for gitlink := range gitlinks {
			if strings.HasPrefix(path, gitlink+"/") {
				contained = true
				break
			}
		}
		if !contained {
			return false
		}
	}
	return true
}

func sortedGitlinkPaths(paths map[string]struct{}) []string {
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func (a *App) finishNormalSync() error {
	a.say("Parent vault synchronization succeeded.")
	a.say("Updating selected submodules at their recorded commits...")
	code, err := a.git.stream("submodule", "update", "--init", "--recursive", "--checkout")
	if err != nil {
		fmt.Fprintln(a.stderr, "[vaultctl] Parent synchronization succeeded, but submodule state may be incomplete.")
		return err
	}
	if code != 0 {
		fmt.Fprintln(a.stderr, "[vaultctl] Parent synchronization succeeded, but submodule state may be incomplete.")
		return fmt.Errorf("submodule update failed (Git exited with status %d)", code)
	}
	a.say("Submodule initialization and update succeeded.")
	a.say("Synchronization completed successfully.")
	return nil
}

func (a *App) currentSyncState() (syncState, error) {
	counts, err := a.captureChecked("compare local and upstream histories", "rev-list", "--left-right", "--count", "HEAD...@{u}")
	if err != nil {
		return 0, err
	}
	return classifyCounts(counts)
}

func (a *App) push(upstream upstreamInfo) error {
	a.say("Pushing %s to %s...", upstream.branch, upstream.name)
	refspec := "HEAD:" + upstream.mergeRef
	if err := a.checkedStream("push to upstream", "push", "--", upstream.remote, refspec); err != nil {
		return err
	}
	return nil
}

func (a *App) gitPathExists(name string) (bool, error) {
	path, err := a.captureChecked("locate Git state", "rev-parse", "--git-path", name)
	if err != nil {
		return false, err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.cfg.VaultPath, path)
	}
	_, err = os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("inspect Git state %s: %w", name, err)
}

func (a *App) detectOperation() (operationState, error) {
	var state operationState
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		exists, err := a.gitPathExists(name)
		if err != nil {
			return operationState{}, err
		}
		state.rebase = state.rebase || exists
	}
	var err error
	state.merge, err = a.gitPathExists("MERGE_HEAD")
	if err != nil {
		return operationState{}, err
	}
	state.cherryPick, err = a.gitPathExists("CHERRY_PICK_HEAD")
	if err != nil {
		return operationState{}, err
	}
	state.revert, err = a.gitPathExists("REVERT_HEAD")
	if err != nil {
		return operationState{}, err
	}
	state.sequencer, err = a.gitPathExists("sequencer")
	if err != nil {
		return operationState{}, err
	}
	return state, nil
}

func (a *App) conflictedFiles() ([]string, error) {
	result, err := a.git.capture("diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	if result.exitCode != 0 {
		return nil, fmt.Errorf("inspect conflicts failed (Git exited with status %d): %s", result.exitCode, strings.TrimSpace(result.stderr))
	}
	output := strings.TrimSpace(result.stdout)
	if output == "" {
		return nil, nil
	}
	return strings.Split(output, "\n"), nil
}

func (a *App) reportConflicts(files []string) {
	fmt.Fprintln(a.stderr, "[vaultctl] Git requires manual conflict resolution.")
	if len(files) > 0 {
		fmt.Fprintln(a.stderr, "[vaultctl] Conflicted files:")
		for _, file := range files {
			fmt.Fprintf(a.stderr, "  %s\n", file)
		}
	}
	fmt.Fprintln(a.stderr, "[vaultctl] Next commands:")
	fmt.Fprintln(a.stderr, "  vaultctl status")
	fmt.Fprintln(a.stderr, "  vaultctl sync --continue")
	fmt.Fprintln(a.stderr, "  vaultctl sync --abort")
}

func (a *App) runSyncCommand(action string, args ...string) error {
	code, err := a.git.stream(args...)
	if err != nil {
		return err
	}
	if code == 0 {
		return nil
	}
	files, conflictErr := a.conflictedFiles()
	if conflictErr != nil {
		return fmt.Errorf("%s failed and conflict inspection also failed: %v", action, conflictErr)
	}
	operation, operationErr := a.detectOperation()
	if operationErr != nil {
		return fmt.Errorf("%s failed and operation inspection also failed: %v", action, operationErr)
	}
	if len(files) > 0 || operation.rebase || operation.merge {
		a.reportConflicts(files)
		return &userError{"vaultctl sync stopped; resolve the conflicts, then continue or abort."}
	}
	return fmt.Errorf("%s failed (Git exited with status %d)", action, code)
}

func (a *App) syncContinue() error {
	if err := a.ensureClientWorktree(); err != nil {
		return err
	}
	operation, err := a.detectOperation()
	if err != nil {
		return err
	}
	if !operation.rebase && !operation.merge {
		if operation.cherryPick {
			return fmt.Errorf("a cherry-pick is in progress; finish it with vaultctl git -- cherry-pick --continue or abort it with vaultctl sync --abort")
		}
		return fmt.Errorf("no rebase or merge is in progress")
	}

	a.say("Staging resolved files...")
	if err := a.checkedStream("stage resolved files", "add", "-A"); err != nil {
		return err
	}
	files, err := a.conflictedFiles()
	if err != nil {
		return err
	}
	if len(files) > 0 {
		a.reportConflicts(files)
		return &userError{"vaultctl sync cannot continue while conflicts remain unresolved."}
	}

	if operation.rebase {
		a.say("Continuing rebase...")
		if err := a.runSyncCommand("continue rebase", "rebase", "--continue"); err != nil {
			return err
		}
	} else {
		a.say("Completing merge...")
		if err := a.runSyncCommand("commit merge", "commit", "--no-edit"); err != nil {
			return err
		}
	}

	upstream, err := a.upstream()
	if err != nil {
		return fmt.Errorf("Git operation completed, but the result cannot be pushed: %w", err)
	}
	if err := a.push(upstream); err != nil {
		return err
	}
	a.say("The vault is synced.")
	return nil
}

func (a *App) syncAbort() error {
	if err := a.ensureClientWorktree(); err != nil {
		return err
	}
	operation, err := a.detectOperation()
	if err != nil {
		return err
	}
	switch {
	case operation.rebase:
		a.say("Aborting rebase...")
		return a.checkedStream("abort rebase", "rebase", "--abort")
	case operation.merge:
		a.say("Aborting merge...")
		return a.checkedStream("abort merge", "merge", "--abort")
	case operation.cherryPick:
		a.say("Aborting cherry-pick...")
		return a.checkedStream("abort cherry-pick", "cherry-pick", "--abort")
	case operation.revert:
		a.say("Aborting revert...")
		return a.checkedStream("abort revert", "revert", "--abort")
	case operation.sequencer:
		return fmt.Errorf("an unfinished Git sequencer is in progress; use vaultctl git -- revert --abort or vaultctl git -- cherry-pick --abort")
	default:
		a.say("No rebase, merge, cherry-pick, or revert is in progress; nothing to abort.")
		return nil
	}
}
