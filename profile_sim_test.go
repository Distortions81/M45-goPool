package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math/big"
	"os"
	"runtime/pprof"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestGenerateGoProfileWithSimulatedMiners runs a short, gated CPU profile by
// spinning up a configurable number of goroutines that mimic miner work. The
// profile output is useful for generating PGO data without running a full
// production pool.
func TestGenerateGoProfileWithSimulatedMiners(t *testing.T) {
	if os.Getenv("GO_PROFILE_SIMULATED_MINERS") == "" {
		t.Skip("set GO_PROFILE_SIMULATED_MINERS=1 to run this profile generator")
	}

	minerCount := parseEnvInt("GO_PROFILE_MINER_COUNT", 16)
	if minerCount <= 0 {
		minerCount = 16
	}
	duration := parseEnvDuration("GO_PROFILE_DURATION", 5*time.Second)
	profilePath := os.Getenv("GO_PROFILE_OUTPUT")
	if profilePath == "" {
		profilePath = "default.pgo"
	}

	t.Logf("profiling %d simulated miners for %s -> %s", minerCount, duration, profilePath)
	out, err := os.Create(profilePath)
	if err != nil {
		t.Fatalf("create profile file: %v", err)
	}
	defer out.Close()

	if err := pprof.StartCPUProfile(out); err != nil {
		t.Fatalf("start cpu profile: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < minerCount; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			simulateMinerLoad(ctx, id)
		}(i)
	}

	wg.Wait()
	pprof.StopCPUProfile()

	info, err := os.Stat(profilePath)
	if err != nil {
		t.Fatalf("stat profile file: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("profile file is empty")
	}
}

func parseEnvInt(key string, def int) int {
	if raw := os.Getenv(key); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			return v
		}
	}
	return def
}

func parseEnvDuration(key string, def time.Duration) time.Duration {
	if raw := os.Getenv(key); raw != "" {
		if v, err := time.ParseDuration(raw); err == nil {
			return v
		}
	}
	return def
}

func simulateMinerLoad(ctx context.Context, minerID int) {
	target := new(big.Int).Lsh(big.NewInt(1), 244)
	increment := big.NewInt(1)
	var buf [32]byte
	val := new(big.Int)
	nonce := uint64(0)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		nonce++
		binary.LittleEndian.PutUint64(buf[0:8], uint64(minerID))
		binary.LittleEndian.PutUint64(buf[8:16], nonce)
		binary.LittleEndian.PutUint64(buf[16:24], uint64(time.Now().UnixNano()))

		sum := sha256.Sum256(buf[:])
		val.SetBytes(sum[:])
		if val.Cmp(target) < 0 {
			val.Add(val, increment)
		} else {
			val.Rsh(val, 1)
		}
	}
}
