package vaultctl

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	ModeClient = "client"
	ModeServer = "server"
)

// Config describes either a normal client clone or the server's split
// bare-repository/worktree layout.
type Config struct {
	Mode      string `json:"mode"`
	VaultPath string `json:"vault_path,omitempty"`
	BareRepo  string `json:"bare_repo,omitempty"`
	Worktree  string `json:"worktree,omitempty"`
	RunAsUser string `json:"run_as_user,omitempty"`
}

func defaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user configuration directory: %w", err)
	}
	return filepath.Join(dir, "vaultctl", "config.json"), nil
}

func loadConfig(explicitPath string) (Config, string, error) {
	path := explicitPath
	if path == "" {
		var err error
		path, err = defaultConfigPath()
		if err != nil {
			return Config{}, "", err
		}
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, path, fmt.Errorf("configuration file not found at %s (use --config to select one)", path)
		}
		return Config{}, path, fmt.Errorf("open configuration %s: %w", path, err)
	}
	defer f.Close()

	var cfg Config
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, path, fmt.Errorf("parse configuration %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("more than one JSON value")
		}
		return Config{}, path, fmt.Errorf("parse configuration %s: %w", path, err)
	}

	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	if err := cfg.validate(); err != nil {
		return Config{}, path, fmt.Errorf("invalid configuration %s: %w", path, err)
	}
	return cfg, path, nil
}

func (c Config) validate() error {
	switch c.Mode {
	case ModeClient:
		if c.VaultPath == "" {
			return fmt.Errorf("vault_path is required in client mode")
		}
		if c.BareRepo != "" || c.Worktree != "" || c.RunAsUser != "" {
			return fmt.Errorf("bare_repo, worktree, and run_as_user are server-mode fields")
		}
	case ModeServer:
		if c.BareRepo == "" {
			return fmt.Errorf("bare_repo is required in server mode")
		}
		if c.Worktree == "" {
			return fmt.Errorf("worktree is required in server mode")
		}
		if c.VaultPath != "" {
			return fmt.Errorf("vault_path is a client-mode field")
		}
	default:
		return fmt.Errorf("mode must be %q or %q", ModeClient, ModeServer)
	}
	return nil
}
