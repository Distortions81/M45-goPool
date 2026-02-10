package main

import "strings"

func clerkConfigured(cfg Config) bool {
	return strings.TrimSpace(cfg.ClerkSecretKey) != "" &&
		strings.TrimSpace(cfg.ClerkPublishableKey) != "" &&
		strings.TrimSpace(cfg.ClerkFrontendAPIURL) != ""
}

func discordConfigured(cfg Config) bool {
	return strings.TrimSpace(cfg.DiscordServerID) != "" &&
		strings.TrimSpace(cfg.DiscordBotToken) != "" &&
		strings.TrimSpace(cfg.DiscordNotifyChannelID) != ""
}

func backblazeCloudConfigured(cfg Config) bool {
	if !cfg.BackblazeBackupEnabled {
		return false
	}
	return strings.TrimSpace(cfg.BackblazeBucket) != "" &&
		strings.TrimSpace(cfg.BackblazeAccountID) != "" &&
		strings.TrimSpace(cfg.BackblazeApplicationKey) != ""
}
