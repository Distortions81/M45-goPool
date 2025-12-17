package main

type sha256SumFunc func([]byte) [32]byte

var sha256Sum sha256SumFunc
