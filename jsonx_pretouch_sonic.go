//go:build !nojsonsimd

package main

import (
	"reflect"

	"github.com/bytedance/sonic"
)

func init() {
	// Sonic uses runtime codegen for best performance. Pretouching key types at
	// startup avoids first-hit latency spikes on the hot paths (stratum + rpc).
	//
	// Errors are best-effort; we fall back to normal behavior if pretouch fails.
	_ = sonic.Pretouch(reflect.TypeOf(StratumRequest{}))
	_ = sonic.Pretouch(reflect.TypeOf(StratumResponse{}))
	_ = sonic.Pretouch(reflect.TypeOf(rpcRequest{}))
	_ = sonic.Pretouch(reflect.TypeOf(rpcResponse{}))
	_ = sonic.Pretouch(reflect.TypeOf(rpcError{}))
}
