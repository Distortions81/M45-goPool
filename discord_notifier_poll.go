package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

func (n *discordNotifier) loop(ctx context.Context) {
	// Throttle: aim to scan all subscribed users within ~30s at steady state
	// while still spreading work out smoothly.
	const (
		checkTick      = 100 * time.Millisecond
		checkBudget    = 90 * time.Millisecond
		targetFullScan = 30 * time.Second
	)

	ticker := time.NewTicker(checkTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			n.close()
			return
		case <-ticker.C:
			n.pollBatch(checkBudget, checkTick, targetFullScan)
		}
	}
}

func (n *discordNotifier) usersPerTick(total int, tick, target time.Duration) int {
	if total <= 0 || tick <= 0 || target <= 0 {
		return 0
	}
	// ceil(total * tick / target)
	return int(math.Ceil(float64(total) * float64(tick) / float64(target)))
}

func (n *discordNotifier) pollBatch(budget, tick, targetFullScan time.Duration) {
	if n == nil || n.s == nil || n.dg == nil || n.s.workerLists == nil {
		return
	}
	now := time.Now()
	if !n.isNetworkOK() {
		return
	}
	n.scheduleMu.Lock()
	startAt := n.startChecksAt
	bootAt := n.bootChecksAt
	bootNoticeSent := n.bootNoticeSent
	n.scheduleMu.Unlock()
	if !startAt.IsZero() && now.Before(startAt) {
		return
	}
	if !bootNoticeSent && !bootAt.IsZero() && !now.Before(bootAt) {
		n.scheduleMu.Lock()
		if !n.bootNoticeSent {
			n.bootNoticeSent = true
		}
		n.scheduleMu.Unlock()
		n.enqueueNotice("Pool has been online for 5+ minutes; Discord notifications are now active.")
	}
	const refreshInterval = 30 * time.Second
	n.stateMu.Lock()
	lastLinksRefresh := n.lastLinksRefresh
	n.stateMu.Unlock()
	if lastLinksRefresh.IsZero() || now.Sub(lastLinksRefresh) >= refreshInterval {
		links, err := n.s.workerLists.ListEnabledDiscordLinks()
		if err != nil || len(links) == 0 {
			n.stateMu.Lock()
			n.links = nil
			n.linkIdx = 0
			n.lastLinksRefresh = now
			n.stateMu.Unlock()
			n.sweep(nil)
			return
		}
		n.stateMu.Lock()
		n.links = links
		n.linkIdx = 0
		n.lastLinksRefresh = now
		n.stateMu.Unlock()
		active := make(map[string]struct{}, len(links))
		for _, link := range links {
			if link.UserID != "" {
				active[link.UserID] = struct{}{}
			}
		}
		n.sweep(active)
	}
	n.stateMu.Lock()
	hasLinks := len(n.links) > 0
	n.stateMu.Unlock()
	if !hasLinks {
		return
	}

	n.stateMu.Lock()
	linkCount := len(n.links)
	n.stateMu.Unlock()
	perTick := n.usersPerTick(linkCount, tick, targetFullScan)
	if perTick < 1 {
		perTick = 1
	}
	deadline := time.Now().Add(budget)
	checked := 0
	for checked < perTick && time.Now().Before(deadline) {
		n.stateMu.Lock()
		if len(n.links) == 0 {
			n.stateMu.Unlock()
			return
		}
		link := n.links[n.linkIdx%len(n.links)]
		n.linkIdx++
		n.stateMu.Unlock()
		if link.UserID == "" || link.DiscordUserID == "" {
			continue
		}
		n.checkUser(link, now)
		checked++
	}
}

func (n *discordNotifier) checkUser(link discordLink, now time.Time) {
	if n == nil || n.s == nil || n.dg == nil || n.s.workerLists == nil {
		return
	}
	if strings.TrimSpace(n.notifyChannelID) == "" {
		return
	}

	saved, err := n.s.workerLists.List(link.UserID)
	if err != nil || len(saved) == 0 {
		n.clearUserOfflineState(link.UserID)
		return
	}

	currentOnline := make(map[string]bool, len(saved))
	nameByHash := make(map[string]string, len(saved))
	for _, sw := range saved {
		if !sw.NotifyEnabled {
			continue
		}
		views, lookupHash := n.s.findSavedWorkerConnections(sw.Name, sw.Hash, now)
		if lookupHash == "" {
			continue
		}
		currentOnline[lookupHash] = len(views) > 0
		if _, ok := nameByHash[lookupHash]; !ok {
			nameByHash[lookupHash] = sw.Name
		}
	}

	offlineOverdue, onlineOverdue := n.updateWorkerStates(link.UserID, currentOnline, now)
	if len(offlineOverdue) == 0 && len(onlineOverdue) == 0 {
		return
	}

	thresholdLabel := formatNotifyThresholdLabel(n.workerNotifyThreshold())
	detailed := len(offlineOverdue) <= 1 && len(onlineOverdue) <= 1
	parts := make([]string, 0, 3)
	if detailed {
		if len(offlineOverdue) > 0 {
			parts = append(parts, "Offline >"+thresholdLabel+": "+strings.Join(renderNames(offlineOverdue, nameByHash), ", "))
		}
		if len(onlineOverdue) > 0 {
			parts = append(parts, "Back online ("+thresholdLabel+"+): "+strings.Join(renderNames(onlineOverdue, nameByHash), ", "))
		}
	} else {
		if len(offlineOverdue) > 0 {
			parts = append(parts, fmt.Sprintf("%d miners offline >%s", len(offlineOverdue), thresholdLabel))
		}
		if len(onlineOverdue) > 0 {
			parts = append(parts, fmt.Sprintf("%d miners back online (%s+)", len(onlineOverdue), thresholdLabel))
		}
	}

	line := strings.Join(parts, " | ")
	n.enqueuePing(link.DiscordUserID, line)
}

// NotifyFoundBlock pings any subscribed Discord users who have this worker
// saved with notifications enabled.
func (n *discordNotifier) NotifyFoundBlock(worker string, height int64, hashHex string, now time.Time) {
	if n == nil || n.s == nil || n.dg == nil || n.s.workerLists == nil {
		return
	}
	if !n.enabled() {
		return
	}
	if strings.TrimSpace(n.notifyChannelID) == "" {
		return
	}
	worker = strings.TrimSpace(worker)
	hashHex = strings.TrimSpace(hashHex)
	if worker == "" || hashHex == "" || height <= 0 {
		return
	}

	subscribers, err := n.s.workerLists.ListNotifiedUsersForWorker(worker)
	if err != nil || len(subscribers) == 0 {
		return
	}

	workerLabel := shortWorkerName(worker, workerNamePrefix, workerNameSuffix)
	if workerLabel == "" {
		workerLabel = worker
	}
	hashLabel := shortDisplayID(hashHex, hashPrefix, hashSuffix)
	if hashLabel == "" {
		hashLabel = hashHex
	}

	// Keep the message short; it's posted in a shared channel.
	line := fmt.Sprintf("Block found: height %d by %s (hash %s)", height, workerLabel, hashLabel)
	_ = now // reserved for future time-based de-dupe/persistence

	seenDiscord := make(map[string]struct{}, 8)
	for _, sub := range subscribers {
		discordUserID, enabled, ok, err := n.s.workerLists.GetDiscordLink(sub.UserID)
		if err != nil || !ok || !enabled {
			continue
		}
		discordUserID = strings.TrimSpace(discordUserID)
		if discordUserID == "" {
			continue
		}
		if _, dup := seenDiscord[discordUserID]; dup {
			continue
		}
		seenDiscord[discordUserID] = struct{}{}
		n.enqueuePing(discordUserID, line)
	}
}

func (n *discordNotifier) workerNotifyThreshold() time.Duration {
	sec := defaultDiscordWorkerNotifyThresholdSeconds
	if n != nil && n.s != nil {
		if v := n.s.Config().DiscordWorkerNotifyThresholdSeconds; v > 0 {
			sec = v
		}
	}
	if sec <= 0 {
		sec = defaultDiscordWorkerNotifyThresholdSeconds
	}
	return time.Duration(sec) * time.Second
}

func formatNotifyThresholdLabel(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	// Keep Discord messages short and stable.
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	return d.Truncate(time.Second).String()
}

func (n *discordNotifier) updateWorkerStates(userID string, current map[string]bool, now time.Time) (offlineOverdue, onlineOverdue []string) {
	// Use one sustained threshold to reduce flapping notifications and require
	// meaningful state changes (tunable via tuning.toml).
	offlineThreshold := n.workerNotifyThreshold()
	onlineBeforeOfflineThreshold := offlineThreshold
	recoveryThreshold := offlineThreshold

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	if n.statusByUser == nil {
		n.statusByUser = make(map[string]map[string]workerNotifyState, 16)
	}
	state := n.statusByUser[userID]
	firstObservation := false
	if state == nil {
		state = make(map[string]workerNotifyState, len(current))
		n.statusByUser[userID] = state
		firstObservation = true
	}

	// Update states based on current online map.
	for hash, online := range current {
		st, ok := state[hash]
		if !ok {
			st = workerNotifyState{Online: online, Since: now}
			if online {
				st.SeenOnline = true
			} else {
				st.SeenOffline = true
			}
			state[hash] = st
			continue
		}

		// Transition: reset timers and notification flags.
		if st.Online != online {
			// Compute how long we were in the previous state (best-effort).
			prevDuration := time.Duration(0)
			if !st.Since.IsZero() {
				prevDuration = now.Sub(st.Since)
				if prevDuration < 0 {
					prevDuration = 0
				}
			}
			wasOnline := st.Online

			if online {
				st.SeenOnline = true
			} else {
				st.SeenOffline = true
			}
			st.Online = online
			st.Since = now
			st.OfflineNotified = false
			st.RecoveryNotified = false
			if wasOnline && !online {
				// Online -> offline: qualify the offline notification based on the
				// length of the preceding online period.
				st.OfflineEligible = prevDuration >= onlineBeforeOfflineThreshold
				st.RecoveryEligible = false
			} else if !wasOnline && online {
				// Offline -> online: qualify the recovery notification based on the
				// length of the preceding offline period.
				st.RecoveryEligible = prevDuration >= offlineThreshold
				st.OfflineEligible = false
			}
			state[hash] = st
			continue
		}

		// First observation seeds state without firing notifications (but timers start).
		if firstObservation {
			continue
		}

		if !online &&
			st.SeenOnline &&
			st.OfflineEligible &&
			!st.OfflineNotified &&
			!st.Since.IsZero() &&
			now.Sub(st.Since) >= offlineThreshold {
			st.OfflineNotified = true
			state[hash] = st
			offlineOverdue = append(offlineOverdue, hash)
			continue
		}

		if online &&
			st.SeenOffline &&
			st.RecoveryEligible &&
			!st.RecoveryNotified &&
			!st.Since.IsZero() &&
			now.Sub(st.Since) >= recoveryThreshold {
			st.RecoveryNotified = true
			st.RecoveryEligible = false
			state[hash] = st
			onlineOverdue = append(onlineOverdue, hash)
			continue
		}

		// Track SeenOnline/SeenOffline over time.
		if online && !st.SeenOnline {
			st.SeenOnline = true
			state[hash] = st
		} else if !online && !st.SeenOffline {
			st.SeenOffline = true
			state[hash] = st
		}
	}

	// If a saved worker disappears, forget it.
	for hash := range state {
		if _, ok := current[hash]; !ok {
			delete(state, hash)
		}
	}

	if len(state) == 0 {
		delete(n.statusByUser, userID)
	}

	return offlineOverdue, onlineOverdue
}

func renderNames(hashes []string, nameByHash map[string]string) []string {
	const maxNames = 20
	out := make([]string, 0, minInt(len(hashes), maxNames))
	for i, h := range hashes {
		if i >= maxNames {
			out = append(out, fmt.Sprintf("…(+%d more)", len(hashes)-maxNames))
			break
		}
		name := strings.TrimSpace(nameByHash[h])
		if name == "" {
			name = h
		} else {
			// Notifications are posted in a shared channel; censor worker names
			// to avoid leaking full wallet identifiers.
			if censored := shortWorkerName(name, 8, 8); censored != "" {
				name = censored
			}
		}
		out = append(out, name)
	}
	return out
}

func (n *discordNotifier) sweep(active map[string]struct{}) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	now := time.Now()
	if !n.lastSweepAt.IsZero() && now.Sub(n.lastSweepAt) < time.Minute {
		return
	}
	n.lastSweepAt = now
	if n.statusByUser == nil {
		return
	}
	if active == nil {
		// Nothing enabled; clear everything.
		n.statusByUser = nil
		return
	}
	for uid := range n.statusByUser {
		if _, ok := active[uid]; !ok {
			delete(n.statusByUser, uid)
		}
	}
}

func (n *discordNotifier) clearUserOfflineState(userID string) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	if n.statusByUser != nil {
		delete(n.statusByUser, userID)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
