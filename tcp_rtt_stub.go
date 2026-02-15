//go:build !linux

package main

import "net"

func estimateConnRTTMS(conn net.Conn) float64 {
	_ = conn
	return 0
}
