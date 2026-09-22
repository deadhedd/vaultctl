package vaultctl

import (
	"strings"
	"testing"
)

// The sync abort help text must describe every operation that the command can
// safely recognize, including revert state added to the sync boundary.
func TestHelpTextDescribesRevertAbort(t *testing.T) {
	if !strings.Contains(helpText, "sync --abort    abort an in-progress rebase, merge, cherry-pick, or revert") {
		t.Fatalf("helpText does not describe revert abort support")
	}
}
