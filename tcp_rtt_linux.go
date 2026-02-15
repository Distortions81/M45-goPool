//go:build linux

package main

import (
	"net"

	"golang.org/x/sys/unix"
)

// estimateConnRTTMS returns the kernel-estimated TCP RTT in milliseconds when
// available (Linux TCP_INFO). Returns 0 when unavailable.
func estimateConnRTTMS(conn net.Conn) float64 {
	tc := findTCPConn(conn)
	if tc == nil {
		return 0
	}
	raw, err := tc.SyscallConn()
	if err != nil {
		return 0
	}
	var info unix.TCPInfo
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		info, sockErr = getTCPInfo(int(fd))
	}); err != nil || sockErr != nil {
		return 0
	}
	if info.Rtt <= 0 {
		return 0
	}
	// Linux tcp_info.rtt is in microseconds.
	return float64(info.Rtt) / 1000.0
}

func getTCPInfo(fd int) (unix.TCPInfo, error) {
	info, err := unix.GetsockoptTCPInfo(fd, unix.IPPROTO_TCP, unix.TCP_INFO)
	if err != nil {
		return unix.TCPInfo{}, err
	}
	return *info, nil
}
