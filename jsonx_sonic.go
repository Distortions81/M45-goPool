//go:build !nojsonsimd

package main

import "github.com/bytedance/sonic"

var fastJSON = sonic.ConfigDefault

func fastJSONMarshal(v interface{}) ([]byte, error) {
	return fastJSON.Marshal(v)
}

func fastJSONUnmarshal(data []byte, v interface{}) error {
	return fastJSON.Unmarshal(data, v)
}
