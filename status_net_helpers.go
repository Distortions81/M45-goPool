package main

import (
	"net"
	"net/http"
	"strings"
)

func remoteHostFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
		parts := strings.Split(fwd, ",")
		if len(parts) > 0 {
			if host := strings.TrimSpace(parts[0]); host != "" {
				return host
			}
		}
	}
	host := r.RemoteAddr
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

func stripPeerPort(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

func formatPeerDisplay(host, resolved string) string {
	resolved = strings.TrimSpace(resolved)
	if resolved != "" && !strings.EqualFold(resolved, host) {
		return resolved
	}
	return host
}
