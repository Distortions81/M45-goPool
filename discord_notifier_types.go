package main

import (
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

type queuedDiscordMessage struct {
	Notices         []string
	UserOrder       []string
	LinesByUser     map[string][]string
	MentionEveryone bool
}
