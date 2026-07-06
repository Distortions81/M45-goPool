package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type stratumMessage struct {
	ID     json.RawMessage   `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

type client struct {
	index      int
	conn       net.Conn
	worker     string
	jobID      string
	initial    chan struct{}
	subscribed chan struct{}
	authorized chan struct{}

	mu         sync.Mutex
	done       chan struct{}
	notifyNano atomic.Int64
}

func newClient(index int, worker string) *client {
	return &client{
		index:      index,
		worker:     worker,
		initial:    make(chan struct{}),
		subscribed: make(chan struct{}),
		authorized: make(chan struct{}),
		done:       make(chan struct{}),
	}
}

func frame(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}

func (c *client) connect(ctx context.Context, host string, port int, ordered bool) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return err
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
	c.conn = conn
	go c.readLoop()

	if _, err := conn.Write(frame(map[string]any{"id": 1, "method": "mining.subscribe", "params": []any{"gofanout/1.0"}})); err != nil {
		return err
	}
	if ordered {
		if err := waitChan(ctx, c.subscribed); err != nil {
			return err
		}
	}
	if _, err := conn.Write(frame(map[string]any{"id": 2, "method": "mining.authorize", "params": []any{c.worker, "x"}})); err != nil {
		return err
	}
	if err := waitChan(ctx, c.authorized); err != nil {
		return err
	}
	return waitChan(ctx, c.initial)
}

func (c *client) readLoop() {
	reader := bufio.NewReaderSize(c.conn, 4096)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var msg stratumMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		switch string(msg.ID) {
		case "1":
			select {
			case <-c.subscribed:
			default:
				close(c.subscribed)
			}
		case "2":
			select {
			case <-c.authorized:
			default:
				close(c.authorized)
			}
		}
		if msg.Method != "mining.notify" || len(msg.Params) == 0 {
			continue
		}
		var jobID string
		if err := json.Unmarshal(msg.Params[0], &jobID); err != nil {
			continue
		}
		c.mu.Lock()
		if c.jobID == "" {
			c.jobID = jobID
			c.mu.Unlock()
			select {
			case <-c.initial:
			default:
				close(c.initial)
			}
			continue
		}
		if jobID != c.jobID {
			c.jobID = jobID
			if c.notifyNano.Load() == 0 {
				c.notifyNano.Store(time.Now().UnixNano())
				close(c.done)
			}
		}
		c.mu.Unlock()
	}
}

func (c *client) resetRound() {
	c.mu.Lock()
	c.notifyNano.Store(0)
	c.done = make(chan struct{})
	c.mu.Unlock()
}

func (c *client) close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

func waitChan(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func rpcCall(ctx context.Context, url, user, pass, method string, params []any) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "1.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("rpc %s returned %s: %s", method, resp.Status, strings.TrimSpace(string(data)))
	}
	var parsed struct {
		Result json.RawMessage `json:"result"`
		Error  any             `json:"error"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("rpc %s error: %v", method, parsed.Error)
	}
	return parsed.Result, nil
}

func pct(values []float64, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	idx := int(float64(len(values)-1) * percentile / 100.0)
	return values[idx]
}

func workerName(address string, index int, suffix bool) string {
	if !suffix {
		return address
	}
	return fmt.Sprintf("%s.w%d", address, index)
}

func openClients(ctx context.Context, host string, port int, address string, connections, batch int, suffix bool, ordered bool) []*client {
	clients := make([]*client, 0, connections)
	for start := 0; start < connections; start += batch {
		end := start + batch
		if end > connections {
			end = connections
		}
		results := make(chan *client, end-start)
		var wg sync.WaitGroup
		for i := start; i < end; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				c := newClient(index, workerName(address, index, suffix))
				if err := c.connect(ctx, host, port, ordered); err == nil {
					results <- c
				} else {
					c.close()
				}
			}(i)
		}
		wg.Wait()
		close(results)
		for c := range results {
			clients = append(clients, c)
		}
		fmt.Printf("ESTABLISHED %d/%d\n", len(clients), connections)
	}
	return clients
}

func main() {
	host := flag.String("host", "", "Stratum host")
	port := flag.Int("port", 3333, "Stratum port")
	address := flag.String("address", "", "worker address")
	rpcURL := flag.String("rpc", "", "bitcoind RPC URL")
	rpcUser := flag.String("rpc-user", "openbench", "RPC username")
	rpcPass := flag.String("rpc-pass", "openbenchpass", "RPC password")
	connections := flag.Int("connections", 10000, "client connections")
	rounds := flag.Int("rounds", 5, "measurement rounds")
	batch := flag.Int("batch", 500, "connect batch size")
	timeout := flag.Duration("timeout", 15*time.Second, "round timeout")
	workerSuffix := flag.Bool("worker-suffix", true, "append .wN to the authorize username")
	orderedHandshake := flag.Bool("ordered-handshake", false, "wait for subscribe response before authorize")
	flag.Parse()

	if *host == "" || *address == "" || *rpcURL == "" {
		fmt.Fprintln(os.Stderr, "--host, --address, and --rpc are required")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var genAddr string
	raw, err := rpcCall(ctx, *rpcURL, *rpcUser, *rpcPass, "getnewaddress", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.Unmarshal(raw, &genAddr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	clients := openClients(ctx, *host, *port, *address, *connections, *batch, *workerSuffix, *orderedHandshake)
	defer func() {
		for _, c := range clients {
			c.close()
		}
	}()
	if len(clients) != *connections {
		fmt.Printf("RESULT conns=%d established=%d incomplete\n", *connections, len(clients))
		os.Exit(2)
	}

	type summary struct {
		round    int
		received int
		avg      float64
		p50      float64
		p95      float64
		p99      float64
		max      float64
	}
	summaries := make([]summary, 0, *rounds)
	for round := 1; round <= *rounds; round++ {
		for _, c := range clients {
			c.resetRound()
		}
		time.Sleep(400 * time.Millisecond)
		t0 := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := rpcCall(ctx, *rpcURL, *rpcUser, *rpcPass, "generatetoaddress", []any{1, genAddr})
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		deadline := time.Now().Add(*timeout)
		for time.Now().Before(deadline) {
			received := 0
			for _, c := range clients {
				if c.notifyNano.Load() > 0 {
					received++
				}
			}
			if received == len(clients) {
				break
			}
			time.Sleep(100 * time.Microsecond)
		}

		lats := make([]float64, 0, len(clients))
		for _, c := range clients {
			if ns := c.notifyNano.Load(); ns > 0 {
				lats = append(lats, float64(time.Unix(0, ns).Sub(t0).Nanoseconds())/1e6)
			}
		}
		if len(lats) == 0 {
			fmt.Printf("ROUND %d received=0/%d\n", round, *connections)
			continue
		}
		sort.Float64s(lats)
		var sum float64
		for _, lat := range lats {
			sum += lat
		}
		s := summary{
			round:    round,
			received: len(lats),
			avg:      sum / float64(len(lats)),
			p50:      lats[len(lats)/2],
			p95:      pct(lats, 95),
			p99:      pct(lats, 99),
			max:      lats[len(lats)-1],
		}
		summaries = append(summaries, s)
		fmt.Printf("ROUND %d received=%d/%d avg=%.1f p50=%.1f p95=%.1f p99=%.1f max=%.1f ms\n",
			s.round, s.received, *connections, s.avg, s.p50, s.p95, s.p99, s.max)
	}

	var best summary
	found := false
	for _, s := range summaries {
		if s.received != *connections {
			continue
		}
		if !found || s.max < best.max {
			best = s
			found = true
		}
	}
	if !found {
		for _, s := range summaries {
			if !found || s.max < best.max {
				best = s
				found = true
			}
		}
	}
	if !found {
		fmt.Printf("RESULT conns=%d established=%d no-notifies\n", *connections, len(clients))
		os.Exit(3)
	}
	fmt.Printf("RESULT conns=%d established=%d rounds=%d best_round=%d received=%d avg=%.1f p50=%.1f p95=%.1f p99=%.1f max=%.1f ms\n",
		*connections, len(clients), *rounds, best.round, best.received, best.avg, best.p50, best.p95, best.p99, best.max)
}
