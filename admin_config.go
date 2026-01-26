package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml"
)

const (
	defaultAdminSessionExpirationSeconds = 900
)

var adminConfigTemplate = `# Administrative control panel (hidden by default).
# This file is auto-generated when goPool starts if it is missing.
# - Set enabled = true and change the credentials before opening /admin.
# - The admin UI can edit config.toml, rewrite the active config, and request a reboot.
# - Major actions such as rebooting require re-entering the password and typing REBOOT.
# Keep this file off version control and serve the UI only on trusted networks.
enabled = false
username = "admin"
password = "%s"
session_expiration_seconds = 900
`

type adminFileConfig struct {
	Enabled                  bool   `toml:"enabled"`
	Username                 string `toml:"username"`
	Password                 string `toml:"password"`
	SessionExpirationSeconds int    `toml:"session_expiration_seconds"`
}

func (cfg adminFileConfig) sessionDuration() time.Duration {
	if cfg.SessionExpirationSeconds <= 0 {
		return time.Duration(defaultAdminSessionExpirationSeconds) * time.Second
	}
	return time.Duration(cfg.SessionExpirationSeconds) * time.Second
}

func ensureAdminConfigFile(dataDir string) (string, error) {
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	configDir := filepath.Join(dataDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", configDir, err)
	}

	adminPath := filepath.Join(configDir, "admin.toml")
	if _, err := os.Stat(adminPath); errors.Is(err, os.ErrNotExist) {
		password, err := generateAdminPassword()
		if err != nil {
			return "", fmt.Errorf("generate admin password: %w", err)
		}
		if err := os.WriteFile(adminPath, []byte(fmt.Sprintf(adminConfigTemplate, password)), 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", adminPath, err)
		}
		logger.Info("generated admin config template (disabled)", "path", adminPath)
	} else if err != nil {
		return "", fmt.Errorf("stat %s: %w", adminPath, err)
	}

	return adminPath, nil
}

func loadAdminConfigFile(path string) (adminFileConfig, error) {
	var cfg adminFileConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.Username = strings.TrimSpace(cfg.Username)
	cfg.Password = strings.TrimSpace(cfg.Password)
	if cfg.SessionExpirationSeconds <= 0 {
		cfg.SessionExpirationSeconds = defaultAdminSessionExpirationSeconds
	}
	return cfg, nil
}

func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	tmpFile, err := os.CreateTemp(dir, "config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	tmpPath = tmpFile.Name()

	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmpPath, path, err)
	}
	tmpPath = ""
	return nil
}

func generateAdminPassword() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
