package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

const (
	staticCacheMaxBytes     = 32 << 20 // 32MB total
	staticCacheMaxFileBytes = 2 << 20  // 2MB per file
)

// fileServerWithFallback tries to serve embedded static files first,
// and falls back to the status server if the file doesn't exist.
type fileServerWithFallback struct {
	staticFS fs.FS
	fallback http.Handler

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

func newEmbeddedStaticFileServer(fallback http.Handler) (*fileServerWithFallback, error) {
	assets, err := newUIAssetLoader()
	if err != nil {
		return nil, err
	}
	staticFS, err := assets.staticFiles()
	if err != nil {
		return nil, err
	}
	return newStaticFileServer(staticFS, fallback), nil
}

func newStaticFileServer(staticFS fs.FS, fallback http.Handler) *fileServerWithFallback {
	return &fileServerWithFallback{
		staticFS: staticFS,
		fallback: fallback,
	}
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
	http.ServeContent(w, r, path.Base(cleanPath), entry.modTime, bytes.NewReader(entry.payload))
	return true
}

func (h *fileServerWithFallback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.serveFallback(w, r)
		return
	}

	if h.ServePath(w, r, r.URL.Path) {
		return
	}
	h.serveFallback(w, r)
}

func (h *fileServerWithFallback) ServePath(w http.ResponseWriter, r *http.Request, requestPath string) bool {
	if h == nil || h.staticFS == nil {
		return false
	}
	cleanPath, ok := cleanStaticAssetPath(requestPath)
	if !ok {
		return false
	}
	if h.ServeCached(w, r, cleanPath) {
		return true
	}

	info, err := fs.Stat(h.staticFS, cleanPath)
	if err != nil || info.IsDir() {
		return false
	}

	payload, err := fs.ReadFile(h.staticFS, cleanPath)
	if err != nil {
		return false
	}
	if int64(len(payload)) != info.Size() {
		return false
	}

	contentType := detectContentType(cleanPath, payload)
	if canCacheStaticFile(info) {
		h.reserveStaticCacheSpace(info.Size())
		h.storeCached(cleanPath, info, payload, contentType)
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, path.Base(cleanPath), info.ModTime(), bytes.NewReader(payload))
	return true
}

func (h *fileServerWithFallback) storeCached(cleanPath string, info fs.FileInfo, payload []byte, contentType string) {
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	if h.cache == nil {
		h.cache = make(map[string]cachedStaticFile)
	}
	if prev, ok := h.cache[cleanPath]; ok {
		h.cacheBytes -= prev.size
	}
	h.cacheBytes += info.Size()
	h.cache[cleanPath] = cachedStaticFile{
		payload:     payload,
		size:        info.Size(),
		modTime:     info.ModTime(),
		contentType: contentType,
	}
}

func (h *fileServerWithFallback) reserveStaticCacheSpace(size int64) {
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	if h.cache == nil {
		h.cache = make(map[string]cachedStaticFile)
	}
	if h.cacheBytes+size > staticCacheMaxBytes {
		h.cache = make(map[string]cachedStaticFile)
		h.cacheBytes = 0
	}
}

func (h *fileServerWithFallback) PreloadCache() error {
	if h == nil {
		return nil
	}
	if h.staticFS == nil {
		return fmt.Errorf("static asset filesystem not configured")
	}
	h.cacheMu.Lock()
	if h.cache == nil {
		h.cache = make(map[string]cachedStaticFile)
	}
	h.cacheMu.Unlock()

	err := fs.WalkDir(h.staticFS, ".", func(assetPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == ".well-known" {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if !canCacheStaticFile(info) {
			return nil
		}
		cleanPath, ok := cleanStaticAssetPath(assetPath)
		if !ok {
			return nil
		}
		payload, err := fs.ReadFile(h.staticFS, cleanPath)
		if err != nil {
			return nil
		}
		if int64(len(payload)) != info.Size() {
			return nil
		}

		h.reserveStaticCacheSpace(info.Size())

		contentType := detectContentType(cleanPath, payload)
		h.storeCached(cleanPath, info, payload, contentType)
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func (h *fileServerWithFallback) ReloadCache() error {
	if h == nil {
		return nil
	}
	h.cacheMu.Lock()
	h.cache = make(map[string]cachedStaticFile)
	h.cacheBytes = 0
	h.cacheMu.Unlock()
	return h.PreloadCache()
}

func (h *fileServerWithFallback) serveFallback(w http.ResponseWriter, r *http.Request) {
	if h != nil && h.fallback != nil {
		h.fallback.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

func canCacheStaticFile(info fs.FileInfo) bool {
	return info != nil && info.Size() > 0 && info.Size() <= staticCacheMaxFileBytes && info.Size() <= staticCacheMaxBytes
}

func detectContentType(cleanPath string, payload []byte) string {
	ext := strings.ToLower(path.Ext(cleanPath))
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		contentType = http.DetectContentType(payload)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return contentType
}

func cleanStaticAssetPath(requestPath string) (string, bool) {
	requestPath = strings.TrimPrefix(requestPath, "/")
	for _, part := range strings.Split(requestPath, "/") {
		if part == ".." {
			return "", false
		}
	}
	cleanPath := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
	if cleanPath == "" || cleanPath == "." || !fs.ValidPath(cleanPath) {
		return "", false
	}
	return cleanPath, true
}
