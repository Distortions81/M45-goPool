package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWithClerkUser_LocalNoAuthInjectsSyntheticUser(t *testing.T) {
	s := &StatusServer{
		savedWorkersLocalNoAuth: true,
	}

	var gotUser *ClerkUser
	h := s.withClerkUser(func(w http.ResponseWriter, r *http.Request) {
		gotUser = ClerkUserFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/saved-workers", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d want %d", rr.Code, http.StatusNoContent)
	}
	if gotUser == nil {
		t.Fatal("expected synthetic clerk user in context")
	}
	if gotUser.UserID != savedWorkersLocalNoAuthUserID {
		t.Fatalf("user_id=%q want %q", gotUser.UserID, savedWorkersLocalNoAuthUserID)
	}
	if gotUser.SessionID != savedWorkersLocalNoAuthUserID {
		t.Fatalf("session_id=%q want %q", gotUser.SessionID, savedWorkersLocalNoAuthUserID)
	}
}

func TestWithClerkUser_LocalNoAuthDisabledDoesNotInjectUser(t *testing.T) {
	s := &StatusServer{}

	var gotUser *ClerkUser
	h := s.withClerkUser(func(w http.ResponseWriter, r *http.Request) {
		gotUser = ClerkUserFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/saved-workers", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d want %d", rr.Code, http.StatusNoContent)
	}
	if gotUser != nil {
		t.Fatalf("expected nil user, got %+v", *gotUser)
	}
}
