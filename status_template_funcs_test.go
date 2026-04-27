package main

import "testing"

func TestFormatDiff_SmallValues(t *testing.T) {
	f := buildTemplateFuncs()["formatDiff"].(func(float64) string)

	tests := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{0.5, "0.5"},
		{0.01, "0.01"},
		{0.0001234, "0.000123"},
		{0.0000009, "0.0000009"},
		{1, "1"},
		{1_000_000, "1.0M"},
		{1_000_000_000, "1.0G"},
		{1_000_000_000_000, "1.0T"},
		{1_000_000_000_000_000, "1.0P"},
	}

	for _, tc := range tests {
		if got := f(tc.in); got != tc.want {
			t.Fatalf("formatDiff(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
