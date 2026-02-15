package main

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestApplyAdminSettingsForm_IgnoresDisabledOperatorDonationFields(t *testing.T) {
	cfg := defaultConfig()
	cfg.StatusTagline = "before"
	cfg.OperatorDonationName = "Alice"
	cfg.OperatorDonationURL = "https://example.com"

	form := url.Values{}
	form.Set("status_tagline", "after")
	// Intentionally omit operator_donation_name/operator_donation_url to mimic
	// disabled inputs (disabled fields are not submitted).
	r := httptest.NewRequest("POST", "/admin/apply", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}

	if err := applyAdminSettingsForm(&cfg, r); err != nil {
		t.Fatalf("applyAdminSettingsForm returned error: %v", err)
	}
	if cfg.StatusTagline != "after" {
		t.Fatalf("expected status_tagline to update, got %q", cfg.StatusTagline)
	}
	if cfg.OperatorDonationName != "Alice" {
		t.Fatalf("expected operator_donation_name to be preserved, got %q", cfg.OperatorDonationName)
	}
	if cfg.OperatorDonationURL != "https://example.com" {
		t.Fatalf("expected operator_donation_url to be preserved, got %q", cfg.OperatorDonationURL)
	}
}

func TestApplyAdminSettingsForm_DefaultDifficultyZeroFallsBackToMinDifficulty(t *testing.T) {
	cfg := defaultConfig()
	cfg.DefaultDifficulty = 0
	cfg.MinDifficulty = 256

	form := url.Values{}
	form.Set("status_tagline", cfg.StatusTagline) // required field (present in UI)
	form.Set("min_difficulty", "1024")
	r := httptest.NewRequest("POST", "/admin/apply", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}

	if err := applyAdminSettingsForm(&cfg, r); err != nil {
		t.Fatalf("applyAdminSettingsForm returned error: %v", err)
	}
	if cfg.MinDifficulty != 1024 {
		t.Fatalf("expected min_difficulty=1024, got %v", cfg.MinDifficulty)
	}
	if cfg.DefaultDifficulty != 0 {
		t.Fatalf("expected default_difficulty to remain unset (0) when not provided, got %v", cfg.DefaultDifficulty)
	}
}

func TestApplyAdminSettingsForm_ShareRequireWorkerMatchToggle(t *testing.T) {
	cfg := defaultConfig()
	cfg.ShareRequireWorkerMatch = false

	form := url.Values{}
	form.Set("status_tagline", cfg.StatusTagline) // required field (present in UI)
	form.Set("share_require_worker_match", "1")
	r := httptest.NewRequest("POST", "/admin/apply", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	if err := applyAdminSettingsForm(&cfg, r); err != nil {
		t.Fatalf("applyAdminSettingsForm returned error: %v", err)
	}
	if !cfg.ShareRequireWorkerMatch {
		t.Fatalf("expected share_require_worker_match to be enabled")
	}

	form = url.Values{}
	form.Set("status_tagline", cfg.StatusTagline)
	// Intentionally omit share_require_worker_match to model an unchecked checkbox.
	r = httptest.NewRequest("POST", "/admin/apply", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	if err := applyAdminSettingsForm(&cfg, r); err != nil {
		t.Fatalf("applyAdminSettingsForm returned error: %v", err)
	}
	if cfg.ShareRequireWorkerMatch {
		t.Fatalf("expected share_require_worker_match to be disabled when omitted")
	}
}

func TestApplyAdminSettingsForm_DisableConnectRateLimitsToggle(t *testing.T) {
	cfg := defaultConfig()
	cfg.DisableConnectRateLimits = false

	form := url.Values{}
	form.Set("status_tagline", cfg.StatusTagline)
	form.Set("disable_connect_rate_limits", "1")
	r := httptest.NewRequest("POST", "/admin/apply", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	if err := applyAdminSettingsForm(&cfg, r); err != nil {
		t.Fatalf("applyAdminSettingsForm returned error: %v", err)
	}
	if !cfg.DisableConnectRateLimits {
		t.Fatalf("expected disable_connect_rate_limits to be enabled")
	}

	form = url.Values{}
	form.Set("status_tagline", cfg.StatusTagline)
	// Intentionally omit disable_connect_rate_limits to model an unchecked checkbox.
	r = httptest.NewRequest("POST", "/admin/apply", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	if err := applyAdminSettingsForm(&cfg, r); err != nil {
		t.Fatalf("applyAdminSettingsForm returned error: %v", err)
	}
	if cfg.DisableConnectRateLimits {
		t.Fatalf("expected disable_connect_rate_limits to be disabled when omitted")
	}
}

func TestApplyAdminSettingsForm_ShareRequireJobIDToggle(t *testing.T) {
	cfg := defaultConfig()
	cfg.ShareRequireJobID = false

	form := url.Values{}
	form.Set("status_tagline", cfg.StatusTagline)
	form.Set("share_require_job_id", "1")
	r := httptest.NewRequest("POST", "/admin/apply", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	if err := applyAdminSettingsForm(&cfg, r); err != nil {
		t.Fatalf("applyAdminSettingsForm returned error: %v", err)
	}
	if !cfg.ShareRequireJobID {
		t.Fatalf("expected share_require_job_id to be enabled")
	}

	form = url.Values{}
	form.Set("status_tagline", cfg.StatusTagline)
	// Intentionally omit share_require_job_id to model an unchecked checkbox.
	r = httptest.NewRequest("POST", "/admin/apply", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	if err := applyAdminSettingsForm(&cfg, r); err != nil {
		t.Fatalf("applyAdminSettingsForm returned error: %v", err)
	}
	if cfg.ShareRequireJobID {
		t.Fatalf("expected share_require_job_id to be disabled when omitted")
	}
}
