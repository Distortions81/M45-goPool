package main

import "sync"

// workerConnectionRegistry tracks active miner connections keyed by worker ID.
type workerConnectionRegistry struct {
	mu    sync.Mutex
	conns map[string]*MinerConn
}

func newWorkerConnectionRegistry() *workerConnectionRegistry {
	return &workerConnectionRegistry{
		conns: make(map[string]*MinerConn),
	}
}

// register associates worker with mc and returns any previous connection for that worker.
func (r *workerConnectionRegistry) register(worker string, mc *MinerConn) *MinerConn {
	if worker == "" || mc == nil {
		return nil
	}
	r.mu.Lock()
	prev := r.conns[worker]
	r.conns[worker] = mc
	r.mu.Unlock()
	if prev == mc {
		return nil
	}
	return prev
}

// unregister removes mc from the worker entry only if it still owns the key.
func (r *workerConnectionRegistry) unregister(worker string, mc *MinerConn) {
	if worker == "" || mc == nil {
		return
	}
	r.mu.Lock()
	if current, ok := r.conns[worker]; ok && current == mc {
		delete(r.conns, worker)
	}
	r.mu.Unlock()
}
