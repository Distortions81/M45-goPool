package main

import (
	"bytes"
	"testing"
)

func TestHexLUTAcceptsUpperAndLower(t *testing.T) {
	dstLower := make([]byte, 4)
	if err := decodeHexToFixedBytes(dstLower, "deadBEEF"); err != nil {
		t.Fatalf("decode lower/mixed: %v", err)
	}

	dstUpper := make([]byte, 4)
	if err := decodeHexToFixedBytes(dstUpper, "DEADBEEF"); err != nil {
		t.Fatalf("decode upper: %v", err)
	}

	if !bytes.Equal(dstLower, dstUpper) {
		t.Fatalf("mixed-case decode mismatch: lower=%x upper=%x", dstLower, dstUpper)
	}

	gotStrLower, err := parseUint32BEHex("deadbeef")
	if err != nil {
		t.Fatalf("parse lower: %v", err)
	}
	gotStrUpper, err := parseUint32BEHex("DEADBEEF")
	if err != nil {
		t.Fatalf("parse upper: %v", err)
	}
	if gotStrLower != gotStrUpper {
		t.Fatalf("parse mismatch: lower=%08x upper=%08x", gotStrLower, gotStrUpper)
	}

	gotBytesLower, err := parseUint32BEHexBytes([]byte("deadbeef"))
	if err != nil {
		t.Fatalf("parse bytes lower: %v", err)
	}
	gotBytesUpper, err := parseUint32BEHexBytes([]byte("DEADBEEF"))
	if err != nil {
		t.Fatalf("parse bytes upper: %v", err)
	}
	if gotBytesLower != gotBytesUpper {
		t.Fatalf("parse bytes mismatch: lower=%08x upper=%08x", gotBytesLower, gotBytesUpper)
	}
}

