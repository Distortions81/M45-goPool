package main

import "time"

// Shared display-shortening settings so worker IDs, hashes, and config
// strings are truncated consistently across the UI.
const (
	workerNamePrefix = 8
	workerNameSuffix = 8

	hashPrefix = 0
	hashSuffix = 16

	payoutAddrPrefix = 8
	payoutAddrSuffix = 8

	coinbaseMsgPrefix = 8
	coinbaseMsgSuffix = 8

	workerLookupMaxBytes        = 256
	workerLookupRateLimitMax    = 10
	workerLookupRateLimitWindow = time.Minute
	// workerLookupMaxEntries bounds the number of distinct keys tracked by
	// the in-memory worker lookup rate limiter. Entries are also expired
	// after a quiet period, so memory usage stays bounded over time.
	workerLookupMaxEntries = 4096
)

const hashPerShare = float64(1 << 32)

const overviewRefreshInterval = defaultRefreshInterval
const poolHashrateTTL = 5 * time.Second
const poolHashrateHistoryWindow = 6 * time.Minute
const poolHashrateDisplayFallbackMaxAge = 2 * time.Minute
const savedWorkerPeriodBucket = 1 * time.Minute
const savedWorkerPeriodHistoryWindow = 24 * time.Hour
const savedWorkerPeriodSlots = int(savedWorkerPeriodHistoryWindow / savedWorkerPeriodBucket)
const savedWorkerPeriodMaxWorkers = 4096
const savedWorkerPeriodPoolKey = "pool"
const blocksRefreshInterval = 3 * time.Second
const settlingHashrateMaxRelativeError = 0.20
const settlingHashrateMinExpectedShares = 8.0
const stableHashrateMaxRelativeError = 0.10
const stableHashrateMinExpectedShares = 16.0
const veryStableHashrateMaxRelativeError = 0.05
const veryStableHashrateMinExpectedShares = 32.0
const settlingHashrateCumulativeMaxRelativeError = 0.20
const stableHashrateCumulativeMaxRelativeError = 0.10
const veryStableHashrateCumulativeMaxRelativeError = 0.05
const hashrateCumulativeAgreementMinAccepted = 16
const hashrateCumulativeAgreementMinConnected = 6 * time.Minute

// apiVersion is a short, human-readable version identifier included in all
// JSON API responses so power users can detect schema changes.
const apiVersion = "1"

const poolErrorHistoryDisplayWindow = time.Hour

// buildTime can be overridden at build time with:
//
//	go build -ldflags="-X main.buildTime=2025-01-02T15:04:05Z"
var buildTime = ""

// buildVersion can be overridden at build time with:
//
//	go build -ldflags="-X main.buildVersion=v1.2.3"
var buildVersion = ""

var knownGenesis = map[string]string{
	"mainnet": "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f",
	"regtest": "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f",
	"testnet": "000000000933ea01ad0ee984209779baaec3ced90fa3f408719526f8d77f4943",
	"signet":  "00000008819873e925422c1ff0f99f7cc9bbb232af63a077a480a3633bee1ef6",
}
