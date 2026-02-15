package main

import "context"

func (c *RPCClient) call(method string, params any, out any) error {
	return c.callCtx(context.Background(), method, params, out)
}

func (c *RPCClient) callLongPoll(method string, params any, out any) error {
	return c.callLongPollCtx(context.Background(), method, params, out)
}
