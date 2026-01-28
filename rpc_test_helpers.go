package main

import "context"

func (c *RPCClient) call(method string, params interface{}, out interface{}) error {
	return c.callCtx(context.Background(), method, params, out)
}

func (c *RPCClient) callLongPoll(method string, params interface{}, out interface{}) error {
	return c.callLongPollCtx(context.Background(), method, params, out)
}
