package main

import (
	"bytes"
	"testing"
)

// BenchmarkSV2ConnHandleSubmitSharesStandard measures the in-process SV2 submit
// path from decoded wire message -> shared submit core -> encoded success frame.
// It does not include network transit time, miner firmware scheduling, or proxy
// buffering, so it serves as a pool-side lower bound.
func BenchmarkSV2ConnHandleSubmitSharesStandard(b *testing.B) {
	job := benchmarkSubmitJob(b)
	metrics := NewPoolMetrics()
	mc := benchmarkMinerConnForSubmit(metrics)

	// Match the stricter submit path used in tests and keep acceptance
	// deterministic so the benchmark exercises the success response path.
	mc.cfg.ShareNTimeMaxForwardSeconds = 600
	mc.cfg.ShareRequireAuthorizedConnection = true
	mc.cfg.ShareJobFreshnessMode = shareJobFreshnessJobID
	mc.cfg.ShareCheckParamFormat = true
	mc.stratumV1.authorized = true
	mc.stratumV1.subscribed = true

	jobID := job.JobID
	mc.jobMu.Lock()
	mc.activeJobs = map[string]*Job{jobID: job}
	mc.lastJob = job
	mc.jobMu.Unlock()
	mc.stratumV1.notify.jobDifficulty[jobID] = 1e-30
	mc.stratumV1.notify.jobScriptTime = map[string]int64{jobID: job.Template.CurTime}
	atomicStoreFloat64(&mc.difficulty, 1e-30)
	mc.shareTarget.Store(targetFromDifficulty(1e-30))

	channelID := uint32(10)
	wireJobID := uint32(7)
	mapper := newStratumV2SubmitMapperState()
	mapper.registerChannel(channelID, stratumV2SubmitChannelMapping{
		WorkerName:          mc.currentWorker(),
		StandardExtranonce2: []byte{0x00, 0x00, 0x00, 0x00},
	})
	mapper.registerJob(channelID, wireJobID, jobID)

	var out bytes.Buffer
	c := &sv2Conn{
		mc:           mc,
		writer:       &out,
		submitMapper: mapper,
		channelPrevHash: map[uint32]stratumV2WireSetNewPrevHash{
			channelID: {ChannelID: channelID, JobID: wireJobID},
		},
	}

	msg := stratumV2WireSubmitSharesStandard{
		ChannelID:      channelID,
		SequenceNumber: 1,
		JobID:          wireJobID,
		Nonce:          1,
		NTime:          uint32(job.Template.CurTime),
		Version:        uint32(job.Template.Version),
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out.Reset()
		msg.SequenceNumber++
		msg.Nonce++
		if err := c.handleSubmitSharesStandard(msg); err != nil {
			b.Fatalf("handleSubmitSharesStandard: %v", err)
		}
	}
}
