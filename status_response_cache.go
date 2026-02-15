package main

import (
	"bytes"
	"net/http"
	"strings"
	"time"
)

const (
	responseCacheMaxEntries = 4096
	responseCacheMaxBytes   = 2 << 20 // 2MB per response
)

type cachedHTTPResponse struct {
	status    int
	header    http.Header
	body      []byte
	expiresAt time.Time
}

type captureResponseWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newCaptureResponseWriter() *captureResponseWriter {
	return &captureResponseWriter{
		header: make(http.Header),
		status: http.StatusOK,
	}
}

func (w *captureResponseWriter) Header() http.Header {
	return w.header
}

func (w *captureResponseWriter) WriteHeader(statusCode int) {
	w.status = statusCode
}

func (w *captureResponseWriter) Write(p []byte) (int, error) {
	return w.body.Write(p)
}

func (w *captureResponseWriter) flushTo(dst http.ResponseWriter, method string) {
	h := dst.Header()
	for k := range h {
		h.Del(k)
	}
	for k, values := range w.header {
		copied := append([]string(nil), values...)
		h[k] = copied
	}
	dst.WriteHeader(w.status)
	if method == http.MethodHead {
		return
	}
	_, _ = dst.Write(w.body.Bytes())
}

func (s *StatusServer) responseCacheKey(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	var b strings.Builder
	if r.Method == http.MethodHead {
		b.WriteString(http.MethodGet)
	} else {
		b.WriteString(r.Method)
	}
	b.WriteString("\n")
	b.WriteString(r.URL.RequestURI())
	b.WriteString("\n")
	b.WriteString(r.Header.Get("Cookie"))
	return b.String()
}

func cloneHeader(src http.Header) http.Header {
	dst := make(http.Header, len(src))
	for k, values := range src {
		dst[k] = append([]string(nil), values...)
	}
	return dst
}

func isResponseCacheable(status int, header http.Header, bodyLen int) bool {
	if status != http.StatusOK {
		return false
	}
	if bodyLen <= 0 || bodyLen > responseCacheMaxBytes {
		return false
	}
	if len(header.Values("Set-Cookie")) > 0 {
		return false
	}
	contentType := strings.ToLower(strings.TrimSpace(header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "text/html") {
		return true
	}
	if strings.HasPrefix(contentType, "application/json") {
		return true
	}
	return false
}

func (s *StatusServer) serveShortResponseCache(next http.Handler) http.Handler {
	if s == nil || next == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			next.ServeHTTP(w, r)
			return
		}
		key := s.responseCacheKey(r)
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}

		now := time.Now()
		s.responseCacheMu.RLock()
		entry, ok := s.responseCache[key]
		s.responseCacheMu.RUnlock()
		if ok && now.Before(entry.expiresAt) {
			h := w.Header()
			for k := range h {
				h.Del(k)
			}
			for k, values := range entry.header {
				h[k] = append([]string(nil), values...)
			}
			w.WriteHeader(entry.status)
			if r.Method != http.MethodHead {
				_, _ = w.Write(entry.body)
			}
			return
		}

		capture := newCaptureResponseWriter()
		next.ServeHTTP(capture, r)
		capture.flushTo(w, r.Method)

		if !isResponseCacheable(capture.status, capture.header, capture.body.Len()) {
			return
		}

		s.responseCacheMu.Lock()
		if s.responseCache == nil {
			s.responseCache = make(map[string]cachedHTTPResponse)
		}
		if len(s.responseCache) >= responseCacheMaxEntries {
			for cacheKey, cacheEntry := range s.responseCache {
				if now.After(cacheEntry.expiresAt) {
					delete(s.responseCache, cacheKey)
				}
			}
			if len(s.responseCache) >= responseCacheMaxEntries {
				s.responseCache = make(map[string]cachedHTTPResponse)
			}
		}
		s.responseCache[key] = cachedHTTPResponse{
			status:    capture.status,
			header:    cloneHeader(capture.header),
			body:      append([]byte(nil), capture.body.Bytes()...),
			expiresAt: now.Add(shortEndpointCacheTTL),
		}
		s.responseCacheMu.Unlock()
	})
}
