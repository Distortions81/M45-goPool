package main

import "testing"

func TestLoadTemplates_Parse(t *testing.T) {
	t.Parallel()

	if _, err := loadTemplates(); err != nil {
		t.Fatalf("loadTemplates error: %v", err)
	}
}
