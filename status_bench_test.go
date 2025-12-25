package main

import (
	"fmt"
	"testing"
)

const benchSharesPerWorker = 15.0

// approximateMaxMiners converts a total share rate back into how many miners
// are represented if each miner emits sharesPerWorker shares per minute.
func approximateMaxMiners(workers []WorkerView, sharesPerWorker float64) int {
	if sharesPerWorker <= 0 {
		return 0
	}
	var totalShareRate float64
	for _, w := range workers {
		totalShareRate += w.ShareRate
	}
	return int(totalShareRate / sharesPerWorker)
}

func BenchmarkApproximateMinerCapacity(b *testing.B) {
	workerSizes := []int{100, 1000, 10000}
	for _, size := range workerSizes {
		b.Run(fmt.Sprintf("%d_workers", size), func(b *testing.B) {
			workers := make([]WorkerView, size)
			for i := range workers {
				workers[i].ShareRate = benchSharesPerWorker + float64(i%5)
			}
			var expectedTotal float64
			for _, w := range workers {
				expectedTotal += w.ShareRate
			}
			expected := int(expectedTotal / benchSharesPerWorker)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if got := approximateMaxMiners(workers, benchSharesPerWorker); got != expected {
					b.Fatalf("unexpected miner estimate: got %d want %d", got, expected)
				}
			}
		})
	}
}
