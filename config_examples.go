package main

import (
	"errors"
	"fmt"
	"github.com/pelletier/go-toml"
	"os"
	"path/filepath"
)

func ensureExampleFiles(dataDir string) {
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	examplesDir := filepath.Join(dataDir, "config", "examples")
	if err := os.MkdirAll(examplesDir, 0o755); err != nil {
		logger.Warn("create examples directory for example configs failed", "dir", examplesDir, "error", err)
		return
	}

	configExamplePath := filepath.Join(examplesDir, "config.toml.example")
	ensureExampleFile(configExamplePath, exampleConfigBytes())
	ensureExampleFile(filepath.Join(examplesDir, "secrets.toml.example"), secretsConfigExample)
	ensureExampleFile(filepath.Join(examplesDir, "tuning.toml.example"), exampleTuningConfigBytes())
}

func ensureExampleFile(path string, contents []byte) {
	if len(contents) == 0 {
		return
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		logger.Warn("write example config failed", "path", path, "error", err)
	}
}

func exampleHeader(text string) []byte {
	return []byte(fmt.Sprintf("# Generated %s example (copy to a real config and edit as needed)\n\n", text))
}

func exampleConfigBytes() []byte {
	cfg := defaultConfig()
	cfg.PayoutAddress = "YOUR_POOL_WALLET_ADDRESS_HERE"
	cfg.PoolDonationAddress = "OPTIONAL_POOL_DONATION_WALLET"
	cfg.CoinbasePoolTag = "" // Don't set a default tag - users should set their own
	fc := buildBaseFileConfig(cfg)
	data, err := toml.Marshal(fc)
	if err != nil {
		logger.Warn("encode config example failed", "error", err)
		return nil
	}
	return append(exampleHeader("base config"), data...)
}

func exampleTuningConfigBytes() []byte {
	cfg := defaultConfig()
	tf := buildTuningFileConfig(cfg)
	data, err := toml.Marshal(tf)
	if err != nil {
		logger.Warn("encode tuning config example failed", "error", err)
		return nil
	}
	return append(exampleHeader("tuning config"), data...)
}

func rewriteConfigFile(path string, cfg Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	fc := buildBaseFileConfig(cfg)
	data, err := toml.Marshal(fc)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, "config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmpFile.Name()
	removeTemp := true
	defer func() {
		if tmpFile != nil {
			_ = tmpFile.Close()
		}
		if removeTemp {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	tmpFile = nil

	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}

	bakPath := path + ".bak"
	if _, err := os.Stat(path); err == nil {
		if err := os.Remove(bakPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", bakPath, err)
		}
		if err := os.Rename(path, bakPath); err != nil {
			return fmt.Errorf("rename %s to %s: %w", path, bakPath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmpName, path, err)
	}
	removeTemp = false
	return nil
}
