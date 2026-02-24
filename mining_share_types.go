package main

// shareContext is protocol-neutral share evaluation output used by submit
// handlers after request parsing/validation.
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
