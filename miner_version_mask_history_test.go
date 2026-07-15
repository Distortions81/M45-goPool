package main

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestBlockOnlyRescueVersions(t *testing.T) {
	const (
		baseVersion   = uint32(0x20006000)
		submittedBits = uint32(0x00004000)
		currentMask   = uint32(0x00002000)
		notifiedMask  = uint32(0x0000e000)
	)

	t.Run("keeps distinct notify-time BIP310 and XOR candidates", func(t *testing.T) {
		current := resolveSubmittedVersion(baseVersion, submittedBits, currentMask, true, false, true)
		got, extra, count := blockOnlyRescueVersions(
			baseVersion,
			submittedBits,
			current,
			submittedVersionMaskPolicy{active: true, mask: currentMask},
			&submittedVersionMaskPolicy{active: true, mask: notifiedMask},
			nil,
		)
		if len(extra) != 0 {
			t.Fatalf("unexpected overflow candidates: %08x", extra)
		}

		if current.useVersion != 0x00004000 {
			t.Fatalf("current version = %08x, want 00004000", current.useVersion)
		}
		if count != 2 {
			t.Fatalf("candidate count = %d, want 2: %08x", count, got)
		}
		want := [3]uint32{0x20004000, 0x20002000}
		if got != want {
			t.Fatalf("candidates = %08x, want %08x", got, want)
		}
	})

	t.Run("adds raw full version without changing compatibility primary", func(t *testing.T) {
		current := resolveSubmittedVersion(baseVersion, submittedBits, currentMask, true, true, true)
		got, extra, count := blockOnlyRescueVersions(
			baseVersion,
			submittedBits,
			current,
			submittedVersionMaskPolicy{active: true, mask: currentMask},
			&submittedVersionMaskPolicy{active: true, mask: notifiedMask},
			nil,
		)
		if len(extra) != 0 {
			t.Fatalf("unexpected overflow candidates: %08x", extra)
		}

		if current.useVersion != 0x20002000 {
			t.Fatalf("current compatibility version = %08x, want XOR version 20002000", current.useVersion)
		}
		want := [3]uint32{0x20004000, submittedBits}
		if count != 2 || got != want {
			t.Fatalf("candidates = %08x count=%d, want %08x", got, count, want)
		}
	})

	t.Run("active zero mask remains distinguishable", func(t *testing.T) {
		current := resolveSubmittedVersion(baseVersion, 0, 0, false, false, true)
		got, extra, count := blockOnlyRescueVersions(
			baseVersion,
			0,
			current,
			submittedVersionMaskPolicy{},
			&submittedVersionMaskPolicy{active: true},
			nil,
		)
		if len(extra) != 0 {
			t.Fatalf("unexpected overflow candidates: %08x", extra)
		}

		if count != 1 || got[0] != baseVersion {
			t.Fatalf("active-zero candidates = %08x count=%d, want base %08x", got, count, baseVersion)
		}
	})

	t.Run("active zero mask can require all three rescue slots", func(t *testing.T) {
		current := resolveSubmittedVersion(baseVersion, submittedBits, 0, true, false, true)
		got, extra, count := blockOnlyRescueVersions(
			baseVersion,
			submittedBits,
			current,
			submittedVersionMaskPolicy{active: true},
			&submittedVersionMaskPolicy{active: true, mask: notifiedMask},
			nil,
		)
		if len(extra) != 0 {
			t.Fatalf("unexpected overflow candidates: %08x", extra)
		}

		if current.useVersion != submittedBits {
			t.Fatalf("strict primary = %08x, want raw version %08x", current.useVersion, submittedBits)
		}
		want := [3]uint32{baseVersion, 0x20004000, 0x20002000}
		if count != 3 || got != want {
			t.Fatalf("active-zero candidates = %08x count=%d, want %08x", got, count, want)
		}
	})

	t.Run("intermediate masks overflow without truncation", func(t *testing.T) {
		const wideBase = uint32(0x2000f000)
		current := resolveSubmittedVersion(wideBase, 0, 0, true, false, true)
		got, extra, count := blockOnlyRescueVersions(
			wideBase,
			0,
			current,
			submittedVersionMaskPolicy{active: true},
			nil,
			[]uint32{0x00001000, 0x00002000, 0x00004000, 0x00008000},
		)
		wantInline := [3]uint32{0x2000e000, 0x2000d000, 0x2000b000}
		wantExtra := []uint32{0x20007000, 0x00000000}
		if count != 5 || got != wantInline || len(extra) != len(wantExtra) ||
			extra[0] != wantExtra[0] || extra[1] != wantExtra[1] {
			t.Fatalf("overflow candidates = %08x extra=%08x count=%d, want %08x extra=%08x count=5",
				got, extra, count, wantInline, wantExtra)
		}
		task := submissionTask{
			blockRescueVersions: got,
			blockRescueExtra:    extra,
			blockRescueCount:    count,
		}
		wantAll := []uint32{wantInline[0], wantInline[1], wantInline[2], wantExtra[0], wantExtra[1]}
		for i, want := range wantAll {
			candidate, ok := task.blockRescueVersion(i)
			if !ok || candidate != want {
				t.Fatalf("task candidate %d = %08x ok=%v, want %08x", i, candidate, ok, want)
			}
		}
	})
}

func TestVersionMaskHistoryIsBoundedAndRecencyOrdered(t *testing.T) {
	mc := &MinerConn{maxRecentJobs: 1}
	mc.versionMu.Lock()
	mc.rememberVersionMaskLocked(0x00002000)
	mc.rememberVersionMaskLocked(0x00004000)
	mc.rememberVersionMaskLocked(0x00002000)
	mc.rememberVersionMaskLocked(0x00008000)
	got := append([]uint32(nil), mc.versionMaskHistory...)
	mc.versionMu.Unlock()
	want := []uint32{0x00002000, 0x00008000}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("version mask history = %08x, want %08x", got, want)
	}
}

func TestNotifyVersionMaskBindingSurvivesReorgUntilJobEviction(t *testing.T) {
	const (
		oldMask = uint32(0x0000e000)
		newMask = uint32(0x00002000)
	)

	mc, conn := minerConnForNotifyTest(t)
	mc.maxRecentJobs = 2
	mc.versionMu.Lock()
	mc.versionRoll = true
	mc.minerMask = oldMask | newMask
	mc.minVerBits = 1
	mc.versionMu.Unlock()

	initial := benchmarkSubmitJobForTest(t)
	initial.Generation = 1
	initial.ScriptTime = initial.Template.CurTime
	initial.VersionMask = oldMask
	mc.sendNotifyFor(initial, true)

	reorg := *initial
	reorg.JobID = "reorg-job"
	reorg.Generation = 2
	reorg.Template.Previous = strings.Repeat("11", 32)
	reorg.PrevHash = reorg.Template.Previous
	reorg.Template.Height--
	reorg.VersionMask = newMask
	mc.sendNotifyFor(&reorg, true)

	ids := notifyJobIDsFromOutput(t, conn.String())
	if len(ids) != 2 {
		t.Fatalf("notify IDs = %#v, want 2", ids)
	}
	oldJob, _, _, _, _, _, oldBinding, oldBindingOK, oldOK := mc.jobForIDWithLast(ids[0])
	if !oldOK || oldJob != initial || !oldBindingOK {
		t.Fatalf("old reorg job binding was not retained: jobOK=%v bindingOK=%v", oldOK, oldBindingOK)
	}
	if !oldBinding.versionRollingActive || oldBinding.versionMask != oldMask {
		t.Fatalf("old binding policy = active:%v mask:%08x, want active:%v mask:%08x",
			oldBinding.versionRollingActive, oldBinding.versionMask, true, oldMask)
	}
	_, _, _, _, _, _, newBinding, newBindingOK, newOK := mc.jobForIDWithLast(ids[1])
	if !newOK || !newBindingOK || !newBinding.versionRollingActive || newBinding.versionMask != newMask {
		t.Fatalf("new binding policy = jobOK:%v bindingOK:%v active:%v mask:%08x",
			newOK, newBindingOK, newBinding.versionRollingActive, newBinding.versionMask)
	}

	latest := reorg
	latest.JobID = "latest-job"
	latest.Generation = 3
	latest.CoinbaseValue--
	latest.Template.CoinbaseValue--
	mc.sendNotifyFor(&latest, false)

	mc.jobMu.Lock()
	_, oldJobRetained := mc.activeJobs[ids[0]]
	_, oldBindingRetained := mc.jobNotifyCoinbase[ids[0]]
	mc.jobMu.Unlock()
	if oldJobRetained || oldBindingRetained {
		t.Fatalf("evicted job retained state: job=%v binding=%v", oldJobRetained, oldBindingRetained)
	}
	retiredLookup := mc.jobForSubmissionWithLast(ids[0])
	if !retiredLookup.found || !retiredLookup.retired || retiredLookup.job != initial || !retiredLookup.coinbaseOK {
		t.Fatalf("evicted reorg binding was not retired exactly: found=%v retired=%v binding=%v",
			retiredLookup.found, retiredLookup.retired, retiredLookup.coinbaseOK)
	}
	if !retiredLookup.coinbase.versionRollingActive || retiredLookup.coinbase.versionMask != oldMask {
		t.Fatalf("retired binding policy = active:%v mask:%08x, want active:%v mask:%08x",
			retiredLookup.coinbase.versionRollingActive, retiredLookup.coinbase.versionMask, true, oldMask)
	}
}

func TestNotifyVersionMaskBindingPreservesActiveZeroMask(t *testing.T) {
	mc, conn := minerConnForNotifyTest(t)
	mc.versionMu.Lock()
	mc.versionRoll = true
	mc.minerMask = defaultVersionMask
	mc.minVerBits = 1
	mc.versionMu.Unlock()

	job := benchmarkSubmitJobForTest(t)
	job.Generation = 1
	job.ScriptTime = job.Template.CurTime
	job.VersionMask = 0
	mc.sendNotifyFor(job, true)

	ids := notifyJobIDsFromOutput(t, conn.String())
	if len(ids) != 1 {
		t.Fatalf("notify IDs = %#v, want 1", ids)
	}
	_, _, _, _, _, _, binding, bindingOK, jobOK := mc.jobForIDWithLast(ids[0])
	if !jobOK || !bindingOK {
		t.Fatalf("active-zero binding missing: jobOK=%v bindingOK=%v", jobOK, bindingOK)
	}
	if !binding.versionRollingActive || binding.versionMask != 0 {
		t.Fatalf("binding policy = active:%v mask:%08x, want active zero mask", binding.versionRollingActive, binding.versionMask)
	}
}

func TestHistoricalVersionMaskRescuesBlockAcrossReorg(t *testing.T) {
	const (
		baseVersion    = uint32(0x20006000)
		submittedBits  = uint32(0x00004000)
		notifiedMask   = uint32(0x0000e000)
		currentMask    = uint32(0x00002000)
		currentVersion = uint32(0x00004000)
		historicalBIP  = uint32(0x20004000)
		historicalXOR  = uint32(0x20002000)
	)

	tests := []struct {
		name          string
		rescueVersion uint32
	}{
		{name: "BIP310 replacement", rescueVersion: historicalBIP},
		{name: "legacy XOR", rescueVersion: historicalXOR},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mc, conn := minerConnForNotifyTest(t)
			mc.maxRecentJobs = 1
			mc.cfg.DataDir = t.TempDir()
			mc.cfg.ShareCheckDuplicate = false
			mc.cfg.ShareCheckParamFormat = true
			mc.cfg.ShareCheckVersionRolling = true
			mc.cfg.ShareAllowOutOfMaskVersionBits = false
			mc.cfg.ShareRequireAuthorizedConnection = true
			mc.cfg.ShareRequireWorkerMatch = true
			mc.cfg.ShareJobFreshnessMode = shareJobFreshnessJobIDPrev
			mc.cfg.ShareNTimeMaxForwardSeconds = 600
			rpc := &countingSubmitRPC{}
			mc.rpc = rpc
			mc.versionMu.Lock()
			mc.versionRoll = true
			mc.minerMask = notifiedMask | currentMask
			mc.minVerBits = 1
			mc.versionMu.Unlock()

			job := benchmarkSubmitJobForTest(t)
			job.Generation = 1
			job.ScriptTime = job.Template.CurTime
			job.Template.Version = int32(baseVersion)
			job.VersionMask = notifiedMask
			mc.sendNotifyFor(job, true)

			reorg := *job
			reorg.JobID = "competing-reorg-job"
			reorg.Generation = 2
			reorg.Template.Previous = strings.Repeat("22", 32)
			reorg.PrevHash = reorg.Template.Previous
			reorg.Template.Height--
			reorg.VersionMask = currentMask
			reorg.Target = new(big.Int)
			reorg.targetBE = [32]byte{}
			mc.sendNotifyFor(&reorg, true)

			ids := notifyJobIDsFromOutput(t, conn.String())
			if len(ids) != 2 {
				t.Fatalf("notify IDs = %#v, want 2", ids)
			}
			lookup := mc.jobForSubmissionWithLast(ids[0])
			oldJob := lookup.job
			binding := lookup.coinbase
			if !lookup.found || !lookup.retired || oldJob != job || !lookup.coinbaseOK {
				t.Fatalf("old reorg job binding missing: found=%v retired=%v bindingOK=%v", lookup.found, lookup.retired, lookup.coinbaseOK)
			}

			extranonce2 := []byte{0, 0, 0, 0}
			_, coinbaseTxID, err := buildNotifiedCoinbaseTx(binding, extranonce2)
			if err != nil {
				t.Fatalf("build notified coinbase: %v", err)
			}
			merkleRoot, ok := computeMerkleRootFromBranches32(coinbaseTxID, job.MerkleBranches)
			if !ok {
				t.Fatal("compute notified merkle root")
			}

			versions := [...]uint32{currentVersion, historicalBIP, historicalXOR}
			var (
				chosenNonce  uint32
				chosenTarget *big.Int
				expectedHead []byte
			)
			for nonce := uint32(0); nonce < 10_000; nonce++ {
				targets := make(map[uint32]*big.Int, len(versions))
				headers := make(map[uint32][]byte, len(versions))
				for _, version := range versions {
					header, err := job.buildBlockHeaderU32(merkleRoot[:], uint32(job.Template.CurTime), nonce, int32(version))
					if err != nil {
						t.Fatalf("build version %08x header: %v", version, err)
					}
					hash := doubleSHA256Array(header)
					var hashBE [32]byte
					copy(hashBE[:], hash[:])
					reverseBytes32(&hashBE)
					targets[version] = new(big.Int).SetBytes(hashBE[:])
					headers[version] = header
				}

				candidate := targets[tc.rescueVersion]
				uniqueMinimum := true
				for _, version := range versions {
					if version != tc.rescueVersion && candidate.Cmp(targets[version]) >= 0 {
						uniqueMinimum = false
						break
					}
				}
				if uniqueMinimum {
					chosenNonce = nonce
					chosenTarget = new(big.Int).Set(candidate)
					expectedHead = append([]byte(nil), headers[tc.rescueVersion]...)
					break
				}
			}
			if chosenTarget == nil {
				t.Fatal("failed to find nonce with unique historical-version minimum hash")
			}
			job.Target = chosenTarget
			job.targetBE = uint256BEFromBigInt(chosenTarget)

			req := &StratumRequest{
				ID:     1,
				Method: "mining.submit",
				Params: []any{
					mc.currentWorker(),
					ids[0],
					"00000000",
					fmt.Sprintf("%08x", uint32(job.Template.CurTime)),
					fmt.Sprintf("%08x", chosenNonce),
					fmt.Sprintf("%08x", submittedBits),
				},
			}
			task, ok := mc.prepareSubmissionTask(req, time.Unix(job.Template.CurTime, 0))
			if !ok {
				t.Fatal("historical block submission rejected before PoW validation")
			}
			if task.useVersion != currentVersion {
				t.Fatalf("authoritative version = %08x, want current-mask version %08x", task.useVersion, currentVersion)
			}
			if task.blockRescueCount != 2 || task.blockRescueVersions != [3]uint32{historicalBIP, historicalXOR} {
				t.Fatalf("block rescue candidates = %08x count=%d", task.blockRescueVersions, task.blockRescueCount)
			}
			if task.policyReject.reason != rejectStaleJob {
				t.Fatalf("policy reject = %v, want stale job after reorg", task.policyReject.reason)
			}

			mc.processSubmissionTask(task)
			flushFoundBlockLog(t)
			if got := rpc.submitCalls.Load(); got != 1 {
				t.Fatalf("submitblock calls = %d, want 1", got)
			}
			if !strings.HasPrefix(rpc.blockHex, hex.EncodeToString(expectedHead)) {
				t.Fatalf("submitted block does not start with rescued version %08x header", tc.rescueVersion)
			}
		})
	}
}
