package main

import "time"

const maxSavedWorkersPerNameDisplay = 16

type savedWorkerEntry struct {
	Name              string
	Hash              string
	NotifyEnabled     bool
	LastOnlineAt      string
	Hashrate          float64
	ShareRate         float64
	Accepted          uint64
	Rejected          uint64
	Difficulty        float64
	EstimatedPingP50MS float64
	EstimatedPingP95MS float64
	NotifyToFirstShareMS float64
	NotifyToFirstShareP50MS float64
	NotifyToFirstShareP95MS float64
	LastShare         time.Time
	ConnectedDuration time.Duration
	ConnectionID      string
	ConnectionSeq     uint64
	BestDifficulty    float64
}

type walletLookupResult struct {
	WorkerView
	AlreadySaved bool
}

func maxSavedWorkerBestDifficulty(entries []SavedWorkerEntry) float64 {
	var best float64
	for _, entry := range entries {
		if entry.BestDifficulty > best {
			best = entry.BestDifficulty
		}
	}
	return best
}
