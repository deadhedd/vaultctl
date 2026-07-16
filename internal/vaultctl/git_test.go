package vaultctl

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildClientGitCommand(t *testing.T) {
	cfg := Config{Mode: ModeClient, VaultPath: "/vault/Main"}
	got, err := buildGitCommand(cfg, []string{"status", "--short"}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	want := commandSpec{
		path: "git",
		args: []string{"status", "--short"},
		dir:  "/vault/Main",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
}

func TestBuildServerGitCommand(t *testing.T) {
	cfg := Config{
		Mode:     ModeServer,
		BareRepo: "/home/git/vaults/Main.git",
		Worktree: "/home/obsidian/vaults/Main",
	}
	got, err := buildGitCommand(cfg, []string{"log", "-n", "3"}, "openbsd")
	if err != nil {
		t.Fatal(err)
	}
	want := commandSpec{
		path: "git",
		args: []string{
			"--git-dir=/home/git/vaults/Main.git",
			"--work-tree=/home/obsidian/vaults/Main",
			"log", "-n", "3",
		},
		dir: "/home/obsidian/vaults/Main",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
}

func TestBuildOpenBSDServerCommandWithServiceUser(t *testing.T) {
	cfg := Config{
		Mode:      ModeServer,
		BareRepo:  "/home/git/vaults/Main.git",
		Worktree:  "/home/obsidian/vaults/Main",
		RunAsUser: "obsidian",
	}
	got, err := buildGitCommand(cfg, []string{"status"}, "openbsd")
	if err != nil {
		t.Fatal(err)
	}
	want := commandSpec{
		path: "doas",
		args: []string{
			"-u", "obsidian", "git",
			"--git-dir=/home/git/vaults/Main.git",
			"--work-tree=/home/obsidian/vaults/Main",
			"status",
		},
		dir: "/home/obsidian/vaults/Main",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
}

func TestRunAsUserRefusesUnsupportedOS(t *testing.T) {
	cfg := Config{
		Mode:      ModeServer,
		BareRepo:  "/repo",
		Worktree:  "/tree",
		RunAsUser: "obsidian",
	}
	_, err := buildGitCommand(cfg, []string{"status"}, "windows")
	if err == nil || !strings.Contains(err.Error(), "OpenBSD") {
		t.Fatalf("error = %v, want OpenBSD explanation", err)
	}
}

func TestGitArgumentsAreCopied(t *testing.T) {
	cfg := Config{Mode: ModeClient, VaultPath: "/vault"}
	args := []string{"status"}
	got, err := buildGitCommand(cfg, args, "linux")
	if err != nil {
		t.Fatal(err)
	}
	args[0] = "destroyed"
	if got.args[0] != "status" {
		t.Fatalf("command retained caller-owned argument slice: %#v", got.args)
	}
}
