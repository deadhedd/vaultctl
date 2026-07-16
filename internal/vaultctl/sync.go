package vaultctl

import (
	"fmt"
	"os"
	"path/filepath"
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
}

func (o operationState) any() bool {
	return o.rebase || o.merge || o.cherryPick
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

	status, err := a.captureChecked("inspect local changes", "status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" {
		message, err := a.commitMessage()
		if err != nil {
			return err
		}
		a.say("Saving local changes before contacting the remote...")
		if err := a.stageAndCommit(message, false); err != nil {
			return err
		}
		remaining, err := a.captureChecked("verify saved local changes", "status", "--porcelain")
		if err != nil {
			return err
		}
		if remaining != "" {
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
		a.say("The vault is already synced.")
		return nil
	case syncBehind:
		a.say("Fast-forwarding to %s...", upstream.name)
		return a.checkedStream("fast-forward", "merge", "--ff-only", upstream.name)
	case syncAhead:
		return a.push(upstream)
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
		return a.push(upstream)
	default:
		return fmt.Errorf("internal error: unknown synchronization state")
	}
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
	a.say("The vault is synced.")
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
	return a.push(upstream)
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
	default:
		a.say("No rebase, merge, or cherry-pick is in progress; nothing to abort.")
		return nil
	}
}
