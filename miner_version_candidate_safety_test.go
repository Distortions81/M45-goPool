package main

import (
	"bytes"
	"fmt"
	"math/big"
	"testing"
	"time"
)

func TestSuppliedVersionBlockCandidatesPreserveWinningHeader(t *testing.T) {
	const (
		baseVersion   = uint32(0x20006000)
		submittedBits = uint32(0x00004000)
		currentMask   = uint32(0x00002000)
		currentBIP310 = uint32(0x20004000)
		rawFull       = submittedBits
		legacyXOR     = uint32(0x20002000)
	)

	tests := []struct {
		name              string
		allowMaskMismatch bool
		ordinaryVersion   uint32
		winningVersion    uint32
		rescueVersions    [3]uint32
	}{
		{
			name:              "strict policy still submits legacy XOR block",
			allowMaskMismatch: false,
			ordinaryVersion:   rawFull,
			winningVersion:    legacyXOR,
			rescueVersions:    [3]uint32{currentBIP310, legacyXOR},
		},
		{
			name:              "compatibility policy still submits raw full-version block",
			allowMaskMismatch: true,
			ordinaryVersion:   legacyXOR,
			winningVersion:    rawFull,
			rescueVersions:    [3]uint32{currentBIP310, rawFull},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mc, conn := minerConnForNotifyTest(t)
			mc.cfg.DataDir = t.TempDir()
			mc.cfg.ShareCheckDuplicate = false
			mc.cfg.ShareCheckParamFormat = true
			mc.cfg.ShareCheckVersionRolling = true
			mc.cfg.ShareAllowOutOfMaskVersionBits = tc.allowMaskMismatch
			mc.cfg.ShareRequireAuthorizedConnection = true
			mc.cfg.ShareRequireWorkerMatch = true
			mc.cfg.ShareJobFreshnessMode = shareJobFreshnessJobID
			mc.cfg.ShareNTimeMaxForwardSeconds = 600
			rpc := &countingSubmitRPC{}
			mc.rpc = rpc
			mc.versionMu.Lock()
			mc.versionRoll = true
			mc.minerMask = currentMask
			mc.minVerBits = 1
			mc.versionMu.Unlock()

			job := benchmarkSubmitJobForTest(t)
			job.Generation = 1
			job.ScriptTime = job.Template.CurTime
			job.Template.Version = int32(baseVersion)
			job.VersionMask = currentMask
			mc.sendNotifyFor(job, true)

			ids := notifyJobIDsFromOutput(t, conn.String())
			if len(ids) != 1 {
				t.Fatalf("notify IDs = %#v, want 1", ids)
			}
			_, _, _, _, _, _, binding, bindingOK, jobOK := mc.jobForIDWithLast(ids[0])
			if !jobOK || !bindingOK {
				t.Fatalf("notified job binding missing: jobOK=%v bindingOK=%v", jobOK, bindingOK)
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

			versions := []uint32{currentBIP310, rawFull, legacyXOR}
			nonce, target, expectedHeader := findUniqueMinimumVersionHeader(
				t,
				job,
				merkleRoot,
				versions,
				tc.winningVersion,
			)
			job.Target = target
			job.targetBE = uint256BEFromBigInt(target)

			req := &StratumRequest{
				ID:     1,
				Method: "mining.submit",
				Params: []any{
					mc.currentWorker(),
					ids[0],
					"00000000",
					fmt.Sprintf("%08x", uint32(job.Template.CurTime)),
					fmt.Sprintf("%08x", nonce),
					fmt.Sprintf("%08x", submittedBits),
				},
			}
			task, ok := mc.prepareSubmissionTask(req, time.Unix(job.Template.CurTime, 0))
			if !ok {
				t.Fatal("block submission rejected before PoW validation")
			}
			if task.useVersion != tc.ordinaryVersion {
				t.Fatalf("ordinary-share version = %08x, want %08x", task.useVersion, tc.ordinaryVersion)
			}
			if task.hasAlternateVersion {
				t.Fatalf("unexpected ordinary-share alternate %08x", task.alternateUseVersion)
			}
			if task.blockRescueCount != 2 || task.blockRescueVersions != tc.rescueVersions {
				t.Fatalf("block rescue candidates = %08x count=%d, want %08x count=2",
					task.blockRescueVersions, task.blockRescueCount, tc.rescueVersions)
			}

			mc.processSubmissionTask(task)
			flushFoundBlockLog(t)
			if got := rpc.submitCalls.Load(); got != 1 {
				t.Fatalf("submitblock calls = %d, want 1", got)
			}
			submittedBlock := make([]byte, len(rpc.blockHex)/2)
			if err := decodeHexToFixedBytes(submittedBlock, rpc.blockHex); err != nil {
				t.Fatalf("decode submitted block: %v", err)
			}
			if len(submittedBlock) < len(expectedHeader) || !bytes.Equal(submittedBlock[:len(expectedHeader)], expectedHeader) {
				t.Fatalf("submitted block header does not use winning version %08x", tc.winningVersion)
			}
		})
	}
}

func TestIntermediateVersionMaskRescuesDelayedBlock(t *testing.T) {
	const (
		baseVersion      = uint32(0x20006000)
		notifyMask       = uint32(0x00002000)
		intermediateMask = uint32(0x00004000)
		currentMask      = uint32(0x00008000)
		currentVersion   = baseVersion
		notifyVersion    = uint32(0x20004000)
		winningVersion   = uint32(0x20002000)
		rawVersion       = uint32(0)
	)

	mc, conn := minerConnForNotifyTest(t)
	mc.cfg.DataDir = t.TempDir()
	mc.cfg.ShareCheckDuplicate = false
	mc.cfg.ShareCheckParamFormat = true
	mc.cfg.ShareCheckVersionRolling = true
	mc.cfg.ShareAllowOutOfMaskVersionBits = false
	mc.cfg.ShareRequireAuthorizedConnection = true
	mc.cfg.ShareRequireWorkerMatch = true
	mc.cfg.ShareJobFreshnessMode = shareJobFreshnessJobID
	mc.cfg.ShareNTimeMaxForwardSeconds = 600
	rpc := &countingSubmitRPC{}
	mc.rpc = rpc
	mc.versionMu.Lock()
	mc.versionRoll = true
	mc.minerMask = notifyMask | intermediateMask | currentMask
	mc.minVerBits = 1
	mc.versionMu.Unlock()

	job := benchmarkSubmitJobForTest(t)
	job.Generation = 1
	job.ScriptTime = job.Template.CurTime
	job.Template.Version = int32(baseVersion)
	job.VersionMask = notifyMask
	mc.sendNotifyFor(job, true)
	ids := notifyJobIDsFromOutput(t, conn.String())
	if len(ids) != 1 {
		t.Fatalf("notify IDs = %#v, want 1", ids)
	}
	_, _, _, _, _, _, binding, bindingOK, jobOK := mc.jobForIDWithLast(ids[0])
	if !jobOK || !bindingOK {
		t.Fatalf("notified job binding missing: jobOK=%v bindingOK=%v", jobOK, bindingOK)
	}

	// BIP310 makes each set_version_mask effective immediately, including for
	// the already-advertised job. Move through a mask that is neither the job's
	// notify-time policy nor the policy current when the delayed block arrives.
	mc.notifyMu.Lock()
	if !mc.updateVersionMask(intermediateMask) {
		t.Fatal("intermediate mask transition was not applied")
	}
	mc.sendVersionMask()
	if !mc.updateVersionMask(currentMask) {
		t.Fatal("current mask transition was not applied")
	}
	mc.sendVersionMask()
	mc.notifyMu.Unlock()

	extranonce2 := []byte{0, 0, 0, 0}
	_, coinbaseTxID, err := buildNotifiedCoinbaseTx(binding, extranonce2)
	if err != nil {
		t.Fatalf("build notified coinbase: %v", err)
	}
	merkleRoot, ok := computeMerkleRootFromBranches32(coinbaseTxID, job.MerkleBranches)
	if !ok {
		t.Fatal("compute notified merkle root")
	}
	versions := []uint32{currentVersion, notifyVersion, winningVersion, rawVersion}
	nonce, target, expectedHeader := findUniqueMinimumVersionHeader(t, job, merkleRoot, versions, winningVersion)
	job.Target = target
	job.targetBE = uint256BEFromBigInt(target)

	req := &StratumRequest{
		ID:     1,
		Method: "mining.submit",
		Params: []any{
			mc.currentWorker(),
			ids[0],
			"00000000",
			fmt.Sprintf("%08x", uint32(job.Template.CurTime)),
			fmt.Sprintf("%08x", nonce),
			"00000000",
		},
	}
	task, ok := mc.prepareSubmissionTask(req, time.Unix(job.Template.CurTime, 0))
	if !ok {
		t.Fatal("intermediate-mask block rejected before PoW validation")
	}
	if task.useVersion != currentVersion {
		t.Fatalf("ordinary version = %08x, want current-mask version %08x", task.useVersion, currentVersion)
	}
	wantInline := [3]uint32{notifyVersion, winningVersion, rawVersion}
	if task.blockRescueCount != 3 || task.blockRescueVersions != wantInline || len(task.blockRescueExtra) != 0 {
		t.Fatalf("block rescue candidates = %08x extra=%08x count=%d, want %08x",
			task.blockRescueVersions, task.blockRescueExtra, task.blockRescueCount, wantInline)
	}

	mc.processSubmissionTask(task)
	flushFoundBlockLog(t)
	if got := rpc.submitCalls.Load(); got != 1 {
		t.Fatalf("submitblock calls = %d, want 1", got)
	}
	submittedBlock := make([]byte, len(rpc.blockHex)/2)
	if err := decodeHexToFixedBytes(submittedBlock, rpc.blockHex); err != nil {
		t.Fatalf("decode submitted block: %v", err)
	}
	if len(submittedBlock) < len(expectedHeader) || !bytes.Equal(submittedBlock[:len(expectedHeader)], expectedHeader) {
		t.Fatalf("submitted block header does not use intermediate mask version %08x", winningVersion)
	}
}

func findUniqueMinimumVersionHeader(
	t *testing.T,
	job *Job,
	merkleRoot [32]byte,
	versions []uint32,
	winner uint32,
) (uint32, *big.Int, []byte) {
	t.Helper()
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

		winningTarget, ok := targets[winner]
		if !ok {
			t.Fatalf("winning version %08x not in candidate set", winner)
		}
		uniqueMinimum := true
		for version, target := range targets {
			if version != winner && winningTarget.Cmp(target) >= 0 {
				uniqueMinimum = false
				break
			}
		}
		if uniqueMinimum {
			return nonce, new(big.Int).Set(winningTarget), append([]byte(nil), headers[winner]...)
		}
	}
	t.Fatal("failed to find nonce with unique winning-version minimum hash")
	return 0, nil, nil
}
