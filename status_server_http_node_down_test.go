package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStatusServerOverview_RendersNodeDownWhenStale(t *testing.T) {
	tmpl, err := loadTemplates("data")
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}

	jm := &JobManager{}
	jm.mu.Lock()
	jm.curJob = &Job{CreatedAt: time.Now()}
	jm.mu.Unlock()
	jm.recordJobError(fmt.Errorf("node indexing"))

	s := &StatusServer{tmpl: tmpl, jobMgr: jm}
	s.UpdateConfig(Config{ListenAddr: ":3333"})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	s.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Node work unavailable") && !strings.Contains(body, "Node connection unavailable") && !strings.Contains(body, "Node indexing/syncing") {
		t.Fatalf("expected node-down page, got body=%q", body[:min(len(body), 300)])
	}
	if !strings.Contains(body, "/logo.png") {
		t.Fatalf("expected logo on node-down page")
	}
}
