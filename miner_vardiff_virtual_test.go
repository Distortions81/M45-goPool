package main

import (
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"
)

type virtualVardiffConfig struct {
	duration      time.Duration
	window        time.Duration
	initialDiff   float64
	minDiff       float64
	maxDiff       float64
	hashrateHps   float64
	idealDiff     float64
	networkDelay  time.Duration
	prepDelayMin  time.Duration
	prepDelayMax  time.Duration
	rngSeed       int64
	targetShares  float64
	vardiffStep   float64
	dampingFactor float64
}

type virtualVardiffResult struct {
	windowRates []float64
	windowDiffs []float64
	finalRate   float64
	finalDiff   float64
	adjustments int32
}

func runVirtualVardiff(cfg virtualVardiffConfig) virtualVardiffResult {
	if cfg.window <= 0 {
		cfg.window = 120 * time.Second
	}
	if cfg.duration <= 0 {
		cfg.duration = 7 * time.Minute
	}
	if cfg.initialDiff <= 0 {
		cfg.initialDiff = 1
	}
	if cfg.minDiff < 0 {
		cfg.minDiff = 0
	}
	if cfg.maxDiff > 0 && cfg.maxDiff < cfg.minDiff {
		cfg.maxDiff = cfg.minDiff
	}
	if cfg.hashrateHps <= 0 && cfg.idealDiff <= 0 {
		cfg.idealDiff = 1024
	}
	if cfg.targetShares <= 0 {
		cfg.targetShares = defaultVarDiffTargetSharesPerMin
	}
	if cfg.vardiffStep <= 1 {
		cfg.vardiffStep = defaultVarDiffStep
	}
	if cfg.dampingFactor <= 0 {
		cfg.dampingFactor = defaultVarDiffDampingFactor
	}
	if cfg.networkDelay <= 0 {
		cfg.networkDelay = 150 * time.Millisecond
	}
	if cfg.prepDelayMin <= 0 {
		cfg.prepDelayMin = 1 * time.Second
	}
	if cfg.prepDelayMax <= 0 {
		cfg.prepDelayMax = 20 * time.Second
	}
	if cfg.prepDelayMax < cfg.prepDelayMin {
		cfg.prepDelayMax = cfg.prepDelayMin
	}

	mc := &MinerConn{
		cfg: Config{
			DifficultyStepGranularity: 4,
			MinDifficulty:             cfg.minDiff,
			MaxDifficulty:             cfg.maxDiff,
		},
		vardiff: VarDiffConfig{
			MinDiff:            cfg.minDiff,
			MaxDiff:            cfg.maxDiff,
			TargetSharesPerMin: cfg.targetShares,
			AdjustmentWindow:   cfg.window,
			Step:               cfg.vardiffStep,
			DampingFactor:      cfg.dampingFactor,
		},
	}
	mc.vardiffState.initialEMAWindowDone.Store(true)
	atomicStoreFloat64(&mc.difficulty, cfg.initialDiff)

	networkHashrate := cfg.hashrateHps
	// Backward-compatible fallback for tests that specify idealDiff instead.
	if networkHashrate <= 0 {
		networkHashrate = (cfg.idealDiff * hashPerShare * cfg.targetShares) / 60.0
	}

	rng := rand.New(rand.NewSource(cfg.rngSeed))
	now := time.Unix(1_700_000_000, 0)
	end := now.Add(cfg.duration)
	lastDiffChange := time.Time{}
	currentPrepDelay := cfg.prepDelayMin

	out := virtualVardiffResult{}
	for now.Before(end) {
		curDiff := atomicLoadFloat64(&mc.difficulty)
		if curDiff <= 0 {
			curDiff = 1
		}

		// Simulate control-loop latency by mining this window at the effective
		// difficulty from just before now.
		effectiveNow := now.Add(-cfg.networkDelay)
		effectiveDiff := curDiff
		if !lastDiffChange.IsZero() && effectiveNow.Before(lastDiffChange) {
			if len(out.windowDiffs) > 0 {
				effectiveDiff = out.windowDiffs[len(out.windowDiffs)-1]
			}
		}

		// "True" shares/min from miner hashrate and effective assigned diff.
		trueRate := (networkHashrate / hashPerShare) * 60.0 / effectiveDiff

		// Build a synthetic snapshot for one adjustment window using Poisson
		// arrivals to emulate the randomness of real share discovery.
		activeFraction := 1.0
		if !lastDiffChange.IsZero() {
			warmupEnd := lastDiffChange.Add(currentPrepDelay)
			windowStart := now.Add(-cfg.window)
			if warmupEnd.After(windowStart) {
				overlap := warmupEnd.Sub(windowStart)
				if overlap > cfg.window {
					overlap = cfg.window
				}
				activeFraction = 1 - (overlap.Seconds() / cfg.window.Seconds())
				if activeFraction < 0 {
					activeFraction = 0
				}
			}
		}
		expectedShares := trueRate * cfg.window.Minutes() * activeFraction
		if expectedShares < 0 {
			expectedShares = 0
		}
		accepted := samplePoisson(rng, expectedShares)
		shareRate := 0.0
		if cfg.window.Minutes() > 0 {
			shareRate = float64(accepted) / cfg.window.Minutes()
		}
		rollingHashrate := 0.0
		if shareRate > 0 {
			rollingHashrate = (shareRate * hashPerShare * curDiff) / 60.0
		}
		snap := minerShareSnapshot{
			Stats: MinerStats{
				WindowStart:       now.Add(-cfg.window),
				WindowAccepted:    accepted,
				WindowSubmissions: accepted,
				WindowDifficulty:  float64(accepted) * curDiff,
			},
			RollingHashrate: rollingHashrate,
		}

		newDiff := mc.suggestedVardiff(now, snap)
		if math.Abs(newDiff-curDiff) > 1e-6 {
			atomicStoreFloat64(&mc.difficulty, newDiff)
			mc.lastDiffChange.Store(now.UnixNano())
			mc.vardiffState.vardiffAdjustments.Add(1)
			lastDiffChange = now
			currentPrepDelay = sampleUniformDuration(rng, cfg.prepDelayMin, cfg.prepDelayMax)
		}

		out.windowRates = append(out.windowRates, shareRate)
		out.windowDiffs = append(out.windowDiffs, atomicLoadFloat64(&mc.difficulty))
		now = now.Add(cfg.window)
	}

	if len(out.windowRates) > 0 {
		out.finalRate = out.windowRates[len(out.windowRates)-1]
		out.finalDiff = out.windowDiffs[len(out.windowDiffs)-1]
	}
	out.adjustments = mc.vardiffState.vardiffAdjustments.Load()
	return out
}

func sampleUniformDuration(rng *rand.Rand, min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	span := max - min
	return min + time.Duration(rng.Float64()*float64(span))
}

func averageLast(values []float64, count int) float64 {
	if len(values) == 0 {
		return 0
	}
	if count <= 0 || count > len(values) {
		count = len(values)
	}
	sum := 0.0
	start := len(values) - count
	for i := start; i < len(values); i++ {
		sum += values[i]
	}
	return sum / float64(count)
}

func adjustmentsUntilDiffBand(diffs []float64, low, high float64) int {
	if len(diffs) == 0 {
		return 0
	}
	adjustments := 0
	prev := diffs[0]
	if prev >= low && prev <= high {
		return 0
	}
	for i := 1; i < len(diffs); i++ {
		if math.Abs(diffs[i]-prev) > 1e-6 {
			adjustments++
		}
		if diffs[i] >= low && diffs[i] <= high {
			return adjustments
		}
		prev = diffs[i]
	}
	return 0
}

func countAdjustments(diffs []float64, start int) int {
	if len(diffs) <= 1 || start >= len(diffs) {
		return 0
	}
	if start < 1 {
		start = 1
	}
	n := 0
	for i := start; i < len(diffs); i++ {
		if math.Abs(diffs[i]-diffs[i-1]) > 1e-6 {
			n++
		}
	}
	return n
}

func percentileInt(values []int, p float64) int {
	if len(values) == 0 {
		return 0
	}
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	cp := append([]int(nil), values...)
	sort.Ints(cp)
	idx := int(math.Round(p * float64(len(cp)-1)))
	return cp[idx]
}

func samplePoisson(rng *rand.Rand, lambda float64) int {
	if lambda <= 0 {
		return 0
	}
	// Knuth's algorithm is simple and accurate for our test-scale lambdas.
	limit := math.Exp(-lambda)
	product := 1.0
	k := 0
	for product > limit {
		k++
		product *= rng.Float64()
	}
	return k - 1
}

func TestVirtualVardiff_SevenMinuteCharacterization(t *testing.T) {
	const trials = 64
	avgFinalRate := 0.0
	avgFinalDiff := 0.0
	avgAdjustments := 0.0

	for i := range trials {
		res := runVirtualVardiff(virtualVardiffConfig{
			duration:      7 * time.Minute,
			window:        120 * time.Second,
			initialDiff:   1000,
			hashrateHps:   1.2e12,
			rngSeed:       int64(1000 + i),
			targetShares:  7,
			vardiffStep:   2,
			dampingFactor: defaultVarDiffDampingFactor,
		})
		avgFinalRate += averageLast(res.windowRates, 2)
		avgFinalDiff += res.finalDiff
		avgAdjustments += float64(res.adjustments)
	}

	avgFinalRate /= trials
	avgFinalDiff /= trials
	avgAdjustments /= trials

	t.Logf("virtual vardiff (7m) avg tail share rate: %.2f shares/min", avgFinalRate)
	t.Logf("virtual vardiff (7m) avg final diff: %.2f", avgFinalDiff)
	t.Logf("virtual vardiff (7m) avg adjustments: %.2f", avgAdjustments)

	// Characterization only: defaults should settle near targetSharesPerMin
	// with some stochastic spread from Poisson arrivals.
	if avgFinalRate < 6.0 || avgFinalRate > 9.0 {
		t.Fatalf("expected tail share rate near target range, got %.2f", avgFinalRate)
	}
	if avgAdjustments <= 0 {
		t.Fatalf("expected some vardiff movement across trials, got %.2f", avgAdjustments)
	}
}

func TestVirtualVardiff_ConvergenceStepsCharacterization(t *testing.T) {
	const trials = 64
	totalSteps := 0
	observed := 0

	for i := range trials {
		res := runVirtualVardiff(virtualVardiffConfig{
			duration:      10 * time.Minute,
			window:        60 * time.Second,
			initialDiff:   1000,
			hashrateHps:   1.2e12,
			rngSeed:       int64(2000 + i),
			targetShares:  7,
			vardiffStep:   2,
			dampingFactor: defaultVarDiffDampingFactor,
		})

		steadyDiff := (1.2e12 / hashPerShare) * 60.0 / 7.0
		steps := adjustmentsUntilDiffBand(res.windowDiffs, steadyDiff*0.85, steadyDiff*1.15)
		if steps > 0 {
			totalSteps += steps
			observed++
		}
	}

	if observed == 0 {
		t.Fatalf("no trial reached the steady-state diff band")
	}
	avgSteps := float64(totalSteps) / float64(observed)
	t.Logf("virtual vardiff avg adjustments to steady-state diff band: %.2f", avgSteps)
}

func TestVirtualVardiff_TuningSweep(t *testing.T) {
	type scenario struct {
		name        string
		initialDiff float64
		minDiff     float64
		maxDiff     float64
		hashrateHps float64
	}
	type profile struct {
		name          string
		window        time.Duration
		dampingFactor float64
		step          float64
	}

	scenarios := []scenario{
		{name: "baseline-start", initialDiff: 1000, minDiff: 1, hashrateHps: 1.2e12},
		{name: "under-diff-start", initialDiff: 250, minDiff: 1, hashrateHps: 1.2e12},
		{name: "high-hashrate-start", initialDiff: 1000, minDiff: 1, hashrateHps: 90e12},
		{name: "low-hashrate-start", initialDiff: 0.00001, minDiff: 1e-8, hashrateHps: 200e3},
		{name: "mid-hashrate-start", initialDiff: 0.001, minDiff: 1e-8, hashrateHps: 3e6},
	}
	profiles := []profile{
		{name: "default", window: defaultVarDiffAdjustmentWindow, dampingFactor: defaultVarDiffDampingFactor, step: 2},
		{name: "faster-damping", window: 120 * time.Second, dampingFactor: 0.7, step: 2},
		{name: "faster-window", window: 60 * time.Second, dampingFactor: 0.7, step: 2},
		{name: "aggressive", window: 45 * time.Second, dampingFactor: 0.85, step: 2},
		{name: "less-step-bias", window: 60 * time.Second, dampingFactor: 0.7, step: 1.7},
	}

	const (
		trials       = 96
		targetShares = 7.0
	)

	meetsGoal := false

	for _, sc := range scenarios {
		t.Logf("scenario=%s initial_diff=%.8g min_diff=%.8g hashrate_hps=%.8g", sc.name, sc.initialDiff, sc.minDiff, sc.hashrateHps)
		for _, pf := range profiles {
			stepsObserved := make([]int, 0, trials)
			tailRates := make([]float64, 0, trials)

			for i := range trials {
				res := runVirtualVardiff(virtualVardiffConfig{
					duration:      10 * time.Minute,
					window:        pf.window,
					initialDiff:   sc.initialDiff,
					minDiff:       sc.minDiff,
					maxDiff:       sc.maxDiff,
					hashrateHps:   sc.hashrateHps,
					rngSeed:       int64(10000 + (i * 37)),
					targetShares:  targetShares,
					vardiffStep:   pf.step,
					dampingFactor: pf.dampingFactor,
				})
				desiredDiff := (sc.hashrateHps / hashPerShare) * 60.0 / targetShares
				bandLow := desiredDiff * 0.90
				bandHigh := desiredDiff * 1.10
				steps := adjustmentsUntilDiffBand(res.windowDiffs, bandLow, bandHigh)
				if steps > 0 {
					stepsObserved = append(stepsObserved, steps)
				}
				tailRates = append(tailRates, averageLast(res.windowRates, 2))
			}

			if len(stepsObserved) == 0 {
				t.Logf("  profile=%s no convergence hits in diff band", pf.name)
				continue
			}

			sumSteps := 0
			sumTail := 0.0
			for _, s := range stepsObserved {
				sumSteps += s
			}
			for _, rate := range tailRates {
				sumTail += rate
			}
			avgSteps := float64(sumSteps) / float64(len(stepsObserved))
			avgTail := sumTail / float64(len(tailRates))
			p50 := percentileInt(stepsObserved, 0.50)
			p90 := percentileInt(stepsObserved, 0.90)
			hitRate := float64(len(stepsObserved)) / float64(trials)

			t.Logf(
				"  profile=%s window=%s damping=%.2f step=%.2f avg_steps=%.2f p50=%d p90=%d hit_rate=%.2f avg_tail_rate=%.2f",
				pf.name, pf.window, pf.dampingFactor, pf.step, avgSteps, p50, p90, hitRate, avgTail,
			)

			if sc.name == "under-diff-start" && avgSteps >= 1 && avgSteps <= 4 {
				meetsGoal = true
			}
		}
	}

	if !meetsGoal {
		t.Fatalf("no profile reached 1-4 average convergence steps in under-diff-start scenario")
	}
}

func TestVirtualVardiff_SteadyStateChurnIsLow(t *testing.T) {
	const trials = 96
	totalLateAdjustments := 0.0

	for i := range trials {
		res := runVirtualVardiff(virtualVardiffConfig{
			duration:      20 * time.Minute,
			window:        60 * time.Second,
			initialDiff:   1000,
			minDiff:       1,
			hashrateHps:   1.2e12,
			rngSeed:       int64(30000 + i),
			targetShares:  7,
			vardiffStep:   2,
			dampingFactor: defaultVarDiffDampingFactor,
		})
		lateStart := len(res.windowDiffs) / 2
		totalLateAdjustments += float64(countAdjustments(res.windowDiffs, lateStart))
	}

	avgLateAdjustments := totalLateAdjustments / trials
	t.Logf("virtual vardiff steady-state churn: avg late-window adjustments %.2f", avgLateAdjustments)
	if avgLateAdjustments > 2.0 {
		t.Fatalf("steady-state vardiff churn too high: %.2f", avgLateAdjustments)
	}
}

func TestVirtualVardiff_LongHorizonBenchmark(t *testing.T) {
	if testing.Short() {
		t.Skip("skip long-horizon virtual benchmark in short mode")
	}
	scenarios := []virtualVardiffConfig{
		{
			duration:      4 * time.Hour,
			window:        defaultVarDiffAdjustmentWindow,
			initialDiff:   1000,
			minDiff:       1,
			hashrateHps:   90e12,
			rngSeed:       91001,
			targetShares:  7,
			vardiffStep:   2,
			dampingFactor: defaultVarDiffDampingFactor,
		},
		{
			duration:      4 * time.Hour,
			window:        defaultVarDiffAdjustmentWindow,
			initialDiff:   0.001,
			minDiff:       1e-8,
			hashrateHps:   3e6,
			rngSeed:       91002,
			targetShares:  7,
			vardiffStep:   2,
			dampingFactor: defaultVarDiffDampingFactor,
		},
	}
	for i, sc := range scenarios {
		res := runVirtualVardiff(sc)
		lateStart := (len(res.windowDiffs) * 3) / 4
		lateChurn := countAdjustments(res.windowDiffs, lateStart)
		t.Logf(
			"long-horizon scenario=%d tail_rate=%.2f final_diff=%.2f adjustments=%d late_churn=%d",
			i+1, averageLast(res.windowRates, 4), res.finalDiff, res.adjustments, lateChurn,
		)
	}
}
