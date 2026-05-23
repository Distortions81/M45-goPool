package main

import (
	"crypto/sha256"
	"testing"

	"github.com/btcsuite/btcd/btcutil/base58"
)

// TestSV2AuthKeyTestVector verifies the authority-key base58check encoding
// against the SV2 spec §4.7 test vector.
func TestSV2AuthKeyTestVector(t *testing.T) {
	// raw_ca_public_key from the spec (32 bytes)
	rawKey := []byte{118, 99, 112, 0, 151, 156, 28, 17, 175, 12, 48, 11, 205, 140, 127, 228, 134, 16, 252, 233, 185, 193, 30, 61, 174, 227, 90, 224, 176, 138, 116, 85}
	expected := "9bXiEd8boQVhq7WddEcERUL5tyyJVFYdU8th3HfbNXK3Yw6GRXh"

	payload := make([]byte, 34)
	payload[0] = 0x01
	payload[1] = 0x00
	copy(payload[2:], rawKey)
	h1 := sha256.Sum256(payload)
	h2 := sha256.Sum256(h1[:])
	full := append(payload, h2[:4]...)
	result := base58.Encode(full)

	if result != expected {
		t.Errorf("expected %s, got %s", expected, result)
	}
}
