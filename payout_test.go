package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestComputeCoinbasePayouts_OnePaymentOut(t *testing.T) {
	workerAddr, workerScript := generateTestWallet(t)
	_ = workerAddr
	plan := coinbasePayoutPlan{
		TotalValue:               1000,
		RemainderScript:          workerScript,
		RequireRemainderPositive: true,
	}

	payouts, breakdown, err := computeCoinbasePayouts(plan)
	if err != nil {
		t.Fatalf("computeCoinbasePayouts error: %v", err)
	}
	if len(payouts) != 1 {
		t.Fatalf("expected 1 output, got %d", len(payouts))
	}
	if payouts[0].Value != 1000 {
		t.Fatalf("single payout value mismatch: got %d, want 1000", payouts[0].Value)
	}
	if !bytes.Equal(payouts[0].Script, workerScript) {
		t.Fatalf("single payout script mismatch")
	}
	if breakdown.RemainderValue != 1000 {
		t.Fatalf("remainder mismatch: got %d, want 1000", breakdown.RemainderValue)
	}
}

func TestComputeCoinbasePayouts_TwoPaymentsOut(t *testing.T) {
	_, poolScript := generateTestWallet(t)
	_, workerScript := generateTestWallet(t)
	plan := coinbasePayoutPlan{
		TotalValue:      1000,
		RemainderScript: workerScript,
		FeeSlices: []coinbaseFeeSlice{
			{Script: poolScript, Percent: 2.0},
		},
		RequireRemainderPositive: true,
	}

	payouts, breakdown, err := computeCoinbasePayouts(plan)
	if err != nil {
		t.Fatalf("computeCoinbasePayouts error: %v", err)
	}
	if len(payouts) != 2 {
		t.Fatalf("expected 2 outputs, got %d", len(payouts))
	}
	if payouts[0].Value != 20 {
		t.Fatalf("pool fee output mismatch: got %d, want 20", payouts[0].Value)
	}
	if !bytes.Equal(payouts[0].Script, poolScript) {
		t.Fatalf("pool fee script mismatch")
	}
	if payouts[1].Value != 980 {
		t.Fatalf("worker output mismatch: got %d, want 980", payouts[1].Value)
	}
	if !bytes.Equal(payouts[1].Script, workerScript) {
		t.Fatalf("worker script mismatch")
	}
	if len(breakdown.FeeSlices) != 1 || breakdown.FeeSlices[0].FeeTotal != 20 {
		t.Fatalf("fee breakdown mismatch: %+v", breakdown.FeeSlices)
	}
}

func TestComputeCoinbasePayouts_ThreePaymentsOut(t *testing.T) {
	_, poolScript := generateTestWallet(t)
	_, donationScript := generateTestWallet(t)
	_, workerScript := generateTestWallet(t)
	plan := coinbasePayoutPlan{
		TotalValue:      1000,
		RemainderScript: workerScript,
		FeeSlices: []coinbaseFeeSlice{
			{
				Script:  poolScript,
				Percent: 2.0,
				SubSlices: []coinbaseFeeSubSlice{
					{Script: donationScript, Percent: 25.0},
				},
			},
		},
		RequireRemainderPositive: true,
	}

	payouts, breakdown, err := computeCoinbasePayouts(plan)
	if err != nil {
		t.Fatalf("computeCoinbasePayouts error: %v", err)
	}
	if len(payouts) != 3 {
		t.Fatalf("expected 3 outputs, got %d", len(payouts))
	}
	if payouts[0].Value != 15 || !bytes.Equal(payouts[0].Script, poolScript) {
		t.Fatalf("pool keeps output mismatch: value=%d", payouts[0].Value)
	}
	if payouts[1].Value != 5 || !bytes.Equal(payouts[1].Script, donationScript) {
		t.Fatalf("donation output mismatch: value=%d", payouts[1].Value)
	}
	if payouts[2].Value != 980 || !bytes.Equal(payouts[2].Script, workerScript) {
		t.Fatalf("worker output mismatch: value=%d", payouts[2].Value)
	}
	if len(breakdown.FeeSlices) != 1 {
		t.Fatalf("expected 1 fee slice breakdown, got %d", len(breakdown.FeeSlices))
	}
	if breakdown.FeeSlices[0].FeeTotal != 20 {
		t.Fatalf("fee total mismatch: got %d, want 20", breakdown.FeeSlices[0].FeeTotal)
	}
	if breakdown.FeeSlices[0].ParentValue != 15 {
		t.Fatalf("pool parent value mismatch: got %d, want 15", breakdown.FeeSlices[0].ParentValue)
	}
	if len(breakdown.FeeSlices[0].SubValues) != 1 || breakdown.FeeSlices[0].SubValues[0] != 5 {
		t.Fatalf("subslice mismatch: %+v", breakdown.FeeSlices[0].SubValues)
	}
}

func TestDualPayoutParams_SamePoolAndWorkerWalletDisablesDualPayout(t *testing.T) {
	poolAddr, poolScript := generateTestWallet(t)
	job := &Job{
		PayoutScript:  poolScript,
		CoinbaseValue: 1000,
	}
	workerName := poolAddr + ".worker"
	mc := &MinerConn{
		cfg: Config{
			PayoutAddress:  poolAddr,
			PoolFeePercent: 2.0,
		},
	}
	mc.setWorkerWallet(workerName, poolAddr, poolScript)

	_, _, _, _, ok := mc.dualPayoutParams(job, workerName)
	if ok {
		t.Fatalf("expected dual payout to be disabled when pool and worker wallets match")
	}
}

func TestDualPayoutParams_DifferentWalletEnablesDualPayout(t *testing.T) {
	poolAddr, poolScript := generateTestWallet(t)
	workerName, workerAddr, workerScript := generateTestWorker(t)
	job := &Job{
		PayoutScript:  poolScript,
		CoinbaseValue: 1000,
	}
	mc := &MinerConn{
		cfg: Config{
			PayoutAddress:  poolAddr,
			PoolFeePercent: 2.0,
		},
	}
	mc.setWorkerWallet(workerName, workerAddr, workerScript)

	gotPoolScript, gotWorkerScript, gotTotal, gotFeePct, ok := mc.dualPayoutParams(job, workerName)
	if !ok {
		t.Fatalf("expected dual payout to be enabled for different wallets")
	}
	if !bytes.Equal(gotPoolScript, poolScript) {
		t.Fatalf("pool script mismatch")
	}
	if !bytes.Equal(gotWorkerScript, workerScript) {
		t.Fatalf("worker script mismatch")
	}
	if gotTotal != 1000 {
		t.Fatalf("total mismatch: got %d, want 1000", gotTotal)
	}
	if gotFeePct != 2.0 {
		t.Fatalf("fee percent mismatch: got %v, want 2.0", gotFeePct)
	}
}

func TestComputeCoinbasePayouts_PoolFeePercentages(t *testing.T) {
	_, poolScript := generateTestWallet(t)
	_, workerScript := generateTestWallet(t)

	tests := []struct {
		name          string
		total         int64
		poolPercent   float64
		requireWorker bool
		wantPool      int64
		wantWorker    int64
		wantErrLike   string
	}{
		{
			name:          "zero_percent",
			total:         1000,
			poolPercent:   0,
			requireWorker: true,
			wantPool:      0,
			wantWorker:    1000,
		},
		{
			name:          "one_point_five_percent_rounds",
			total:         1000,
			poolPercent:   1.5,
			requireWorker: true,
			wantPool:      15,
			wantWorker:    985,
		},
		{
			name:          "negative_percent_clamps_to_zero",
			total:         1000,
			poolPercent:   -3.0,
			requireWorker: true,
			wantPool:      0,
			wantWorker:    1000,
		},
		{
			name:          "high_percent_clamps_to_99_99",
			total:         1000,
			poolPercent:   250.0,
			requireWorker: false,
			wantPool:      1000,
			wantWorker:    0,
		},
		{
			name:          "high_percent_with_positive_remainder_required_fails",
			total:         1000,
			poolPercent:   250.0,
			requireWorker: true,
			wantErrLike:   "remainder payout must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := coinbasePayoutPlan{
				TotalValue:      tt.total,
				RemainderScript: workerScript,
				FeeSlices: []coinbaseFeeSlice{
					{Script: poolScript, Percent: tt.poolPercent},
				},
				RequireRemainderPositive: tt.requireWorker,
			}
			payouts, _, err := computeCoinbasePayouts(plan)
			if tt.wantErrLike != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrLike) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErrLike, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("computeCoinbasePayouts error: %v", err)
			}
			if len(payouts) != 2 {
				t.Fatalf("expected 2 payouts, got %d", len(payouts))
			}
			if payouts[0].Value != tt.wantPool || !bytes.Equal(payouts[0].Script, poolScript) {
				t.Fatalf("pool payout mismatch: value=%d want=%d", payouts[0].Value, tt.wantPool)
			}
			if payouts[1].Value != tt.wantWorker || !bytes.Equal(payouts[1].Script, workerScript) {
				t.Fatalf("worker payout mismatch: value=%d want=%d", payouts[1].Value, tt.wantWorker)
			}
		})
	}
}

func TestComputeCoinbasePayouts_DonationPercentages(t *testing.T) {
	_, poolScript := generateTestWallet(t)
	_, donationScript := generateTestWallet(t)
	_, workerScript := generateTestWallet(t)

	tests := []struct {
		name            string
		total           int64
		poolPercent     float64
		donationPercent float64
		wantPoolKeep    int64
		wantDonation    int64
		wantWorker      int64
	}{
		{
			name:            "zero_donation",
			total:           1000,
			poolPercent:     2.0,
			donationPercent: 0,
			wantPoolKeep:    20,
			wantDonation:    0,
			wantWorker:      980,
		},
		{
			name:            "fractional_donation_rounds",
			total:           1000,
			poolPercent:     2.0,
			donationPercent: 12.5,
			wantPoolKeep:    17,
			wantDonation:    3,
			wantWorker:      980,
		},
		{
			name:            "half_donation",
			total:           1000,
			poolPercent:     2.0,
			donationPercent: 50.0,
			wantPoolKeep:    10,
			wantDonation:    10,
			wantWorker:      980,
		},
		{
			name:            "negative_donation_clamps_to_zero",
			total:           1000,
			poolPercent:     2.0,
			donationPercent: -5.0,
			wantPoolKeep:    20,
			wantDonation:    0,
			wantWorker:      980,
		},
		{
			name:            "over_100_donation_clamps_to_100",
			total:           1000,
			poolPercent:     2.0,
			donationPercent: 200.0,
			wantPoolKeep:    0,
			wantDonation:    20,
			wantWorker:      980,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := coinbasePayoutPlan{
				TotalValue:      tt.total,
				RemainderScript: workerScript,
				FeeSlices: []coinbaseFeeSlice{
					{
						Script:  poolScript,
						Percent: tt.poolPercent,
						SubSlices: []coinbaseFeeSubSlice{
							{Script: donationScript, Percent: tt.donationPercent},
						},
					},
				},
				RequireRemainderPositive: true,
			}

			payouts, breakdown, err := computeCoinbasePayouts(plan)
			if err != nil {
				t.Fatalf("computeCoinbasePayouts error: %v", err)
			}
			if len(payouts) != 3 {
				t.Fatalf("expected 3 payouts, got %d", len(payouts))
			}
			if payouts[0].Value != tt.wantPoolKeep || !bytes.Equal(payouts[0].Script, poolScript) {
				t.Fatalf("pool keep payout mismatch: value=%d want=%d", payouts[0].Value, tt.wantPoolKeep)
			}
			if payouts[1].Value != tt.wantDonation || !bytes.Equal(payouts[1].Script, donationScript) {
				t.Fatalf("donation payout mismatch: value=%d want=%d", payouts[1].Value, tt.wantDonation)
			}
			if payouts[2].Value != tt.wantWorker || !bytes.Equal(payouts[2].Script, workerScript) {
				t.Fatalf("worker payout mismatch: value=%d want=%d", payouts[2].Value, tt.wantWorker)
			}

			if len(breakdown.FeeSlices) != 1 {
				t.Fatalf("expected single fee breakdown, got %d", len(breakdown.FeeSlices))
			}
			if breakdown.FeeSlices[0].ParentValue != tt.wantPoolKeep {
				t.Fatalf("breakdown parent mismatch: got %d, want %d", breakdown.FeeSlices[0].ParentValue, tt.wantPoolKeep)
			}
			if len(breakdown.FeeSlices[0].SubValues) != 1 || breakdown.FeeSlices[0].SubValues[0] != tt.wantDonation {
				t.Fatalf("breakdown donation mismatch: %+v", breakdown.FeeSlices[0].SubValues)
			}
		})
	}
}

func TestDualPayoutParams_NonPositivePoolFeeDisablesDualPayout(t *testing.T) {
	poolAddr, poolScript := generateTestWallet(t)
	workerName, workerAddr, workerScript := generateTestWorker(t)
	job := &Job{
		PayoutScript:  poolScript,
		CoinbaseValue: 1000,
	}

	tests := []struct {
		name string
		fee  float64
	}{
		{name: "zero_fee", fee: 0},
		{name: "negative_fee", fee: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := &MinerConn{
				cfg: Config{
					PayoutAddress:  poolAddr,
					PoolFeePercent: tt.fee,
				},
			}
			mc.setWorkerWallet(workerName, workerAddr, workerScript)
			_, _, _, _, ok := mc.dualPayoutParams(job, workerName)
			if ok {
				t.Fatalf("expected dual payout disabled for pool fee %v", tt.fee)
			}
		})
	}
}
