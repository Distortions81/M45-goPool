package main

import (
	"os"
	"strings"
	"testing"
)

func TestSavedWorkersTemplate_RenderStoredHashratesScoped(t *testing.T) {
	t.Parallel()

	b, err := os.ReadFile("data/templates/saved_workers.tmpl")
	if err != nil {
		t.Fatalf("read saved_workers.tmpl: %v", err)
	}

	s := string(b)
	// The saved-workers page has multiple <script> blocks. The status-box script is the one
	// that references FIAT_CURRENCY and drives the "status_boxes" UI.
	const scriptMarker = "const FIAT_CURRENCY = '{{.FiatCurrency}}';"
	start := strings.Index(s, scriptMarker)
	if start < 0 {
		t.Fatalf("missing marker %q in saved_workers.tmpl", scriptMarker)
	}

	sub := s[start:]
	defIdx := strings.Index(sub, "function renderStoredHashrates()")
	callIdx := strings.Index(sub, "renderStoredHashrates();")
	if callIdx < 0 {
		t.Fatalf("missing renderStoredHashrates() call in status-box script")
	}
	if defIdx < 0 || defIdx > callIdx {
		t.Fatalf("status-box script must define renderStoredHashrates() before calling it (def=%d call=%d)", defIdx, callIdx)
	}
}

