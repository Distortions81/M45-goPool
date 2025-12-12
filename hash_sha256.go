package main

import (
	stdsha "crypto/sha256"

	simdsha "github.com/minio/sha256-simd"
)

type sha256SumFunc func([]byte) [32]byte

var sha256Sum sha256SumFunc = stdsha.Sum256

func setSha256Implementation(useSimd bool) {
	if useSimd {
		sha256Sum = simdsha.Sum256
		return
	}
	sha256Sum = stdsha.Sum256
}
