package main

import "strings"

func formatNodeZMQAddr(cfg Config) string {
	hashAddr := strings.TrimSpace(cfg.ZMQHashBlockAddr)
	rawAddr := strings.TrimSpace(cfg.ZMQRawBlockAddr)

	if hashAddr != "" && rawAddr != "" && hashAddr == rawAddr {
		return "hashblock+rawblock " + hashAddr
	}

	var parts []string
	if hashAddr != "" {
		parts = append(parts, "hashblock "+hashAddr)
	}
	if rawAddr != "" {
		parts = append(parts, "rawblock "+rawAddr)
	}
	return strings.Join(parts, " | ")
}
