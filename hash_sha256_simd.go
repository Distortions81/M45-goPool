//go:build !noavx

package main

import simdsha "github.com/minio/sha256-simd"

func init() {
	sha256Sum = simdsha.Sum256
}

func sha256ImplementationName() string {
	return "sha256-simd"
}
