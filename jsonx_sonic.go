//go:build !nojsonsimd

package main

import "github.com/bytedance/sonic"

var fastJSON = sonic.ConfigDefault

func fastJSONMarshal(v any) ([]byte, error) {
	return fastJSON.Marshal(v)
}

func fastJSONUnmarshal(data []byte, v any) error {
	return fastJSON.Unmarshal(data, v)
}
