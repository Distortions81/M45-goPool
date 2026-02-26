package main

import (
	"database/sql"
	"sync"
	"time"
)

const maxSavedWorkersPerUser = 64

type workerListStore struct {
	db     *sql.DB
	ownsDB bool

	bestDiffMu      sync.Mutex
	bestDiffPending map[string]float64
	bestDiffCh      chan bestDiffUpdate
	bestDiffStop    chan struct{}
	bestDiffWg      sync.WaitGroup

	minuteBestMu   sync.Mutex
	minuteBestByID map[string]*savedWorkerMinuteBestRing
}

type discordLink struct {
	UserID        string
	DiscordUserID string
	Enabled       bool
	LinkedAt      time.Time
	UpdatedAt     time.Time
}

type ClerkUserRecord struct {
	UserID    string
	FirstSeen time.Time
	LastSeen  time.Time
	SeenCount int
}

type bestDiffUpdate struct {
	hash string
	diff float64
}

type savedWorkerMinuteBestRing struct {
	minutes    [savedWorkerPeriodSlots]uint32
	bestQ      [savedWorkerPeriodSlots]uint16
	lastMinute uint32
}
