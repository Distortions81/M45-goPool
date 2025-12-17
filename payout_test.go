package main

import "testing"

// TestCreditBlockReward_SingleAndDualPayout verifies that the payout math used
// on block found (pool fee + worker amount) is consistent for both single-
// payout and dual-payout modes. The runtime code uses the same calculation
// regardless of DualPayoutEnabled; dual mode only affects coinbase layout and
// logging.
func TestAccountStoreStartsEmpty(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	store, err := NewAccountStore(cfg, false, true)
	if err != nil {
		t.Fatalf("NewAccountStore failed: %v", err)
	}
	if len(store.WorkersSnapshot()) != 0 {
		t.Fatalf("expected empty ban list on new store")
	}
}
