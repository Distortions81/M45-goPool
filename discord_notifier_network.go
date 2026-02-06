package main

import (
	"context"
	"net"
	"time"
)

func (n *discordNotifier) isNetworkOK() bool {
	if n == nil {
		return false
	}
	n.scheduleMu.Lock()
	ignoreUntil := n.ignoreNetworkUntil
	n.scheduleMu.Unlock()
	if !ignoreUntil.IsZero() && time.Now().Before(ignoreUntil) {
		// Ignore connectivity checks during the initial boot gate.
		return true
	}
	n.netMu.Lock()
	defer n.netMu.Unlock()
	if !n.networkKnown {
		// Until we have a signal either way, assume OK (startup will quickly check).
		return true
	}
	return n.networkOK
}

func (n *discordNotifier) setNetworkOK(ok bool, now time.Time) {
	if n == nil {
		return
	}
	n.netMu.Lock()
	prevKnown := n.networkKnown
	prevOK := n.networkOK
	n.networkKnown = true
	n.networkOK = ok
	n.netMu.Unlock()

	// On any network transition (offline OR online), reset notifier state so
	// we don't spam everyone due to our own connectivity blip.
	//
	// On first observation (prevKnown=false), do not reset state or emit a
	// channel message; this avoids pushing the boot gate back and avoids a
	// confusing "network back online" message at startup.
	if prevKnown && prevOK != ok {
		n.resetAllNotificationState(now)
		if ok {
			n.enqueueNotice("Network connectivity is back online; notifications will resume after the warm-up delay.")
		} else {
			n.enqueueNotice("Network connectivity appears offline; notifications are paused and state has been reset.")
		}
	}
}

func (n *discordNotifier) resetAllNotificationState(now time.Time) {
	if n == nil {
		return
	}
	// Clear per-user state so we re-seed without notifications after recovery.
	n.stateMu.Lock()
	n.statusByUser = nil
	n.links = nil
	n.linkIdx = 0
	n.lastLinksRefresh = time.Time{}
	n.lastSweepAt = time.Time{}
	n.stateMu.Unlock()

	// Clear queued pings.
	n.pingMu.Lock()
	n.pingQueue = nil
	n.droppedQueuedLines = 0
	n.lastDropNoticeAt = time.Time{}
	n.pingMu.Unlock()

	// Apply the same startup delay before resuming checks. When the network is
	// offline, checks are already gated by isNetworkOK().
	n.scheduleMu.Lock()
	n.startChecksAt = now.Add(5 * time.Minute)
	n.scheduleMu.Unlock()
}

func (n *discordNotifier) networkLoop(ctx context.Context) {
	const (
		onlineCheckInterval  = 15 * time.Second
		offlineCheckInterval = 15 * time.Second
		streakThreshold      = 4
	)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			now := time.Now()

			// Ignore network connectivity during the initial boot gate to avoid
			// disabling notifications due to transient startup conditions.
			n.scheduleMu.Lock()
			ignoreUntil := n.ignoreNetworkUntil
			n.scheduleMu.Unlock()
			if !ignoreUntil.IsZero() && now.Before(ignoreUntil) {
				n.netMu.Lock()
				n.netOKStreak = 0
				n.netBadStreak = 0
				n.netMu.Unlock()
				select {
				case <-ctx.Done():
					return
				case <-time.After(onlineCheckInterval):
				}
				continue
			}

			ok := checkNetworkConnectivity()

			n.netMu.Lock()
			if ok {
				n.netOKStreak++
				n.netBadStreak = 0
			} else {
				n.netBadStreak++
				n.netOKStreak = 0
			}
			known := n.networkKnown
			currentOK := n.networkOK
			okStreak := n.netOKStreak
			badStreak := n.netBadStreak
			n.netMu.Unlock()

			// Require a few consecutive results before flipping state to reduce
			// churn from transient dial failures.
			shouldFlip := false
			targetOK := currentOK
			if !known {
				shouldFlip = true
				targetOK = ok
			} else if currentOK && badStreak >= streakThreshold {
				shouldFlip = true
				targetOK = false
			} else if !currentOK && okStreak >= streakThreshold {
				shouldFlip = true
				targetOK = true
			}
			if shouldFlip {
				n.setNetworkOK(targetOK, now)
			}

			interval := onlineCheckInterval
			if !n.isNetworkOK() {
				interval = offlineCheckInterval
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
		}
	}
}

func checkNetworkConnectivity() bool {
	// Simple routing-level test using IPs to avoid DNS dependency.
	// We consider the network "up" if we can connect to any well-known host.
	targets := []string{
		"1.1.1.1:443", // Cloudflare
		"8.8.8.8:443", // Google DNS
		"9.9.9.9:443", // Quad9
	}
	d := net.Dialer{Timeout: 2 * time.Second}
	for _, addr := range targets {
		conn, err := d.Dial("tcp", addr)
		if err == nil && conn != nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}
