package main

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func samplePoissonArrival(rng *rand.Rand, ratePerSecond float64) time.Duration {
	if ratePerSecond <= 0 {
		return 24 * time.Hour
	}
	u := rng.Float64()
	if u <= 0 {
		u = math.SmallestNonzeroFloat64
	}
	seconds := -math.Log(u) / ratePerSecond
	return time.Duration(seconds * float64(time.Second))
}

type emaQualityMetrics struct {
	settleSeconds float64
	preSwitchMAPE float64
}

func simulateEMAQuality(seed int64, tauSeconds float64, hashrate1, hashrate2 float64) emaQualityMetrics {
	cfg := Config{
		HashrateEMATauSeconds: tauSeconds,
	}
	mc := &MinerConn{cfg: cfg}

	targetShares := 7.0
	targetDiff := (hashrate1 / hashPerShare) * 60.0 / targetShares
	if targetDiff <= 0 {
		targetDiff = 1
	}
	shareRate1PerMin := (hashrate1 / hashPerShare) * 60.0 / targetDiff
	shareRate2PerMin := (hashrate2 / hashPerShare) * 60.0 / targetDiff
	rate1 := shareRate1PerMin / 60.0
	rate2 := shareRate2PerMin / 60.0

	start := time.Unix(1_700_000_000, 0)
	switchAt := start.Add(10 * time.Minute)
	end := start.Add(30 * time.Minute)
	now := start
	rng := rand.New(rand.NewSource(seed))

	settleAt := time.Time{}
	preErrSum := 0.0
	preErrN := 0

	for now.Before(end) {
		phaseRate := rate1
		trueHashrate := hashrate1
		if !now.Before(switchAt) {
			phaseRate = rate2
			trueHashrate = hashrate2
		}
		now = now.Add(samplePoissonArrival(rng, phaseRate))
		if now.After(end) {
			break
		}

		mc.statsMu.Lock()
		mc.updateHashrateLocked(targetDiff, now)
		est := mc.rollingHashrateValue
		mc.statsMu.Unlock()
		if est <= 0 {
			continue
		}

		if now.Before(switchAt) && now.After(switchAt.Add(-5*time.Minute)) {
			preErrSum += math.Abs(est-trueHashrate) / trueHashrate
			preErrN++
		}
		if !now.Before(switchAt) && settleAt.IsZero() {
			relErr := math.Abs(est-hashrate2) / hashrate2
			if relErr <= 0.10 {
				settleAt = now
			}
		}
	}

	metrics := emaQualityMetrics{
		settleSeconds: end.Sub(switchAt).Seconds(),
		preSwitchMAPE: 1.0,
	}
	if !settleAt.IsZero() {
		metrics.settleSeconds = settleAt.Sub(switchAt).Seconds()
	}
	if preErrN > 0 {
		metrics.preSwitchMAPE = preErrSum / float64(preErrN)
	}
	return metrics
}

func TestTuning_HashrateEMASweep(t *testing.T) {
	type candidate struct {
		name string
		tau  float64
	}
	candidates := []candidate{
		{name: "tau600", tau: 600},
		{name: "tau300", tau: 300},
		{name: "tau180", tau: 180},
	}
	// Cover both low and high hashrate profiles.
	profiles := []float64{200e3, 3e6, 1.2e12, 90e12}
	const trials = 48

	type aggregate struct {
		settle float64
		mape   float64
	}
	agg := map[string]aggregate{}
	for _, c := range candidates {
		sumSettle := 0.0
		sumMAPE := 0.0
		count := 0
		for _, hr := range profiles {
			for i := range trials {
				m := simulateEMAQuality(int64(5000+i)+int64(hr/1e3), c.tau, hr, hr*2)
				sumSettle += m.settleSeconds
				sumMAPE += m.preSwitchMAPE
				count++
			}
		}
		agg[c.name] = aggregate{
			settle: sumSettle / float64(count),
			mape:   sumMAPE / float64(count),
		}
		t.Logf("EMA %s: avg_settle=%.1fs avg_preswitch_mape=%.3f", c.name, agg[c.name].settle, agg[c.name].mape)
	}

	// tau=300 should be meaningfully faster than tau=600 while staying stable.
	if agg["tau300"].settle >= agg["tau600"].settle*0.85 {
		t.Fatalf("tau300 settle %.1fs not sufficiently faster than tau600 %.1fs", agg["tau300"].settle, agg["tau600"].settle)
	}
	if agg["tau300"].mape > agg["tau600"].mape*1.35 {
		t.Fatalf("tau300 mape %.3f too noisy vs tau600 %.3f", agg["tau300"].mape, agg["tau600"].mape)
	}
}

func simulateBan(seed int64, threshold int, window time.Duration, invalidRatePerMin float64, duration time.Duration) (banned bool, banAfter time.Duration) {
	mc := &MinerConn{
		cfg: Config{
			BanInvalidSubmissionsAfter:    threshold,
			BanInvalidSubmissionsWindow:   window,
			BanInvalidSubmissionsDuration: 15 * time.Minute,
		},
	}
	now := time.Unix(1_700_000_000, 0)
	end := now.Add(duration)
	rng := rand.New(rand.NewSource(seed))

	ratePerSecond := invalidRatePerMin / 60.0
	for now.Before(end) {
		now = now.Add(samplePoissonArrival(rng, ratePerSecond))
		if now.After(end) {
			break
		}
		if hit, _ := mc.noteInvalidSubmit(now, rejectInvalidNonce); hit {
			return true, now.Sub(time.Unix(1_700_000_000, 0))
		}
	}
	return false, duration
}

func TestTuning_AutoBanSweep(t *testing.T) {
	const trials = 128
	window := 5 * time.Minute
	duration := 20 * time.Minute

	benignRatePerMin := 0.2
	attackRatePerMin := 30.0

	type result struct {
		benignBans int
		attackBans int
		attackAvg  float64
	}

	run := func(threshold int) result {
		out := result{}
		attackSum := 0.0
		for i := range trials {
			if banned, _ := simulateBan(int64(9000+i), threshold, window, benignRatePerMin, duration); banned {
				out.benignBans++
			}
			if banned, after := simulateBan(int64(12000+i), threshold, window, attackRatePerMin, duration); banned {
				out.attackBans++
				attackSum += after.Seconds()
			}
		}
		if out.attackBans > 0 {
			out.attackAvg = attackSum / float64(out.attackBans)
		}
		return out
	}

	r60 := run(60)
	r40 := run(40)
	t.Logf("autoban threshold=60: benign_bans=%d/%d attack_bans=%d/%d attack_avg=%.1fs", r60.benignBans, trials, r60.attackBans, trials, r60.attackAvg)
	t.Logf("autoban threshold=40: benign_bans=%d/%d attack_bans=%d/%d attack_avg=%.1fs", r40.benignBans, trials, r40.attackBans, trials, r40.attackAvg)

	if r40.benignBans > 0 {
		t.Fatalf("threshold 40 produced benign bans: %d", r40.benignBans)
	}
	if r40.attackBans < trials {
		t.Fatalf("threshold 40 failed to reliably ban attacker: %d/%d", r40.attackBans, trials)
	}
	if r40.attackAvg >= r60.attackAvg*0.80 {
		t.Fatalf("threshold 40 ban response %.1fs not sufficiently faster than threshold 60 %.1fs", r40.attackAvg, r60.attackAvg)
	}
}
