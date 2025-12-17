//go:build !nojsonsimd

package main

import "github.com/bytedance/sonic"

func fastJSONMarshal(v interface{}) ([]byte, error) {
	return sonic.Marshal(v)
}

func fastJSONUnmarshal(data []byte, v interface{}) error {
	return sonic.Unmarshal(data, v)
}
