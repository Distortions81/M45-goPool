package main

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockingVersionMaskConn struct {
	recordConn
	maskStarted chan struct{}
	releaseMask chan struct{}
	maskOnce    sync.Once
}

type blockingConfigureResponseConn struct {
	recordConn
	responseStarted chan struct{}
	releaseResponse chan struct{}
	responseOnce    sync.Once
}

func (c *blockingConfigureResponseConn) Write(b []byte) (int, error) {
	if bytes.Contains(b, []byte(`"id":7`)) && !bytes.Contains(b, []byte(`"method"`)) {
		c.responseOnce.Do(func() { close(c.responseStarted) })
		<-c.releaseResponse
	}
	return c.recordConn.Write(b)
}

func (c *blockingVersionMaskConn) Write(b []byte) (int, error) {
	if bytes.Contains(b, []byte(`"method":"mining.set_version_mask"`)) {
		c.maskOnce.Do(func() { close(c.maskStarted) })
		<-c.releaseMask
	}
	return c.recordConn.Write(b)
}

func TestResolveSubmittedVersionActiveZeroMaskPreservesJobVersion(t *testing.T) {
	const baseVersion = uint32(0x2000e000)

	got := resolveSubmittedVersion(baseVersion, 0, 0, true, false, true)
	if got.useVersion != baseVersion {
		t.Fatalf("active zero-mask version=%08x want job version %08x", got.useVersion, baseVersion)
	}
	if got.versionDiff != 0 {
		t.Fatalf("active zero-mask version diff=%08x want zero", got.versionDiff)
	}
	if got.hasAlternateVersion {
		t.Fatal("active zero mask should not create a distinct alternate version")
	}
}

func TestDeniedVersionConfigureDoesNotAutoActivateLater(t *testing.T) {
	conn := &writeRecorderConn{}
	mc := &MinerConn{
		id:          "configure-denied-version-mask",
		conn:        conn,
		subscribed:  true,
		poolMask:    0x00000001,
		versionMask: 0x00000001,
		minVerBits:  1,
	}

	mc.handleConfigure(&StratumRequest{
		ID:     1,
		Method: "mining.configure",
		Params: []any{
			[]any{"version-rolling"},
			map[string]any{"version-rolling.mask": "00000002"},
		},
	})

	active, mask := mc.versionRollingPolicySnapshot()
	if active {
		t.Fatal("disjoint configure unexpectedly activated version rolling")
	}
	if mask != 0x00000001 {
		t.Fatalf("inactive compatibility mask=%08x want pool mask 00000001", mask)
	}
	if out := conn.String(); !strings.Contains(out, `"version-rolling":false`) || strings.Contains(out, `"method":"mining.set_version_mask"`) {
		t.Fatalf("unexpected denied configure output: %q", out)
	}

	// A later pool mask happens to overlap the miner's denied request. That must
	// not activate BIP310 without a new successful mining.configure exchange.
	mc.updateVersionMask(0x00000002)
	active, mask = mc.versionRollingPolicySnapshot()
	if active {
		t.Fatal("pool mask transition auto-activated a denied negotiation")
	}
	if mask != 0x00000002 {
		t.Fatalf("inactive compatibility mask=%08x want updated pool mask 00000002", mask)
	}
}

func TestConfigureWithoutMaskKeepsFullMinerCapability(t *testing.T) {
	conn := &writeRecorderConn{}
	mc := &MinerConn{
		id:          "configure-default-version-mask",
		conn:        conn,
		subscribed:  true,
		poolMask:    0x00000001,
		versionMask: 0x00000001,
		minVerBits:  1,
	}

	mc.handleConfigure(&StratumRequest{
		ID:     6,
		Method: "mining.configure",
		Params: []any{[]any{"version-rolling"}, map[string]any{}},
	})

	mc.versionMu.Lock()
	if !mc.versionRoll || mc.versionMask != 0x00000001 || mc.minerMask != ^uint32(0) {
		active, mask, minerMask := mc.versionRoll, mc.versionMask, mc.minerMask
		mc.versionMu.Unlock()
		t.Fatalf("default negotiation state active=%v mask=%08x miner=%08x", active, mask, minerMask)
	}
	mc.versionMu.Unlock()

	// The omitted mask means ffffffff, so a later server-mask move must not be
	// restricted to the bit that happened to be available at configuration.
	mc.updateVersionMask(0x00000002)
	active, mask := mc.versionRollingPolicySnapshot()
	if !active || mask != 0x00000002 {
		t.Fatalf("updated default-mask policy active=%v mask=%08x want active/00000002", active, mask)
	}
}

func TestConfigureResponseAndMaskPrecedeConcurrentNotify(t *testing.T) {
	mc, _ := minerConnForNotifyTest(t)
	conn := &blockingConfigureResponseConn{
		responseStarted: make(chan struct{}),
		releaseResponse: make(chan struct{}),
	}
	mc.conn = conn
	mc.versionMu.Lock()
	mc.poolMask = 0x0000e000
	mc.versionMask = 0x0000e000
	mc.minVerBits = 1
	mc.versionMu.Unlock()

	configureDone := make(chan struct{})
	go func() {
		mc.handleConfigure(&StratumRequest{
			ID:     7,
			Method: "mining.configure",
			Params: []any{
				[]any{"version-rolling"},
				map[string]any{"version-rolling.mask": "00006000"},
			},
		})
		close(configureDone)
	}()
	select {
	case <-conn.responseStarted:
	case <-time.After(time.Second):
		t.Fatal("configure response did not reach the blocked write")
	}

	job := benchmarkSubmitJobForTest(t)
	job.VersionMask = 0x00002000
	notifyStarted := make(chan struct{})
	notifyDone := make(chan struct{})
	go func() {
		close(notifyStarted)
		mc.sendNotifyFor(job, true)
		close(notifyDone)
	}()
	<-notifyStarted
	select {
	case <-notifyDone:
		t.Fatal("notify bypassed the in-flight configure response")
	case <-time.After(25 * time.Millisecond):
	}
	if strings.Contains(conn.String(), `"method":"mining.notify"`) {
		t.Fatalf("notify was written before configure completed: %q", conn.String())
	}

	close(conn.releaseResponse)
	select {
	case <-configureDone:
	case <-time.After(time.Second):
		t.Fatal("configure did not finish after releasing its response")
	}
	select {
	case <-notifyDone:
	case <-time.After(time.Second):
		t.Fatal("notify did not finish after configure")
	}

	out := conn.String()
	responseAt := strings.Index(out, `"id":7`)
	configuredMaskAt := strings.Index(out, `"params":["00006000"]`)
	updatedMaskAt := strings.Index(out, `"params":["00002000"]`)
	notifyAt := strings.Index(out, `"method":"mining.notify"`)
	if responseAt < 0 || configuredMaskAt < responseAt || updatedMaskAt < configuredMaskAt || notifyAt < updatedMaskAt {
		t.Fatalf("unexpected configure/mask/notify order: %q", out)
	}
}

func TestNotifySendsActiveZeroMaskAndSubmitUsesJobVersion(t *testing.T) {
	mc, conn := minerConnForNotifyTest(t)
	mc.cfg.ShareCheckVersionRolling = true
	mc.cfg.ShareAllowOutOfMaskVersionBits = false
	mc.cfg.ShareRequireAuthorizedConnection = true
	mc.cfg.ShareJobFreshnessMode = shareJobFreshnessJobID
	mc.cfg.ShareCheckParamFormat = true

	mc.versionMu.Lock()
	mc.versionRoll = true
	mc.poolMask = 0x00000001
	mc.minerMask = 0x00000001
	mc.versionMask = 0x00000001
	mc.minVerBits = 1
	mc.versionMu.Unlock()

	job := benchmarkSubmitJobForTest(t)
	job.Template.Version = int32(0x2000e000)
	job.VersionMask = 0x00000002 // disjoint from the negotiated miner mask
	mc.sendNotifyFor(job, true)

	active, mask := mc.versionRollingPolicySnapshot()
	if !active {
		t.Fatal("zero mask deactivated an already-negotiated BIP310 extension")
	}
	if mask != 0 {
		t.Fatalf("negotiated mask=%08x want zero", mask)
	}

	out := conn.String()
	maskAt := strings.Index(out, `"method":"mining.set_version_mask"`)
	notifyAt := strings.Index(out, `"method":"mining.notify"`)
	if maskAt < 0 || !strings.Contains(out, `"params":["00000000"]`) {
		t.Fatalf("zero mask notification missing: %q", out)
	}
	if notifyAt < 0 || maskAt > notifyAt {
		t.Fatalf("zero mask must be sent before the new job: %q", out)
	}

	ids := notifyJobIDsFromOutput(t, out)
	if len(ids) != 1 {
		t.Fatalf("notify ids=%#v want one", ids)
	}
	req := &StratumRequest{
		ID:     2,
		Method: "mining.submit",
		Params: []any{
			mc.currentWorker(),
			ids[0],
			"00000000",
			"6553f100",
			"00000001",
			"00000000",
		},
	}
	task, ok := mc.prepareSubmissionTask(req, time.Unix(job.Template.CurTime, 0))
	if !ok {
		t.Fatal("active zero-mask submit was rejected during preparation")
	}
	if task.useVersion != uint32(job.Template.Version) {
		t.Fatalf("submit version=%08x want job version %08x", task.useVersion, uint32(job.Template.Version))
	}
	if task.policyReject.reason != rejectUnknown {
		t.Fatalf("active zero-mask submit got policy reject %v", task.policyReject.reason)
	}

	invalidReq := *req
	invalidReq.ID = 3
	invalidReq.Params = append([]any(nil), req.Params...)
	invalidReq.Params[5] = "2000e000"
	invalidTask, ok := mc.prepareSubmissionTask(&invalidReq, time.Unix(job.Template.CurTime, 0))
	if !ok {
		t.Fatal("out-of-mask version bits should remain processable for block safety")
	}
	if invalidTask.policyReject.reason != rejectInvalidVersionMask {
		t.Fatalf("active zero mask accepted nonzero version_bits with policy %v", invalidTask.policyReject.reason)
	}
}

func TestLateConfigureAppliesCurrentMaskToAlreadyNotifiedJob(t *testing.T) {
	mc, conn := minerConnForNotifyTest(t)
	mc.cfg.ShareCheckVersionRolling = true
	mc.cfg.ShareAllowOutOfMaskVersionBits = false
	mc.cfg.ShareRequireAuthorizedConnection = true
	mc.cfg.ShareJobFreshnessMode = shareJobFreshnessJobID
	mc.cfg.ShareCheckParamFormat = true

	job := benchmarkSubmitJobForTest(t)
	job.Template.Version = int32(0x2000e000)
	job.VersionMask = 0x0000e000
	mc.sendNotifyFor(job, true)
	ids := notifyJobIDsFromOutput(t, conn.String())
	if len(ids) != 1 {
		t.Fatalf("notify ids=%#v want one", ids)
	}

	mc.handleConfigure(&StratumRequest{
		ID:     3,
		Method: "mining.configure",
		Params: []any{
			[]any{"version-rolling"},
			map[string]any{"version-rolling.mask": "00006000"},
		},
	})

	active, mask := mc.versionRollingPolicySnapshot()
	if !active || mask != 0x00006000 {
		t.Fatalf("current policy=(active=%v mask=%08x), want active mask 00006000", active, mask)
	}
	task, ok := mc.prepareSubmissionTask(&StratumRequest{
		ID:     4,
		Method: "mining.submit",
		Params: []any{
			mc.currentWorker(),
			ids[0],
			"00000000",
			"6553f100",
			"00000001",
			"00002000",
		},
	}, time.Unix(job.Template.CurTime, 0))
	if !ok {
		t.Fatal("late-configure submit was rejected during preparation")
	}
	if task.useVersion != 0x2000a000 {
		t.Fatalf("submit version=%08x want current-mask version 2000a000", task.useVersion)
	}
	if task.policyReject.reason != rejectUnknown {
		t.Fatalf("late-configure submit got policy reject %v", task.policyReject.reason)
	}
}

func TestSubmitWaitsForVersionMaskWrite(t *testing.T) {
	mc, _ := minerConnForNotifyTest(t)
	conn := &blockingVersionMaskConn{
		maskStarted: make(chan struct{}),
		releaseMask: make(chan struct{}),
	}
	mc.conn = conn
	mc.cfg.ShareCheckVersionRolling = true
	mc.cfg.ShareAllowOutOfMaskVersionBits = false
	mc.cfg.ShareRequireAuthorizedConnection = true
	mc.cfg.ShareJobFreshnessMode = shareJobFreshnessJobID
	mc.cfg.ShareCheckParamFormat = true

	mc.versionMu.Lock()
	mc.versionRoll = true
	mc.poolMask = 0x00000001
	mc.minerMask = 0x00000001
	mc.versionMask = 0x00000001
	mc.minVerBits = 1
	mc.versionMu.Unlock()

	oldJob := benchmarkSubmitJobForTest(t)
	oldJob.Template.Version = int32(0x20000000)
	oldJob.VersionMask = 0x00000001
	mc.sendNotifyFor(oldJob, true)
	ids := notifyJobIDsFromOutput(t, conn.String())
	if len(ids) != 1 {
		t.Fatalf("notify ids=%#v want one", ids)
	}

	newJob := *oldJob
	newJob.JobID = oldJob.JobID + "-zero-mask"
	newJob.Generation = oldJob.Generation + 1
	newJob.VersionMask = 0x00000002
	notifyDone := make(chan struct{})
	go func() {
		mc.sendNotifyFor(&newJob, true)
		close(notifyDone)
	}()
	select {
	case <-conn.maskStarted:
	case <-time.After(time.Second):
		t.Fatal("version-mask transition did not reach the blocked write")
	}

	type submitResult struct {
		task submissionTask
		ok   bool
	}
	submitDone := make(chan submitResult, 1)
	go func() {
		task, ok := mc.prepareSubmissionTask(&StratumRequest{
			ID:     5,
			Method: "mining.submit",
			Params: []any{
				mc.currentWorker(), ids[0], "00000000", "6553f100", "00000001", "00000000",
			},
		}, time.Unix(oldJob.Template.CurTime, 0))
		submitDone <- submitResult{task: task, ok: ok}
	}()

	select {
	case <-submitDone:
		t.Fatal("submit observed the new mask before mining.set_version_mask completed")
	case <-time.After(25 * time.Millisecond):
	}
	close(conn.releaseMask)

	select {
	case <-notifyDone:
	case <-time.After(time.Second):
		t.Fatal("notify did not finish after releasing the mask write")
	}
	select {
	case result := <-submitDone:
		if !result.ok {
			t.Fatal("submit did not finish successfully after the mask write")
		}
		if result.task.useVersion != uint32(oldJob.Template.Version) {
			t.Fatalf("submit version=%08x want %08x", result.task.useVersion, uint32(oldJob.Template.Version))
		}
	case <-time.After(time.Second):
		t.Fatal("submit remained blocked after the mask write")
	}
}
