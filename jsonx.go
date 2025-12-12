package main

import (
	stdjson "encoding/json"

	"github.com/bytedance/sonic"
)

// fastJSONMarshal encodes v as JSON using the Sonic encoder, which is
// optimized for throughput and lower allocations compared to encoding/json.
// Callers should prefer this on hot paths (e.g., stratum and RPC).
func fastJSONMarshal(v interface{}) ([]byte, error) {
	return sonic.Marshal(v)
}

// fastJSONUnmarshal decodes JSON data into v using Sonic. It is a drop-in
// replacement for encoding/json.Unmarshal for typical Go structs.
func fastJSONUnmarshal(data []byte, v interface{}) error {
	return sonic.Unmarshal(data, v)
}

// jsonNumber is kept as an alias to encoding/json.Number so existing code
// that relies on that type (e.g., in miner.go) continues to compile.
type jsonNumber = stdjson.Number
