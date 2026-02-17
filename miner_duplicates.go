package main

import (
	"sync"
	"time"
)

// duplicateShareKey is a compact, comparable representation of a share
// submission used for duplicate detection. It stores a bounded prefix of
// the concatenated extranonce2, ntime, nonce, and version fields.
type duplicateShareKey struct {
	n   uint8
	buf [maxDuplicateShareKeyBytes]byte
}

// duplicateShareSet is a hash-based duplicate detection cache with bounded size.
// Uses LRU eviction to remove oldest entries when at capacity.
type duplicateShareSet struct {
	mu    sync.Mutex
	m     map[duplicateShareKey]struct{}
	order []duplicateShareKey // Track insertion order for LRU eviction
}

// evictedCacheEntry holds a share cache for an evicted job during grace period.
type evictedCacheEntry struct {
	cache     *duplicateShareSet
	evictedAt time.Time
}

func makeDuplicateShareKey(dst *duplicateShareKey, extranonce2, ntime, nonce string, version uint32) {
	*dst = duplicateShareKey{}
	write := func(s string) {
		for i := 0; i < len(s) && int(dst.n) < maxDuplicateShareKeyBytes; i++ {
			dst.buf[dst.n] = s[i]
			dst.n++
		}
	}
	writeUint32Hex := func(v uint32) {
		const hexChars = "0123456789abcdef"
		for shift := 28; shift >= 0 && int(dst.n) < maxDuplicateShareKeyBytes; shift -= 4 {
			dst.buf[dst.n] = hexChars[int((v>>uint(shift))&0xF)]
			dst.n++
		}
	}
	if dst.n < maxDuplicateShareKeyBytes {
		write(extranonce2)
	}
	if dst.n < maxDuplicateShareKeyBytes {
		dst.buf[dst.n] = ':'
		dst.n++
	}
	if dst.n < maxDuplicateShareKeyBytes {
		write(ntime)
	}
	if dst.n < maxDuplicateShareKeyBytes {
		dst.buf[dst.n] = ':'
		dst.n++
	}
	if dst.n < maxDuplicateShareKeyBytes {
		write(nonce)
	}
	if dst.n < maxDuplicateShareKeyBytes {
		dst.buf[dst.n] = ':'
		dst.n++
	}
	if dst.n < maxDuplicateShareKeyBytes {
		writeUint32Hex(version)
	}
}

func makeDuplicateShareKeyDecoded(dst *duplicateShareKey, extranonce2 []byte, ntime, nonce uint32, version uint32) {
	*dst = duplicateShareKey{}

	writeBytes := func(b []byte) {
		if int(dst.n) >= maxDuplicateShareKeyBytes {
			return
		}
		remain := maxDuplicateShareKeyBytes - int(dst.n)
		if len(b) > remain {
			b = b[:remain]
		}
		n := copy(dst.buf[dst.n:], b)
		dst.n += uint8(n)
	}
	writeU32 := func(v uint32) {
		remain := maxDuplicateShareKeyBytes - int(dst.n)
		if remain <= 0 {
			return
		}
		if remain >= 4 {
			i := dst.n
			dst.buf[i+0] = byte(v >> 24)
			dst.buf[i+1] = byte(v >> 16)
			dst.buf[i+2] = byte(v >> 8)
			dst.buf[i+3] = byte(v)
			dst.n += 4
			return
		}
		shift := uint(24)
		for remain > 0 {
			dst.buf[dst.n] = byte(v >> shift)
			dst.n++
			remain--
			shift -= 8
		}
	}

	writeBytes(extranonce2)
	writeU32(ntime)
	writeU32(nonce)
	writeU32(version)
}

// seenOrAdd reports whether key has already been seen, and records it if not.
// O(1) lookup via hash map. Uses LRU eviction when reaching capacity.
func (s *duplicateShareSet) seenOrAdd(key duplicateShareKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.m == nil {
		s.m = make(map[duplicateShareKey]struct{}, duplicateShareHistory)
		s.order = make([]duplicateShareKey, 0, duplicateShareHistory)
	}

	if _, seen := s.m[key]; seen {
		return true
	}

	// Evict oldest 10% when at capacity (keeps 90% of recent history)
	if len(s.order) >= duplicateShareHistory {
		evictCount := max(duplicateShareHistory/10, 1)
		for i := 0; i < evictCount; i++ {
			delete(s.m, s.order[i])
		}
		s.order = s.order[evictCount:]
	}

	// Add new key
	s.m[key] = struct{}{}
	s.order = append(s.order, key)

	return false
}
