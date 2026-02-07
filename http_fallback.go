package main

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
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
	wwwDir     string

	cacheMu     sync.RWMutex
	cache       map[string]cachedStaticFile
	cacheBytes  int64
	cacheFrozen bool
}

type cachedStaticFile struct {
	payload     []byte
	size        int64
	modTime     time.Time
	contentType string
}

func (h *fileServerWithFallback) ServeCached(w http.ResponseWriter, r *http.Request, cleanPath string) bool {
	if h == nil || cleanPath == "" {
		return false
	}
	h.cacheMu.RLock()
	entry, ok := h.cache[cleanPath]
	h.cacheMu.RUnlock()
	if !ok || len(entry.payload) == 0 {
		return false
	}
	if entry.contentType != "" {
		w.Header().Set("Content-Type", entry.contentType)
	}
	http.ServeContent(w, r, filepath.Base(cleanPath), entry.modTime, bytes.NewReader(entry.payload))
	return true
}

func (h *fileServerWithFallback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check if file exists in www directory using os.Root for secure path resolution.
	// os.Root provides OS-level guarantees against path traversal by ensuring all
	// file operations stay within the root directory, similar to chroot.
	urlPath := strings.TrimPrefix(r.URL.Path, "/")
	cleanPath := filepath.Clean(urlPath)

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.fileServer.ServeHTTP(w, r)
		return
	}

	if h.ServeCached(w, r, cleanPath) {
		return
	}

	// Use os.Root to safely check if file exists within wwwDir.
	// This automatically prevents any path traversal attempts.
	info, err := h.wwwRoot.Stat(cleanPath)
	if err == nil && !info.IsDir() {
		if h.cacheFrozen {
			h.fileServer.ServeHTTP(w, r)
			return
		}

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

		contentType := detectContentType(cleanPath, payload)
		h.storeCached(cleanPath, info, payload, contentType)
		w.Header().Set("Content-Type", contentType)
		http.ServeContent(w, r, filepath.Base(cleanPath), info.ModTime(), bytes.NewReader(payload))
		return
	}
	h.fallback.ServeHTTP(w, r)
}

func (h *fileServerWithFallback) storeCached(cleanPath string, info os.FileInfo, payload []byte, contentType string) {
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
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
}

func (h *fileServerWithFallback) PreloadCache() error {
	if h == nil {
		return nil
	}
	if h.wwwDir == "" {
		return fmt.Errorf("www directory not configured")
	}
	h.cacheMu.Lock()
	if h.cache == nil {
		h.cache = make(map[string]cachedStaticFile)
	}
	h.cacheFrozen = false
	h.cacheMu.Unlock()

	err := filepath.WalkDir(h.wwwDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == ".well-known" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() <= 0 || info.Size() > staticCacheMaxFileBytes || info.Size() > staticCacheMaxBytes {
			return nil
		}
		rel, err := filepath.Rel(h.wwwDir, path)
		if err != nil {
			return nil
		}
		cleanPath := filepath.Clean(rel)
		payload, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if int64(len(payload)) != info.Size() {
			return nil
		}

		h.cacheMu.Lock()
		if h.cacheBytes+info.Size() > staticCacheMaxBytes {
			h.cache = make(map[string]cachedStaticFile)
			h.cacheBytes = 0
		}
		h.cacheMu.Unlock()

		contentType := detectContentType(cleanPath, payload)
		h.storeCached(cleanPath, info, payload, contentType)
		return nil
	})
	if err != nil {
		return err
	}
	h.cacheMu.Lock()
	h.cacheFrozen = true
	h.cacheMu.Unlock()
	return nil
}

func (h *fileServerWithFallback) ReloadCache() error {
	if h == nil {
		return nil
	}
	h.cacheMu.Lock()
	h.cache = make(map[string]cachedStaticFile)
	h.cacheBytes = 0
	h.cacheFrozen = false
	h.cacheMu.Unlock()
	return h.PreloadCache()
}

func detectContentType(cleanPath string, payload []byte) string {
	ext := strings.ToLower(filepath.Ext(cleanPath))
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		contentType = http.DetectContentType(payload)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return contentType
}
