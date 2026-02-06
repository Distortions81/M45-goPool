package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

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
