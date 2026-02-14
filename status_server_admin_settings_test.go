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

func TestApplyAdminSettingsForm_SubmitWorkerNameMatchToggle(t *testing.T) {
	cfg := defaultConfig()
	cfg.SubmitWorkerNameMatch = false

	form := url.Values{}
	form.Set("status_tagline", cfg.StatusTagline) // required field (present in UI)
	form.Set("submit_worker_name_match", "1")
	r := httptest.NewRequest("POST", "/admin/apply", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	if err := applyAdminSettingsForm(&cfg, r); err != nil {
		t.Fatalf("applyAdminSettingsForm returned error: %v", err)
	}
	if !cfg.SubmitWorkerNameMatch {
		t.Fatalf("expected submit_worker_name_match to be enabled")
	}

	form = url.Values{}
	form.Set("status_tagline", cfg.StatusTagline)
	// Intentionally omit submit_worker_name_match to model an unchecked checkbox.
	r = httptest.NewRequest("POST", "/admin/apply", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatalf("ParseForm: %v", err)
	}
	if err := applyAdminSettingsForm(&cfg, r); err != nil {
		t.Fatalf("applyAdminSettingsForm returned error: %v", err)
	}
	if cfg.SubmitWorkerNameMatch {
		t.Fatalf("expected submit_worker_name_match to be disabled when omitted")
	}
}
