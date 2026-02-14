package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newStatusServerForJSONTests() *StatusServer {
	s := &StatusServer{
		jsonCache: make(map[string]cachedJSONResponse),
	}
	s.UpdateConfig(Config{FiatCurrency: "USD"})
	s.statusMu.Lock()
	s.cachedStatus = StatusData{
		FoundBlocks: []FoundBlockView{
			{
				Height: 900001,
				Hash:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				Worker: "bc1qverylongworkeraddresscomponent.worker01",
			},
			{
				Height: 900000,
				Hash:   "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
				Worker: "bc1qanotherverylongworkercomponent.worker02",
			},
			{
				Height: 899999,
				Hash:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Worker: "bc1qthirdlongworkercomponent.worker03",
			},
		},
	}
	s.lastStatusBuild = time.Now()
	s.statusMu.Unlock()
	return s
}

func TestJSONEndpoints_MethodNotAllowed(t *testing.T) {
	s := newStatusServerForJSONTests()
	tests := []struct {
		name    string
		path    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{name: "overview", path: "/api/overview", handler: s.handleOverviewPageJSON},
		{name: "pool-page", path: "/api/pool-page", handler: s.handlePoolPageJSON},
		{name: "node", path: "/api/node", handler: s.handleNodePageJSON},
		{name: "server", path: "/api/server", handler: s.handleServerPageJSON},
		{name: "pool-hashrate", path: "/api/pool-hashrate", handler: s.handlePoolHashrateJSON},
		{name: "blocks", path: "/api/blocks", handler: s.handleBlocksListJSON},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			rr := httptest.NewRecorder()
			tc.handler(rr, req)
			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rr.Code)
			}
		})
	}
}

func TestHandleBlocksListJSON_LimitAndCensoring(t *testing.T) {
	s := newStatusServerForJSONTests()

	t.Run("respects_limit", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/blocks?limit=2", nil)
		rr := httptest.NewRecorder()
		s.handleBlocksListJSON(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
		}

		var blocks []FoundBlockView
		if err := json.Unmarshal(rr.Body.Bytes(), &blocks); err != nil {
			t.Fatalf("decode blocks response: %v", err)
		}
		if len(blocks) != 2 {
			t.Fatalf("expected 2 blocks, got %d", len(blocks))
		}
		if blocks[0].Hash == s.cachedStatus.FoundBlocks[0].Hash {
			t.Fatalf("expected censored block hash, got original")
		}
		if blocks[0].Worker == s.cachedStatus.FoundBlocks[0].Worker {
			t.Fatalf("expected censored worker name, got original")
		}
	})

	t.Run("invalid_limit_falls_back", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/blocks?limit=999", nil)
		rr := httptest.NewRecorder()
		s.handleBlocksListJSON(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
		}

		var blocks []FoundBlockView
		if err := json.Unmarshal(rr.Body.Bytes(), &blocks); err != nil {
			t.Fatalf("decode blocks response: %v", err)
		}
		if len(blocks) != 3 {
			t.Fatalf("expected fallback to all 3 available blocks, got %d", len(blocks))
		}
	})
}

func TestHandlePoolHashrateJSON_IncludeHistoryToggle(t *testing.T) {
	s := newStatusServerForJSONTests()
	now := time.Now()
	s.appendPoolHashrateHistory(123.45, 910000, now.Add(-time.Minute))

	reqNoHistory := httptest.NewRequest(http.MethodGet, "/api/pool-hashrate", nil)
	rrNoHistory := httptest.NewRecorder()
	s.handlePoolHashrateJSON(rrNoHistory, reqNoHistory)
	if rrNoHistory.Code != http.StatusOK {
		t.Fatalf("no-history status: expected %d, got %d", http.StatusOK, rrNoHistory.Code)
	}

	var payloadNoHistory map[string]any
	if err := json.Unmarshal(rrNoHistory.Body.Bytes(), &payloadNoHistory); err != nil {
		t.Fatalf("decode no-history response: %v", err)
	}
	if _, ok := payloadNoHistory["pool_hashrate_history"]; ok {
		t.Fatalf("expected no pool_hashrate_history field without include_history")
	}

	reqWithHistory := httptest.NewRequest(http.MethodGet, "/api/pool-hashrate?include_history=1", nil)
	rrWithHistory := httptest.NewRecorder()
	s.handlePoolHashrateJSON(rrWithHistory, reqWithHistory)
	if rrWithHistory.Code != http.StatusOK {
		t.Fatalf("with-history status: expected %d, got %d", http.StatusOK, rrWithHistory.Code)
	}

	var payloadWithHistory struct {
		PoolHashrateHistory []poolHashrateHistoryPoint `json:"pool_hashrate_history"`
	}
	if err := json.Unmarshal(rrWithHistory.Body.Bytes(), &payloadWithHistory); err != nil {
		t.Fatalf("decode with-history response: %v", err)
	}
	if len(payloadWithHistory.PoolHashrateHistory) == 0 {
		t.Fatalf("expected non-empty pool_hashrate_history with include_history=1")
	}
}
