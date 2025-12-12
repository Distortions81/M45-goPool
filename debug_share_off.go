//go:build !debug

package main

import (
	"encoding/hex"
	"math/big"
)

// buildShareDebug is a lightweight stub used in non-debug builds. It captures
// only basic header/hash/target/merkle metadata and skips expensive coinbase
// reconstruction and decoding so the hot share path remains cheap.
func (mc *MinerConn) buildShareDebug(job *Job, worker string, header []byte, hash []byte, target *big.Int, extranonce2 string, merkleRoot []byte) *ShareDebug {
	if job == nil {
		return nil
	}

	debug := &ShareDebug{
		Header:    hex.EncodeToString(header),
		ShareHash: hex.EncodeToString(hash),
	}
	if target != nil {
		debug.Target = hex.EncodeToString(target.FillBytes(make([]byte, 32)))
	}
	if len(merkleRoot) == 32 {
		debug.MerkleRootBE = hex.EncodeToString(merkleRoot)
		debug.MerkleRootLE = hex.EncodeToString(reverseBytes(merkleRoot))
	}
	return debug
}
