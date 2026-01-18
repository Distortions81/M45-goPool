package main

import (
	"path/filepath"
	"strings"
)

func migrateLegacyStateFiles(dataDir string) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	// Use the shared state database connection
	db := getSharedStateDB()
	if db == nil {
		logger.Warn("shared state db not initialized for legacy migrations")
		return
	}

	legacyFoundBlocks := filepath.Join(dataDir, "state", "found_blocks.jsonl")
	if err := migrateFoundBlocksJSONLToDB(db, legacyFoundBlocks); err != nil {
		logger.Warn("migrate found blocks log to sqlite", "error", err, "path", legacyFoundBlocks)
	}
}
