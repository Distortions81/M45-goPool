package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

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
	return discordConfigured(n.s.Config())
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
