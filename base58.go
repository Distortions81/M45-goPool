package main

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func encodeBase58Uint64(value uint64) string {
	if value == 0 {
		return string(base58Alphabet[0])
	}
	var buf [16]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = base58Alphabet[value%58]
		value /= 58
	}
	return string(buf[i:])
}
