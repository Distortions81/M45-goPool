package main

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

const (
	defaultAdminLogSource = "pool"
	adminLogTailMaxBytes  = 512 * 1024
	adminLogTailMaxLines  = 400
)

type adminLogSourceInfo struct {
	Key    string
	Prefix string
	Label  string
}

var adminLogSources = []adminLogSourceInfo{
	{Key: "pool", Prefix: "pool-", Label: "Pool"},
	{Key: "errors", Prefix: "errors-", Label: "Errors"},
	{Key: "debug", Prefix: "debug-", Label: "Debug"},
	{Key: "net", Prefix: "net-debug-", Label: "Network Debug"},
}

func adminLogSourceKeys() []string {
	keys := make([]string, 0, len(adminLogSources))
	for _, src := range adminLogSources {
		// errors.log routing is disabled; hide it from the admin UI source list.
		// We still keep the source registered so historical errors-*.log files can
		// be accessed directly via ?source=errors.
		if src.Key == "errors" {
			continue
		}
		keys = append(keys, src.Key)
	}
	return keys
}

func normalizeAdminLogSource(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	for _, src := range adminLogSources {
		if raw == src.Key {
			return raw
		}
	}
	return ""
}

func adminLogSourceByKey(key string) adminLogSourceInfo {
	key = normalizeAdminLogSource(key)
	if key == "" {
		key = defaultAdminLogSource
	}
	for _, src := range adminLogSources {
		if src.Key == key {
			return src
		}
	}
	return adminLogSources[0]
}

func (s *StatusServer) handleAdminLogsPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Redirect(w, r, "/admin/logs", http.StatusSeeOther)
		return
	}
	data, _, _ := s.buildAdminPageData(r, r.URL.Query().Get("notice"))
	if !data.AdminEnabled {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if !data.LoggedIn {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	data.AdminSection = "logs"
	s.renderAdminPageTemplate(w, r, data, "admin_logs")
}

func (s *StatusServer) handleAdminLogsTail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	src := adminLogSourceByKey(r.URL.Query().Get("source"))
	path, modAt, err := s.latestAdminLogPath(src)
	if err != nil {
		http.Error(w, "failed to read logs", http.StatusInternalServerError)
		return
	}
	lines := []string{}
	truncated := false
	if path != "" {
		lines, truncated, err = tailFileLines(path, adminLogTailMaxBytes, adminLogTailMaxLines)
		if err != nil {
			http.Error(w, "failed to read log file", http.StatusInternalServerError)
			return
		}
	}
	resp := struct {
		Source       string   `json:"source"`
		Label        string   `json:"label"`
		File         string   `json:"file,omitempty"`
		LastModified string   `json:"last_modified,omitempty"`
		Truncated    bool     `json:"truncated"`
		Lines        []string `json:"lines"`
	}{
		Source:    src.Key,
		Label:     src.Label,
		File:      filepath.Base(path),
		Truncated: truncated,
		Lines:     lines,
	}
	if !modAt.IsZero() {
		resp.LastModified = modAt.UTC().Format(time.RFC3339)
	}

	setShortJSONCacheHeaders(w, true)
	out, err := sonic.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
	if _, err := w.Write(out); err != nil {
		logger.Debug("admin logs json write failed", "error", err, "source", src.Key)
	}
}

func parseAdminBool(raw string) bool {
	v := strings.ToLower(strings.TrimSpace(raw))
	return v == "1" || v == "true" || v == "on" || v == "yes"
}

func (s *StatusServer) handleAdminLogsSetFlags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.isAdminAuthenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	adminCfg, err := loadAdminConfigFile(s.adminConfigPath)
	if err != nil {
		http.Error(w, "admin config unavailable", http.StatusInternalServerError)
		return
	}
	if !adminCfg.Enabled {
		http.Error(w, "admin disabled", http.StatusForbidden)
		return
	}
	password := r.FormValue("password")
	if password == "" || !s.adminPasswordMatches(adminCfg, password) {
		http.Error(w, "invalid password", http.StatusForbidden)
		return
	}

	debugEnabledRequested := parseAdminBool(r.FormValue("debug"))
	netDebugEnabledRequested := parseAdminBool(r.FormValue("net_debug"))

	// Simplified operator model: app logs are always INFO+ in pool.log; debug
	// toggles additional DEBUG logs (and verbose runtime traces).
	cfg := s.Config()
	cfg.LogDebug = debugEnabledRequested
	cfg.LogNetDebug = netDebugEnabledRequested
	s.UpdateConfig(cfg)

	debugErrMsg := ""
	if debugEnabledRequested {
		path, pathErr := initDebugLogOutput(cfg, "", "")
		if pathErr != nil {
			debugErrMsg = pathErr.Error()
		} else {
			logger.setDebugWriter(newDailyRollingFileWriter(path))
		}
	} else {
		logger.setDebugWriter(nil)
	}

	if cfg.LogDebug {
		setLogLevel(logLevelDebug)
	} else {
		setLogLevel(logLevelInfo)
	}
	debugLogging = debugEnabled()
	verboseRuntimeLogging = verboseRuntimeEnabled()

	netDebugApplied := false
	netDebugSupported := netLogRuntimeSupported()
	netDebugErrMsg := ""
	if netDebugEnabledRequested {
		path, pathErr := initNetLogOutput(cfg, "", "")
		if pathErr != nil {
			netDebugErrMsg = pathErr.Error()
		} else if err := setNetLogRuntime(true, newDailyRollingFileWriter(path)); err != nil {
			netDebugErrMsg = err.Error()
		} else {
			netDebugApplied = true
		}
	} else {
		if err := setNetLogRuntime(false, nil); err != nil {
			netDebugErrMsg = err.Error()
		}
	}

	logger.Info("admin updated logging flags",
		"component", "admin",
		"kind", "logging",
		"debug", debugEnabledRequested,
		"net_debug", netDebugEnabledRequested,
		"net_debug_supported", netDebugSupported,
	)

	resp := struct {
		OK              bool   `json:"ok"`
		Debug           bool   `json:"debug"`
		NetDebug        bool   `json:"net_debug"`
		NetDebugSupport bool   `json:"net_debug_supported"`
		Error           string `json:"error,omitempty"`
	}{
		OK:              debugErrMsg == "" && netDebugErrMsg == "",
		Debug:           debugLogging,
		NetDebug:        netLogRuntimeEnabled(),
		NetDebugSupport: netDebugSupported,
	}
	_ = netDebugApplied
	if debugErrMsg != "" && netDebugErrMsg != "" {
		resp.Error = fmt.Sprintf("debug toggle failed: %s; net-debug toggle failed: %s", debugErrMsg, netDebugErrMsg)
	} else if debugErrMsg != "" {
		resp.Error = fmt.Sprintf("debug toggle failed: %s", debugErrMsg)
	} else if netDebugErrMsg != "" {
		resp.Error = fmt.Sprintf("net-debug toggle failed: %s", netDebugErrMsg)
	}
	setShortJSONCacheHeaders(w, true)
	out, err := sonic.Marshal(resp)
	if err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
	if _, err := w.Write(out); err != nil {
		logger.Debug("admin set log flags json write failed", "component", "http", "kind", "write", "error", err)
	}
}

func (s *StatusServer) latestAdminLogPath(src adminLogSourceInfo) (string, time.Time, error) {
	cfg := s.Config()
	dataDir := strings.TrimSpace(cfg.DataDir)
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	logDir := filepath.Join(dataDir, "logs")
	entries, err := os.ReadDir(logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", time.Time{}, nil
		}
		return "", time.Time{}, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, src.Prefix) || !strings.HasSuffix(name, ".log") {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return "", time.Time{}, nil
	}
	sort.Strings(names)
	chosen := names[len(names)-1]
	full := filepath.Join(logDir, chosen)
	info, err := os.Stat(full)
	if err != nil {
		return "", time.Time{}, err
	}
	return full, info.ModTime(), nil
}

func tailFileLines(path string, maxBytes int64, maxLines int) ([]string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	size := info.Size()
	start := int64(0)
	if maxBytes > 0 && size > maxBytes {
		start = size - maxBytes
	}
	if _, err := f.Seek(start, 0); err != nil {
		return nil, false, err
	}
	sc := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1024*1024)
	lines := make([]string, 0, 256)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, false, err
	}

	// Drop possible partial first line when we started mid-file.
	if start > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	truncated := start > 0
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
		truncated = true
	}
	return lines, truncated, nil
}
