package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type discordNotifier struct {
	s           *StatusServer
	dg          *discordgo.Session
	guildID     string
	stateMu     sync.Mutex
	lastByUser  map[string]map[string]bool // clerk user_id -> workerHash -> online
	lastSweepAt time.Time
}

func (n *discordNotifier) enabled() bool {
	if n == nil || n.s == nil {
		return false
	}
	cfg := n.s.Config()
	return strings.TrimSpace(cfg.DiscordServerID) != "" && strings.TrimSpace(cfg.DiscordBotToken) != ""
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

	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return err
	}
	dg.Identify.Intents = discordgo.MakeIntent(discordgo.IntentsGuilds | discordgo.IntentsDirectMessages)

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

		_ = respondEphemeral(s, i, "Enabled. You’ll receive a DM when your saved workers go online/offline.")
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
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			n.close()
			return
		case <-ticker.C:
			n.pollOnce()
		}
	}
}

func (n *discordNotifier) pollOnce() {
	if n == nil || n.s == nil || n.dg == nil || n.s.workerLists == nil {
		return
	}
	links, err := n.s.workerLists.ListEnabledDiscordLinks()
	if err != nil || len(links) == 0 {
		n.sweep(nil)
		return
	}

	active := make(map[string]struct{}, len(links))
	for _, link := range links {
		if link.UserID == "" || link.DiscordUserID == "" {
			continue
		}
		active[link.UserID] = struct{}{}

		saved, err := n.s.workerLists.List(link.UserID)
		if err != nil || len(saved) == 0 {
			continue
		}

		now := time.Now()
		onlineNow := make(map[string]bool, len(saved))
		nameByHash := make(map[string]string, len(saved))
		for _, sw := range saved {
			views, lookupHash := n.s.findSavedWorkerConnections(sw.Name, sw.Hash, now)
			if lookupHash == "" {
				continue
			}
			onlineNow[lookupHash] = len(views) > 0
			if _, ok := nameByHash[lookupHash]; !ok {
				nameByHash[lookupHash] = sw.Name
			}
		}

		wentOnline, wentOffline := n.diffAndUpdate(link.UserID, onlineNow)
		if len(wentOnline) == 0 && len(wentOffline) == 0 {
			continue
		}

		var parts []string
		if len(wentOnline) > 0 {
			parts = append(parts, "Online: "+strings.Join(renderNames(wentOnline, nameByHash), ", "))
		}
		if len(wentOffline) > 0 {
			parts = append(parts, "Offline: "+strings.Join(renderNames(wentOffline, nameByHash), ", "))
		}
		msg := strings.Join(parts, "\n")
		_ = n.sendDM(link.DiscordUserID, msg)
	}

	n.sweep(active)
}

func (n *discordNotifier) diffAndUpdate(userID string, current map[string]bool) (wentOnline, wentOffline []string) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	if n.lastByUser == nil {
		n.lastByUser = make(map[string]map[string]bool, 16)
	}
	prev := n.lastByUser[userID]
	if prev == nil {
		prev = make(map[string]bool, len(current))
		n.lastByUser[userID] = prev
		// First observation: seed without emitting notifications.
		for k, v := range current {
			prev[k] = v
		}
		return nil, nil
	}

	for hash, online := range current {
		if before, ok := prev[hash]; ok {
			if before == online {
				continue
			}
			if online {
				wentOnline = append(wentOnline, hash)
			} else {
				wentOffline = append(wentOffline, hash)
			}
		}
		prev[hash] = online
	}

	// If a saved worker disappears, forget it (no notification).
	for hash := range prev {
		if _, ok := current[hash]; !ok {
			delete(prev, hash)
		}
	}
	return wentOnline, wentOffline
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

func (n *discordNotifier) sendDM(discordUserID, msg string) error {
	if n == nil || n.dg == nil {
		return nil
	}
	discordUserID = strings.TrimSpace(discordUserID)
	msg = strings.TrimSpace(msg)
	if discordUserID == "" || msg == "" {
		return nil
	}
	ch, err := n.dg.UserChannelCreate(discordUserID)
	if err != nil || ch == nil {
		return err
	}
	_, err = n.dg.ChannelMessageSend(ch.ID, msg)
	return err
}

func (n *discordNotifier) sweep(active map[string]struct{}) {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	now := time.Now()
	if !n.lastSweepAt.IsZero() && now.Sub(n.lastSweepAt) < time.Minute {
		return
	}
	n.lastSweepAt = now
	if n.lastByUser == nil {
		return
	}
	if active == nil {
		// Nothing enabled; clear everything.
		n.lastByUser = nil
		return
	}
	for uid := range n.lastByUser {
		if _, ok := active[uid]; !ok {
			delete(n.lastByUser, uid)
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
