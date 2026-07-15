package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerListStoreRemoveUserReturnsCommittedHashes(t *testing.T) {
	store, err := newWorkerListStore(filepath.Join(t.TempDir(), "saved_workers.sqlite"))
	if err != nil {
		t.Fatalf("newWorkerListStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const (
		userA   = "remove-user-a"
		userB   = "remove-user-b"
		workerX = "bc1qexampleaddress00000000000000000000000000.remove-x"
		workerY = "bc1qexampleaddress00000000000000000000000000.remove-y"
	)
	for _, item := range []struct {
		user   string
		worker string
	}{
		{user: userA, worker: workerX},
		{user: userA, worker: workerY},
		{user: userB, worker: workerX},
	} {
		if err := store.Add(item.user, item.worker); err != nil {
			t.Fatalf("store.Add(%q, %q): %v", item.user, item.worker, err)
		}
	}
	if err := store.RecordClerkUserSeen(userA, time.Now()); err != nil {
		t.Fatalf("RecordClerkUserSeen(%q): %v", userA, err)
	}
	if err := store.RecordClerkUserSeen(userB, time.Now()); err != nil {
		t.Fatalf("RecordClerkUserSeen(%q): %v", userB, err)
	}

	// Legacy databases could contain differently-cased copies before storage
	// normalization. The refresh contract should still return one normalized
	// identity for every affected live-worker hash.
	hashX := workerNameHash(workerX)
	if _, err := store.db.Exec(`
		INSERT INTO saved_workers
			(user_id, worker, worker_hash, worker_display, notify_enabled, best_difficulty)
		VALUES (?, ?, ?, ?, 1, 0)
	`, userA, "legacy-uppercase-x", strings.ToUpper(hashX), "legacy-uppercase-x"); err != nil {
		t.Fatalf("insert legacy uppercase worker: %v", err)
	}

	hashes, err := store.RemoveUser(userA)
	if err != nil {
		t.Fatalf("RemoveUser: %v", err)
	}
	sort.Strings(hashes)
	wantHashes := []string{hashX, workerNameHash(workerY)}
	sort.Strings(wantHashes)
	if strings.Join(hashes, ",") != strings.Join(wantHashes, ",") {
		t.Fatalf("removed hashes = %v, want %v", hashes, wantHashes)
	}

	workersA, err := store.List(userA)
	if err != nil {
		t.Fatalf("List(%q): %v", userA, err)
	}
	if len(workersA) != 0 {
		t.Fatalf("deleted user still has %d workers", len(workersA))
	}
	workersB, err := store.List(userB)
	if err != nil {
		t.Fatalf("List(%q): %v", userB, err)
	}
	if len(workersB) != 1 || workersB[0].Hash != hashX {
		t.Fatalf("shared owner workers = %+v, want retained hash %q", workersB, hashX)
	}

	users, err := store.ListAllClerkUsers()
	if err != nil {
		t.Fatalf("ListAllClerkUsers: %v", err)
	}
	if len(users) != 1 || users[0].UserID != userB {
		t.Fatalf("remaining clerk users = %+v, want only %q", users, userB)
	}
}

func TestWorkerListStoreRemoveUserReturnsNoHashesOnRollback(t *testing.T) {
	store, err := newWorkerListStore(filepath.Join(t.TempDir(), "saved_workers.sqlite"))
	if err != nil {
		t.Fatalf("newWorkerListStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const (
		user   = "remove-user-rollback"
		worker = "bc1qexampleaddress00000000000000000000000000.remove-rollback"
	)
	if err := store.Add(user, worker); err != nil {
		t.Fatalf("store.Add: %v", err)
	}
	if err := store.RecordClerkUserSeen(user, time.Now()); err != nil {
		t.Fatalf("RecordClerkUserSeen: %v", err)
	}
	if _, err := store.db.Exec(`
		CREATE TRIGGER reject_remove_user
		BEFORE DELETE ON clerk_users
		WHEN OLD.user_id = 'remove-user-rollback'
		BEGIN
			SELECT RAISE(ABORT, 'forced remove-user rollback');
		END
	`); err != nil {
		t.Fatalf("create rollback trigger: %v", err)
	}

	hashes, err := store.RemoveUser(user)
	if err == nil {
		t.Fatal("RemoveUser succeeded despite forced transaction failure")
	}
	if len(hashes) != 0 {
		t.Fatalf("RemoveUser returned hashes after rollback: %v", hashes)
	}
	workers, listErr := store.List(user)
	if listErr != nil {
		t.Fatalf("List after rollback: %v", listErr)
	}
	if len(workers) != 1 || workers[0].Hash != workerNameHash(worker) {
		t.Fatalf("saved workers after rollback = %+v, want original worker", workers)
	}
	var clerkUsers int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM clerk_users WHERE user_id = ?", user).Scan(&clerkUsers); err != nil {
		t.Fatalf("query clerk user after rollback: %v", err)
	}
	if clerkUsers != 1 {
		t.Fatalf("clerk user count after rollback = %d, want 1", clerkUsers)
	}
}

func TestAdminRemoveUserRefreshesEveryAffectedLiveConnection(t *testing.T) {
	const (
		deletedUser = "admin-delete-user"
		otherUser   = "admin-other-user"
		worker      = "bc1qexampleaddress00000000000000000000000000.admin-delete"
		unrelated   = "bc1qexampleaddress00000000000000000000000000.admin-unrelated"
	)

	t.Run("last owner", func(t *testing.T) {
		store, server, password, token := newAdminRemoveUserTestServer(t)
		if err := store.Add(deletedUser, worker); err != nil {
			t.Fatalf("save deleted worker: %v", err)
		}
		if err := store.Add(otherUser, unrelated); err != nil {
			t.Fatalf("save unrelated worker: %v", err)
		}

		hash := workerNameHash(worker)
		first := registerSavedWorkerTestConn(server.workerRegistry, store, worker, 1, true)
		second := registerSavedWorkerTestConn(server.workerRegistry, store, worker, 2, true)
		unrelatedConn := registerSavedWorkerTestConn(server.workerRegistry, store, unrelated, 3, true)

		invokeAdminRemoveUser(t, server, password, token, deletedUser)

		if tracked := savedWorkerTestTracked(first); tracked {
			t.Fatal("first live connection remained tracked after last-owner deletion")
		}
		if tracked := savedWorkerTestTracked(second); tracked {
			t.Fatal("second live connection remained tracked after last-owner deletion")
		}
		if tracked := savedWorkerTestTracked(unrelatedConn); !tracked {
			t.Fatal("unrelated live connection was changed by deletion")
		}
		_, tracked, err := store.BestDifficultyForHash(hash)
		if err != nil {
			t.Fatalf("BestDifficultyForHash: %v", err)
		}
		if tracked {
			t.Fatal("deleted worker still exists in saved-worker store")
		}
	})

	t.Run("shared owner", func(t *testing.T) {
		store, server, password, token := newAdminRemoveUserTestServer(t)
		if err := store.Add(deletedUser, worker); err != nil {
			t.Fatalf("save deleted owner's worker: %v", err)
		}
		if err := store.Add(otherUser, worker); err != nil {
			t.Fatalf("save shared owner's worker: %v", err)
		}

		first := registerSavedWorkerTestConn(server.workerRegistry, store, worker, 1, false)
		second := registerSavedWorkerTestConn(server.workerRegistry, store, worker, 2, false)

		invokeAdminRemoveUser(t, server, password, token, deletedUser)

		if tracked := savedWorkerTestTracked(first); !tracked {
			t.Fatal("first shared-worker connection was not refreshed to tracked")
		}
		if tracked := savedWorkerTestTracked(second); !tracked {
			t.Fatal("second shared-worker connection was not refreshed to tracked")
		}
		workers, err := store.List(otherUser)
		if err != nil {
			t.Fatalf("List shared owner: %v", err)
		}
		if len(workers) != 1 || workers[0].Hash != workerNameHash(worker) {
			t.Fatalf("shared owner's worker = %+v, want retained worker", workers)
		}
	})
}

func newAdminRemoveUserTestServer(t *testing.T) (*workerListStore, *StatusServer, string, string) {
	t.Helper()
	store, err := newWorkerListStore(filepath.Join(t.TempDir(), "saved_workers.sqlite"))
	if err != nil {
		t.Fatalf("newWorkerListStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const (
		password = "admin-delete-test-password"
		token    = "admin-delete-test-session"
	)
	adminPath := filepath.Join(t.TempDir(), "admin.toml")
	adminCfg := adminFileConfig{
		Enabled:                  true,
		Username:                 "admin",
		Password:                 password,
		SessionExpirationSeconds: defaultAdminSessionExpirationSeconds,
	}
	if err := os.WriteFile(adminPath, []byte(renderAdminConfig(adminCfg)), 0o600); err != nil {
		t.Fatalf("write admin config: %v", err)
	}

	server := &StatusServer{
		workerLists:     store,
		workerRegistry:  newWorkerConnectionRegistry(),
		adminConfigPath: adminPath,
		adminSessions: map[string]time.Time{
			token: time.Now().Add(time.Hour),
		},
		cachedStatus:    StatusData{},
		lastStatusBuild: time.Now(),
	}
	server.UpdateConfig(defaultConfig())
	return store, server, password, token
}

func registerSavedWorkerTestConn(registry *workerConnectionRegistry, store *workerListStore, worker string, seq uint64, tracked bool) *MinerConn {
	hash := workerNameHash(worker)
	mc := &MinerConn{
		savedWorkerStore:     store,
		registeredWorker:     worker,
		registeredWorkerHash: hash,
		savedWorkerTracked:   tracked,
	}
	atomic.StoreUint64(&mc.connectionSeq, seq)
	registry.register(hash, "", mc)
	return mc
}

func savedWorkerTestTracked(mc *MinerConn) bool {
	mc.savedWorkerMu.Lock()
	defer mc.savedWorkerMu.Unlock()
	return mc.savedWorkerTracked
}

func invokeAdminRemoveUser(t *testing.T, server *StatusServer, password, token, userID string) {
	t.Helper()
	form := url.Values{}
	form.Set("password", password)
	form.Add("user_id", userID)
	req := httptest.NewRequest(http.MethodPost, "/admin/logins/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: token})
	recorder := httptest.NewRecorder()

	server.handleAdminLoginDelete(recorder, req)

	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("admin delete status = %d, want %d; body=%q", recorder.Code, http.StatusSeeOther, recorder.Body.String())
	}
	if location := recorder.Header().Get("Location"); location != "/admin/logins?notice=saved_worker_deleted" {
		t.Fatalf("admin delete redirect = %q", location)
	}
}
