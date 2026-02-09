package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

func buildMerkleBranches(txids [][]byte) []string {
	if len(txids) == 0 {
		return []string{}
	}
	// Pre-allocate with exact size: nil placeholder + all txids
	layer := make([][]byte, 1+len(txids))
	layer[0] = nil
	copy(layer[1:], txids)

	// Pre-allocate steps slice - merkle tree depth is log2(txcount)
	// For 4000 txs, depth is ~12. Pre-allocate 16 to be safe.
	steps := make([]string, 0, 16)
	L := len(layer)
	for L > 1 {
		steps = append(steps, hex.EncodeToString(layer[1]))
		if L%2 == 1 {
			layer = append(layer, layer[L-1])
			L++
		}
		next := make([][]byte, 0, L/2)
		for i := 1; i+1 < L; i += 2 {
			joined := append(append([]byte{}, layer[i]...), layer[i+1]...)
			next = append(next, doubleSHA256(joined))
		}
		layer = append([][]byte{nil}, next...)
		L = len(layer)
	}
	return steps
}

// computeMerkleRootFromBranches computes the merkle root by starting with the
// coinbase txid (BE) and applying each branch (LE) in order, returning a BE root.
func computeMerkleRootFromBranches(coinbaseHash []byte, branches []string) []byte {
	root := coinbaseHash
	var hashBuf [32]byte
	var concatBuf [64]byte
	for _, b := range branches {
		if len(b) != 64 {
			return nil
		}
		n, err := hex.Decode(hashBuf[:], []byte(b))
		if err != nil || n != 32 {
			return nil
		}
		copy(concatBuf[:32], root)
		copy(concatBuf[32:], hashBuf[:])
		root = doubleSHA256(concatBuf[:])
	}
	return root
}

// buildBlockHeaderFromHex constructs the block header bytes for SHA256d jobs.
// This differs from the canonical Bitcoin header layout:
//
//	header[0:4]   = nonce (BE hex from miner)
//	header[4:8]   = bits  (BE hex from template)
//	header[8:12]  = ntime (BE hex from miner)
//	header[12:44] = merkleRoot (LE bytes)
//	header[44:76] = previousblockhash (BE bytes from template)
//	header[76:80] = version (big-endian uint32)
//
// The entire 80-byte header is then reversed before hashing.
// This helper is intended for test and block-construction paths where the
// previous block hash and bits fields are available only as hex strings. For
// hot share-validation paths that already have a Job, prefer Job.buildBlockHeader,
// which reuses pre-decoded header fields.
func buildBlockHeaderFromHex(version int32, prevhash string, merkleRootBE []byte, ntimeHex string, bitsHex string, nonceHex string) ([]byte, error) {
	if len(merkleRootBE) != 32 {
		return nil, fmt.Errorf("merkle root must be 32 bytes")
	}

	// Use static arrays to avoid allocations
	var prev [32]byte
	var ntimeBytes [4]byte
	var bitsBytes [4]byte
	var nonceBytes [4]byte
	var hdr [80]byte
	var merkleReversed [32]byte

	// Decode prevhash
	if len(prevhash) != 64 {
		return nil, fmt.Errorf("prevhash hex must be 64 chars")
	}
	n, err := hex.Decode(prev[:], []byte(prevhash))
	if err != nil || n != 32 {
		return nil, fmt.Errorf("decode prevhash: %w", err)
	}

	// Decode ntime
	if len(ntimeHex) != 8 {
		return nil, fmt.Errorf("ntime hex must be 8 chars")
	}
	n, err = hex.Decode(ntimeBytes[:], []byte(ntimeHex))
	if err != nil || n != 4 {
		return nil, fmt.Errorf("decode ntime: %w", err)
	}

	// Decode bits
	if len(bitsHex) != 8 {
		return nil, fmt.Errorf("bits hex must be 8 chars")
	}
	n, err = hex.Decode(bitsBytes[:], []byte(bitsHex))
	if err != nil || n != 4 {
		return nil, fmt.Errorf("decode bits: %w", err)
	}

	// Decode nonce
	if len(nonceHex) != 8 {
		return nil, fmt.Errorf("nonce hex must be 8 chars")
	}
	n, err = hex.Decode(nonceBytes[:], []byte(nonceHex))
	if err != nil || n != 4 {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}

	// Reverse merkle root in place
	for i := 0; i < 32; i++ {
		merkleReversed[i] = merkleRootBE[31-i]
	}

	// Build header
	copy(hdr[0:4], nonceBytes[:])
	copy(hdr[4:8], bitsBytes[:])
	copy(hdr[8:12], ntimeBytes[:])
	copy(hdr[12:44], merkleReversed[:])
	copy(hdr[44:76], prev[:])
	uver := uint32(version)
	hdr[76] = byte(uver >> 24)
	hdr[77] = byte(uver >> 16)
	hdr[78] = byte(uver >> 8)
	hdr[79] = byte(uver)

	// Foundation/template.serializeHeader reverses the entire header buffer
	// before hashing; mirror that here. Reverse in place.
	for i := 0; i < 40; i++ {
		hdr[i], hdr[79-i] = hdr[79-i], hdr[i]
	}

	return hdr[:], nil
}

// buildBlockHeader constructs the block header bytes using precomputed per-job
// header pieces (previous block hash bytes and bits bytes) stored on Job. It
// avoids redundant hex decoding on every share submission and is used on
// performance-sensitive paths such as share validation and submitblock rebuilds.
func (job *Job) buildBlockHeader(merkleRootBE []byte, ntimeHex string, nonceHex string, version int32) ([]byte, error) {
	if len(merkleRootBE) != 32 {
		return nil, fmt.Errorf("merkle root must be 32 bytes")
	}

	var ntimeBytes [4]byte
	var nonceBytes [4]byte
	var hdr [80]byte
	var merkleReversed [32]byte

	// Decode ntime
	if len(ntimeHex) != 8 {
		return nil, fmt.Errorf("ntime hex must be 8 chars")
	}
	n, err := hex.Decode(ntimeBytes[:], []byte(ntimeHex))
	if err != nil || n != 4 {
		return nil, fmt.Errorf("decode ntime: %w", err)
	}

	// Decode nonce
	if len(nonceHex) != 8 {
		return nil, fmt.Errorf("nonce hex must be 8 chars")
	}
	n, err = hex.Decode(nonceBytes[:], []byte(nonceHex))
	if err != nil || n != 4 {
		return nil, fmt.Errorf("decode nonce: %w", err)
	}

	// Reverse merkle root in place
	for i := 0; i < 32; i++ {
		merkleReversed[i] = merkleRootBE[31-i]
	}

	// Build header using precomputed prevHashBytes and bitsBytes.
	copy(hdr[0:4], nonceBytes[:])
	copy(hdr[4:8], job.bitsBytes[:])
	copy(hdr[8:12], ntimeBytes[:])
	copy(hdr[12:44], merkleReversed[:])
	copy(hdr[44:76], job.prevHashBytes[:])
	uver := uint32(version)
	hdr[76] = byte(uver >> 24)
	hdr[77] = byte(uver >> 16)
	hdr[78] = byte(uver >> 8)
	hdr[79] = byte(uver)

	for i := 0; i < 40; i++ {
		hdr[i], hdr[79-i] = hdr[79-i], hdr[i]
	}

	return hdr[:], nil
}

// buildBlockHeaderU32 is a faster variant of buildBlockHeader that avoids hex
// decoding by taking already-parsed big-endian ntime/nonce values.
func (job *Job) buildBlockHeaderU32(merkleRootBE []byte, ntime uint32, nonce uint32, version int32) ([]byte, error) {
	if len(merkleRootBE) != 32 {
		return nil, fmt.Errorf("merkle root must be 32 bytes")
	}

	var ntimeBytes [4]byte
	var nonceBytes [4]byte
	var hdr [80]byte
	var merkleReversed [32]byte

	binary.BigEndian.PutUint32(ntimeBytes[:], ntime)
	binary.BigEndian.PutUint32(nonceBytes[:], nonce)

	for i := 0; i < 32; i++ {
		merkleReversed[i] = merkleRootBE[31-i]
	}

	copy(hdr[0:4], nonceBytes[:])
	copy(hdr[4:8], job.bitsBytes[:])
	copy(hdr[8:12], ntimeBytes[:])
	copy(hdr[12:44], merkleReversed[:])
	copy(hdr[44:76], job.prevHashBytes[:])
	uver := uint32(version)
	hdr[76] = byte(uver >> 24)
	hdr[77] = byte(uver >> 16)
	hdr[78] = byte(uver >> 8)
	hdr[79] = byte(uver)

	for i := 0; i < 40; i++ {
		hdr[i], hdr[79-i] = hdr[79-i], hdr[i]
	}

	return hdr[:], nil
}

func buildBlockWithScriptTime(job *Job, extranonce1 []byte, extranonce2 []byte, ntimeHex string, nonceHex string, version int32, payoutScript []byte, scriptTime int64) (string, []byte, []byte, []byte, error) {
	if len(extranonce2) != job.Extranonce2Size {
		return "", nil, nil, nil, fmt.Errorf("extranonce2 must be %d bytes", job.Extranonce2Size)
	}
	if len(payoutScript) == 0 {
		return "", nil, nil, nil, fmt.Errorf("payout script is required")
	}

	coinbaseTx, coinbaseTxid, err := serializeCoinbaseTx(job.Template.Height, extranonce1, extranonce2, job.TemplateExtraNonce2Size, payoutScript, job.CoinbaseValue, job.WitnessCommitment, job.Template.CoinbaseAux.Flags, job.CoinbaseMsg, scriptTime)
	if err != nil {
		return "", nil, nil, nil, fmt.Errorf("coinbase build: %w", err)
	}

	// Build the raw merkle root from the coinbase txid plus the tx list.
	// coinbaseTxid is canonical (big-endian); txids are little-endian.
	merkleRootBE := computeMerkleRootFromBranches(coinbaseTxid, job.MerkleBranches)

	header, err := buildBlockHeaderFromHex(version, job.Template.Previous, merkleRootBE, ntimeHex, job.Template.Bits, nonceHex)
	if err != nil {
		return "", nil, nil, nil, err
	}

	var buf bytes.Buffer

	buf.Write(header)
	writeVarInt(&buf, uint64(1+len(job.Transactions)))
	buf.Write(coinbaseTx)

	for _, tx := range job.Transactions {
		raw, err := hex.DecodeString(tx.Data)
		if err != nil {
			return "", nil, nil, nil, fmt.Errorf("decode tx data: %w", err)
		}
		buf.Write(raw)
	}

	blockHex := hex.EncodeToString(buf.Bytes())
	headerHash := doubleSHA256(header)
	return blockHex, headerHash, header, merkleRootBE, nil
}
