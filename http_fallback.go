package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// fileServerWithFallback tries to serve static files from www directory first,
// and falls back to the status server if the file doesn't exist.
type fileServerWithFallback struct {
	fileServer http.Handler
	fallback   http.Handler
	wwwRoot    *os.Root
}

func (h *fileServerWithFallback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check if file exists in www directory using os.Root for secure path resolution.
	// os.Root provides OS-level guarantees against path traversal by ensuring all
	// file operations stay within the root directory, similar to chroot.
	urlPath := strings.TrimPrefix(r.URL.Path, "/")
	cleanPath := filepath.Clean(urlPath)

	// Use os.Root to safely check if file exists within wwwDir.
	// This automatically prevents any path traversal attempts.
	info, err := h.wwwRoot.Stat(cleanPath)
	if err == nil && !info.IsDir() {
		h.fileServer.ServeHTTP(w, r)
		return
	}
	h.fallback.ServeHTTP(w, r)
}
