package main

import (
	"testing"
)

func TestStartupPrimedDifficulty_LowersBeforeFirstShare(t *testing.T) {
	mc := &MinerConn{
		cfg: Config{
			MinDifficulty:             1,
			DifficultyStepGranularity: 4,
		},
		vardiff: VarDiffConfig{
			MinDiff: 1,
		},
	}
	got := mc.startupPrimedDifficulty(1000)
	if got >= 1000 {
		t.Fatalf("got %.8g want < 1000 before first accepted share", got)
	}
}

func TestStartupPrimedDifficulty_DisabledAfterAcceptedShare(t *testing.T) {
	mc := &MinerConn{
		cfg: Config{
			MinDifficulty:             1,
			DifficultyStepGranularity: 4,
		},
		vardiff: VarDiffConfig{
			MinDiff: 1,
		},
	}
	mc.statsMu.Lock()
	mc.stats.Accepted = 1
	mc.statsMu.Unlock()
	got := mc.startupPrimedDifficulty(1000)
	if got != 1000 {
		t.Fatalf("got %.8g want %.8g after accepted shares", got, 1000.0)
	}
}

func TestStartupPrimedDifficulty_HonorsLockSuggestedDifficulty(t *testing.T) {
	mc := &MinerConn{
		cfg: Config{
			LockSuggestedDifficulty:   true,
			MinDifficulty:             1,
			DifficultyStepGranularity: 4,
		},
		vardiff: VarDiffConfig{
			MinDiff: 1,
		},
	}
	got := mc.startupPrimedDifficulty(1000)
	if got != 1000 {
		t.Fatalf("got %.8g want %.8g when suggested difficulty is locked", got, 1000.0)
	}
}
