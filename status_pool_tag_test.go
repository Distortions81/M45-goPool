package main

import "testing"

func TestDisplayPoolTagFromCoinbaseMessage(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"/goPool/", "/goPool/"},
		{"goPool", "/goPool/"},
		{"/goPool", "/goPool/"},
		{"/goPool/ABCD-1234", "/goPool/"},
		{"/goPool/ABCD-1234/", "/goPool/"},
		{"//goPool//ABCD//", "/goPool/"},
	}

	for _, tc := range tests {
		if got := displayPoolTagFromCoinbaseMessage(tc.in); got != tc.want {
			t.Fatalf("displayPoolTagFromCoinbaseMessage(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

