package main

import (
	"sync"
	"sync/atomic"
)

// workerConnectionRegistry tracks current miner connections by connection ID.
// The primary storage is a map indexed by connection number, with a secondary
// map for looking up connections by worker name SHA256.
type workerConnectionRegistry struct {
	mu            sync.Mutex
	conns         map[uint64]*MinerConn   // connection seq -> MinerConn
	nameToConnIDs map[string][]uint64     // SHA256 -> list of connection IDs
}

func newWorkerConnectionRegistry() *workerConnectionRegistry {
	return &workerConnectionRegistry{
		conns:         make(map[uint64]*MinerConn),
		nameToConnIDs: make(map[string][]uint64),
	}
}

// register associates the hashed worker name with mc and returns any previous
// connection that owned that hash.
func (r *workerConnectionRegistry) register(hash string, mc *MinerConn) *MinerConn {
	if hash == "" || mc == nil {
		return nil
	}

	connSeq := atomic.LoadUint64(&mc.connectionSeq)
	if connSeq == 0 {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Store connection by its sequence number
	r.conns[connSeq] = mc

	// Check if there's a previous connection with this worker hash
	var prev *MinerConn
	if connIDs, exists := r.nameToConnIDs[hash]; exists && len(connIDs) > 0 {
		// Find the most recent connection that isn't this one
		for i := len(connIDs) - 1; i >= 0; i-- {
			id := connIDs[i]
			if id != connSeq {
				if candidate := r.conns[id]; candidate != nil && candidate != mc {
					prev = candidate
					break
				}
			}
		}

		// Add this connection to the list
		r.nameToConnIDs[hash] = append(connIDs, connSeq)
	} else {
		// First connection with this worker name
		r.nameToConnIDs[hash] = []uint64{connSeq}
	}

	return prev
}

// unregister removes mc from the registry for the given worker hash.
func (r *workerConnectionRegistry) unregister(hash string, mc *MinerConn) {
	if hash == "" || mc == nil {
		return
	}

	connSeq := atomic.LoadUint64(&mc.connectionSeq)
	if connSeq == 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Remove from the main connection map
	if current, exists := r.conns[connSeq]; exists && current == mc {
		delete(r.conns, connSeq)
	}

	// Remove from the name-to-connection-ID map
	if connIDs, exists := r.nameToConnIDs[hash]; exists {
		// Filter out this connection's ID
		filtered := make([]uint64, 0, len(connIDs))
		for _, id := range connIDs {
			if id != connSeq {
				filtered = append(filtered, id)
			}
		}

		if len(filtered) == 0 {
			delete(r.nameToConnIDs, hash)
		} else {
			r.nameToConnIDs[hash] = filtered
		}
	}
}

// getConnectionsByHash returns all active connections for a given worker hash.
func (r *workerConnectionRegistry) getConnectionsByHash(hash string) []*MinerConn {
	if hash == "" {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	connIDs, exists := r.nameToConnIDs[hash]
	if !exists || len(connIDs) == 0 {
		return nil
	}

	// Collect all active connections
	result := make([]*MinerConn, 0, len(connIDs))
	for _, id := range connIDs {
		if mc := r.conns[id]; mc != nil {
			result = append(result, mc)
		}
	}

	return result
}
