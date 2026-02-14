package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildAdminPageData_OperatorStatsUsesRuntimeStatus(t *testing.T) {
	tmpDir := t.TempDir()
	adminPath := filepath.Join(tmpDir, "admin.toml")
	if err := os.WriteFile(adminPath, []byte("enabled = true\n"), 0o644); err != nil {
		t.Fatalf("write admin config: %v", err)
	}

	s := &StatusServer{
		adminConfigPath: adminPath,
	}
	s.UpdateConfig(defaultConfig())
	s.statusMu.Lock()
	s.cachedStatus = StatusData{
		ActiveMiners:    7,
		PoolHashrate:    12345,
		SharesPerSecond: 3.5,
	}
	s.lastStatusBuild = time.Now()
	s.statusMu.Unlock()

	data, _, _ := s.buildAdminPageData(nil, "")
	if data.OperatorStats.Pool.ActiveMiners != 7 {
		t.Fatalf("active miners got %d want 7", data.OperatorStats.Pool.ActiveMiners)
	}
	if data.OperatorStats.Pool.PoolHashrate != 12345 {
		t.Fatalf("pool hashrate got %v want 12345", data.OperatorStats.Pool.PoolHashrate)
	}
	if data.OperatorStats.Pool.SharesPerSecond != 3.5 {
		t.Fatalf("shares/sec got %v want 3.5", data.OperatorStats.Pool.SharesPerSecond)
	}
}
