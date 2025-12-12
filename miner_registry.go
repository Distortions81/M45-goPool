package main

import "sync"

// MinerRegistry tracks active MinerConn instances with a mutex held only during
// add/remove operations. Snapshotting allows status/metrics code to walk a
// consistent view without blocking share handling.
type MinerRegistry struct {
	mu    sync.Mutex
	conns map[*MinerConn]struct{}
}

func NewMinerRegistry() *MinerRegistry {
	return &MinerRegistry{
		conns: make(map[*MinerConn]struct{}),
	}
}

func (r *MinerRegistry) Add(mc *MinerConn) {
	if mc == nil {
		return
	}
	r.mu.Lock()
	r.conns[mc] = struct{}{}
	r.mu.Unlock()
}

func (r *MinerRegistry) Remove(mc *MinerConn) {
	if mc == nil {
		return
	}
	r.mu.Lock()
	delete(r.conns, mc)
	r.mu.Unlock()
}

func (r *MinerRegistry) Count() int {
	r.mu.Lock()
	n := len(r.conns)
	r.mu.Unlock()
	return n
}

func (r *MinerRegistry) Snapshot() []*MinerConn {
	r.mu.Lock()
	out := make([]*MinerConn, 0, len(r.conns))
	for mc := range r.conns {
		out = append(out, mc)
	}
	r.mu.Unlock()
	return out
}
