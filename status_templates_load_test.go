package main

import "testing"

func TestLoadTemplates_Parse(t *testing.T) {
	t.Parallel()

	if _, err := loadTemplates("data"); err != nil {
		t.Fatalf("loadTemplates(data) error: %v", err)
	}
}

