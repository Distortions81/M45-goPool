package main

import (
	"fmt"
)

func workerNameHash(name string) string {
	if name == "" {
		return ""
	}
	sum := sha256Sum([]byte(name))
	return fmt.Sprintf("%x", sum[:])
}
