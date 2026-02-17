package main

import (
	"encoding/hex"
	"strings"
)

func workerNameHashTrimmed(name string) string {
	if name == "" {
		return ""
	}
	sum := sha256Sum([]byte(name))
	return hex.EncodeToString(sum[:])
}

func workerNameHash(name string) string {
	name = strings.TrimSpace(name)
	return workerNameHashTrimmed(name)
}
