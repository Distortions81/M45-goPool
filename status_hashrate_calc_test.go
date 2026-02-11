package main

import (
	"testing"
	"time"
)

func TestWorkerHashrateEstimate_UsesWindowDifficultyWhenRollingZero(t *testing.T) {
	now := time.Unix(1700000000, 0)
	view := WorkerView{
		RollingHashrate:  0,
		WindowStart:      now.Add(-60 * time.Second),
		WindowDifficulty: 120,
	}

	got := workerHashrateEstimate(view, now)
	want := (120.0 * hashPerShare) / 60.0
	if !almostEqualFloat64(got, want, 1e-9*want) {
		t.Fatalf("hashrate=%.6f want %.6f", got, want)
	}
}

func TestWorkerHashrateEstimate_UsesDifficultyTimesShareRateAsLastFallback(t *testing.T) {
	view := WorkerView{
		RollingHashrate:  0,
		WindowStart:      time.Time{},
		WindowDifficulty: 0,
		ShareRate:        6,
		Difficulty:       2,
	}

	got := workerHashrateEstimate(view, time.Unix(1700000000, 0))
	want := (2.0 * hashPerShare * 6.0) / 60.0
	if !almostEqualFloat64(got, want, 1e-9*want) {
		t.Fatalf("hashrate=%.6f want %.6f", got, want)
	}
}

func TestWorkerHashrateEstimate_DoesNotReportBeforeBootstrapWindow(t *testing.T) {
	now := time.Unix(1700000000, 0)
	view := WorkerView{
		RollingHashrate:  0,
		WindowStart:      now.Add(-(initialHashrateEMATau - time.Second)),
		WindowDifficulty: 120,
		ShareRate:        6,
		Difficulty:       2,
	}

	if got := workerHashrateEstimate(view, now); got != 0 {
		t.Fatalf("hashrate=%.6f want 0 before bootstrap window", got)
	}
}
