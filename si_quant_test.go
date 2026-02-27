package main

import (
	"math"
	"testing"
)

func TestSIQuant16Zero(t *testing.T) {
	if got := encodeSIQuant16(0); got != 0 {
		t.Fatalf("encodeSIQuant16(0) = %d, want 0", got)
	}
	if got := decodeSIQuant16(0); got != 0 {
		t.Fatalf("decodeSIQuant16(0) = %v, want 0", got)
	}
}

func TestSIQuant16RoundTripRelativeError(t *testing.T) {
	values := []float64{
		1,
		12.345,
		999.9,
		1000,
		1234.56,
		999_999,
		1_234_567,
		75_000_000_000,
		123_456_789_012_345,
	}
	for _, v := range values {
		code := encodeSIQuant16(v)
		got := decodeSIQuant16(code)
		if got <= 0 {
			t.Fatalf("decodeSIQuant16(encode(%v)) = %v", v, got)
		}
		relErr := math.Abs(got-v) / v
		if relErr > 1e-3 {
			t.Fatalf("roundtrip relErr too high for %v: got=%v code=%d relErr=%g", v, got, code, relErr)
		}
	}
}

func TestSIQuant16MonotonicForOrderedInputs(t *testing.T) {
	values := []float64{
		0,
		1,
		2,
		10,
		999.9,
		1000,
		1001,
		10_000,
		1_000_000,
		1_000_000_000,
		100_000_000_000_000,
	}
	prev := uint16(0)
	for i, v := range values {
		code := encodeSIQuant16(v)
		if i > 0 && code < prev {
			t.Fatalf("non-monotonic: v=%v code=%d < prev=%d", v, code, prev)
		}
		prev = code
	}
}

func TestSIQuant16Saturates(t *testing.T) {
	maxCode := uint16((siQuant16MaxExp << siQuant16MantBits) | siQuant16MantMask)
	if got := encodeSIQuant16(math.Inf(1)); got != maxCode {
		t.Fatalf("encodeSIQuant16(+Inf) = %d, want %d", got, maxCode)
	}
	if got := encodeSIQuant16(1e300); got != maxCode {
		t.Fatalf("encodeSIQuant16(1e300) = %d, want %d", got, maxCode)
	}
}

func TestSIQuant8Zero(t *testing.T) {
	if got := encodeSIQuant8(0); got != 0 {
		t.Fatalf("encodeSIQuant8(0) = %d, want 0", got)
	}
	if got := decodeSIQuant8(0); got != 0 {
		t.Fatalf("decodeSIQuant8(0) = %v, want 0", got)
	}
}

func TestSIQuant8RoundTripRelativeError(t *testing.T) {
	values := []float64{
		1,
		12.345,
		999.9,
		1000,
		1234.56,
		999_999,
		1_234_567,
		75_000_000_000,
		123_456_789_012_345,
	}
	for _, v := range values {
		code := encodeSIQuant8(v)
		got := decodeSIQuant8(code)
		if got <= 0 {
			t.Fatalf("decodeSIQuant8(encode(%v)) = %v", v, got)
		}
		relErr := math.Abs(got-v) / v
		if relErr > 0.2 {
			t.Fatalf("roundtrip relErr too high for %v: got=%v code=%d relErr=%g", v, got, code, relErr)
		}
	}
}
