package main

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestConnectionTimeoutZeroDisablesIdleExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mc := &MinerConn{
		cfg:          Config{ConnectionTimeout: 0},
		lastActivity: now.Add(-24 * time.Hour),
	}

	if expired, reason := mc.idleExpired(now); expired {
		t.Fatalf("zero timeout expired idle connection: %q", reason)
	}
	if got := mc.currentReadTimeout(); got != initialReadTimeout {
		t.Fatalf("read polling timeout = %s, want %s", got, initialReadTimeout)
	}
	if got := mc.timeoutRiskDownshift(now, 2, time.Time{}, now.Add(-time.Hour), 1, hashPerShare); got != 0 {
		t.Fatalf("timeout risk downshift = %v, want 0 when timeout is disabled", got)
	}
}

func TestNewMinerConnPreservesDisabledConnectionTimeout(t *testing.T) {
	cfg := defaultConfig()
	cfg.ConnectionTimeout = 0
	jm := NewJobManager(nil, cfg, nil, nil, nil)
	mc := NewMinerConn(context.Background(), nopConn{}, jm, nil, cfg, nil, nil, nil, nil, nil, false)
	t.Cleanup(mc.cleanup)

	if mc.cfg.ConnectionTimeout != 0 {
		t.Fatalf("connection timeout = %s, want disabled (0)", mc.cfg.ConnectionTimeout)
	}
}

func TestValidateConfigAllowsDisabledConnectionTimeout(t *testing.T) {
	cfg := defaultConfig()
	cfg.AllowPublicRPC = true
	cfg.PayoutAddress = "test-payout-address"
	cfg.ConnectionTimeout = 0
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("zero connection timeout failed validation: %v", err)
	}

	cfg.ConnectionTimeout = minMinerTimeout - time.Second
	if err := validateConfig(cfg); err == nil {
		t.Fatalf("expected nonzero connection timeout below %s to fail validation", minMinerTimeout)
	}
}

func TestAdminAllowsDisabledConnectionTimeout(t *testing.T) {
	cfg := defaultConfig()
	form := url.Values{
		"status_tagline":             {cfg.StatusTagline},
		"connection_timeout_seconds": {"0"},
	}
	req := httptest.NewRequest("POST", "/admin/apply", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := req.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	if err := applyAdminSettingsForm(&cfg, req); err != nil {
		t.Fatalf("zero connection timeout failed admin validation: %v", err)
	}
	if cfg.ConnectionTimeout != 0 {
		t.Fatalf("connection timeout = %s, want disabled (0)", cfg.ConnectionTimeout)
	}
}
