package vaultctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadClientConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := "{\n  \"mode\": \"CLIENT\",\n  \"vault_path\": \"/vault\"\n}\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, usedPath, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if usedPath != path {
		t.Fatalf("used path = %q, want %q", usedPath, path)
	}
	if cfg.Mode != ModeClient || cfg.VaultPath != "/vault" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestLoadServerConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := "{\n" +
		"  \"mode\": \"server\",\n" +
		"  \"bare_repo\": \"/srv/Main.git\",\n" +
		"  \"worktree\": \"/srv/Main\",\n" +
		"  \"run_as_user\": \"obsidian\"\n" +
		"}\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Mode != ModeServer || cfg.BareRepo != "/srv/Main.git" ||
		cfg.Worktree != "/srv/Main" || cfg.RunAsUser != "obsidian" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestConfigRejectsUnknownAndModeSpecificFields(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "unknown field",
			json: "{\"mode\":\"client\",\"vault_path\":\"/vault\",\"remote\":\"origin\"}",
			want: "unknown field",
		},
		{
			name: "server field in client mode",
			json: "{\"mode\":\"client\",\"vault_path\":\"/vault\",\"worktree\":\"/wrong\"}",
			want: "server-mode fields",
		},
		{
			name: "client field in server mode",
			json: "{\"mode\":\"server\",\"bare_repo\":\"/repo\",\"worktree\":\"/tree\",\"vault_path\":\"/wrong\"}",
			want: "client-mode field",
		},
		{
			name: "missing client path",
			json: "{\"mode\":\"client\"}",
			want: "vault_path is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tt.json), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err := loadConfig(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("loadConfig error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestMissingConfigHasActionableError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	_, _, err := loadConfig(path)
	if err == nil {
		t.Fatal("loadConfig unexpectedly succeeded")
	}
	if !strings.Contains(err.Error(), "--config") {
		t.Fatalf("error is not actionable: %v", err)
	}
}
