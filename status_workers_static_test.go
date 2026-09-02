package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleUmbrelPage(t *testing.T) {
	tmpl, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}

	s := &StatusServer{
		tmpl:  tmpl,
		start: time.Now(),
	}
	s.UpdateConfig(defaultConfig())

	req := httptest.NewRequest(http.MethodGet, "/umbrel", nil)
	rec := httptest.NewRecorder()
	s.handleUmbrelPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("Content-Type=%q, want HTML", got)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"Run M45 goPool on Umbrel",
		"https://github.com/M45Core/M45-Umbrel-Community-App-Store",
		"stratum+tcp://&lt;umbrel-lan-ip&gt;:23456",
		"3B86bWqfjdQeLEr8nkeeWU6ygksc2K7MoL",
		"https://m45core.com/umbrel",
		"https://m45core.com/umbrel-og.png",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}
