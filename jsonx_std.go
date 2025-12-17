//go:build nojsonsimd

package main

import stdjson "encoding/json"

func fastJSONMarshal(v interface{}) ([]byte, error) {
	return stdjson.Marshal(v)
}

func fastJSONUnmarshal(data []byte, v interface{}) error {
	return stdjson.Unmarshal(data, v)
}
