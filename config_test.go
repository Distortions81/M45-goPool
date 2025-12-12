package main

import (
	"bytes"
	"context"
	"errors"
	"math/bits"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSanitizePayoutAddress(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty",
			in:   "",
			want: "",
		},
		{
			name: "whitespace only",
			in:   " \n\t\r",
			want: "",
		},
		{
			name: "valid bech32 unchanged",
			in:   "bc1qagc0l2cvx0c0mx23rkjpwhe7klelynj98h82tj",
			want: "bc1qagc0l2cvx0c0mx23rkjpwhe7klelynj98h82tj",
		},
		{
			name: "bech32 with surrounding whitespace and punctuation",
			in:   "  \n\t bc1qabc123!@#\n\r",
			want: "bc1qabc123",
		},
		{
			name: "mixed case base58 with spaces",
			in:   " 1BoatSLRHtKNngkdXEeobR76b53LETtpyT \n",
			want: "1BoatSLRHtKNngkdXEeobR76b53LETtpyT",
		},
		{
			name: "only punctuation dropped to empty",
			in:   "!!!@@@\n###",
			want: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizePayoutAddress(tt.in)
			if got != tt.want {
				t.Fatalf("sanitizePayoutAddress(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

type fakeVersionMaskRPC struct {
	chain  string
	err    error
	called bool
}

func (f *fakeVersionMaskRPC) callCtx(ctx context.Context, method string, params interface{}, out interface{}) error {
	f.called = true
	if f.err != nil {
		return f.err
	}
	if method != "getblockchaininfo" {
		return errors.New("unexpected method")
	}
	v := reflect.ValueOf(out)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return errors.New("out must be non-nil pointer")
	}
	elem := v.Elem()
	if elem.Kind() != reflect.Struct {
		return errors.New("out must point to struct")
	}
	field := elem.FieldByName("Chain")
	if !field.IsValid() || !field.CanSet() || field.Kind() != reflect.String {
		return errors.New("struct has no settable Chain field")
	}
	field.SetString(f.chain)
	return nil
}

func TestAutoConfigureVersionMaskFromNode_NoRPCOrConfig(t *testing.T) {
	cfg := &Config{}
	autoConfigureVersionMaskFromNode(context.Background(), nil, cfg)
	if cfg.VersionMask != 0 || cfg.VersionMaskConfigured {
		t.Fatalf("expected config unchanged when rpc is nil")
	}
	autoConfigureVersionMaskFromNode(context.Background(), &fakeVersionMaskRPC{}, nil)
}

func TestAutoConfigureVersionMaskFromNode_AlreadyConfigured(t *testing.T) {
	cfg := &Config{
		VersionMask:           0xdeadbeef,
		VersionMaskConfigured: true,
	}
	rpc := &fakeVersionMaskRPC{chain: "main"}
	autoConfigureVersionMaskFromNode(context.Background(), rpc, cfg)
	if rpc.called {
		t.Fatalf("expected no RPC call when VersionMaskConfigured is true")
	}
	if cfg.VersionMask != 0xdeadbeef {
		t.Fatalf("VersionMask changed: got %08x, want %08x", cfg.VersionMask, uint32(0xdeadbeef))
	}
}

func TestAutoConfigureVersionMaskFromNode_RPCError(t *testing.T) {
	cfg := &Config{}
	rpc := &fakeVersionMaskRPC{err: errors.New("boom")}
	autoConfigureVersionMaskFromNode(context.Background(), rpc, cfg)
	if !rpc.called {
		t.Fatalf("expected RPC to be called")
	}
	if cfg.VersionMask != 0 || cfg.VersionMaskConfigured {
		t.Fatalf("expected config unchanged on RPC error")
	}
}

func TestAutoConfigureVersionMaskFromNode_UnknownChain(t *testing.T) {
	cfg := &Config{}
	rpc := &fakeVersionMaskRPC{chain: "mysterynet"}
	autoConfigureVersionMaskFromNode(context.Background(), rpc, cfg)
	if !rpc.called {
		t.Fatalf("expected RPC to be called")
	}
	if cfg.VersionMask != 0 || cfg.VersionMaskConfigured {
		t.Fatalf("expected config unchanged for unknown chain")
	}
}

func TestAutoConfigureVersionMaskFromNode_MainnetAndTestnet(t *testing.T) {
	cases := []struct {
		name  string
		chain string
	}{
		{name: "main", chain: "main"},
		{name: "MAINNET uppercase with spaces", chain: "  MAINNET  "},
		{name: "empty chain", chain: ""},
		{name: "testnet", chain: "testnet"},
		{name: "testnet3", chain: "testnet3"},
		{name: "signet", chain: "signet"},
	}
	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{MinVersionBits: 1}
			rpc := &fakeVersionMaskRPC{chain: tt.chain}
			autoConfigureVersionMaskFromNode(context.Background(), rpc, cfg)
			if !rpc.called {
				t.Fatalf("expected RPC to be called")
			}
			if cfg.VersionMask != defaultVersionMask {
				t.Fatalf("VersionMask = %08x, want %08x", cfg.VersionMask, defaultVersionMask)
			}
			if !cfg.VersionMaskConfigured {
				t.Fatalf("VersionMaskConfigured = false, want true")
			}
			available := bits.OnesCount32(cfg.VersionMask)
			if cfg.MinVersionBits < 0 || cfg.MinVersionBits > available {
				t.Fatalf("MinVersionBits out of range: %d (available %d)", cfg.MinVersionBits, available)
			}
		})
	}
}

func TestAutoConfigureVersionMaskFromNode_RegtestClampsMinVersionBits(t *testing.T) {
	cfg := &Config{
		MinVersionBits: 32,
	}
	rpc := &fakeVersionMaskRPC{chain: "regtest"}
	autoConfigureVersionMaskFromNode(context.Background(), rpc, cfg)

	if cfg.VersionMask != 0x3fffe000 {
		t.Fatalf("VersionMask = %08x, want %08x", cfg.VersionMask, uint32(0x3fffe000))
	}
	if !cfg.VersionMaskConfigured {
		t.Fatalf("VersionMaskConfigured = false, want true")
	}
	available := bits.OnesCount32(cfg.VersionMask)
	if cfg.MinVersionBits != available {
		t.Fatalf("MinVersionBits = %d, want %d", cfg.MinVersionBits, available)
	}
}

func TestAutoConfigureVersionMaskFromNode_RegtestNegativeMinBits(t *testing.T) {
	cfg := &Config{
		MinVersionBits: -5,
	}
	rpc := &fakeVersionMaskRPC{chain: "regtest"}
	autoConfigureVersionMaskFromNode(context.Background(), rpc, cfg)
	if cfg.MinVersionBits != 0 {
		t.Fatalf("MinVersionBits = %d, want 0", cfg.MinVersionBits)
	}
}

func TestAutoConfigureAcceptRateLimits_DisabledWhenMaxConnsZero(t *testing.T) {
	cfg := &Config{
		MaxConns:            0,
		MaxAcceptsPerSecond: defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:      defaultMaxAcceptBurst,
	}
	autoConfigureAcceptRateLimits(cfg, true)
	// Should remain unchanged when MaxConns is 0
	if cfg.MaxAcceptsPerSecond != defaultMaxAcceptsPerSecond {
		t.Fatalf("MaxAcceptsPerSecond changed: got %d, want %d", cfg.MaxAcceptsPerSecond, defaultMaxAcceptsPerSecond)
	}
	if cfg.MaxAcceptBurst != defaultMaxAcceptBurst {
		t.Fatalf("MaxAcceptBurst changed: got %d, want %d", cfg.MaxAcceptBurst, defaultMaxAcceptBurst)
	}
}

func TestAutoConfigureAcceptRateLimits_SmallPool(t *testing.T) {
	cfg := &Config{
		MaxConns:              50,
		MaxAcceptsPerSecond:   defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:        defaultMaxAcceptBurst,
		AcceptReconnectWindow: 15, // 15 second total window
		AcceptBurstWindow:     5,  // 5 second burst window
	}
	autoConfigureAcceptRateLimits(cfg, false)
	// For 50 miners with 15s window:
	// - Burst fraction: 5/15 = 0.33
	// - Burst: 50 * 0.33 = 16, minimum is 20
	// - Remaining: 50 * 0.67 = 33
	// - Rate: 33 / 10 = 3, minimum is 10
	if cfg.MaxAcceptsPerSecond != 10 {
		t.Fatalf("MaxAcceptsPerSecond = %d, want 10 (minimum)", cfg.MaxAcceptsPerSecond)
	}
	if cfg.MaxAcceptBurst != 20 {
		t.Fatalf("MaxAcceptBurst = %d, want 20 (minimum)", cfg.MaxAcceptBurst)
	}
}

func TestAutoConfigureAcceptRateLimits_MediumPool(t *testing.T) {
	cfg := &Config{
		MaxConns:              1000,
		MaxAcceptsPerSecond:   defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:        defaultMaxAcceptBurst,
		AcceptReconnectWindow: 15, // 15 second total window
		AcceptBurstWindow:     5,  // 5 second burst window
	}
	autoConfigureAcceptRateLimits(cfg, false)
	// For 1000 miners with 15s window:
	// - Burst fraction: 5/15 = 0.33
	// - Burst: 1000 * 0.33 = 333
	// - Remaining: 1000 * 0.67 = 667
	// - Rate: 667 / 10 = 66 accepts/sec
	if cfg.MaxAcceptsPerSecond != 66 {
		t.Fatalf("MaxAcceptsPerSecond = %d, want 66", cfg.MaxAcceptsPerSecond)
	}
	if cfg.MaxAcceptBurst != 333 {
		t.Fatalf("MaxAcceptBurst = %d, want 333", cfg.MaxAcceptBurst)
	}
}

func TestAutoConfigureAcceptRateLimits_LargePool(t *testing.T) {
	cfg := &Config{
		MaxConns:              10000,
		MaxAcceptsPerSecond:   defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:        defaultMaxAcceptBurst,
		AcceptReconnectWindow: 15, // 15 second total window
		AcceptBurstWindow:     5,  // 5 second burst window
	}
	autoConfigureAcceptRateLimits(cfg, false)
	// For 10000 miners with 15s window:
	// - Burst fraction: 5/15 = 0.33
	// - Burst: 10000 * 0.33 = 3333
	// - Remaining: 10000 * 0.67 = 6667
	// - Rate: 6667 / 10 = 666 accepts/sec
	if cfg.MaxAcceptsPerSecond != 666 {
		t.Fatalf("MaxAcceptsPerSecond = %d, want 666", cfg.MaxAcceptsPerSecond)
	}
	if cfg.MaxAcceptBurst != 3333 {
		t.Fatalf("MaxAcceptBurst = %d, want 3333", cfg.MaxAcceptBurst)
	}
}

func TestAutoConfigureAcceptRateLimits_VeryLargePoolCapped(t *testing.T) {
	cfg := &Config{
		MaxConns:              150000,
		MaxAcceptsPerSecond:   defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:        defaultMaxAcceptBurst,
		AcceptReconnectWindow: 15, // 15 second total window
		AcceptBurstWindow:     5,  // 5 second burst window
	}
	autoConfigureAcceptRateLimits(cfg, false)
	// For 150000 miners with 15s window:
	// - Burst fraction: 5/15 = 0.33
	// - Burst: 150000 * 0.33 = 50000, but capped at 25000
	// - Remaining: 150000 * 0.67 = 100000
	// - Rate: 100000 / 10 = 10000 (at cap)
	if cfg.MaxAcceptsPerSecond != 10000 {
		t.Fatalf("MaxAcceptsPerSecond = %d, want 10000 (capped)", cfg.MaxAcceptsPerSecond)
	}
	if cfg.MaxAcceptBurst != 25000 {
		t.Fatalf("MaxAcceptBurst = %d, want 25000 (capped)", cfg.MaxAcceptBurst)
	}
}

func TestAutoConfigureAcceptRateLimits_RespectsExplicitConfig(t *testing.T) {
	// Create a temporary config file with explicit rate limits
	cfg := &Config{
		MaxConns:            1000,
		MaxAcceptsPerSecond: 200, // explicitly set
		MaxAcceptBurst:      400, // explicitly set
	}

	// Simulate that config was loaded from file - the function should
	// not change explicitly configured values
	// Note: In reality this would be handled by checking the fileConfig,
	// but here we're testing that non-default values are preserved
	originalAccepts := cfg.MaxAcceptsPerSecond
	originalBurst := cfg.MaxAcceptBurst

	// This won't change values that are different from defaults
	autoConfigureAcceptRateLimits(cfg, true)

	// Values should remain as they were since they're not at default
	if cfg.MaxAcceptsPerSecond != originalAccepts {
		t.Fatalf("MaxAcceptsPerSecond changed from explicit value: got %d, want %d",
			cfg.MaxAcceptsPerSecond, originalAccepts)
	}
	if cfg.MaxAcceptBurst != originalBurst {
		t.Fatalf("MaxAcceptBurst changed from explicit value: got %d, want %d",
			cfg.MaxAcceptBurst, originalBurst)
	}
}

func TestAutoConfigureAcceptRateLimits_AutoModeEnabled(t *testing.T) {
	// When AutoAcceptRateLimits is true, it should always override
	cfg := &Config{
		MaxConns:              1000,
		MaxAcceptsPerSecond:   200, // explicitly set to non-default
		MaxAcceptBurst:        400, // explicitly set to non-default
		AutoAcceptRateLimits:  true,
		AcceptReconnectWindow: 15, // 15 second total window
		AcceptBurstWindow:     5,  // 5 second burst window
	}

	autoConfigureAcceptRateLimits(cfg, false)

	// For 1000 miners with auto mode and 15s window:
	// - Burst: 1000 * 0.33 = 333
	// - Rate: 667 / 10 = 66
	if cfg.MaxAcceptsPerSecond != 66 {
		t.Fatalf("MaxAcceptsPerSecond = %d, want 66 (auto mode should override)", cfg.MaxAcceptsPerSecond)
	}
	if cfg.MaxAcceptBurst != 333 {
		t.Fatalf("MaxAcceptBurst = %d, want 333 (auto mode should override)", cfg.MaxAcceptBurst)
	}
}

func TestAutoConfigureAcceptRateLimits_AutoModeScaling(t *testing.T) {
	tests := []struct {
		name            string
		maxConns        int
		wantAcceptRate  int
		wantAcceptBurst int
	}{
		{
			name:            "small_pool_100",
			maxConns:        100,
			wantAcceptRate:  10, // (100 * 0.67) / 10 = 6, minimum 10
			wantAcceptBurst: 33, // 100 * 0.33 = 33
		},
		{
			name:            "medium_pool_5000",
			maxConns:        5000,
			wantAcceptRate:  333,  // (5000 * 0.67) / 10 = 333
			wantAcceptBurst: 1666, // 5000 * 0.33 = 1666
		},
		{
			name:            "large_pool_50000",
			maxConns:        50000,
			wantAcceptRate:  3333,  // (50000 * 0.67) / 10 = 3333
			wantAcceptBurst: 16666, // 50000 * 0.33 = 16666
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				MaxConns:              tt.maxConns,
				MaxAcceptsPerSecond:   defaultMaxAcceptsPerSecond,
				MaxAcceptBurst:        defaultMaxAcceptBurst,
				AutoAcceptRateLimits:  true,
				AcceptReconnectWindow: 15, // 15 second total window
				AcceptBurstWindow:     5,  // 5 second burst window
			}

			autoConfigureAcceptRateLimits(cfg, false)

			if cfg.MaxAcceptsPerSecond != tt.wantAcceptRate {
				t.Fatalf("MaxAcceptsPerSecond = %d, want %d", cfg.MaxAcceptsPerSecond, tt.wantAcceptRate)
			}
			if cfg.MaxAcceptBurst != tt.wantAcceptBurst {
				t.Fatalf("MaxAcceptBurst = %d, want %d", cfg.MaxAcceptBurst, tt.wantAcceptBurst)
			}
		})
	}
}

func TestAutoConfigureAcceptRateLimits_CustomWindows(t *testing.T) {
	// Test with custom time windows
	cfg := &Config{
		MaxConns:              1000,
		MaxAcceptsPerSecond:   defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:        defaultMaxAcceptBurst,
		AutoAcceptRateLimits:  true,
		AcceptReconnectWindow: 30, // 30 second total window
		AcceptBurstWindow:     10, // 10 second burst window
	}

	autoConfigureAcceptRateLimits(cfg, false)

	// For 1000 miners with 30s window, 10s burst:
	// - Burst fraction: 10/30 = 0.33
	// - Burst: 1000 * 0.33 = 333
	// - Remaining: 1000 * 0.67 = 667
	// - Rate: 667 / 20 = 33 accepts/sec
	if cfg.MaxAcceptsPerSecond != 33 {
		t.Fatalf("MaxAcceptsPerSecond = %d, want 33", cfg.MaxAcceptsPerSecond)
	}
	if cfg.MaxAcceptBurst != 333 {
		t.Fatalf("MaxAcceptBurst = %d, want 333", cfg.MaxAcceptBurst)
	}
}

func TestAutoConfigureAcceptRateLimits_FastReconnectWindow(t *testing.T) {
	// Test with very fast 5-second reconnect window
	cfg := &Config{
		MaxConns:              1000,
		MaxAcceptsPerSecond:   defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:        defaultMaxAcceptBurst,
		AutoAcceptRateLimits:  true,
		AcceptReconnectWindow: 5, // 5 second total window
		AcceptBurstWindow:     2, // 2 second burst window
	}

	autoConfigureAcceptRateLimits(cfg, false)

	// For 1000 miners with 5s window, 2s burst:
	// - Burst fraction: 2/5 = 0.4
	// - Burst: 1000 * 0.4 = 400
	// - Remaining: 1000 * 0.6 = 600
	// - Rate: 600 / 3 = 200 accepts/sec
	if cfg.MaxAcceptsPerSecond != 200 {
		t.Fatalf("MaxAcceptsPerSecond = %d, want 200", cfg.MaxAcceptsPerSecond)
	}
	if cfg.MaxAcceptBurst != 400 {
		t.Fatalf("MaxAcceptBurst = %d, want 400", cfg.MaxAcceptBurst)
	}
}

func TestAutoConfigureAcceptRateLimits_SteadyStateSmallPool(t *testing.T) {
	cfg := &Config{
		MaxConns:                          100,
		MaxAcceptsPerSecond:               defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:                    defaultMaxAcceptBurst,
		AcceptSteadyStateRate:             defaultAcceptSteadyStateRate,
		AcceptSteadyStateReconnectPercent: 5.0, // 5% expected reconnections
		AcceptSteadyStateReconnectWindow:  60,  // over 60 seconds
		AcceptReconnectWindow:             15,
		AcceptBurstWindow:                 5,
	}
	autoConfigureAcceptRateLimits(cfg, false)
	// For 100 miners × 5% = 5 expected reconnects over 60s = 0.08/sec
	// But minimum is 5/sec
	if cfg.AcceptSteadyStateRate != 5 {
		t.Fatalf("AcceptSteadyStateRate = %d, want 5 (minimum)", cfg.AcceptSteadyStateRate)
	}
}

func TestAutoConfigureAcceptRateLimits_SteadyStateMediumPool(t *testing.T) {
	cfg := &Config{
		MaxConns:                          10000,
		MaxAcceptsPerSecond:               defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:                    defaultMaxAcceptBurst,
		AcceptSteadyStateRate:             defaultAcceptSteadyStateRate,
		AcceptSteadyStateReconnectPercent: 5.0, // 5% expected reconnections
		AcceptSteadyStateReconnectWindow:  60,  // over 60 seconds
		AcceptReconnectWindow:             15,
		AcceptBurstWindow:                 5,
	}
	autoConfigureAcceptRateLimits(cfg, false)
	// For 10000 miners × 5% = 500 expected reconnects over 60s = 8.33/sec
	if cfg.AcceptSteadyStateRate != 8 {
		t.Fatalf("AcceptSteadyStateRate = %d, want 8", cfg.AcceptSteadyStateRate)
	}
}

func TestAutoConfigureAcceptRateLimits_SteadyStateLargePool(t *testing.T) {
	cfg := &Config{
		MaxConns:                          50000,
		MaxAcceptsPerSecond:               defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:                    defaultMaxAcceptBurst,
		AcceptSteadyStateRate:             defaultAcceptSteadyStateRate,
		AcceptSteadyStateReconnectPercent: 5.0, // 5% expected reconnections
		AcceptSteadyStateReconnectWindow:  60,  // over 60 seconds
		AcceptReconnectWindow:             15,
		AcceptBurstWindow:                 5,
	}
	autoConfigureAcceptRateLimits(cfg, false)
	// For 50000 miners × 5% = 2500 expected reconnects over 60s = 41.66/sec
	if cfg.AcceptSteadyStateRate != 41 {
		t.Fatalf("AcceptSteadyStateRate = %d, want 41", cfg.AcceptSteadyStateRate)
	}
}

func TestAutoConfigureAcceptRateLimits_SteadyStateVeryLargePoolCapped(t *testing.T) {
	cfg := &Config{
		MaxConns:                          200000,
		MaxAcceptsPerSecond:               defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:                    defaultMaxAcceptBurst,
		AcceptSteadyStateRate:             defaultAcceptSteadyStateRate,
		AcceptSteadyStateReconnectPercent: 5.0, // 5% expected reconnections
		AcceptSteadyStateReconnectWindow:  60,  // over 60 seconds
		AcceptReconnectWindow:             15,
		AcceptBurstWindow:                 5,
	}
	autoConfigureAcceptRateLimits(cfg, false)
	// For 200000 miners × 5% = 10000 expected reconnects over 60s = 166.66/sec
	if cfg.AcceptSteadyStateRate != 166 {
		t.Fatalf("AcceptSteadyStateRate = %d, want 166", cfg.AcceptSteadyStateRate)
	}
}

func TestAutoConfigureAcceptRateLimits_SteadyStateHighPercent(t *testing.T) {
	cfg := &Config{
		MaxConns:                          10000,
		MaxAcceptsPerSecond:               defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:                    defaultMaxAcceptBurst,
		AcceptSteadyStateRate:             defaultAcceptSteadyStateRate,
		AcceptSteadyStateReconnectPercent: 10.0, // 10% expected reconnections
		AcceptSteadyStateReconnectWindow:  60,   // over 60 seconds
		AcceptReconnectWindow:             15,
		AcceptBurstWindow:                 5,
	}
	autoConfigureAcceptRateLimits(cfg, false)
	// For 10000 miners × 10% = 1000 expected reconnects over 60s = 16.66/sec
	if cfg.AcceptSteadyStateRate != 16 {
		t.Fatalf("AcceptSteadyStateRate = %d, want 16", cfg.AcceptSteadyStateRate)
	}
}

func TestAutoConfigureAcceptRateLimits_SteadyStateShortWindow(t *testing.T) {
	cfg := &Config{
		MaxConns:                          10000,
		MaxAcceptsPerSecond:               defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:                    defaultMaxAcceptBurst,
		AcceptSteadyStateRate:             defaultAcceptSteadyStateRate,
		AcceptSteadyStateReconnectPercent: 5.0, // 5% expected reconnections
		AcceptSteadyStateReconnectWindow:  30,  // over 30 seconds
		AcceptReconnectWindow:             15,
		AcceptBurstWindow:                 5,
	}
	autoConfigureAcceptRateLimits(cfg, false)
	// For 10000 miners × 5% = 500 expected reconnects over 30s = 16.66/sec
	if cfg.AcceptSteadyStateRate != 16 {
		t.Fatalf("AcceptSteadyStateRate = %d, want 16", cfg.AcceptSteadyStateRate)
	}
}

func TestAutoConfigureAcceptRateLimits_SteadyStateAutoMode(t *testing.T) {
	cfg := &Config{
		MaxConns:                          10000,
		MaxAcceptsPerSecond:               defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:                    defaultMaxAcceptBurst,
		AcceptSteadyStateRate:             100, // explicitly set to non-default
		AcceptSteadyStateReconnectPercent: 5.0,
		AcceptSteadyStateReconnectWindow:  60,
		AutoAcceptRateLimits:              true, // auto mode should override
		AcceptReconnectWindow:             15,
		AcceptBurstWindow:                 5,
	}
	autoConfigureAcceptRateLimits(cfg, false)
	// Auto mode should override explicit value
	// For 10000 miners × 5% = 500 expected reconnects over 60s = 8.33/sec
	if cfg.AcceptSteadyStateRate != 8 {
		t.Fatalf("AcceptSteadyStateRate = %d, want 8 (auto mode should override)", cfg.AcceptSteadyStateRate)
	}
}

func TestAutoConfigureAcceptRateLimits_SteadyStateRespectsExplicit(t *testing.T) {
	cfg := &Config{
		MaxConns:                          10000,
		MaxAcceptsPerSecond:               defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:                    defaultMaxAcceptBurst,
		AcceptSteadyStateRate:             75, // explicitly set to non-default
		AcceptSteadyStateReconnectPercent: 5.0,
		AcceptSteadyStateReconnectWindow:  60,
		AutoAcceptRateLimits:              false, // auto mode disabled
		AcceptReconnectWindow:             15,
		AcceptBurstWindow:                 5,
	}
	autoConfigureAcceptRateLimits(cfg, false)
	// Should not change because it's not the default value and auto mode is off
	if cfg.AcceptSteadyStateRate != 75 {
		t.Fatalf("AcceptSteadyStateRate = %d, want 75 (should not change)", cfg.AcceptSteadyStateRate)
	}
}

func TestAutoConfigureAcceptRateLimits_SteadyStateAbove1000Capped(t *testing.T) {
	cfg := &Config{
		MaxConns:                          500000,
		MaxAcceptsPerSecond:               defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:                    defaultMaxAcceptBurst,
		AcceptSteadyStateRate:             defaultAcceptSteadyStateRate,
		AcceptSteadyStateReconnectPercent: 10.0, // 10% expected reconnections
		AcceptSteadyStateReconnectWindow:  30,   // over 30 seconds
		AcceptReconnectWindow:             15,
		AcceptBurstWindow:                 5,
	}
	autoConfigureAcceptRateLimits(cfg, false)
	// For 500000 miners × 10% = 50000 expected reconnects over 30s = 1666/sec
	// Should be capped at 1000
	if cfg.AcceptSteadyStateRate != 1000 {
		t.Fatalf("AcceptSteadyStateRate = %d, want 1000 (capped)", cfg.AcceptSteadyStateRate)
	}
}

func TestRewriteConfigFile_BackupAndAtomic(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")

	initial := []byte("initial config data")
	if err := os.WriteFile(cfgPath, initial, 0o644); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	if err := rewriteConfigFile(cfgPath, defaultConfig()); err != nil {
		t.Fatalf("rewrite config: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !bytes.Contains(data, []byte(`"pool_fee_percent"`)) {
		t.Fatalf("config missing expected field")
	}

	bakData, err := os.ReadFile(cfgPath + ".bak")
	if err != nil {
		t.Fatalf("read .bak: %v", err)
	}
	if !bytes.Equal(bakData, initial) {
		t.Fatalf(".bak content mismatch: got %q, want %q", bakData, initial)
	}

	if err := os.WriteFile(cfgPath+".bak", []byte("stale backup"), 0o644); err != nil {
		t.Fatalf("write stale backup: %v", err)
	}

	cfg := defaultConfig()
	cfg.PayoutAddress = "1Pool"
	if err := rewriteConfigFile(cfgPath, cfg); err != nil {
		t.Fatalf("rewrite config again: %v", err)
	}

	secondBak, err := os.ReadFile(cfgPath + ".bak")
	if err != nil {
		t.Fatalf("read updated .bak: %v", err)
	}
	if bytes.Contains(secondBak, []byte("stale backup")) {
		t.Fatalf("stale backup persisted: %q", secondBak)
	}
	if !bytes.Contains(secondBak, []byte(`"payout_address": ""`)) {
		t.Fatalf(".bak missing previous config content")
	}
}
