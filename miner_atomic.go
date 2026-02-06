package main

import (
	"math"
	"sync/atomic"
)

// Helper functions for atomic float64 operations (stored as uint64 bits)
func atomicLoadFloat64(addr *atomic.Uint64) float64 {
	return math.Float64frombits(addr.Load())
}

func atomicStoreFloat64(addr *atomic.Uint64, val float64) {
	addr.Store(math.Float64bits(val))
}
