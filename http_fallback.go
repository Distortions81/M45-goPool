package main

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	staticCacheMaxBytes     = 32 << 20 // 32MB total
	staticCacheMaxFileBytes = 2 << 20  // 2MB per file
)

// fileServerWithFallback tries to serve static files from www directory first,
// and falls back to the status server if the file doesn't exist.
type fileServerWithFallback struct {
	fileServer http.Handler
	fallback   http.Handler
	wwwRoot    *os.Root

	cacheMu    sync.RWMutex
	cache      map[string]cachedStaticFile
	cacheBytes int64
}

type cachedStaticFile struct {
	payload     []byte
	size        int64
	modTime     time.Time
	contentType string
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
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.fileServer.ServeHTTP(w, r)
			return
		}

		h.cacheMu.RLock()
		if entry, ok := h.cache[cleanPath]; ok && entry.size == info.Size() && entry.modTime.Equal(info.ModTime()) && len(entry.payload) > 0 {
			if entry.contentType != "" {
				w.Header().Set("Content-Type", entry.contentType)
			}
			h.cacheMu.RUnlock()
			http.ServeContent(w, r, filepath.Base(cleanPath), entry.modTime, bytes.NewReader(entry.payload))
			return
		}
		h.cacheMu.RUnlock()

		if info.Size() <= 0 || info.Size() > staticCacheMaxFileBytes || info.Size() > staticCacheMaxBytes {
			h.fileServer.ServeHTTP(w, r)
			return
		}

		h.cacheMu.Lock()
		if h.cacheBytes+info.Size() > staticCacheMaxBytes {
			h.cache = make(map[string]cachedStaticFile)
			h.cacheBytes = 0
		}
		h.cacheMu.Unlock()

		file, err := h.wwwRoot.Open(cleanPath)
		if err != nil {
			h.fileServer.ServeHTTP(w, r)
			return
		}
		defer file.Close()

		payload, err := io.ReadAll(file)
		if err != nil {
			h.fileServer.ServeHTTP(w, r)
			return
		}
		if int64(len(payload)) != info.Size() {
			h.fileServer.ServeHTTP(w, r)
			return
		}

		ext := strings.ToLower(filepath.Ext(cleanPath))
		contentType := mime.TypeByExtension(ext)
		if contentType == "" {
			contentType = http.DetectContentType(payload)
		}
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		h.cacheMu.Lock()
		if h.cache == nil {
			h.cache = make(map[string]cachedStaticFile)
		}
		if _, ok := h.cache[cleanPath]; !ok {
			h.cacheBytes += info.Size()
		}
		h.cache[cleanPath] = cachedStaticFile{
			payload:     payload,
			size:        info.Size(),
			modTime:     info.ModTime(),
			contentType: contentType,
		}
		h.cacheMu.Unlock()

		w.Header().Set("Content-Type", contentType)
		http.ServeContent(w, r, filepath.Base(cleanPath), info.ModTime(), bytes.NewReader(payload))
		return
	}
	h.fallback.ServeHTTP(w, r)
}
