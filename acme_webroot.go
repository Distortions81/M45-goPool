package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	acmeChallengeURLPrefix = "/.well-known/acme-challenge/"
	acmeWebrootDirName     = "certbot-webroot"
)

func acmeWebrootDir(dataDir string) string {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	return filepath.Join(dataDir, acmeWebrootDirName)
}

func ensureACMEWebroot(dataDir string) (string, error) {
	root := acmeWebrootDir(dataDir)
	challengeDir := filepath.Join(root, ".well-known", "acme-challenge")
	if err := os.MkdirAll(challengeDir, 0o755); err != nil {
		return "", err
	}
	return root, nil
}

type acmeChallengeHandler struct {
	handler http.Handler
}

func newACMEChallengeHandler(webroot string) *acmeChallengeHandler {
	webroot = strings.TrimSpace(webroot)
	if webroot == "" {
		return nil
	}
	challengeDir := filepath.Join(webroot, ".well-known", "acme-challenge")
	return &acmeChallengeHandler{
		handler: http.StripPrefix(acmeChallengeURLPrefix, http.FileServer(http.Dir(challengeDir))),
	}
}

func (h *acmeChallengeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || !h.ServeChallenge(w, r) {
		http.NotFound(w, r)
	}
}

func (h *acmeChallengeHandler) ServeChallenge(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || h.handler == nil || r == nil {
		return false
	}
	if !strings.HasPrefix(r.URL.Path, acmeChallengeURLPrefix) {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}
	h.handler.ServeHTTP(w, r)
	return true
}

func serveACMEOrFallback(acme *acmeChallengeHandler, fallback http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if acme != nil && acme.ServeChallenge(w, r) {
			return
		}
		if fallback != nil {
			fallback.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
}
