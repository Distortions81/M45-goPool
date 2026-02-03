package main

import (
	"context"
	"fmt"
	"math"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type discordNotifier struct {
	s                  *StatusServer
	dg                 *discordgo.Session
	guildID            string
	scheduleMu         sync.Mutex
	startChecksAt      time.Time
	bootChecksAt       time.Time
	bootNoticeSent     bool
	ignoreNetworkUntil time.Time
	notifyChannelID    string
	stateMu            sync.Mutex
	statusByUser       map[string]map[string]workerNotifyState // clerk user_id -> workerHash -> state
	lastSweepAt        time.Time
	links              []discordLink
	linkIdx            int
	lastLinksRefresh   time.Time

	pingMu    sync.Mutex
	pingQueue []queuedDiscordMessage

	droppedQueuedLines int
	lastDropNoticeAt   time.Time

	netMu        sync.Mutex
	networkOK    bool
	networkKnown bool
	netOKStreak  int
	netBadStreak int
}

func (n *discordNotifier) savedWorkersURL() string {
	if n == nil || n.s == nil {
		return ""
	}
	domain := strings.TrimSpace(n.s.Config().StatusBrandDomain)
	if domain == "" {
		return ""
	}
	base := domain
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	base = strings.TrimRight(base, "/")
	return base + "/saved-workers"
}

func (n *discordNotifier) enabled() bool {
	if n == nil || n.s == nil {
		return false
	}
	cfg := n.s.Config()
	return strings.TrimSpace(cfg.DiscordServerID) != "" &&
		strings.TrimSpace(cfg.DiscordBotToken) != "" &&
		strings.TrimSpace(cfg.DiscordNotifyChannelID) != ""
}

func (n *discordNotifier) start(ctx context.Context) error {
	if n == nil || n.s == nil {
		return fmt.Errorf("notifier not configured")
	}
	if !n.enabled() {
		return nil
	}
	cfg := n.s.Config()
	token := strings.TrimSpace(cfg.DiscordBotToken)
	n.guildID = strings.TrimSpace(cfg.DiscordServerID)
	n.notifyChannelID = strings.TrimSpace(cfg.DiscordNotifyChannelID)
	n.scheduleMu.Lock()
	n.startChecksAt = time.Now().Add(5 * time.Minute)
	n.bootChecksAt = n.startChecksAt
	n.bootNoticeSent = false
	n.ignoreNetworkUntil = n.startChecksAt
	n.scheduleMu.Unlock()

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return err
	}
	dg.Identify.Intents = discordgo.MakeIntent(discordgo.IntentsGuilds)

	// Reset notification state on Discord disconnect/reconnect to avoid spurious
	// offline/online storms from our own connectivity hiccups.
	dg.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) {
		n.resetAllNotificationState(time.Now())
	})
	dg.AddHandler(func(_ *discordgo.Session, _ *discordgo.Ready) {
		n.resetAllNotificationState(time.Now())
	})
	dg.AddHandler(func(_ *discordgo.Session, _ *discordgo.Resumed) {
		n.resetAllNotificationState(time.Now())
	})

	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type != discordgo.InteractionApplicationCommand {
			return
		}
		n.handleCommand(s, i)
	})

	if err := dg.Open(); err != nil {
		return err
	}
	n.dg = dg

	if err := n.registerCommands(); err != nil {
		logger.Warn("discord command registration failed", "error", err)
	}

	go n.loop(ctx)
	go n.pingLoop(ctx)
	go n.networkLoop(ctx)
	logger.Info("discord notifier started", "guild_id", n.guildID)
	return nil
}

func (n *discordNotifier) close() {
	if n == nil || n.dg == nil {
		return
	}
	_ = n.dg.Close()
}

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

func (n *discordNotifier) registerCommands() error {
	if n == nil || n.dg == nil {
		return nil
	}
	appID := ""
	if n.dg.State != nil && n.dg.State.User != nil {
		appID = n.dg.State.User.ID
	}
	if appID == "" || n.guildID == "" {
		return fmt.Errorf("missing appID or guildID")
	}

	cmds := []*discordgo.ApplicationCommand{
		{
			Name:        "notify",
			Description: "Enable goPool notifications using a one-time code",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "code",
					Description: "One-time code from goPool",
					Type:        discordgo.ApplicationCommandOptionString,
					Required:    true,
				},
			},
		},
		{
			Name:        "notify-stop",
			Description: "Disable goPool notifications",
		},
	}

	_, err := n.dg.ApplicationCommandBulkOverwrite(appID, n.guildID, cmds)
	return err
}

func (n *discordNotifier) handleCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if n == nil || n.s == nil || s == nil || i == nil {
		return
	}
	if strings.TrimSpace(i.GuildID) != "" && n.guildID != "" && i.GuildID != n.guildID {
		return
	}
	if i.Member == nil || i.Member.User == nil {
		return
	}

	name := i.ApplicationCommandData().Name
	switch name {
	case "notify":
		code := ""
		for _, opt := range i.ApplicationCommandData().Options {
			if opt.Type == discordgo.ApplicationCommandOptionString && opt.Name == "code" {
				code = strings.TrimSpace(opt.StringValue())
			}
		}
		if code == "" {
			_ = respondEphemeral(s, i, "Missing code.")
			return
		}

		userID, ok := n.s.redeemOneTimeCode(code, time.Now())
		if !ok || userID == "" {
			_ = respondEphemeral(s, i, "Invalid or expired code. Generate a new one-time code from goPool and try again.")
			return
		}
		if n.s.workerLists != nil {
			if err := n.s.workerLists.UpsertDiscordLink(userID, i.Member.User.ID, true, time.Now()); err != nil {
				logger.Warn("discord link upsert failed", "error", err)
				_ = respondEphemeral(s, i, "Failed to enable notifications (server error).")
				return
			}
		} else {
			_ = respondEphemeral(s, i, "Notifications are not enabled on this pool.")
			return
		}

		channelRef := ""
		if ch := strings.TrimSpace(n.notifyChannelID); ch != "" {
			channelRef = fmt.Sprintf(" in <#%s>", ch)
		}
		_ = respondEphemeral(s, i, "Enabled. You’ll be pinged"+channelRef+" when a saved worker stays offline for over 2 minutes (and again when it’s back online for 2+ minutes), and when a saved worker finds a block. To turn this off, run `/notify-stop`.")
	case "notify-stop":
		if n.s.workerLists != nil {
			_ = n.s.workerLists.DisableDiscordLinkByDiscordUserID(i.Member.User.ID, time.Now())
		}
		_ = respondEphemeral(s, i, "Disabled.")
	default:
		// ignore
	}
}

func respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, msg string) error {
	if s == nil || i == nil {
		return nil
	}
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: msg,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

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
	if n.lastLinksRefresh.IsZero() || now.Sub(n.lastLinksRefresh) >= refreshInterval {
		links, err := n.s.workerLists.ListEnabledDiscordLinks()
		if err != nil || len(links) == 0 {
			n.links = nil
			n.linkIdx = 0
			n.lastLinksRefresh = now
			n.sweep(nil)
			return
		}
		n.links = links
		n.linkIdx = 0
		n.lastLinksRefresh = now
		active := make(map[string]struct{}, len(links))
		for _, link := range links {
			if link.UserID != "" {
				active[link.UserID] = struct{}{}
			}
		}
		n.sweep(active)
	}
	if len(n.links) == 0 {
		return
	}

	perTick := n.usersPerTick(len(n.links), tick, targetFullScan)
	if perTick < 1 {
		perTick = 1
	}
	deadline := time.Now().Add(budget)
	checked := 0
	for checked < perTick && time.Now().Before(deadline) {
		link := n.links[n.linkIdx%len(n.links)]
		n.linkIdx++
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

type queuedDiscordMessage struct {
	Notices     []string
	UserOrder   []string
	LinesByUser map[string][]string
}

func (n *discordNotifier) noticePrefix() string {
	if n == nil || n.s == nil {
		return "[goPool] "
	}
	cfg := n.s.Config()

	// Prefer the stable coinbase-based tag when available, but fall back to
	// PoolTagPrefix so config reloads (which may omit CoinbaseMsg) still keep a
	// distinct tag in Discord messages.
	tag := displayPoolTagFromCoinbaseMessage(cfg.CoinbaseMsg)
	if tag == "" {
		brand := poolSoftwareName
		if cfg.PoolTagPrefix != "" {
			brand = cfg.PoolTagPrefix + "-" + brand
		}
		tag = "/" + brand + "/"
	}
	// For Discord notices, use a compact bracket tag without slashes.
	tag = strings.Trim(tag, "/")
	if tag == "" {
		tag = poolSoftwareName
	}
	return "[" + tag + "] "
}

func (n *discordNotifier) enqueueNotice(msg string) {
	if n == nil {
		return
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	n.enqueueQueuedLine("", n.noticePrefix()+msg)
}

func (n *discordNotifier) enqueuePing(discordUserID, line string) {
	if n == nil {
		return
	}
	discordUserID = strings.TrimSpace(discordUserID)
	line = strings.TrimSpace(line)
	if discordUserID == "" || line == "" {
		return
	}
	n.enqueueQueuedLine(discordUserID, line)
}

func (n *discordNotifier) enqueueQueuedLine(mentionUserID, line string) {
	if n == nil {
		return
	}
	mentionUserID = strings.TrimSpace(mentionUserID)
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	const (
		maxMessagesQueued = 3
		maxChars          = 1000
	)

	if len(line) > maxChars {
		line = line[:maxChars]
	}

	n.pingMu.Lock()
	defer n.pingMu.Unlock()

	// Try to append to the last queued message if it fits (group per user so we
	// only mention each user once per message).
	if len(n.pingQueue) > 0 {
		lastIdx := len(n.pingQueue) - 1
		last := n.pingQueue[lastIdx]

		if mentionUserID == "" {
			last.Notices = append(last.Notices, line)
		} else {
			if last.LinesByUser == nil {
				last.LinesByUser = make(map[string][]string, 8)
			}
			if _, ok := last.LinesByUser[mentionUserID]; !ok {
				last.UserOrder = append(last.UserOrder, mentionUserID)
			}
			last.LinesByUser[mentionUserID] = append(last.LinesByUser[mentionUserID], line)
		}

		if rendered, _ := renderQueuedMessage(last); len(rendered) <= maxChars {
			n.pingQueue[lastIdx] = last
			return
		}
	}

	// Start a new message if we still have capacity; otherwise drop.
	if len(n.pingQueue) >= maxMessagesQueued {
		n.droppedQueuedLines++
		return
	}
	msg := queuedDiscordMessage{}
	if mentionUserID == "" {
		msg.Notices = []string{line}
	} else {
		msg.UserOrder = []string{mentionUserID}
		msg.LinesByUser = map[string][]string{mentionUserID: {line}}
	}
	n.pingQueue = append(n.pingQueue, msg)
}

func renderQueuedMessage(m queuedDiscordMessage) (content string, mentions []string) {
	lines := make([]string, 0, len(m.Notices)+len(m.UserOrder))
	for _, n := range m.Notices {
		n = strings.TrimSpace(n)
		if n != "" {
			lines = append(lines, n)
		}
	}
	if m.LinesByUser != nil {
		for _, uid := range m.UserOrder {
			uid = strings.TrimSpace(uid)
			if uid == "" {
				continue
			}
			parts := m.LinesByUser[uid]
			clean := make([]string, 0, len(parts))
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					clean = append(clean, p)
				}
			}
			if len(clean) == 0 {
				continue
			}
			lines = append(lines, fmt.Sprintf("<@%s> %s", uid, strings.Join(clean, " | ")))
			mentions = append(mentions, uid)
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), mentions
}

func (n *discordNotifier) subscribedUserCount() int {
	if n == nil {
		return 0
	}
	n.stateMu.Lock()
	cached := len(n.links)
	n.stateMu.Unlock()
	if cached > 0 {
		return cached
	}
	if n.s == nil || n.s.workerLists == nil {
		return 0
	}
	if links, err := n.s.workerLists.ListEnabledDiscordLinks(); err == nil {
		return len(links)
	}
	return 0
}

func (n *discordNotifier) pingLoop(ctx context.Context) {
	// 1 message per 10 seconds max.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.sendNextQueuedMessage()
		}
	}
}

func (n *discordNotifier) sendNextQueuedMessage() {
	if n == nil || n.dg == nil {
		return
	}
	if !n.isNetworkOK() {
		return
	}
	channelID := strings.TrimSpace(n.notifyChannelID)
	if channelID == "" {
		return
	}

	n.pingMu.Lock()
	if len(n.pingQueue) == 0 {
		n.pingMu.Unlock()
		return
	}
	queued := append([]queuedDiscordMessage(nil), n.pingQueue...)
	n.pingMu.Unlock()

	// Outage guard: if we'd ping "too many" subscribed users at once, treat it as a
	// localized outage (e.g. upstream connectivity) and drop the notification burst.
	// Threshold: > max(100 users, 10% of subscribed users).
	uniqueUsers := make(map[string]struct{}, 256)
	for _, m := range queued {
		for _, id := range m.UserOrder {
			id = strings.TrimSpace(id)
			if id != "" {
				uniqueUsers[id] = struct{}{}
			}
		}
	}
	affected := len(uniqueUsers)
	subscribed := n.subscribedUserCount()
	if subscribed > 0 {
		pctThreshold := int(math.Ceil(float64(subscribed) * 0.10))
		if pctThreshold < 0 {
			pctThreshold = 0
		}
		threshold := 100
		if pctThreshold > threshold {
			threshold = pctThreshold
		}
		if affected > threshold {
			logger.Warn("notification burst dropped (possible localized outage)",
				"affected_users", affected,
				"subscribed_users", subscribed,
				"threshold", threshold,
			)
			n.resetAllNotificationState(time.Now())
			n.enqueueNotice(fmt.Sprintf(
				"Notification burst suppressed (possible localized outage): %d users (of %d) exceeded threshold %d. Notifications paused and state reset.",
				affected, subscribed, threshold,
			))
			return
		}
	}

	// Peek the next message; only pop it after a successful send.
	n.pingMu.Lock()
	if len(n.pingQueue) == 0 {
		n.pingMu.Unlock()
		return
	}
	next := n.pingQueue[0]
	n.pingMu.Unlock()
	msg, mentions := renderQueuedMessage(next)

	if msg == "" {
		n.pingMu.Lock()
		if len(n.pingQueue) > 0 {
			n.pingQueue = n.pingQueue[1:]
		}
		n.pingMu.Unlock()
		return
	}

	if url := n.savedWorkersURL(); url != "" {
		footer := "[[check status]](" + url + ")"
		if len(msg)+2+len(footer) <= 1000 {
			msg = msg + "\n" + footer
		}
	}

	_, err := n.dg.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content: msg,
		AllowedMentions: &discordgo.MessageAllowedMentions{
			Users: mentions,
		},
	})
	if err != nil {
		logger.Warn("discord notify send failed", "error", err)
		return
	}

	prefix := n.noticePrefix()

	n.pingMu.Lock()
	if len(n.pingQueue) > 0 {
		n.pingQueue = n.pingQueue[1:]
	}
	if n.droppedQueuedLines > 0 {
		now := time.Now()
		if n.lastDropNoticeAt.IsZero() || now.Sub(n.lastDropNoticeAt) >= time.Minute {
			dropped := n.droppedQueuedLines
			n.droppedQueuedLines = 0
			n.lastDropNoticeAt = now
			// Best-effort: only enqueue if there's capacity.
			if len(n.pingQueue) < 3 {
				n.pingQueue = append(n.pingQueue, queuedDiscordMessage{
					Notices: []string{prefix + fmt.Sprintf("Notification backlog full; dropped %d updates to stay within rate limits.", dropped)},
				})
			}
		}
	}
	n.pingMu.Unlock()
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
