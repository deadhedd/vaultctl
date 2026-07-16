package vaultctl

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestParseCLI(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want invocation
	}{
		{
			name: "default help",
			want: invocation{command: "help"},
		},
		{
			name: "explicit config",
			args: []string{"--config", "/tmp/vault.json", "status"},
			want: invocation{configPath: "/tmp/vault.json", command: "status"},
		},
		{
			name: "equals config",
			args: []string{"--config=/tmp/vault.json", "sync", "--merge"},
			want: invocation{configPath: "/tmp/vault.json", command: "sync", args: []string{"--merge"}},
		},
		{
			name: "raw git separator",
			args: []string{"git", "--", "status", "--short"},
			want: invocation{command: "git", args: []string{"--", "status", "--short"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCLI(tt.args)
			if err != nil {
				t.Fatalf("parseCLI: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseCLI() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestInvocationValidation(t *testing.T) {
	valid := []invocation{
		{command: "status"},
		{command: "sync"},
		{command: "sync", args: []string{"--continue"}},
		{command: "sync", args: []string{"--abort"}},
		{command: "sync", args: []string{"--merge"}},
		{command: "git", args: []string{"--", "status"}},
		{command: "git", args: []string{"status"}},
	}
	for _, inv := range valid {
		if err := validateInvocation(inv); err != nil {
			t.Errorf("validateInvocation(%#v): %v", inv, err)
		}
	}

	invalid := []invocation{
		{command: "unknown"},
		{command: "status", args: []string{"extra"}},
		{command: "sync", args: []string{"--merge", "--continue"}},
		{command: "sync", args: []string{"--force"}},
		{command: "git"},
		{command: "git", args: []string{"--"}},
	}
	for _, inv := range invalid {
		if err := validateInvocation(inv); err == nil {
			t.Errorf("validateInvocation(%#v) unexpectedly succeeded", inv)
		}
	}
}

func TestHelpDoesNotRequireConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := RunCLI(nil, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("RunCLI help status = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "sync --continue") {
		t.Fatalf("help output missing sync commands:\n%s", stdout.String())
	}
}

func TestBadGlobalOptionIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := RunCLI([]string{"--bogus"}, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("status = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown global option") {
		t.Fatalf("unexpected error: %s", stderr.String())
	}
}
