package main

import (
	"testing"
	"time"
)

func TestDiscordNotifierUpdateWorkerStates_OfflineAndRecoveryThresholds(t *testing.T) {
	n := &discordNotifier{}
	userID := "user-1"
	hash := "worker-hash"

	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	current := map[string]bool{hash: true}

	offline, online := n.updateWorkerStates(userID, current, t0)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected notifications on first observation: offline=%v online=%v", offline, online)
	}

	// Keep the worker online long enough to qualify for offline notifications.
	t1 := t0.Add(6 * time.Minute)
	offline, online = n.updateWorkerStates(userID, current, t1)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected notifications while online: offline=%v online=%v", offline, online)
	}

	// Transition to offline: still no immediate notification.
	t2 := t1
	current[hash] = false
	offline, online = n.updateWorkerStates(userID, current, t2)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected notifications on offline transition: offline=%v online=%v", offline, online)
	}

	t3 := t2.Add(4 * time.Minute)
	offline, online = n.updateWorkerStates(userID, current, t3)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected notifications before offline threshold: offline=%v online=%v", offline, online)
	}

	t4 := t2.Add(5 * time.Minute)
	offline, online = n.updateWorkerStates(userID, current, t4)
	if len(offline) != 1 || offline[0] != hash || len(online) != 0 {
		t.Fatalf("expected offline notification after threshold: offline=%v online=%v", offline, online)
	}

	// Transition back online after a qualifying offline duration.
	t5 := t4.Add(30 * time.Second)
	current[hash] = true
	offline, online = n.updateWorkerStates(userID, current, t5)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected notifications immediately after recovery: offline=%v online=%v", offline, online)
	}

	t6 := t5.Add(4 * time.Minute)
	offline, online = n.updateWorkerStates(userID, current, t6)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected notifications before recovery threshold: offline=%v online=%v", offline, online)
	}

	t7 := t5.Add(5 * time.Minute)
	offline, online = n.updateWorkerStates(userID, current, t7)
	if len(offline) != 0 || len(online) != 1 || online[0] != hash {
		t.Fatalf("expected recovery notification after threshold: offline=%v online=%v", offline, online)
	}
}

func TestDiscordNotifierUpdateWorkerStates_NoRecoveryWhenOfflineShort(t *testing.T) {
	n := &discordNotifier{}
	userID := "user-2"
	hash := "worker-hash-2"

	t0 := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	current := map[string]bool{hash: true}

	offline, online := n.updateWorkerStates(userID, current, t0)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected notifications on first observation: offline=%v online=%v", offline, online)
	}

	// Qualify for offline notifications, but go offline only briefly.
	t1 := t0.Add(6 * time.Minute)
	current[hash] = false
	offline, online = n.updateWorkerStates(userID, current, t1)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected notifications on offline transition: offline=%v online=%v", offline, online)
	}

	t2 := t1.Add(1 * time.Minute) // < 5m offline
	current[hash] = true
	offline, online = n.updateWorkerStates(userID, current, t2)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected notifications on short offline recovery: offline=%v online=%v", offline, online)
	}

	t3 := t2.Add(10 * time.Minute)
	offline, online = n.updateWorkerStates(userID, current, t3)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected recovery notification after short offline: offline=%v online=%v", offline, online)
	}
}

func TestDiscordNotifierUpdateWorkerStates_NoOfflineWhenOnlineTooShort(t *testing.T) {
	n := &discordNotifier{}
	userID := "user-4"
	hash := "worker-hash-4"

	t0 := time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC)
	current := map[string]bool{hash: true}

	offline, online := n.updateWorkerStates(userID, current, t0)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected notifications on first observation: offline=%v online=%v", offline, online)
	}

	// Not online long enough before going offline -> should never notify offline.
	t1 := t0.Add(1 * time.Minute)
	current[hash] = false
	offline, online = n.updateWorkerStates(userID, current, t1)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected notifications on offline transition: offline=%v online=%v", offline, online)
	}

	t2 := t1.Add(10 * time.Minute)
	offline, online = n.updateWorkerStates(userID, current, t2)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected offline notification without qualifying prior online: offline=%v online=%v", offline, online)
	}
}

func TestDiscordNotifierUpdateWorkerStates_TuningOverrideSeconds(t *testing.T) {
	s := &StatusServer{}
	cfg := defaultConfig()
	cfg.DiscordWorkerNotifyThresholdSeconds = 2
	s.UpdateConfig(cfg)

	n := &discordNotifier{s: s}
	userID := "user-5"
	hash := "worker-hash-5"

	t0 := time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC)
	current := map[string]bool{hash: true}

	offline, online := n.updateWorkerStates(userID, current, t0)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected notifications on first observation: offline=%v online=%v", offline, online)
	}

	// Qualify for offline notifications.
	t1 := t0.Add(3 * time.Second)
	offline, online = n.updateWorkerStates(userID, current, t1)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected notifications while online: offline=%v online=%v", offline, online)
	}

	// Go offline and wait for threshold.
	t2 := t1
	current[hash] = false
	offline, online = n.updateWorkerStates(userID, current, t2)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected notifications on offline transition: offline=%v online=%v", offline, online)
	}

	t3 := t2.Add(2 * time.Second)
	offline, online = n.updateWorkerStates(userID, current, t3)
	if len(offline) != 1 || offline[0] != hash || len(online) != 0 {
		t.Fatalf("expected offline notification after threshold: offline=%v online=%v", offline, online)
	}

	// Come back online; notify recovery after staying online for threshold.
	t4 := t3.Add(1 * time.Second)
	current[hash] = true
	offline, online = n.updateWorkerStates(userID, current, t4)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected notifications immediately after recovery: offline=%v online=%v", offline, online)
	}

	t5 := t4.Add(2 * time.Second)
	offline, online = n.updateWorkerStates(userID, current, t5)
	if len(offline) != 0 || len(online) != 1 || online[0] != hash {
		t.Fatalf("expected recovery notification after threshold: offline=%v online=%v", offline, online)
	}
}

func TestDiscordNotifierUpdateWorkerStates_NoOfflineWithoutPriorOnline(t *testing.T) {
	n := &discordNotifier{}
	userID := "user-3"
	hash := "worker-hash-3"

	t0 := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	current := map[string]bool{hash: false}

	offline, online := n.updateWorkerStates(userID, current, t0)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected notifications on first observation: offline=%v online=%v", offline, online)
	}

	t1 := t0.Add(5 * time.Minute)
	offline, online = n.updateWorkerStates(userID, current, t1)
	if len(offline) != 0 || len(online) != 0 {
		t.Fatalf("unexpected offline notification without prior online: offline=%v online=%v", offline, online)
	}
}
