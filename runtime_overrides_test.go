package main

import "testing"

func TestListenerOverrideValueDisablesWithSentinel(t *testing.T) {
	for _, input := range []string{"off", "none", "disabled", " OFF "} {
		got, ok := listenerOverrideValue(input)
		if !ok {
			t.Fatalf("listenerOverrideValue(%q) did not report override", input)
		}
		if got != "" {
			t.Fatalf("listenerOverrideValue(%q) = %q, want empty listener", input, got)
		}
	}
}

func TestApplyRuntimeOverridesCanDisableOptionalListeners(t *testing.T) {
	cfg := defaultConfig()
	cfg.StatusAddr = ":80"
	cfg.StatusTLSAddr = ":443"
	cfg.StratumTLSListen = ":4333"

	err := applyRuntimeOverrides(&cfg, runtimeOverrides{
		statusAddr:       "off",
		statusTLSAddr:    "off",
		stratumTLSListen: "off",
	})
	if err != nil {
		t.Fatalf("applyRuntimeOverrides: %v", err)
	}
	if cfg.StatusAddr != "" {
		t.Fatalf("StatusAddr = %q, want disabled", cfg.StatusAddr)
	}
	if cfg.StatusTLSAddr != "" {
		t.Fatalf("StatusTLSAddr = %q, want disabled", cfg.StatusTLSAddr)
	}
	if cfg.StratumTLSListen != "" {
		t.Fatalf("StratumTLSListen = %q, want disabled", cfg.StratumTLSListen)
	}
}
