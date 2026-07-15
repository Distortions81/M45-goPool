package main

import (
	"strings"
	"testing"
)

func notifiedCleanFlag(t *testing.T, msg StratumMessage) bool {
	t.Helper()
	if len(msg.Params) < 9 {
		t.Fatalf("notify params too short: %#v", msg.Params)
	}
	clean, ok := msg.Params[8].(bool)
	if !ok {
		t.Fatalf("notify clean flag has type %T, want bool", msg.Params[8])
	}
	return clean
}

func TestSendNotifyForCleanSemantics(t *testing.T) {
	mc, conn := minerConnForNotifyTest(t)
	initial := benchmarkSubmitJobForTest(t)
	initial.Generation = 1
	initial.ScriptTime = initial.Template.CurTime
	initial.VersionMask = defaultVersionMask
	mc.sendNotifyFor(initial, true)

	nonClean := *initial
	nonClean.JobID = "same-chain-coinbase-update"
	nonClean.Generation = 2
	nonClean.Clean = false
	nonClean.CoinbaseValue--
	nonClean.Template.CoinbaseValue--
	mc.sendNotifyFor(&nonClean, false)

	managerClean := nonClean
	managerClean.JobID = "same-chain-manager-clean"
	managerClean.Generation = 3
	managerClean.Clean = true
	mc.sendNotifyFor(&managerClean, false)

	msgs := notifyMessagesFromOutput(t, conn.String())
	if len(msgs) != 3 {
		t.Fatalf("notify count = %d, want 3", len(msgs))
	}
	if !notifiedCleanFlag(t, msgs[0]) {
		t.Fatal("initial notify must be clean")
	}
	if notifiedCleanFlag(t, msgs[1]) {
		t.Fatal("same-chain coinbase update must remain non-clean")
	}
	if !notifiedCleanFlag(t, msgs[2]) {
		t.Fatal("manager-classified hard-policy update must remain clean")
	}
}

func TestCoalescedJobPreservesCleanTransition(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Job)
	}{
		{
			name: "chain reorg",
			mutate: func(job *Job) {
				job.Template.Previous = strings.Repeat("11", 32)
				job.PrevHash = job.Template.Previous
				job.Template.Height--
			},
		},
		{
			name: "same-parent version policy",
			mutate: func(job *Job) {
				job.Template.Version++
			},
		},
		{
			name: "same-parent mintime",
			mutate: func(job *Job) {
				job.Template.Mintime++
			},
		},
		{
			name: "same-parent rolling mask",
			mutate: func(job *Job) {
				job.VersionMask &^= 1 << 13
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mc, conn := minerConnForNotifyTest(t)
			initial := benchmarkSubmitJobForTest(t)
			initial.Generation = 1
			initial.ScriptTime = initial.Template.CurTime
			initial.VersionMask = defaultVersionMask
			mc.sendNotifyFor(initial, true)

			cleanIntermediate := *initial
			cleanIntermediate.JobID = "clean-intermediate"
			cleanIntermediate.Generation = 2
			cleanIntermediate.Clean = true
			tc.mutate(&cleanIntermediate)

			latest := cleanIntermediate
			latest.JobID = "later-non-clean-update"
			latest.Generation = 3
			latest.Clean = false
			latest.CoinbaseValue--
			latest.Template.CoinbaseValue--

			pending := make(chan *Job, 1)
			pending <- &cleanIntermediate
			if dropped := sendJobNonBlocking(pending, &latest); !dropped {
				t.Fatal("expected later job to replace pending clean job")
			}
			delivered := <-pending
			if delivered != &latest {
				t.Fatalf("delivered job = %q, want latest", delivered.JobID)
			}
			mc.sendNotifyFor(delivered, false)

			msgs := notifyMessagesFromOutput(t, conn.String())
			if len(msgs) != 2 {
				t.Fatalf("notify count = %d, want 2", len(msgs))
			}
			if !notifiedCleanFlag(t, msgs[1]) {
				t.Fatal("coalesced hard transition was not delivered with clean_jobs=true")
			}
		})
	}
}
