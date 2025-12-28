package main

import (
	"fmt"
	"strings"
)

func workerNameHash(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	sum := sha256Sum([]byte(name))
	return fmt.Sprintf("%x", sum[:])
}
