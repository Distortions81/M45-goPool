package main

type submitParams struct {
	worker           string
	jobID            string
	extranonce2      string
	ntime            string
	nonce            string
	submittedVersion uint32
}

type shareContext struct {
	header     []byte
	cbTx       []byte
	merkleRoot []byte
	hashLE     []byte
	hashHex    string
	shareDiff  float64
	isBlock    bool
}

func uint256BELessOrEqual(a [32]byte, b [32]byte) bool {
	for i := range 32 {
		if a[i] < b[i] {
			return true
		}
		if a[i] > b[i] {
			return false
		}
	}
	return true
}
