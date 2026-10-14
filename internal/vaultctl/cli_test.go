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

// covers: AC-8
func TestValidateInvocation_SubmoduleRecoveryArguments(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "parent continuation remains valid", args: []string{"--continue"}},
		{name: "parent abort remains valid", args: []string{"--abort"}},
		{name: "parent merge remains valid", args: []string{"--merge"}},
		{name: "child continuation accepts a path", args: []string{"--continue", "--submodule", "Projects/child"}},
		{name: "child abort accepts a path", args: []string{"--abort", "--submodule", "Projects/child"}},
		{name: "merge does not accept a child path", args: []string{"--merge", "--submodule", "Projects/child"}, wantErr: true},
		{name: "child path must follow the recovery action", args: []string{"--submodule", "Projects/child", "--continue"}, wantErr: true},
		{name: "child path cannot be empty", args: []string{"--continue", "--submodule", ""}, wantErr: true},
		{name: "child path is required", args: []string{"--continue", "--submodule"}, wantErr: true},
		{name: "extra child arguments are rejected", args: []string{"--abort", "--submodule", "Projects/child", "extra"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"sync"}, tt.args...)
			inv, err := parseCLI(args)
			if err != nil {
				t.Fatalf("parseCLI(%q): %v", args, err)
			}
			err = validateInvocation(inv)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateInvocation(%#v) error = %v, want error %t", inv, err, tt.wantErr)
			}
		})
	}
}
