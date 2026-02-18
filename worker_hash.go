package main

import "strings"

func workerNameHashTrimmed(name string) string {
	if name == "" {
		return ""
	}
	sum := sha256Sum([]byte(name))
	return hexEncode32LowerString(&sum)
}

func workerNameHash(name string) string {
	name = strings.TrimSpace(name)
	return workerNameHashTrimmed(name)
}
