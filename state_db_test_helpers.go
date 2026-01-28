package main

import "database/sql"

// setSharedStateDBForTest allows tests to set a custom shared DB.
// Returns a cleanup function that restores the previous state.
func setSharedStateDBForTest(db *sql.DB) func() {
	sharedStateDBMu.Lock()
	oldDB := sharedStateDB
	sharedStateDB = db
	sharedStateDBMu.Unlock()
	return func() {
		sharedStateDBMu.Lock()
		sharedStateDB = oldDB
		sharedStateDBMu.Unlock()
	}
}
