package main

import (
	"fmt"
	"net"
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

// httpRedirectInjector wraps an http.Handler and injects a meta refresh redirect
// into HTML responses for the main page when served over HTTP.
type httpRedirectInjector struct {
	mux       http.Handler
	httpsAddr string
}

func (h *httpRedirectInjector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only inject redirect for the main page
	if r.URL.Path != "/" {
		h.mux.ServeHTTP(w, r)
		return
	}

	// Calculate HTTPS target URL
	host := r.Host
	if hostOnly, _, err := net.SplitHostPort(host); err == nil {
		host = hostOnly
	}
	_, tlsPort, err := net.SplitHostPort(h.httpsAddr)
	targetHost := host
	if err == nil && tlsPort != "" && tlsPort != "443" {
		targetHost = net.JoinHostPort(host, tlsPort)
	}
	httpsURL := "https://" + targetHost + r.URL.RequestURI()

	// Capture the response
	rec := &responseRecorder{
		ResponseWriter: w,
		statusCode:     200,
	}
	h.mux.ServeHTTP(rec, r)

	// If it's HTML, inject meta refresh
	if rec.statusCode == 200 && strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		body := rec.body.String()
		// Inject meta refresh right after <head> tag
		metaTag := fmt.Sprintf(`<meta http-equiv="refresh" content="0; url=%s">`, httpsURL)
		if idx := strings.Index(body, "<head>"); idx != -1 {
			body = body[:idx+6] + "\n" + metaTag + body[idx+6:]
		} else if idx := strings.Index(body, "<HEAD>"); idx != -1 {
			body = body[:idx+6] + "\n" + metaTag + body[idx+6:]
		}
		w.Write([]byte(body))
	}
}

// responseRecorder captures the response body for modification
type responseRecorder struct {
	http.ResponseWriter
	body       strings.Builder
	statusCode int
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	return len(b), nil
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	r.statusCode = statusCode
	// Don't call underlying WriteHeader yet - we'll write everything at once
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
