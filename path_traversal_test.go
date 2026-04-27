package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// TestFileServerPathTraversal verifies that the fileServerWithFallback
// correctly prevents path traversal attacks.
func TestFileServerPathTraversal(t *testing.T) {
	staticFS := fstest.MapFS{
		"test.txt": &fstest.MapFile{
			Data: []byte("safe content"),
			Mode: fs.FileMode(0o644),
		},
	}

	// Create a fallback handler that returns 404
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("Not found"))
	})

	handler := newStaticFileServer(staticFS, fallback)

	tests := []struct {
		name          string
		path          string
		expectStatus  int
		shouldContain string
	}{
		{
			name:          "Valid file access",
			path:          "/test.txt",
			expectStatus:  http.StatusOK,
			shouldContain: "safe content",
		},
		{
			name:          "Path traversal attempt with ../",
			path:          "/../secrets.txt",
			expectStatus:  http.StatusNotFound,
			shouldContain: "Not found",
		},
		{
			name:          "Path traversal with multiple ../",
			path:          "/../../secrets.txt",
			expectStatus:  http.StatusNotFound,
			shouldContain: "Not found",
		},
		{
			name:          "Path traversal with encoded ../",
			path:          "/%2e%2e/secrets.txt",
			expectStatus:  http.StatusNotFound,
			shouldContain: "Not found",
		},
		{
			name:          "Non-existent file in www",
			path:          "/nonexistent.txt",
			expectStatus:  http.StatusNotFound,
			shouldContain: "Not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.expectStatus {
				t.Errorf("Expected status %d, got %d", tt.expectStatus, rec.Code)
			}

			body := rec.Body.String()
			if tt.shouldContain != "" && !strings.Contains(body, tt.shouldContain) {
				t.Errorf("Expected body to contain %q, got %q", tt.shouldContain, body)
			}

			// Most importantly: ensure we never serve the sensitive file
			if body == "sensitive data" {
				t.Fatal("SECURITY ISSUE: Sensitive file was served via path traversal!")
			}
		})
	}
}

func TestEmbeddedStaticFileServerServesAssets(t *testing.T) {
	handler, err := newEmbeddedStaticFileServer(http.NotFoundHandler())
	if err != nil {
		t.Fatalf("newEmbeddedStaticFileServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/style.css", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/css") {
		t.Fatalf("Content-Type=%q, want text/css", got)
	}
	if !strings.Contains(rec.Body.String(), "--bg-gradient-start") {
		t.Fatalf("expected embedded stylesheet body, got %q", rec.Body.String()[:min(len(rec.Body.String()), 120)])
	}
}
