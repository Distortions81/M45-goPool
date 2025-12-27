package main

import "sync"

// workerConnectionRegistry tracks current miner connections keyed by worker SHA256.
type workerConnectionRegistry struct {
	mu    sync.Mutex
	conns map[string]*MinerConn
}

func newWorkerConnectionRegistry() *workerConnectionRegistry {
	return &workerConnectionRegistry{
		conns: make(map[string]*MinerConn),
	}
}

// register associates the hashed worker name with mc and returns any previous
// connection that owned that hash.
func (r *workerConnectionRegistry) register(hash string, mc *MinerConn) *MinerConn {
	if hash == "" || mc == nil {
		return nil
	}
	r.mu.Lock()
	prev := r.conns[hash]
	r.conns[hash] = mc
	r.mu.Unlock()
	if prev == mc {
		return nil
	}
	return prev
}

// unregister removes mc from the registry for the given worker hash.
func (r *workerConnectionRegistry) unregister(hash string, mc *MinerConn) {
	if hash == "" || mc == nil {
		return
	}
	r.mu.Lock()
	if current, ok := r.conns[hash]; ok && current == mc {
		delete(r.conns, hash)
	}
	r.mu.Unlock()
}
