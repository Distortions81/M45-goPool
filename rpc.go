package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	rpcRetryDelay = 100 * time.Millisecond
)

type rpcRequest struct {
	Jsonrpc string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
	ID     int             `json:"id"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

type httpStatusError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *httpStatusError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("rpc http status %s: %s", e.Status, e.Body)
	}
	return fmt.Sprintf("rpc http status %s", e.Status)
}

type RPCClient struct {
	url     string
	user    string
	pass    string
	client  *http.Client
	lp      *http.Client
	idMu    sync.Mutex
	nextID  int
	metrics *PoolMetrics
	connected atomic.Bool
	unhealthy  atomic.Bool

	authMu        sync.RWMutex
	cookiePath    string
	cookieModTime time.Time
	cookieSize    int64

	lastErrMu sync.RWMutex
	lastErr   error

	hookMu     sync.RWMutex
	resultHook func(method string, params interface{}, raw json.RawMessage)
}

func NewRPCClient(cfg Config, metrics *PoolMetrics) *RPCClient {
	// Use a shared Transport so RPC calls reuse connections and avoid
	// per-request TCP/TLS handshakes. This improves latency consistency
	// and reduces overhead under load.
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   60 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		IdleConnTimeout: 60 * time.Second,
		// Bitcoind RPC doesn't use Expect: 100-continue, but keep a small
		// timeout so misbehaving proxies can't stall us indefinitely.
		ExpectContinueTimeout: 1 * time.Second,
	}

	c := &RPCClient{
		url:     cfg.RPCURL,
		user:    cfg.RPCUser,
		pass:    cfg.RPCPass,
		metrics: metrics,
		client: &http.Client{
			Timeout:   60 * time.Second,
			Transport: transport,
		},
		lp: &http.Client{
			Timeout:   0, // longpoll waits for bitcoind to respond on new blocks
			Transport: transport,
		},
		nextID:     1,
		cookiePath: strings.TrimSpace(cfg.RPCCookiePath),
	}
	c.initCookieStat()
	return c
}

func (c *RPCClient) initCookieStat() {
	if c.cookiePath == "" {
		return
	}
	info, err := os.Stat(c.cookiePath)
	if err != nil {
		return
	}
	c.authMu.Lock()
	c.cookieModTime = info.ModTime()
	c.cookieSize = info.Size()
	c.authMu.Unlock()
}

func (c *RPCClient) reloadCookieIfChanged() {
	if c.cookiePath == "" {
		return
	}
	info, err := os.Stat(c.cookiePath)
	if err != nil {
		return
	}
	c.authMu.RLock()
	modTime := c.cookieModTime
	size := c.cookieSize
	c.authMu.RUnlock()
	if info.ModTime().Equal(modTime) && info.Size() == size {
		return
	}
	user, pass, err := readRPCCookie(c.cookiePath)
	if err != nil {
		logger.Warn("reload rpc cookie", "path", c.cookiePath, "error", err)
		return
	}
	c.authMu.Lock()
	c.user = strings.TrimSpace(user)
	c.pass = strings.TrimSpace(pass)
	c.cookieModTime = info.ModTime()
	c.cookieSize = info.Size()
	c.authMu.Unlock()
	logger.Info("rpc cookie reloaded", "path", c.cookiePath)
}

// SetResultHook registers a callback that is invoked after every successful
// RPC call, with the method name, request params, and raw JSON result.
// It is safe to call on a running client; subsequent calls replace the hook.
func (c *RPCClient) SetResultHook(hook func(method string, params interface{}, raw json.RawMessage)) {
	c.hookMu.Lock()
	c.resultHook = hook
	c.hookMu.Unlock()
}

func (c *RPCClient) call(method string, params interface{}, out interface{}) error {
	return c.callCtx(context.Background(), method, params, out)
}

func (c *RPCClient) callLongPoll(method string, params interface{}, out interface{}) error {
	return c.callLongPollCtx(context.Background(), method, params, out)
}

func (c *RPCClient) callCtx(ctx context.Context, method string, params interface{}, out interface{}) error {
	return c.callWithClientCtx(ctx, c.client, method, params, out)
}

func (c *RPCClient) callLongPollCtx(ctx context.Context, method string, params interface{}, out interface{}) error {
	return c.callWithClientCtx(ctx, c.lp, method, params, out)
}

func (c *RPCClient) callWithClientCtx(ctx context.Context, client *http.Client, method string, params interface{}, out interface{}) error {
	for {
		if ctx.Err() != nil {
			c.recordLastError(ctx.Err())
			return ctx.Err()
		}
		err := c.performCall(ctx, client, method, params, out)
		if err == nil {
			if c.unhealthy.Swap(false) && c.metrics != nil {
				verb := "reconnected"
				if !c.connected.Load() {
					verb = "connected"
				}
				c.metrics.RecordErrorEvent("rpc", verb+" to "+c.endpointLabel(), time.Now())
			}
			c.connected.Store(true)
			c.recordRPCCallSuccess()
			return nil
		}
		c.recordLastError(err)
		if c.metrics != nil {
			c.metrics.RecordRPCError(err)
		}
		if isRPCConnectivityError(err) {
			c.unhealthy.Store(true)
		}
		if c.shouldRetry(err) {
			c.reloadCookieIfChanged()
			if err := sleepContext(ctx, rpcRetryDelay); err != nil {
				return err
			}
			continue
		}
		return err
	}
}

func (c *RPCClient) endpointLabel() string {
	raw := strings.TrimSpace(c.url)
	if raw == "" {
		return "(unknown)"
	}
	u, err := url.Parse(raw)
	if err == nil && u.Host != "" {
		return u.Host
	}
	// Best-effort fallback for non-URL inputs; never include user/pass.
	if idx := strings.Index(raw, "@"); idx != -1 && idx+1 < len(raw) {
		raw = raw[idx+1:]
	}
	raw = strings.TrimLeft(raw, "/")
	if raw == "" {
		return "(unknown)"
	}
	return raw
}

func (c *RPCClient) EndpointLabel() string {
	return c.endpointLabel()
}

func isRPCConnectivityError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode >= 500
	}
	return false
}

func (c *RPCClient) performCall(ctx context.Context, client *http.Client, method string, params interface{}, out interface{}) error {
	c.idMu.Lock()
	id := c.nextID
	c.nextID++
	c.idMu.Unlock()

	reqObj := rpcRequest{
		Jsonrpc: "1.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	body, err := fastJSONMarshal(reqObj)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	c.authMu.RLock()
	user, pass := c.user, c.pass
	c.authMu.RUnlock()
	if user != "" || pass != "" {
		req.SetBasicAuth(user, pass)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := client.Do(req)
	if c.metrics != nil {
		c.metrics.ObserveRPCLatency(method, client == c.lp, time.Since(start))
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		// Some daemons include a useful JSON-RPC error even when returning a non-200 status.
		// Surface the RPC error (e.g. -32601 method not found) instead of losing it behind the HTTP status.
		var rpcResp rpcResponse
		if err := fastJSONUnmarshal(data, &rpcResp); err == nil && rpcResp.Error != nil {
			return rpcResp.Error
		}
		errBody := string(bytes.TrimSpace(data))
		return &httpStatusError{StatusCode: resp.StatusCode, Status: resp.Status, Body: errBody}
	}

	if len(data) == 0 {
		return fmt.Errorf("rpc empty response body")
	}

	var rpcResp rpcResponse
	if err := fastJSONUnmarshal(data, &rpcResp); err != nil {
		return fmt.Errorf("decode rpc response: %w", err)
	}
	if rpcResp.Error != nil {
		return rpcResp.Error
	}

	// Publish the raw result to any registered hook so other components
	// can opportunistically warm caches.
	c.hookMu.RLock()
	hook := c.resultHook
	c.hookMu.RUnlock()
	if hook != nil {
		hook(method, params, rpcResp.Result)
	}

	if out == nil {
		return nil
	}
	return fastJSONUnmarshal(rpcResp.Result, out)
}

func (c *RPCClient) recordRPCCallSuccess() {
	c.lastErrMu.Lock()
	c.lastErr = nil
	c.lastErrMu.Unlock()
}

func (c *RPCClient) shouldRetry(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case http.StatusUnauthorized:
			return c.cookiePath != ""
		default:
			return statusErr.StatusCode >= 500
		}
	}
	return false
}

func (c *RPCClient) recordLastError(err error) {
	if err == nil {
		return
	}
	c.lastErrMu.Lock()
	c.lastErr = err
	c.lastErrMu.Unlock()
}

func (c *RPCClient) LastError() error {
	c.lastErrMu.RLock()
	defer c.lastErrMu.RUnlock()
	return c.lastErr
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// BlockHeader represents a Bitcoin block header from getblockheader RPC
type BlockHeader struct {
	Hash              string  `json:"hash"`
	Confirmations     int64   `json:"confirmations"`
	Height            int64   `json:"height"`
	Version           int32   `json:"version"`
	VersionHex        string  `json:"versionHex"`
	MerkleRoot        string  `json:"merkleroot"`
	Time              int64   `json:"time"`
	MedianTime        int64   `json:"mediantime"`
	Nonce             uint32  `json:"nonce"`
	Bits              string  `json:"bits"`
	Difficulty        float64 `json:"difficulty"`
	ChainWork         string  `json:"chainwork"`
	NTx               int     `json:"nTx"`
	PreviousBlockHash string  `json:"previousblockhash"`
	NextBlockHash     string  `json:"nextblockhash"`
}

// GetBestBlockHash returns the hash of the best (tip) block in the longest blockchain
func (c *RPCClient) GetBestBlockHash(ctx context.Context) (string, error) {
	var hash string
	err := c.callCtx(ctx, "getbestblockhash", nil, &hash)
	return hash, err
}

// GetBlockHash returns the hash of the block at the given height
func (c *RPCClient) GetBlockHash(ctx context.Context, height int64) (string, error) {
	var hash string
	err := c.callCtx(ctx, "getblockhash", []interface{}{height}, &hash)
	return hash, err
}

// GetBlockHeader returns the block header for the given block hash
func (c *RPCClient) GetBlockHeader(ctx context.Context, hash string) (*BlockHeader, error) {
	var header BlockHeader
	// verbose=true to get JSON instead of hex
	err := c.callCtx(ctx, "getblockheader", []interface{}{hash, true}, &header)
	if err != nil {
		return nil, err
	}
	return &header, nil
}

// Fetch the scriptPubKey for the payout address using local address
// validation instead of relying on bitcoind wallet RPCs. This avoids extra
// RPC calls and does not require the node's wallet to know about the
// address. Accepts base58 and bech32/bech32m destinations.
func fetchPayoutScript(_ *RPCClient, addr string) ([]byte, error) {
	if addr == "" {
		return nil, fmt.Errorf("PAYOUT_ADDRESS env var is required for coinbase outputs")
	}

	params := ChainParams()
	script, err := scriptForAddress(addr, params)
	if err != nil {
		return nil, fmt.Errorf("invalid payout address %s: %w", addr, err)
	}
	return script, nil
}
