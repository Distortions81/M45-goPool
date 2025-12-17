//go:build noavx

package main

import stdsha "crypto/sha256"

func init() {
	sha256Sum = stdsha.Sum256
}

func sha256ImplementationName() string {
	return "crypto/sha256"
}
