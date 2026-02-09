package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/wire"
)

func parseCoinbaseOutputs(t *testing.T, raw []byte) []*wire.TxOut {
	t.Helper()
	var tx wire.MsgTx
	if err := tx.Deserialize(bytes.NewReader(raw)); err != nil {
		t.Fatalf("deserialize coinbase: %v", err)
	}
	return tx.TxOut
}

func TestValidateCoinbasePayoutOutputs_Errors(t *testing.T) {
	t.Run("empty_outputs", func(t *testing.T) {
		err := validateCoinbasePayoutOutputs(nil)
		if err == nil || !strings.Contains(err.Error(), "at least one payout output") {
			t.Fatalf("expected empty outputs error, got %v", err)
		}
	})

	t.Run("too_many_outputs", func(t *testing.T) {
		outputs := make([]coinbasePayoutOutput, maxCoinbasePayoutOutputs+1)
		for i := range outputs {
			outputs[i] = coinbasePayoutOutput{Script: []byte{0x51}, Value: 1}
		}
		err := validateCoinbasePayoutOutputs(outputs)
		if err == nil || !strings.Contains(err.Error(), "too many payout outputs") {
			t.Fatalf("expected too many outputs error, got %v", err)
		}
	})

	t.Run("missing_script", func(t *testing.T) {
		err := validateCoinbasePayoutOutputs([]coinbasePayoutOutput{{Script: nil, Value: 1}})
		if err == nil || !strings.Contains(err.Error(), "script required") {
			t.Fatalf("expected missing script error, got %v", err)
		}
	})

	t.Run("negative_value", func(t *testing.T) {
		err := validateCoinbasePayoutOutputs([]coinbasePayoutOutput{{Script: []byte{0x51}, Value: -1}})
		if err == nil || !strings.Contains(err.Error(), "cannot be negative") {
			t.Fatalf("expected negative value error, got %v", err)
		}
	})
}

func TestComputeCoinbasePayouts_MultipleFeeSlicesAndSubslice(t *testing.T) {
	_, poolScript := generateTestWallet(t)
	_, donationScript := generateTestWallet(t)
	_, opsScript := generateTestWallet(t)
	_, workerScript := generateTestWallet(t)

	plan := coinbasePayoutPlan{
		TotalValue:      1000,
		RemainderScript: workerScript,
		FeeSlices: []coinbaseFeeSlice{
			{
				Script:  poolScript,
				Percent: 2.0,
				SubSlices: []coinbaseFeeSubSlice{
					{Script: donationScript, Percent: 50.0},
				},
			},
			{
				Script:  opsScript,
				Percent: 1.0,
			},
		},
		RequireRemainderPositive: true,
	}

	payouts, breakdown, err := computeCoinbasePayouts(plan)
	if err != nil {
		t.Fatalf("computeCoinbasePayouts error: %v", err)
	}
	if len(payouts) != 4 {
		t.Fatalf("expected 4 payouts, got %d", len(payouts))
	}

	// pool fee 20 split 50/50 => pool keeps 10 + donation 10; ops fee 10; worker 970.
	if payouts[0].Value != 10 || !bytes.Equal(payouts[0].Script, poolScript) {
		t.Fatalf("pool keep mismatch: value=%d", payouts[0].Value)
	}
	if payouts[1].Value != 10 || !bytes.Equal(payouts[1].Script, donationScript) {
		t.Fatalf("donation mismatch: value=%d", payouts[1].Value)
	}
	if payouts[2].Value != 10 || !bytes.Equal(payouts[2].Script, opsScript) {
		t.Fatalf("ops mismatch: value=%d", payouts[2].Value)
	}
	if payouts[3].Value != 970 || !bytes.Equal(payouts[3].Script, workerScript) {
		t.Fatalf("worker mismatch: value=%d", payouts[3].Value)
	}

	if len(breakdown.FeeSlices) != 2 {
		t.Fatalf("expected 2 fee slices in breakdown, got %d", len(breakdown.FeeSlices))
	}
	if breakdown.FeeSlices[0].FeeTotal != 20 || breakdown.FeeSlices[1].FeeTotal != 10 {
		t.Fatalf("unexpected fee totals: %+v", breakdown.FeeSlices)
	}
	if breakdown.RemainderValue != 970 {
		t.Fatalf("remainder mismatch: got %d, want 970", breakdown.RemainderValue)
	}
}

func TestSerializeCoinbaseTxPayoutsPredecoded_PaymentCountsAndOrdering(t *testing.T) {
	height := int64(101)
	ex1 := []byte{0x01, 0x02, 0x03, 0x04}
	ex2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	templateExtra := 8
	commitment := []byte{0x6a, 0x01, 0x00}

	tests := []struct {
		name                 string
		payouts              []coinbasePayoutOutput
		withCommitment       bool
		wantOrderedScripts   [][]byte
		wantOrderedValues    []int64
		wantTotalOutputCount int
	}{
		{
			name: "one_payment",
			payouts: []coinbasePayoutOutput{
				{Script: []byte{0x51}, Value: 100},
			},
			wantOrderedScripts:   [][]byte{{0x51}},
			wantOrderedValues:    []int64{100},
			wantTotalOutputCount: 1,
		},
		{
			name: "two_payments",
			payouts: []coinbasePayoutOutput{
				{Script: []byte{0x51}, Value: 20},
				{Script: []byte{0x52}, Value: 980},
			},
			wantOrderedScripts:   [][]byte{{0x52}, {0x51}},
			wantOrderedValues:    []int64{980, 20},
			wantTotalOutputCount: 2,
		},
		{
			name: "five_payments_with_stable_tie_order",
			payouts: []coinbasePayoutOutput{
				{Script: []byte{0x51}, Value: 7},
				{Script: []byte{0x52}, Value: 7},
				{Script: []byte{0x53}, Value: 3},
				{Script: []byte{0x54}, Value: 2},
				{Script: []byte{0x55}, Value: 1},
			},
			withCommitment:       true,
			wantOrderedScripts:   [][]byte{{0x51}, {0x52}, {0x53}, {0x54}, {0x55}},
			wantOrderedValues:    []int64{7, 7, 3, 2, 1},
			wantTotalOutputCount: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c []byte
			if tt.withCommitment {
				c = commitment
			}
			raw, _, err := serializeCoinbaseTxPayoutsPredecoded(
				height,
				ex1,
				ex2,
				templateExtra,
				tt.payouts,
				c,
				nil,
				"test",
				0,
			)
			if err != nil {
				t.Fatalf("serializeCoinbaseTxPayoutsPredecoded error: %v", err)
			}

			outs := parseCoinbaseOutputs(t, raw)
			if len(outs) != tt.wantTotalOutputCount {
				t.Fatalf("output count mismatch: got %d, want %d", len(outs), tt.wantTotalOutputCount)
			}

			payoutStart := 0
			if tt.withCommitment {
				if outs[0].Value != 0 || !bytes.Equal(outs[0].PkScript, commitment) {
					t.Fatalf("commitment output mismatch")
				}
				payoutStart = 1
			}

			for i := range tt.wantOrderedValues {
				got := outs[payoutStart+i]
				if got.Value != tt.wantOrderedValues[i] {
					t.Fatalf("output %d value mismatch: got %d, want %d", i, got.Value, tt.wantOrderedValues[i])
				}
				if !bytes.Equal(got.PkScript, tt.wantOrderedScripts[i]) {
					t.Fatalf("output %d script mismatch: got %x, want %x", i, got.PkScript, tt.wantOrderedScripts[i])
				}
			}
		})
	}
}

