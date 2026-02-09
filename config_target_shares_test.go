package main

import "testing"

func TestApplyTuningConfig_TargetSharesPerMin(t *testing.T) {
	cfg := defaultConfig()
	cfg.TargetSharesPerMin = 5

	override := tuningFileConfig{}
	value := 7.5
	override.Difficulty.TargetSharesPerMin = &value

	applyTuningConfig(&cfg, override)
	if cfg.TargetSharesPerMin != value {
		t.Fatalf("TargetSharesPerMin=%v want %v", cfg.TargetSharesPerMin, value)
	}
}

func TestValidateConfig_TargetSharesPerMinMustBePositive(t *testing.T) {
	cfg := defaultConfig()
	cfg.TargetSharesPerMin = 0
	if err := validateConfig(cfg); err == nil {
		t.Fatalf("expected error for non-positive target_shares_per_min")
	}
}
