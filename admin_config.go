package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml"
)

const (
	defaultAdminSessionExpirationSeconds = 900
	minAdminPasswordLen                  = 16
)

var adminConfigTemplate = `# Administrative control panel (hidden by default).
# This file is auto-generated when goPool starts if it is missing.
# - Set enabled = true and change the credentials before opening /admin.
# - The admin UI edits live (in-memory) settings and can request a reboot.
# - Major actions such as rebooting require re-entering the password and typing REBOOT.
# - password_sha256 is used for authentication; password can be cleared after first login.
# - On startup, if password is set, goPool verifies/refreshes password_sha256 to match it.
# - After a successful admin login, goPool clears password and keeps password_sha256.
# Keep this file off version control and serve the UI only on trusted networks.
enabled = %t
username = %s
password = %s
password_sha256 = %s
session_expiration_seconds = %d
`

type adminFileConfig struct {
	Enabled                  bool   `toml:"enabled"`
	Username                 string `toml:"username"`
	Password                 string `toml:"password"`
	PasswordSHA256           string `toml:"password_sha256"`
	SessionExpirationSeconds int    `toml:"session_expiration_seconds"`
}

func (cfg adminFileConfig) sessionDuration() time.Duration {
	if cfg.SessionExpirationSeconds <= 0 {
		return time.Duration(defaultAdminSessionExpirationSeconds) * time.Second
	}
	return time.Duration(cfg.SessionExpirationSeconds) * time.Second
}

func renderAdminConfig(cfg adminFileConfig) string {
	username := strings.TrimSpace(cfg.Username)
	if username == "" {
		username = "admin"
	}
	password := strings.TrimSpace(cfg.Password)
	passwordHash := strings.TrimSpace(cfg.PasswordSHA256)
	return fmt.Sprintf(
		adminConfigTemplate,
		cfg.Enabled,
		strconv.Quote(username),
		strconv.Quote(password),
		strconv.Quote(passwordHash),
		cfg.SessionExpirationSeconds,
	)
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
		cfg := adminFileConfig{
			Enabled:                  false,
			Username:                 "admin",
			Password:                 password,
			PasswordSHA256:           adminPasswordHash(password),
			SessionExpirationSeconds: defaultAdminSessionExpirationSeconds,
		}
		if err := os.WriteFile(adminPath, []byte(renderAdminConfig(cfg)), 0o600); err != nil {
			return "", fmt.Errorf("write %s: %w", adminPath, err)
		}
		logger.Info("generated admin config template (disabled)", "path", adminPath)
	} else if err != nil {
		return "", fmt.Errorf("stat %s: %w", adminPath, err)
	} else {
		cfg, err := loadAdminConfigFile(adminPath)
		if err != nil {
			return "", err
		}
		needsRewrite := false
		if cfg.Password != "" {
			hash := adminPasswordHash(cfg.Password)
			if cfg.PasswordSHA256 == "" || !strings.EqualFold(cfg.PasswordSHA256, hash) {
				cfg.PasswordSHA256 = hash
				needsRewrite = true
			}
		}
		if cfg.Password == "" && cfg.PasswordSHA256 == "" {
			password, err := generateAdminPassword()
			if err != nil {
				return "", fmt.Errorf("generate admin password: %w", err)
			}
			cfg.Password = password
			cfg.PasswordSHA256 = adminPasswordHash(password)
			needsRewrite = true
			logger.Warn("admin password was missing; generated a new one", "path", adminPath)
		}
		if len(cfg.Password) > 0 && len(cfg.Password) < minAdminPasswordLen {
			password, err := generateAdminPassword()
			if err != nil {
				return "", fmt.Errorf("generate admin password: %w", err)
			}
			cfg.Password = password
			cfg.PasswordSHA256 = adminPasswordHash(password)
			needsRewrite = true
			logger.Warn("admin password was missing/weak; generated a new one", "path", adminPath)
		}
		if needsRewrite {
			if err := atomicWriteFileMode(adminPath, []byte(renderAdminConfig(cfg)), 0o600); err != nil {
				return "", fmt.Errorf("rewrite %s: %w", adminPath, err)
			}
		}
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
	cfg.PasswordSHA256 = strings.TrimSpace(strings.ToLower(cfg.PasswordSHA256))
	if cfg.SessionExpirationSeconds <= 0 {
		cfg.SessionExpirationSeconds = defaultAdminSessionExpirationSeconds
	}
	return cfg, nil
}

func atomicWriteFile(path string, data []byte) error {
	return atomicWriteFileMode(path, data, 0o644)
}

func atomicWriteFileMode(path string, data []byte, mode os.FileMode) error {
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

	if err := os.Chmod(tmpPath, mode); err != nil {
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

func adminPasswordHash(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}
