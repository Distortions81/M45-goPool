package main

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"testing"
	"time"
)

func benchmarkSubmitJob(b *testing.B) *Job {
	b.Helper()

	const (
		prevHash = "0000000000000000000000000000000000000000000000000000000000000000"
		bitsHex  = "1d00ffff"
	)

	var prevBytes [32]byte
	if n, err := hex.Decode(prevBytes[:], []byte(prevHash)); err != nil || n != 32 {
		b.Fatalf("decode prevhash: %v", err)
	}
	var bitsBytes [4]byte
	if n, err := hex.Decode(bitsBytes[:], []byte(bitsHex)); err != nil || n != 4 {
		b.Fatalf("decode bits: %v", err)
	}

	tpl := GetBlockTemplateResult{
		Height:        101,
		CurTime:       1700000000,
		Mintime:       1700000000,
		Bits:          bitsHex,
		Previous:      prevHash,
		CoinbaseValue: 50 * 1e8,
	}

	return &Job{
		JobID:                   "bench-submit-job",
		Template:                tpl,
		Target:                  new(big.Int), // effectively never a block share
		Extranonce2Size:         4,
		TemplateExtraNonce2Size: 8,
		PayoutScript:            []byte{0x51}, // OP_TRUE; structure-only benchmark script
		WitnessCommitment:       "",
		CoinbaseMsg:             "goPool-bench-submit",
		ScriptTime:              tpl.CurTime,
		MerkleBranches:          nil,
		Transactions:            nil,
		CoinbaseValue:           tpl.CoinbaseValue,
		PrevHash:                tpl.Previous,
		prevHashBytes:           prevBytes,
		bitsBytes:               bitsBytes,
		coinbaseFlagsBytes:      nil,
		witnessCommitScript:     nil,
	}
}

func benchmarkMinerConnForSubmit(metrics *PoolMetrics) *MinerConn {
	benchWorker, benchWallet, benchScript := generateBenchmarkWorker()
	cfg := Config{
		PoolFeePercent: 0, // keep dual-payout path disabled in this benchmark
	}
	mc := &MinerConn{
		id:             "bench-miner",
		cfg:            cfg,
		vardiff:        defaultVarDiff,
		metrics:        metrics,
		extranonce1:    []byte{0x01, 0x02, 0x03, 0x04},
		lockDifficulty: true,
		connectedAt:    time.Now(),
		authorized:     true,
		subscribed:     true,
		stats: MinerStats{
			Worker:       benchWorker,
			WorkerSHA256: workerNameHash(benchWorker),
		},
		jobDifficulty: make(map[string]float64, 1),
		maxRecentJobs: 1,
		// Leave statsUpdates nil so recordShare executes synchronously; the
		// CPU cost is still representative, without goroutine scheduling noise.
		statsUpdates: nil,
	}
	atomicStoreFloat64(&mc.difficulty, 1)
	mc.shareTarget.Store(targetFromDifficulty(1))
	mc.setWorkerWallet(benchWorker, benchWallet, benchScript)

	mc.conn = nopConn{}
	return mc
}

func BenchmarkProcessSubmissionTaskAcceptedShare(b *testing.B) {
	job := benchmarkSubmitJob(b)
	metrics := NewPoolMetrics()
	const benchSharesPerWorkerPerMin = 15.0

	ntimeHex := fmt.Sprintf("%08x", uint32(job.Template.CurTime))

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		mc := benchmarkMinerConnForSubmit(metrics)
		mc.jobDifficulty[job.JobID] = 1e-12

		task := submissionTask{
			mc:               mc,
			reqID:            1,
			job:              job,
			jobID:            job.JobID,
			workerName:       "worker1",
			extranonce2:      "00000000",
			extranonce2Bytes: []byte{0, 0, 0, 0},
			ntime:            ntimeHex,
			ntimeVal:         uint32(job.Template.CurTime),
			nonce:            "00000000",
			nonceVal:         0,
			versionHex:       "00000001",
			useVersion:       1,
			scriptTime:       job.ScriptTime,
			receivedAt:       time.Unix(1700000000, 0),
		}

		var i int64
		for pb.Next() {
			i++
			task.receivedAt = time.Unix(1700000000+i, 0)
			mc.processSubmissionTask(task)
		}
	})
	b.StopTimer()

	if b.N > 0 {
		nsPerShare := float64(b.Elapsed().Nanoseconds()) / float64(b.N)
		b.ReportMetric(nsPerShare, "ns/share")
		if nsPerShare > 0 {
			sharesPerSecond := 1e9 / nsPerShare
			b.ReportMetric(sharesPerSecond, "shares/s")
			if benchSharesPerWorkerPerMin > 0 {
				workers := sharesPerSecond * 60 / benchSharesPerWorkerPerMin
				b.ReportMetric(workers, "workers@15spm")
				b.ReportMetric(workers*0.7, "workers@15spm_70pct")
			}
		}
	}
}

func BenchmarkHandleSubmitAndProcessAcceptedShare(b *testing.B) {
	job := benchmarkSubmitJob(b)
	metrics := NewPoolMetrics()
	const benchSharesPerWorkerPerMin = 15.0

	ntimeHex := fmt.Sprintf("%08x", uint32(job.Template.CurTime))
	jobID := job.JobID

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		mc := benchmarkMinerConnForSubmit(metrics)
		mc.cfg.ShareNTimeMaxForwardSeconds = 600
		mc.jobMu.Lock()
		mc.activeJobs = map[string]*Job{jobID: job}
		mc.lastJob = job
		mc.jobDifficulty[jobID] = 1e-12
		mc.jobMu.Unlock()

		var i uint32
		for pb.Next() {
			i++
			nonceHex := fmt.Sprintf("%08x", i)
			en2Hex := fmt.Sprintf("%08x", i^0x9e3779b9)

			req := &StratumRequest{
				ID:     1,
				Method: "mining.submit",
				Params: []any{"worker1", jobID, en2Hex, ntimeHex, nonceHex},
			}

			now := time.Unix(1700000000+int64(i), 0)
			task, ok := mc.prepareSubmissionTask(req, now)
			if !ok {
				b.Fatalf("prepareSubmissionTask unexpectedly rejected a bench share")
			}
			mc.processSubmissionTask(task)
		}
	})
	b.StopTimer()

	if b.N > 0 {
		nsPerShare := float64(b.Elapsed().Nanoseconds()) / float64(b.N)
		b.ReportMetric(nsPerShare, "ns/share")
		if nsPerShare > 0 {
			sharesPerSecond := 1e9 / nsPerShare
			b.ReportMetric(sharesPerSecond, "shares/s")
			if benchSharesPerWorkerPerMin > 0 {
				workers := sharesPerSecond * 60 / benchSharesPerWorkerPerMin
				b.ReportMetric(workers, "workers@15spm")
				b.ReportMetric(workers*0.7, "workers@15spm_70pct")
			}
		}
	}
}

func BenchmarkHandleSubmitAndProcessAcceptedShare_DupCheckEnabled(b *testing.B) {
	job := benchmarkSubmitJob(b)
	metrics := NewPoolMetrics()
	const benchSharesPerWorkerPerMin = 15.0

	ntimeHex := fmt.Sprintf("%08x", uint32(job.Template.CurTime))
	jobID := job.JobID

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		mc := benchmarkMinerConnForSubmit(metrics)
		mc.cfg.ShareCheckDuplicate = true
		mc.cfg.ShareNTimeMaxForwardSeconds = 600
		mc.jobMu.Lock()
		mc.activeJobs = map[string]*Job{jobID: job}
		mc.lastJob = job
		mc.jobDifficulty[jobID] = 1e-12
		mc.jobMu.Unlock()

		var i uint32
		for pb.Next() {
			i++
			nonceHex := fmt.Sprintf("%08x", i)
			en2Hex := fmt.Sprintf("%08x", i^0x9e3779b9)

			req := &StratumRequest{
				ID:     1,
				Method: "mining.submit",
				Params: []any{"worker1", jobID, en2Hex, ntimeHex, nonceHex},
			}

			now := time.Unix(1700000000+int64(i), 0)
			task, ok := mc.prepareSubmissionTask(req, now)
			if !ok {
				b.Fatalf("prepareSubmissionTask unexpectedly rejected a bench share")
			}
			mc.processSubmissionTask(task)
		}
	})
	b.StopTimer()

	if b.N > 0 {
		nsPerShare := float64(b.Elapsed().Nanoseconds()) / float64(b.N)
		b.ReportMetric(nsPerShare, "ns/share")
		if nsPerShare > 0 {
			sharesPerSecond := 1e9 / nsPerShare
			b.ReportMetric(sharesPerSecond, "shares/s")
			if benchSharesPerWorkerPerMin > 0 {
				workers := sharesPerSecond * 60 / benchSharesPerWorkerPerMin
				b.ReportMetric(workers, "workers@15spm")
				b.ReportMetric(workers*0.7, "workers@15spm_70pct")
			}
		}
	}
}

func BenchmarkPrepareSubmissionTaskAcceptedShare_DupCheckDisabled(b *testing.B) {
	job := benchmarkSubmitJob(b)
	metrics := NewPoolMetrics()

	ntimeHex := fmt.Sprintf("%08x", uint32(job.Template.CurTime))
	jobID := job.JobID

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		mc := benchmarkMinerConnForSubmit(metrics)
		mc.cfg.ShareCheckDuplicate = false
		mc.cfg.ShareNTimeMaxForwardSeconds = 600
		mc.jobMu.Lock()
		mc.activeJobs = map[string]*Job{jobID: job}
		mc.lastJob = job
		mc.jobDifficulty[jobID] = 1e-12
		mc.jobMu.Unlock()

		var i uint32
		for pb.Next() {
			i++
			nonceHex := fmt.Sprintf("%08x", i)
			en2Hex := fmt.Sprintf("%08x", i^0x9e3779b9)

			req := &StratumRequest{
				ID:     1,
				Method: "mining.submit",
				Params: []any{"worker1", jobID, en2Hex, ntimeHex, nonceHex},
			}

			now := time.Unix(1700000000+int64(i), 0)
			if _, ok := mc.prepareSubmissionTask(req, now); !ok {
				b.Fatalf("prepareSubmissionTask unexpectedly rejected a bench share")
			}
		}
	})
}

func BenchmarkPrepareSubmissionTaskAcceptedShare_DupCheckEnabled(b *testing.B) {
	job := benchmarkSubmitJob(b)
	metrics := NewPoolMetrics()

	ntimeHex := fmt.Sprintf("%08x", uint32(job.Template.CurTime))
	jobID := job.JobID

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		mc := benchmarkMinerConnForSubmit(metrics)
		mc.cfg.ShareCheckDuplicate = true
		mc.cfg.ShareNTimeMaxForwardSeconds = 600
		mc.jobMu.Lock()
		mc.activeJobs = map[string]*Job{jobID: job}
		mc.lastJob = job
		mc.jobDifficulty[jobID] = 1e-12
		mc.jobMu.Unlock()

		var i uint32
		for pb.Next() {
			i++
			nonceHex := fmt.Sprintf("%08x", i)
			en2Hex := fmt.Sprintf("%08x", i^0x9e3779b9)

			req := &StratumRequest{
				ID:     1,
				Method: "mining.submit",
				Params: []any{"worker1", jobID, en2Hex, ntimeHex, nonceHex},
			}

			now := time.Unix(1700000000+int64(i), 0)
			if _, ok := mc.prepareSubmissionTask(req, now); !ok {
				b.Fatalf("prepareSubmissionTask unexpectedly rejected a bench share")
			}
		}
	})
}
