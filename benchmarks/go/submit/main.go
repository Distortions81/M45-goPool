package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/big"
	"net"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type message struct {
	ID     json.RawMessage   `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
	Result json.RawMessage   `json:"result"`
	Error  json.RawMessage   `json:"error"`
}

type job struct {
	id              string
	ntime           string
	extranonce2     string
	version         uint32
	nbits           uint32
	netTarget       *big.Int
	headerPrefix    []byte
	nonceCounter    uint32
	submitPrefixFmt string
}

type pendingSubmit struct {
	t0 time.Time
}

type client struct {
	index      int
	worker     string
	conn       net.Conn
	writerMu   sync.Mutex
	subscribed chan struct{}
	authorized chan struct{}
	hasJob     chan struct{}
	authOK     atomic.Bool

	extranonce1     string
	extranonce2Size int
	currentJob      atomic.Pointer[job]

	nextID   atomic.Int64
	pending  sync.Map
	inflight chan struct{}

	measuring atomic.Bool
	submits   atomic.Int64
	accepts   atomic.Int64
	rejects   atomic.Int64
	errors    atomic.Int64

	latMu sync.Mutex
	lats  []float64
}

func frame(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}

func doubleSHA(b []byte) []byte {
	first := sha256.Sum256(b)
	second := sha256.Sum256(first[:])
	return second[:]
}

func prevhashHeaderBytes(prevhash string) ([]byte, error) {
	raw, err := hexBytes(prevhash)
	if err != nil {
		return nil, err
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("prevhash length %d", len(raw))
	}
	out := make([]byte, 0, 32)
	for i := 0; i < 32; i += 4 {
		out = append(out, raw[i+3], raw[i+2], raw[i+1], raw[i])
	}
	return out, nil
}

func hexBytes(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("odd hex length")
	}
	out := make([]byte, len(s)/2)
	for i := range out {
		v, err := strconv.ParseUint(s[i*2:i*2+2], 16, 8)
		if err != nil {
			return nil, err
		}
		out[i] = byte(v)
	}
	return out, nil
}

func putLE32(dst []byte, v uint32) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v >> 16)
	dst[3] = byte(v >> 24)
}

func bitsToTarget(nbitsHex string) (*big.Int, uint32, error) {
	bits64, err := strconv.ParseUint(nbitsHex, 16, 32)
	if err != nil {
		return nil, 0, err
	}
	bits := uint32(bits64)
	exp := bits >> 24
	mant := bits & 0x00ffffff
	target := new(big.Int).SetUint64(uint64(mant))
	if exp > 3 {
		target.Lsh(target, uint(8*(exp-3)))
	} else {
		target.Rsh(target, uint(8*(3-exp)))
	}
	return target, bits, nil
}

func uint32HexLEField(v uint32) string {
	return fmt.Sprintf("%08x", v)
}

func makeClient(index int, worker string, pipeline int) *client {
	return &client{
		index:           index,
		worker:          worker,
		subscribed:      make(chan struct{}),
		authorized:      make(chan struct{}),
		hasJob:          make(chan struct{}),
		extranonce2Size: 8,
		nextID:          atomic.Int64{},
		inflight:        make(chan struct{}, pipeline),
	}
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
	c.nextID.Store(10)
	go c.readLoop()

	if err := c.write(frame(map[string]any{"id": 1, "method": "mining.subscribe", "params": []any{"gosubmit/1.0"}})); err != nil {
		return err
	}
	if ordered {
		if err := wait(ctx, c.subscribed); err != nil {
			return err
		}
	}
	if err := c.write(frame(map[string]any{"id": 2, "method": "mining.authorize", "params": []any{c.worker, "x"}})); err != nil {
		return err
	}
	if err := wait(ctx, c.subscribed); err != nil {
		return err
	}
	if err := wait(ctx, c.authorized); err != nil {
		return err
	}
	if !c.authOK.Load() {
		return fmt.Errorf("authorize rejected")
	}
	return wait(ctx, c.hasJob)
}

func (c *client) write(b []byte) error {
	c.writerMu.Lock()
	defer c.writerMu.Unlock()
	_, err := c.conn.Write(b)
	return err
}

func wait(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *client) readLoop() {
	reader := bufio.NewReaderSize(c.conn, 8192)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		recv := time.Now()
		var msg message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Method == "mining.notify" {
			c.prepareJob(msg.Params)
			continue
		}
		if len(msg.ID) == 0 || string(msg.ID) == "null" {
			continue
		}
		id, _ := strconv.ParseInt(string(msg.ID), 10, 64)
		switch id {
		case 1:
			var result []json.RawMessage
			if json.Unmarshal(msg.Result, &result) == nil && len(result) >= 3 {
				_ = json.Unmarshal(result[1], &c.extranonce1)
				var size int
				if json.Unmarshal(result[2], &size) == nil {
					c.extranonce2Size = size
				}
			}
			closeOnce(c.subscribed)
		case 2:
			var ok bool
			_ = json.Unmarshal(msg.Result, &ok)
			c.authOK.Store(ok)
			closeOnce(c.authorized)
		default:
			if value, ok := c.pending.LoadAndDelete(id); ok {
				<-c.inflight
				if c.measuring.Load() {
					p := value.(pendingSubmit)
					c.submits.Add(1)
					var ok bool
					_ = json.Unmarshal(msg.Result, &ok)
					if ok {
						c.accepts.Add(1)
					} else {
						c.rejects.Add(1)
					}
					lat := float64(recv.Sub(p.t0).Nanoseconds()) / 1e6
					c.latMu.Lock()
					c.lats = append(c.lats, lat)
					c.latMu.Unlock()
				}
			}
		}
	}
}

func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func (c *client) prepareJob(params []json.RawMessage) {
	if len(params) < 8 || c.extranonce1 == "" {
		return
	}
	var fields []any
	if err := json.Unmarshal(mustMarshal(params), &fields); err != nil {
		return
	}
	jobID, _ := fields[0].(string)
	prevhash, _ := fields[1].(string)
	coinbase1, _ := fields[2].(string)
	coinbase2, _ := fields[3].(string)
	versionHex, _ := fields[5].(string)
	nbitsHex, _ := fields[6].(string)
	ntime, _ := fields[7].(string)
	branchesAny, _ := fields[4].([]any)

	extranonce2 := ""
	for i := 0; i < c.extranonce2Size; i++ {
		extranonce2 += "00"
	}
	coinbase, err := hexBytes(coinbase1 + c.extranonce1 + extranonce2 + coinbase2)
	if err != nil {
		return
	}
	root := doubleSHA(coinbase)
	for _, branchAny := range branchesAny {
		branchHex, _ := branchAny.(string)
		branch, err := hexBytes(branchHex)
		if err != nil {
			return
		}
		combined := append(append([]byte{}, root...), branch...)
		root = doubleSHA(combined)
	}
	version64, err := strconv.ParseUint(versionHex, 16, 32)
	if err != nil {
		return
	}
	nbitsTarget, nbits, err := bitsToTarget(nbitsHex)
	if err != nil {
		return
	}
	ntime64, err := strconv.ParseUint(ntime, 16, 32)
	if err != nil {
		return
	}
	prev, err := prevhashHeaderBytes(prevhash)
	if err != nil {
		return
	}
	prefix := make([]byte, 0, 76)
	tmp := make([]byte, 4)
	putLE32(tmp, uint32(version64))
	prefix = append(prefix, tmp...)
	prefix = append(prefix, prev...)
	prefix = append(prefix, root...)
	putLE32(tmp, uint32(ntime64))
	prefix = append(prefix, tmp...)
	putLE32(tmp, nbits)
	prefix = append(prefix, tmp...)

	j := &job{
		id:              jobID,
		ntime:           ntime,
		extranonce2:     extranonce2,
		version:         uint32(version64),
		nbits:           nbits,
		netTarget:       nbitsTarget,
		headerPrefix:    prefix,
		submitPrefixFmt: fmt.Sprintf(`,"method":"mining.submit","params":[%q,%q,%q,%q,"`, c.worker, jobID, extranonce2, ntime),
	}
	c.currentJob.Store(j)
	closeOnce(c.hasJob)
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (j *job) nextAboveTargetNonce() uint32 {
	header := make([]byte, 80)
	copy(header, j.headerPrefix)
	for {
		n := atomic.AddUint32(&j.nonceCounter, 1) - 1
		putLE32(header[76:], n)
		first := sha256.Sum256(header)
		second := sha256.Sum256(first[:])
		hashInt := new(big.Int).SetBytes(reverse32(second[:]))
		if hashInt.Cmp(j.netTarget) > 0 {
			return n
		}
	}
}

func reverse32(in []byte) []byte {
	out := make([]byte, len(in))
	for i := range in {
		out[len(in)-1-i] = in[i]
	}
	return out
}

func (c *client) flood(ctx context.Context, start <-chan struct{}) {
	<-start
	for ctx.Err() == nil {
		j := c.currentJob.Load()
		if j == nil {
			time.Sleep(time.Millisecond)
			continue
		}
		select {
		case c.inflight <- struct{}{}:
		case <-ctx.Done():
			return
		}
		nonce := j.nextAboveTargetNonce()
		id := c.nextID.Add(1)
		c.pending.Store(id, pendingSubmit{t0: time.Now()})
		line := fmt.Sprintf(`{"id":%d%s%08x"]}`+"\n", id, j.submitPrefixFmt, nonce)
		if err := c.write([]byte(line)); err != nil {
			c.errors.Add(1)
			return
		}
	}
}

func (c *client) reset() {
	c.measuring.Store(true)
	c.submits.Store(0)
	c.accepts.Store(0)
	c.rejects.Store(0)
	c.errors.Store(0)
	c.latMu.Lock()
	c.lats = nil
	c.latMu.Unlock()
}

func (c *client) snapshot() (submits, accepts, rejects, errors int64, lats []float64) {
	c.latMu.Lock()
	lats = append([]float64(nil), c.lats...)
	c.latMu.Unlock()
	return c.submits.Load(), c.accepts.Load(), c.rejects.Load(), c.errors.Load(), lats
}

func workerName(address string, index int, suffix bool) string {
	if !suffix {
		return address
	}
	return fmt.Sprintf("%s.w%d", address, index)
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	k := float64(len(sorted)-1) * p
	lo := int(k)
	hi := lo + 1
	if hi >= len(sorted) {
		hi = len(sorted) - 1
	}
	return sorted[lo] + (sorted[hi]-sorted[lo])*(k-float64(lo))
}

func main() {
	host := flag.String("host", "", "Stratum host")
	port := flag.Int("port", 3333, "Stratum port")
	address := flag.String("address", "", "worker address")
	connections := flag.Int("connections", 50, "connections")
	pipeline := flag.Int("pipeline", 16, "in-flight submits per connection")
	warmup := flag.Duration("warmup", 3*time.Second, "warmup duration")
	duration := flag.Duration("duration", 20*time.Second, "measurement duration")
	batch := flag.Int("batch", 500, "connect batch size")
	workerSuffix := flag.Bool("worker-suffix", true, "append .wN to authorize username")
	ordered := flag.Bool("ordered-handshake", false, "wait for subscribe response before authorize")
	flag.Parse()

	if *host == "" || *address == "" {
		fmt.Fprintln(os.Stderr, "--host and --address are required")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	clients := make([]*client, 0, *connections)
	for start := 0; start < *connections; start += *batch {
		end := start + *batch
		if end > *connections {
			end = *connections
		}
		var wg sync.WaitGroup
		ch := make(chan *client, end-start)
		for i := start; i < end; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				c := makeClient(index, workerName(*address, index, *workerSuffix), *pipeline)
				if err := c.connect(ctx, *host, *port, *ordered); err == nil {
					ch <- c
				} else {
					if c.conn != nil {
						_ = c.conn.Close()
					}
				}
			}(i)
		}
		wg.Wait()
		close(ch)
		for c := range ch {
			clients = append(clients, c)
		}
		fmt.Printf("ESTABLISHED %d/%d\n", len(clients), *connections)
	}
	if len(clients) != *connections {
		fmt.Fprintf(os.Stderr, "only established %d/%d\n", len(clients), *connections)
		os.Exit(2)
	}

	runCtx, stop := context.WithCancel(context.Background())
	startCh := make(chan struct{})
	for _, c := range clients {
		go c.flood(runCtx, startCh)
	}
	close(startCh)
	time.Sleep(*warmup)
	for _, c := range clients {
		c.reset()
	}
	t0 := time.Now()
	time.Sleep(*duration)
	elapsed := time.Since(t0).Seconds()
	for _, c := range clients {
		c.measuring.Store(false)
	}
	stop()
	time.Sleep(300 * time.Millisecond)

	var submits, accepts, rejects, errors int64
	var lats []float64
	for _, c := range clients {
		s, a, r, e, clats := c.snapshot()
		submits += s
		accepts += a
		rejects += r
		errors += e
		lats = append(lats, clats...)
		_ = c.conn.Close()
	}
	sort.Float64s(lats)
	result := map[string]any{
		"connections":       *connections,
		"pipeline":          *pipeline,
		"duration_s":        math.Round(elapsed*1000) / 1000,
		"submits":           submits,
		"accepts":           accepts,
		"rejects":           rejects,
		"errors":            errors,
		"validated_per_sec": math.Round((float64(submits)/elapsed)*10) / 10,
		"latency_ms": map[string]float64{
			"p50": pct(lats, 0.50),
			"p95": pct(lats, 0.95),
			"p99": pct(lats, 0.99),
			"max": func() float64 {
				if len(lats) == 0 {
					return 0
				}
				return lats[len(lats)-1]
			}(),
		},
	}
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(result)
}
