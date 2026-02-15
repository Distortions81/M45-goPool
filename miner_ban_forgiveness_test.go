package main

import (
	"testing"
	"time"
)

func TestNoteInvalidSubmit_ValidSharesReduceEffectiveInvalids(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mc := &MinerConn{
		cfg: Config{
			BanInvalidSubmissionsAfter:  4,
			BanInvalidSubmissionsWindow: 5 * time.Minute,
		},
	}
	// 4 invalids would normally ban at threshold=4.
	for i := range 3 {
		if banned, _ := mc.noteInvalidSubmit(now.Add(time.Duration(i)*time.Second), rejectInvalidNonce); banned {
			t.Fatalf("unexpected early ban at invalid %d", i+1)
		}
	}
	// Two valid shares forgive one invalid (bounded).
	mc.noteValidSubmit(now.Add(4 * time.Second))
	mc.noteValidSubmit(now.Add(5 * time.Second))

	if banned, eff := mc.noteInvalidSubmit(now.Add(6*time.Second), rejectInvalidNonce); banned {
		t.Fatalf("unexpected ban with forgiveness applied; effective=%d", eff)
	}
}

func TestNoteInvalidSubmit_ForgivenessIsCapped(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	threshold := 10
	mc := &MinerConn{
		cfg: Config{
			BanInvalidSubmissionsAfter:  threshold,
			BanInvalidSubmissionsWindow: 5 * time.Minute,
		},
	}
	// Large amount of valid shares should not erase more than cap (50%).
	for i := range 200 {
		mc.noteValidSubmit(now.Add(time.Duration(i) * time.Second))
	}
	// With threshold 10 and cap 50%, effective forgiveness maxes at 5.
	// So 15 invalids should still ban.
	for i := range 14 {
		if banned, _ := mc.noteInvalidSubmit(now.Add(time.Duration(300+i)*time.Second), rejectInvalidNonce); banned {
			t.Fatalf("unexpected early ban at invalid %d", i+1)
		}
	}
	if banned, eff := mc.noteInvalidSubmit(now.Add(320*time.Second), rejectInvalidNonce); !banned {
		t.Fatalf("expected ban at capped effective threshold, effective=%d", eff)
	}
}
