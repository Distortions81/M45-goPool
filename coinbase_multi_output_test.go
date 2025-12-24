package main

import "testing"

func TestSerializeCoinbaseTxPayoutsPredecoded_ManyOutputs(t *testing.T) {
	height := int64(1)
	ex1 := []byte{0x01, 0x02, 0x03, 0x04}
	ex2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	templateExtra := 8

	payouts := []coinbasePayoutOutput{
		{Script: []byte{0x51}, Value: 1},
		{Script: []byte{0x52}, Value: 2},
		{Script: []byte{0x53}, Value: 3},
		{Script: []byte{0x54}, Value: 4},
	}

	raw, _, err := serializeCoinbaseTxPayoutsPredecoded(height, ex1, ex2, templateExtra, payouts, nil, nil, "test", 0)
	if err != nil {
		t.Fatalf("serializeCoinbaseTxPayoutsPredecoded error: %v", err)
	}

	// Parse: version(4) || vin_count(varint) || prevout(36) || script_len(varint) || script || seq(4) || vout_count(varint)
	i := 4
	vinCount, n, err := readVarInt(raw[i:])
	if err != nil {
		t.Fatalf("read vinCount: %v", err)
	}
	if vinCount != 1 {
		t.Fatalf("vinCount=%d, want 1", vinCount)
	}
	i += n
	i += 32 + 4
	scriptLen, n, err := readVarInt(raw[i:])
	if err != nil {
		t.Fatalf("read scriptLen: %v", err)
	}
	i += n + int(scriptLen)
	i += 4
	voutCount, _, err := readVarInt(raw[i:])
	if err != nil {
		t.Fatalf("read voutCount: %v", err)
	}
	if voutCount != uint64(len(payouts)) {
		t.Fatalf("voutCount=%d, want %d", voutCount, len(payouts))
	}
}

func TestSerializeCoinbaseTxPayoutsPredecoded_CommitmentPlusPayouts(t *testing.T) {
	height := int64(1)
	ex1 := []byte{0x01, 0x02, 0x03, 0x04}
	ex2 := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	templateExtra := 8

	payouts := []coinbasePayoutOutput{
		{Script: []byte{0x51}, Value: 1},
		{Script: []byte{0x52}, Value: 2},
	}
	commitmentScript := []byte{0x6a, 0x01, 0x00} // minimal OP_RETURN-ish placeholder

	raw, _, err := serializeCoinbaseTxPayoutsPredecoded(height, ex1, ex2, templateExtra, payouts, commitmentScript, nil, "test", 0)
	if err != nil {
		t.Fatalf("serializeCoinbaseTxPayoutsPredecoded error: %v", err)
	}

	i := 4
	_, n, err := readVarInt(raw[i:])
	if err != nil {
		t.Fatalf("read vinCount: %v", err)
	}
	i += n
	i += 32 + 4
	scriptLen, n, err := readVarInt(raw[i:])
	if err != nil {
		t.Fatalf("read scriptLen: %v", err)
	}
	i += n + int(scriptLen)
	i += 4
	voutCount, _, err := readVarInt(raw[i:])
	if err != nil {
		t.Fatalf("read voutCount: %v", err)
	}
	if voutCount != uint64(len(payouts)+1) {
		t.Fatalf("voutCount=%d, want %d", voutCount, len(payouts)+1)
	}
}
