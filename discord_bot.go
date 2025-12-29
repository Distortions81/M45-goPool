package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type discordNotifier struct {
	s                *StatusServer
	dg               *discordgo.Session
	guildID          string
	startChecksAt    time.Time
	notifyChannelID  string
	stateMu          sync.Mutex
	statusByUser     map[string]map[string]workerNotifyState // clerk user_id -> workerHash -> state
	lastSweepAt      time.Time
	links            []discordLink
	linkIdx          int
	lastLinksRefresh time.Time

	pingMu    sync.Mutex
	pingQueue []pingEntry
}

type workerNotifyState struct {
	Online          bool
	Since           time.Time
	OfflineNotified bool

	RecoveryEligible bool
	RecoveryNotified bool
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
	n.startChecksAt = time.Now().Add(5 * time.Minute)

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return err
	}
	dg.Identify.Intents = discordgo.MakeIntent(discordgo.IntentsGuilds)

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

	if err := n.registerCommands(ctx); err != nil {
		logger.Warn("discord command registration failed", "error", err)
	}

	go n.loop(ctx)
	go n.pingLoop(ctx)
	logger.Info("discord notifier started", "guild_id", n.guildID)
	return nil
}

func (n *discordNotifier) close() {
	if n == nil || n.dg == nil {
		return
	}
	_ = n.dg.Close()
}

func (n *discordNotifier) registerCommands(ctx context.Context) error {
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
					Description: "One-time code from the goPool saved-workers page",
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

		_ = respondEphemeral(s, i, "Enabled. You’ll be pinged in the configured channel when a saved worker stays offline for over 2 minutes.")
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
	if !n.startChecksAt.IsZero() && now.Before(n.startChecksAt) {
		return
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
	line := fmt.Sprintf("<@%s> ", link.DiscordUserID)
	parts := make([]string, 0, 2)
	if len(offlineOverdue) > 0 {
		parts = append(parts, "Offline >2m: "+strings.Join(renderNames(offlineOverdue, nameByHash), ", "))
	}
	if len(onlineOverdue) > 0 {
		parts = append(parts, "Back online (2+ min): "+strings.Join(renderNames(onlineOverdue, nameByHash), ", "))
	}
	line += strings.Join(parts, " | ")
	n.enqueuePing(link.DiscordUserID, line)
}

func (n *discordNotifier) updateWorkerStates(userID string, current map[string]bool, now time.Time) (offlineOverdue, onlineOverdue []string) {
	const (
		offlineThreshold  = 2 * time.Minute
		recoveryThreshold = 2 * time.Minute
	)

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
			state[hash] = workerNotifyState{Online: online, Since: now}
			continue
		}

		// Transition: reset timers and notification flags.
		if st.Online != online {
			st.Online = online
			st.Since = now
			st.OfflineNotified = false
			st.RecoveryNotified = false
			st.RecoveryEligible = online // only eligible when we just came online from offline
			state[hash] = st
			continue
		}

		// First observation seeds state without firing notifications (but timers start).
		if firstObservation {
			continue
		}

		if !online && !st.OfflineNotified && !st.Since.IsZero() && now.Sub(st.Since) >= offlineThreshold {
			st.OfflineNotified = true
			state[hash] = st
			offlineOverdue = append(offlineOverdue, hash)
			continue
		}

		if online && st.RecoveryEligible && !st.RecoveryNotified && !st.Since.IsZero() && now.Sub(st.Since) >= recoveryThreshold {
			st.RecoveryNotified = true
			st.RecoveryEligible = false
			state[hash] = st
			onlineOverdue = append(onlineOverdue, hash)
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
		}
		out = append(out, name)
	}
	return out
}

type pingEntry struct {
	DiscordUserID string
	Line          string
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
	n.pingMu.Lock()
	defer n.pingMu.Unlock()
	const maxQueue = 100_000
	if len(n.pingQueue) >= maxQueue {
		// Drop oldest to keep memory bounded.
		n.pingQueue = n.pingQueue[1:]
	}
	n.pingQueue = append(n.pingQueue, pingEntry{DiscordUserID: discordUserID, Line: line})
}

func (n *discordNotifier) pingLoop(ctx context.Context) {
	// 25 messages/minute max.
	ticker := time.NewTicker(time.Minute / 25)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.sendNextPingBatch()
		}
	}
}

func (n *discordNotifier) sendNextPingBatch() {
	if n == nil || n.dg == nil {
		return
	}
	channelID := strings.TrimSpace(n.notifyChannelID)
	if channelID == "" {
		return
	}

	const maxChars = 1000

	n.pingMu.Lock()
	if len(n.pingQueue) == 0 {
		n.pingMu.Unlock()
		return
	}

	used := 0
	msg := ""
	userIDs := make(map[string]struct{}, 16)
	for i := 0; i < len(n.pingQueue); i++ {
		line := n.pingQueue[i].Line
		if line == "" {
			used++
			continue
		}
		if len(line) > maxChars {
			line = line[:maxChars]
		}
		candidate := line
		if msg != "" {
			candidate = msg + "\n" + line
		}
		if len(candidate) > maxChars {
			break
		}
		msg = candidate
		userIDs[n.pingQueue[i].DiscordUserID] = struct{}{}
		used++
	}
	if used == 0 || strings.TrimSpace(msg) == "" {
		n.pingMu.Unlock()
		return
	}
	n.pingMu.Unlock()

	mentions := make([]string, 0, len(userIDs))
	for id := range userIDs {
		if strings.TrimSpace(id) != "" {
			mentions = append(mentions, id)
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

	n.pingMu.Lock()
	if used > len(n.pingQueue) {
		used = len(n.pingQueue)
	}
	n.pingQueue = n.pingQueue[used:]
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
