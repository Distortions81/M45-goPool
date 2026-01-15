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
	db, err := openStateDB(stateDBPathFromDataDir(dataDir))
	if err != nil {
		logger.Warn("open sqlite state db for legacy migrations", "error", err)
		return
	}
	defer db.Close()

	legacyFoundBlocks := filepath.Join(dataDir, "state", "found_blocks.jsonl")
	if err := migrateFoundBlocksJSONLToDB(db, legacyFoundBlocks); err != nil {
		logger.Warn("migrate found blocks log to sqlite", "error", err, "path", legacyFoundBlocks)
	}
}
