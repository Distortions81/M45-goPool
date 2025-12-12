package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestFileServerPathTraversal verifies that the fileServerWithFallback
// correctly prevents path traversal attacks.
func TestFileServerPathTraversal(t *testing.T) {
	// Create a temporary www directory with a test file
	tmpDir := t.TempDir()
	wwwDir := filepath.Join(tmpDir, "www")
	if err := os.MkdirAll(wwwDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a test file inside www directory
	testFile := filepath.Join(wwwDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("safe content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a sensitive file outside www directory
	sensitiveFile := filepath.Join(tmpDir, "secrets.txt")
	if err := os.WriteFile(sensitiveFile, []byte("sensitive data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a fallback handler that returns 404
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("Not found"))
	})

	// Create the file server with fallback using os.Root for security
	wwwRoot, err := os.OpenRoot(wwwDir)
	if err != nil {
		t.Fatalf("Failed to open www root: %v", err)
	}
	defer wwwRoot.Close()

	handler := &fileServerWithFallback{
		fileServer: http.FileServer(http.Dir(wwwDir)),
		fallback:   fallback,
		wwwRoot:    wwwRoot,
	}

	tests := []struct {
		name          string
		path          string
		expectStatus  int
		expectContent string
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
			if tt.shouldContain != "" && body != tt.shouldContain {
				t.Errorf("Expected body to contain %q, got %q", tt.shouldContain, body)
			}

			// Most importantly: ensure we never serve the sensitive file
			if body == "sensitive data" {
				t.Fatal("SECURITY ISSUE: Sensitive file was served via path traversal!")
			}
		})
	}
}
