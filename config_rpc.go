package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/btcsuite/btcd/btcutil"
)

func finalizeRPCCredentials(cfg *Config, secretsPath string, forceCredentials bool, configPath string) error {
	if forceCredentials {
		if err := loadRPCredentialsFromSecrets(cfg, secretsPath); err != nil {
			return err
		}
		return nil
	}

	if cfg.AllowPublicRPC && strings.TrimSpace(cfg.RPCCookiePath) == "" {
		return nil
	}

	if strings.TrimSpace(cfg.RPCCookiePath) == "" {
		auto, found, tried := autodetectRPCCookiePath()
		if auto != "" {
			cfg.RPCCookiePath = auto
			if found {
				logger.Info("autodetected bitcoind rpc cookie", "path", auto)
			} else {
				pathsDesc := "none"
				if len(tried) > 0 {
					pathsDesc = strings.Join(tried, ", ")
				}
				warnCookieMissing("rpc cookie not present yet; will keep watching", "path", auto, "tried_paths", pathsDesc)
			}
		} else {
			pathsDesc := "none"
			if len(tried) > 0 {
				pathsDesc = strings.Join(tried, ", ")
			}
			warnCookieMissing("rpc cookie autodetect failed", "tried_paths", pathsDesc)
			return fmt.Errorf("node.rpc_cookie_path is required when RPC credentials are not forced; configure it to use bitcoind's auth cookie (autodetect checked: %s)", pathsDesc)
		}
	}
	cfg.rpcCookieWatch = strings.TrimSpace(cfg.RPCCookiePath) != ""
	if cfg.rpcCookieWatch {
		if loaded, _, err := applyRPCCookieCredentials(cfg); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				warnCookieMissing("rpc cookie missing; will keep watching", "path", cfg.RPCCookiePath)
			} else {
				warnCookieMissing("failed to read rpc cookie", "path", cfg.RPCCookiePath, "error", err)
			}
		} else if loaded {
			logger.Info("rpc cookie loaded", "path", cfg.RPCCookiePath)
			persistRPCCookiePathIfNeeded(configPath, cfg)
		}
	}
	return nil
}

func loadRPCredentialsFromSecrets(cfg *Config, secretsPath string) error {
	sc, ok, err := loadSecretsFile(secretsPath)
	if err != nil {
		return err
	}
	if !ok {
		printRPCSecretHint(cfg, secretsPath)
		return fmt.Errorf("secrets file missing: %s", secretsPath)
	}
	user := strings.TrimSpace(sc.RPCUser)
	pass := strings.TrimSpace(sc.RPCPass)
	if user == "" || pass == "" {
		return fmt.Errorf("secrets file %s must include rpc_user and rpc_pass", secretsPath)
	}
	cfg.RPCUser = user
	cfg.RPCPass = pass
	return nil
}

func printRPCSecretHint(cfg *Config, secretsPath string) {
	secretsExamplePath := filepath.Join(cfg.DataDir, "config", "examples", "secrets.toml.example")
	fmt.Printf("\n🔐 RPC credentials are required when node.rpc_cookie_path is not configured.\n\n")
	fmt.Printf("   To configure RPC credentials:\n")
	fmt.Printf("   1. Copy the example: %s\n", secretsExamplePath)
	fmt.Printf("   2. To:                 %s\n", secretsPath)
	fmt.Printf("   3. Edit the file and set your rpc_user and rpc_pass\n")
	fmt.Printf("   4. Restart goPool\n\n")
}

func applyRPCCookieCredentials(cfg *Config) (bool, string, error) {
	path := strings.TrimSpace(cfg.RPCCookiePath)
	if path == "" {
		return false, path, nil
	}
	actualPath, user, pass, err := readRPCCookieWithFallback(path)
	if actualPath != "" {
		cfg.RPCCookiePath = actualPath
	}
	if err != nil {
		return false, actualPath, err
	}
	cfg.RPCUser = strings.TrimSpace(user)
	cfg.RPCPass = strings.TrimSpace(pass)
	return true, actualPath, nil
}

func rpcCookiePathCandidates(basePath string) []string {
	trimmed := strings.TrimSpace(basePath)
	if trimmed == "" {
		return nil
	}
	if info, err := os.Stat(trimmed); err == nil && info.IsDir() {
		return []string{filepath.Join(trimmed, ".cookie")}
	}
	candidates := []string{trimmed}
	if !strings.HasSuffix(trimmed, ".cookie") {
		candidates = append(candidates, filepath.Join(trimmed, ".cookie"))
	}
	return candidates
}

func readRPCCookieWithFallback(basePath string) (string, string, string, error) {
	candidates := rpcCookiePathCandidates(basePath)
	if len(candidates) == 0 {
		return "", "", "", fmt.Errorf("invalid cookie path")
	}
	var lastErr error
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err != nil {
			lastErr = fmt.Errorf("read %s: %w", candidate, err)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return candidate, "", "", lastErr
		}
		token := strings.TrimSpace(string(data))
		parts := strings.SplitN(token, ":", 2)
		if len(parts) != 2 {
			return candidate, "", "", fmt.Errorf("unexpected cookie format")
		}
		return candidate, strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("read %s: %w", candidates[len(candidates)-1], os.ErrNotExist)
	}
	return candidates[len(candidates)-1], "", "", lastErr
}

func readRPCCookie(path string) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", path, err)
	}
	token := strings.TrimSpace(string(data))
	parts := strings.SplitN(token, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("unexpected cookie format")
	}
	return parts[0], parts[1], nil
}

func rpcCookieCandidates() []string {
	var candidates []string
	if envDir := strings.TrimSpace(os.Getenv("BITCOIN_DATADIR")); envDir != "" {
		candidates = append(candidates, filepath.Join(envDir, ".cookie"))
		for _, net := range []string{"regtest", "testnet3", "signet"} {
			candidates = append(candidates, filepath.Join(envDir, net, ".cookie"))
		}
	}
	candidates = append(candidates, btcdCookieCandidates()...)
	candidates = append(candidates, linuxCookieCandidates()...)
	return candidates
}

func autodetectRPCCookiePath() (string, bool, []string) {
	candidates := rpcCookieCandidates()
	tried := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		tried = append(tried, candidate)
		if fileExists(candidate) {
			return candidate, true, tried
		}
	}
	if len(tried) > 0 {
		return tried[0], false, tried
	}
	return "", false, tried
}

func warnCookieMissing(msg string, attrs ...any) {
	logger.Warn(msg, attrs...)
	entry := msg
	if formatted := formatAttrs(attrs); formatted != "" {
		entry += " " + formatted
	}
	entry += "\n"
	_, _ = os.Stdout.Write([]byte(entry))
}

func persistRPCCookiePathIfNeeded(configPath string, cfg *Config) {
	if configPath == "" {
		return
	}
	path := strings.TrimSpace(cfg.RPCCookiePath)
	if path == "" || cfg.rpCCookiePathFromConfig == path {
		return
	}
	if err := rewriteConfigFile(configPath, *cfg); err != nil {
		logger.Warn("persist rpc cookie path", "path", path, "error", err)
		return
	}
	cfg.rpCCookiePathFromConfig = path
	logger.Info("persisted rpc cookie path", "path", path, "config", configPath)
}

func linuxCookieCandidates() []string {
	home, _ := os.UserHomeDir()
	h := func(p string) string {
		if strings.HasPrefix(p, "~/") && home != "" {
			return filepath.Join(home, p[2:])
		}
		return p
	}
	return []string{
		h("~/.bitcoin/.cookie"),
		h("~/.bitcoin/regtest/.cookie"),
		h("~/.bitcoin/testnet3/.cookie"),
		h("~/.bitcoin/signet/.cookie"),
		"/var/lib/bitcoin/.cookie",
		"/var/lib/bitcoin/regtest/.cookie",
		"/var/lib/bitcoin/testnet3/.cookie",
		"/var/lib/bitcoin/signet/.cookie",
		"/home/bitcoin/.bitcoin/.cookie",
		"/home/bitcoin/.bitcoin/regtest/.cookie",
		"/home/bitcoin/.bitcoin/testnet3/.cookie",
		"/home/bitcoin/.bitcoin/signet/.cookie",
		"/etc/bitcoin/.cookie",
	}
}

// btcdCookieCandidates mirrors btcsuite/btcd/rpcclient's layout for btcd's default
// datadir so we can reuse the same cookie locations before falling back to the
// general linux list.
func btcdCookieCandidates() []string {
	home := btcutil.AppDataDir("btcd", false)
	if home == "" {
		return nil
	}
	dataDir := filepath.Join(home, "data")
	networks := []string{"regtest", "testnet3", "testnet4", "signet", "simnet"}
	candidates := make([]string, 0, len(networks)+1)
	candidates = append(candidates, filepath.Join(dataDir, ".cookie"))
	for _, net := range networks {
		candidates = append(candidates, filepath.Join(dataDir, net, ".cookie"))
	}
	return candidates
}
