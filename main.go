package main

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	debugpkg "runtime/debug"
	pprof "runtime/pprof"
	"strings"
	"sync"
	"syscall"
	"time"
)

// poolSoftwareName is the name of the pool software used throughout the codebase
// for branding, coinbase messages, and certificates.
const poolSoftwareName = "goPool"

// duplicateShareHistory controls how many recent submissions per job are
// tracked for duplicate-share detection. It is implemented as a fixed-size
// ring so memory usage is bounded and independent of share rate.
const duplicateShareHistory = 100

// workerPageCacheLimit bounds the number of cached worker_status pages kept
// in memory. Entries also expire after overviewRefreshInterval.
const workerPageCacheLimit = 100

// acceptRateLimiter is a simple token-bucket limiter for new TCP accepts. It
// enforces an average rate (tokens added per second) with a small burst
// capacity so short spikes are allowed without overwhelming the node.
type acceptRateLimiter struct {
	rate   float64   // tokens per second
	burst  float64   // maximum tokens
	tokens float64   // current tokens
	last   time.Time // last refill time
	mu     sync.Mutex
}

// reconnectTracker tracks per-IP connection attempts over a sliding window
// and temporarily bans addresses that reconnect too aggressively.
type reconnectTracker struct {
	mu          sync.Mutex
	entries     map[string]*reconnectEntry
	threshold   int
	window      time.Duration
	banDuration time.Duration
}

type reconnectEntry struct {
	count       int
	reset       time.Time
	bannedUntil time.Time
}

func newReconnectTracker(threshold int, window, banDuration time.Duration) *reconnectTracker {
	if threshold <= 0 || window <= 0 || banDuration <= 0 {
		return nil
	}
	return &reconnectTracker{
		entries:     make(map[string]*reconnectEntry),
		threshold:   threshold,
		window:      window,
		banDuration: banDuration,
	}
}

func (rt *reconnectTracker) allow(host string, now time.Time) bool {
	if rt == nil || host == "" {
		return true
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()

	entry, ok := rt.entries[host]
	if !ok {
		entry = &reconnectEntry{
			reset: now.Add(rt.window),
		}
		rt.entries[host] = entry
	}

	if !entry.bannedUntil.IsZero() && now.Before(entry.bannedUntil) {
		return false
	}

	if now.After(entry.reset) {
		entry.count = 0
		entry.reset = now.Add(rt.window)
		entry.bannedUntil = time.Time{}
	}

	entry.count++
	if entry.count > rt.threshold {
		entry.bannedUntil = now.Add(rt.banDuration)
		return false
	}

	return true
}

// fileServerWithFallback tries to serve static files from www directory first,
// and falls back to the status server if the file doesn't exist.
type fileServerWithFallback struct {
	fileServer http.Handler
	fallback   http.Handler
	wwwRoot    *os.Root
}

func (h *fileServerWithFallback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check if file exists in www directory using os.Root for secure path resolution.
	// os.Root provides OS-level guarantees against path traversal by ensuring all
	// file operations stay within the root directory, similar to chroot.
	urlPath := strings.TrimPrefix(r.URL.Path, "/")
	cleanPath := filepath.Clean(urlPath)

	// Use os.Root to safely check if file exists within wwwDir.
	// This automatically prevents any path traversal attempts.
	info, err := h.wwwRoot.Stat(cleanPath)
	if err == nil && !info.IsDir() {
		h.fileServer.ServeHTTP(w, r)
		return
	}
	h.fallback.ServeHTTP(w, r)
}

func newAcceptRateLimiter(maxPerSecond, burst int) *acceptRateLimiter {
	if maxPerSecond <= 0 {
		return nil
	}
	rate := float64(maxPerSecond)
	burstSize := float64(burst)
	if burstSize <= 0 {
		burstSize = rate
	}
	// By default we allow up to one second's worth of connections to
	// arrive in a short spike; operators can raise/lower burst via
	// config to suit their hardware.
	return &acceptRateLimiter{
		rate:   rate,
		burst:  burstSize,
		tokens: burstSize,
		last:   time.Now(),
	}
}

// updateRate dynamically updates the rate and burst capacity of the limiter.
// This is used to transition from reconnection mode to steady-state mode.
func (l *acceptRateLimiter) updateRate(newRate, newBurst int) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	l.rate = float64(newRate)
	l.burst = float64(newBurst)
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
}

// wait blocks as needed so that no more than "rate" accepts occur on average,
// with up to "burst" accepts allowed in a short spike. It respects ctx
// cancellation so shutdown is not delayed by the limiter.
func (l *acceptRateLimiter) wait(ctx context.Context) bool {
	if l == nil {
		return true
	}

	l.mu.Lock()
	if l.rate <= 0 {
		l.mu.Unlock()
		return true
	}

	now := time.Now()
	if l.last.IsZero() {
		l.last = now
	}
	elapsed := now.Sub(l.last).Seconds()
	if elapsed > 0 {
		l.tokens += elapsed * l.rate
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		l.last = now
	}

	if l.tokens >= 1 {
		l.tokens -= 1
		l.mu.Unlock()
		return true
	}

	need := 1 - l.tokens
	rate := l.rate
	l.mu.Unlock()

	wait := time.Duration(need / rate * float64(time.Second))
	if wait <= 0 {
		wait = time.Millisecond
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
	}

	l.mu.Lock()
	l.last = time.Now()
	if l.tokens < 1 {
		l.tokens = 0
	} else {
		l.tokens -= 1
	}
	l.mu.Unlock()
	return true
}

func main() {
	// Top-level panic handler: ensure any unexpected panic is captured to
	// panic.log with a stack trace so operators can inspect it.
	defer func() {
		if r := recover(); r != nil {
			path := "panic.log"
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
				defer f.Close()
				ts := time.Now().UTC().Format(time.RFC3339)
				fmt.Fprintf(f, "[%s] panic: %v\nbuild_time=%s\n%s\n\n",
					ts, r, buildTime, debugpkg.Stack())
			}
		}
	}()

	debugpkg.SetGCPercent(200)

	bindFlag := flag.String("bind", "", "bind to specific IP address for stratum listener (e.g. 0.0.0.0 or 192.168.1.100)")
	rpcURLFlag := flag.String("rpc-url", "", "override RPC URL (e.g. http://127.0.0.1:8332)")
	secretsPathFlag := flag.String("secrets", "", "path to secrets.toml (overrides default under data_dir/config)")
	floodFlag := flag.Bool("flood", false, "enable flood-test mode (force min/max difficulty to 0.01)")
	mainnetFlag := flag.Bool("mainnet", false, "force mainnet defaults for RPC/ZMQ ports")
	testnetFlag := flag.Bool("testnet", false, "force testnet defaults for RPC/ZMQ ports")
	signetFlag := flag.Bool("signet", false, "force signet defaults for RPC/ZMQ ports")
	regtestFlag := flag.Bool("regtest", false, "force regtest defaults for RPC/ZMQ ports")
	noZMQFlag := flag.Bool("no-zmq", false, "disable ZMQ subscriptions and rely on RPC/longpoll only (SLOW)")
	rewriteConfigFlag := flag.Bool("rewrite-config", false, "rewrite config file with effective settings on startup")
	profileFlag := flag.Bool("profile", false, "collect a 60s CPU profile to default.pgo on startup")
	httpsOnlyFlag := flag.Bool("https-only", true, "serve status UI over HTTPS only (auto-generating a self-signed cert if none is present)")
	disableJSONFlag := flag.Bool("disable-json-endpoint", false, "disable JSON status endpoints for debugging")
	stdoutLogFlag := flag.Bool("stdoutlog", false, "mirror logs to stdout in addition to writing to files")
	noCleanBansFlag := flag.Bool("no-clean-bans", false, "skip rewriting the ban list on startup (keep expired bans)")
	flag.Parse()
	cleanBansOnStartup := !*noCleanBansFlag

	level := logLevelInfo
	if verboseEnabled() {
		level = logLevelInfo
	}
	if debugEnabled() {
		level = logLevelDebug
	}
	setLogLevel(level)
	// Raise the GC target when GOGC is not explicitly set so high-throughput
	// pools spend less CPU in GC at the cost of a modestly larger heap.
	if os.Getenv("GOGC") == "" {
		debugpkg.SetGCPercent(300)
	}
	// Mirror build-time debug/verbose settings into globals used by hot paths.
	debugLogging = debugEnabled()
	verboseLogging = verboseEnabled()

	// Optional one-shot CPU profiling: when -profile is set, capture a
	// 60-second CPU profile to default.pgo using runtime/pprof. The file
	// can be fed to "go tool pprof" or used as input for PGO builds.
	if *profileFlag {
		f, err := os.Create("default.pgo")
		if err != nil {
			logger.Warn("profile open failed", "error", err)
		} else if err := pprof.StartCPUProfile(f); err != nil {
			logger.Warn("profile start failed", "error", err)
			_ = f.Close()
		} else {
			logger.Info("cpu profiling started", "duration", "60s", "path", "default.pgo")
			go func() {
				time.Sleep(60 * time.Second)
				pprof.StopCPUProfile()
				_ = f.Close()
				logger.Info("cpu profiling finished", "path", "default.pgo")
			}()
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Set up SIGUSR1 handler for template reloading (will be connected to statusServer later)
	reloadChan := make(chan os.Signal, 1)
	signal.Notify(reloadChan, syscall.SIGUSR1)

	cfgPath := defaultConfigPath()
	cfg := loadConfig(cfgPath, *secretsPathFlag)
	if *floodFlag {
		// Flood-test mode: clamp difficulty to a very low fixed
		// level so miners send many low-difficulty shares. This is
		// intended for local testing / benchmarks, not production.
		cfg.MinDifficulty = 0.0000001
		cfg.MaxDifficulty = 0.0000001
	}
	if err := validateConfig(cfg); err != nil {
		fatal("config", err)
	}

	// Apply CLI overrides for RPC and network selection on top of file/env config.
	selectedNetworks := 0
	if *mainnetFlag {
		selectedNetworks++
	}
	if *testnetFlag {
		selectedNetworks++
	}
	if *signetFlag {
		selectedNetworks++
	}
	if *regtestFlag {
		selectedNetworks++
	}
	if selectedNetworks > 1 {
		fatal("config", fmt.Errorf("only one of -mainnet, -testnet, -signet, -regtest may be set"))
	}

	if *rpcURLFlag != "" {
		cfg.RPCURL = *rpcURLFlag
	} else if cfg.RPCURL == "http://127.0.0.1:8332" {
		// If a network is selected and RPC URL is still the compiled-in
		// mainnet default, adjust it to a network-appropriate default.
		switch {
		case *testnetFlag:
			cfg.RPCURL = "http://127.0.0.1:18332"
		case *signetFlag:
			cfg.RPCURL = "http://127.0.0.1:38332"
		case *regtestFlag:
			cfg.RPCURL = "http://127.0.0.1:18443"
		}
	}

	if *bindFlag != "" {
		_, port, err := net.SplitHostPort(cfg.ListenAddr)
		if err != nil {
			cfg.ListenAddr = net.JoinHostPort(*bindFlag, strings.TrimPrefix(cfg.ListenAddr, ":"))
		} else {
			cfg.ListenAddr = net.JoinHostPort(*bindFlag, port)
		}

		if cfg.StatusAddr != "" {
			_, port, err = net.SplitHostPort(cfg.StatusAddr)
			if err != nil {
				cfg.StatusAddr = net.JoinHostPort(*bindFlag, strings.TrimPrefix(cfg.StatusAddr, ":"))
			} else {
				cfg.StatusAddr = net.JoinHostPort(*bindFlag, port)
			}
		}

		// Apply to status HTTPS address
		if cfg.StatusTLSAddr != "" {
			_, port, err = net.SplitHostPort(cfg.StatusTLSAddr)
			if err != nil {
				cfg.StatusTLSAddr = net.JoinHostPort(*bindFlag, strings.TrimPrefix(cfg.StatusTLSAddr, ":"))
			} else {
				cfg.StatusTLSAddr = net.JoinHostPort(*bindFlag, port)
			}
		}

		// Apply to stratum TLS address if configured
		if cfg.StratumTLSListen != "" {
			_, port, err = net.SplitHostPort(cfg.StratumTLSListen)
			if err != nil {
				cfg.StratumTLSListen = net.JoinHostPort(*bindFlag, strings.TrimPrefix(cfg.StratumTLSListen, ":"))
			} else {
				cfg.StratumTLSListen = net.JoinHostPort(*bindFlag, port)
			}
		}
	}

	// Provide a sensible default ZMQ address when a network is selected and
	// none was configured.
	if cfg.ZMQBlockAddr == "" {
		if *mainnetFlag || *testnetFlag || *signetFlag || *regtestFlag {
			cfg.ZMQBlockAddr = "tcp://127.0.0.1:28332"
		}
	}

	if *noZMQFlag {
		cfg.ZMQBlockAddr = ""
	} else if cfg.ZMQBlockAddr == "" {
		fatal("config", fmt.Errorf("missing zmq_block_addr; set it in config.toml or use -no-zmq to disable ZMQ"))
	}

	// Select btcd network params for local address validation based on the
	// configured/selected network. Defaults to mainnet when no explicit
	// network flag is provided.
	switch {
	case *regtestFlag:
		SetChainParams("regtest")
	case *testnetFlag:
		SetChainParams("testnet3")
	case *signetFlag:
		SetChainParams("signet")
	default:
		SetChainParams("mainnet")
	}

	// Derive a concise, pool-branded coinbase tag of the form
	// "goPool-<status_brand_name>", truncated to at most 50 bytes and
	// restricted to printable ASCII so it stays within standard coinbase
	// scriptSig bounds.
	tag := poolSoftwareName
	brand := strings.TrimSpace(cfg.StatusBrandName)
	if brand != "" {
		tag = poolSoftwareName + "-" + brand
	}
	// Keep only printable ASCII bytes.
	var buf []byte
	for i := 0; i < len(tag); i++ {
		b := tag[i]
		if b >= 0x20 && b <= 0x7e {
			buf = append(buf, b)
		}
	}
	if len(buf) == 0 {
		buf = []byte(poolSoftwareName)
	}
	if len(buf) > 40 {
		buf = buf[:40]
	}
	cfg.CoinbaseMsg = string(buf)

	// After loading config, applying CLI/network overrides, and deriving
	// the effective coinbase tag (all of which are local operations),
	// optionally rewrite the config file so subsequent restarts can reuse
	// these effective settings. This runs before any RPC/node checks so
	// -rewrite-config still takes effect even if the node is unreachable.
	if *rewriteConfigFlag {
		if err := rewriteConfigFile(cfgPath, cfg); err != nil {
			logger.Warn("rewrite config file", "path", cfgPath, "error", err)
		}
	}

	logPath, err := initLogOutput(cfg)
	if err != nil {
		fatal("log file", err)
	}
	errorLogPath, err := initErrorLogOutput(cfg)
	if err != nil {
		fatal("error log file", err)
	}
	var debugLogPath string
	if debugEnabled() {
		debugLogPath, err = initDebugLogOutput(cfg)
		if err != nil {
			fatal("debug log file", err)
		}
	}
	configureFileLogging(logPath, errorLogPath, debugLogPath, *stdoutLogFlag)
	defer logger.Stop()

	if !cleanBansOnStartup {
		logger.Warn("ban cleanup on startup disabled", "flag", "no-clean-bans")
	}

	var netLogPath string
	if debugEnabled() {
		var err error
		netLogPath, err = initNetLogOutput(cfg)
		if err != nil {
			fatal("net log file", err)
		}
		setNetLogWriter(newRollingFileWriter(netLogPath))
	}

	logger.Info("starting pool", "listen_addr", cfg.ListenAddr, "status_addr", cfg.StatusAddr)
	logger.Info("effective config", "config", cfg.Effective())
	logger.Info("sha256 implementation", "implementation", sha256ImplementationName())

	// Config sanity checks.
	if cfg.PoolFeePercent <= 0 {
		logger.Warn("pool_fee_percent is 0; operator will not receive a fee")
	}
	if cfg.PoolFeePercent > 10 {
		logger.Warn("high pool_fee_percent; verify configuration", "pool_fee_percent", cfg.PoolFeePercent)
	}

	metrics := NewPoolMetrics()
	metrics.SetBestSharesFile(filepath.Join(cfg.DataDir, "state", "best_shares.json"))
	startTime := time.Now()
	rpcClient := NewRPCClient(cfg, metrics)
	// Best-effort replay of any blocks that failed submitblock while the
	// node RPC was unavailable in previous runs.
	startPendingSubmissionReplayer(ctx, cfg, rpcClient)

	accounting, err := NewAccountStore(cfg, debugEnabled(), cleanBansOnStartup)
	if err != nil {
		fatal("accounting", err)
	}

	registry := NewMinerRegistry()
	workerRegistry := newWorkerConnectionRegistry()

	var reconnectLimiter *reconnectTracker
	if cfg.ReconnectBanThreshold > 0 && cfg.ReconnectBanWindowSeconds > 0 && cfg.ReconnectBanDurationSeconds > 0 {
		reconnectLimiter = newReconnectTracker(
			cfg.ReconnectBanThreshold,
			time.Duration(cfg.ReconnectBanWindowSeconds)*time.Second,
			time.Duration(cfg.ReconnectBanDurationSeconds)*time.Second,
		)
	}

	// Start the status webserver before connecting to the node so operators
	// can see connection state while bitcoind starts up.
	statusServer := NewStatusServer(ctx, nil, metrics, registry, accounting, rpcClient, cfg, startTime)
	// Opportunistically warm node-info cache from normal RPC traffic without
	// changing how callers issue RPCs.
	rpcClient.SetResultHook(statusServer.handleRPCResult)

	// Start SIGUSR1 handler for live template reloading
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-reloadChan:
				logger.Info("SIGUSR1 received, reloading templates")
				if err := statusServer.ReloadTemplates(); err != nil {
					logger.Error("template reload failed", "error", err)
				}
			}
		}
	}()

	// Prepare www directory for static files (certbot .well-known, logo.png, style.css, etc.)
	wwwDir := filepath.Join(cfg.DataDir, "www")
	if err := os.MkdirAll(wwwDir, 0o755); err != nil {
		logger.Warn("create www directory", "error", err)
	}

	disableJSONEndpoints := *disableJSONFlag
	if disableJSONEndpoints {
		logger.Warn("JSON status endpoints disabled", "flag", "disable-json-endpoint")
	}

	mux := http.NewServeMux()
	// Focused API endpoints
	if !disableJSONEndpoints {
		// Page-specific endpoints (minimal payloads)
		mux.HandleFunc("/api/overview", statusServer.handleOverviewPageJSON)
		mux.HandleFunc("/api/pool-page", statusServer.handlePoolPageJSON)
		mux.HandleFunc("/api/node", statusServer.handleNodePageJSON)
		mux.HandleFunc("/api/server", statusServer.handleServerPageJSON)
		mux.HandleFunc("/api/pool-hashrate", statusServer.handlePoolHashrateJSON)

		// Other endpoints
		mux.HandleFunc("/api/pool", statusServer.handlePoolStatsJSON)
		mux.HandleFunc("/api/blocks", statusServer.handleBlocksListJSON)
	}
	// HTML endpoints
	mux.HandleFunc("/worker", statusServer.handleWorkerStatus)
	mux.HandleFunc("/worker/sha256", statusServer.handleWorkerStatusBySHA256)
	mux.HandleFunc("/node", statusServer.handleNodeInfo)
	mux.HandleFunc("/pool", statusServer.handlePoolInfo)
	mux.HandleFunc("/server", statusServer.handleServerInfoPage)
	mux.HandleFunc("/about", statusServer.handleAboutPage)
	// Alternative worker lookup URLs (SHA256-based)
	mux.HandleFunc("/user/", func(w http.ResponseWriter, r *http.Request) {
		statusServer.handleWorkerLookup(w, r, "/user")
	})
	mux.HandleFunc("/users/", func(w http.ResponseWriter, r *http.Request) {
		statusServer.handleWorkerLookup(w, r, "/users")
	})
	mux.HandleFunc("/stats/", func(w http.ResponseWriter, r *http.Request) {
		statusServer.handleWorkerLookup(w, r, "/stats")
	})
	mux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		statusServer.handleWorkerLookup(w, r, "/app")
	})
	// Catch-all: try static files first, fall back to status server
	// Use os.OpenRoot for secure, chroot-like file serving that prevents path traversal.
	wwwRoot, err := os.OpenRoot(wwwDir)
	if err != nil {
		logger.Warn("open www root", "error", err, "path", wwwDir)
		// Fall back to status server only if we can't open the www directory
		mux.Handle("/", statusServer)
	} else {
		mux.Handle("/", &fileServerWithFallback{
			fileServer: http.FileServer(http.Dir(wwwDir)),
			fallback:   statusServer,
			wwwRoot:    wwwRoot,
		})
	}
	// Prepare shared TLS certificate paths for both HTTPS status UI and
	// optional Stratum TLS. A self-signed cert is generated on demand.
	var certPath, keyPath string
	var certReloader *certReloader
	needStatusTLS := strings.TrimSpace(cfg.StatusTLSAddr) != "" || *httpsOnlyFlag
	if needStatusTLS || strings.TrimSpace(cfg.StratumTLSListen) != "" {
		certPath = filepath.Join(cfg.DataDir, "tls_cert.pem")
		keyPath = filepath.Join(cfg.DataDir, "tls_key.pem")
		if err := ensureSelfSignedCert(certPath, keyPath); err != nil {
			fatal("tls cert", err)
		}
		// Set up auto-reloading certificate manager for certbot renewals
		var err error
		certReloader, err = newCertReloader(certPath, keyPath)
		if err != nil {
			fatal("tls cert reloader", err)
		}
		// Start watching for certificate changes (checks hourly)
		go certReloader.watch(ctx)
		logger.Info("tls certificate auto-reload enabled", "check_interval", "1h")
	}

	httpAddr := strings.TrimSpace(cfg.StatusAddr)
	httpsAddr := strings.TrimSpace(cfg.StatusTLSAddr)

	var statusHTTPServer *http.Server
	var statusHTTPSServer *http.Server

	if *httpsOnlyFlag {
		// In HTTPS-only mode, ensure we have some TLS address. If
		// none was configured, reuse the HTTP status address.
		if httpsAddr == "" {
			httpsAddr = httpAddr
		}
		// Start HTTPS status server with auto-reloading certificates.
		if httpsAddr != "" {
			tlsConfig := &tls.Config{
				GetCertificate: certReloader.getCertificate,
			}
			statusHTTPSServer = &http.Server{
				Addr:      httpsAddr,
				Handler:   mux,
				TLSConfig: tlsConfig,
			}
			go func() {
				logger.Info("status page listening (https)", "addr", httpsAddr, "cert", certPath)
				if err := statusHTTPSServer.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
					fatal("status server error", err)
				}
			}()
		}

		// If HTTP and HTTPS ports differ, start a small HTTP
		// redirector on the HTTP port that sends users to the
		// HTTPS URL on the TLS port.
		if httpAddr != "" && httpAddr != httpsAddr {
			redirectHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				host := r.Host
				// Strip any existing port from Host.
				if h, _, err := net.SplitHostPort(host); err == nil {
					host = h
				}
				_, tlsPort, err := net.SplitHostPort(httpsAddr)
				targetHost := host
				if err == nil && tlsPort != "" && tlsPort != "443" {
					targetHost = net.JoinHostPort(host, tlsPort)
				}
				target := "https://" + targetHost + r.URL.RequestURI()
				http.Redirect(w, r, target, http.StatusMovedPermanently)
			})
			statusHTTPServer = &http.Server{
				Addr:    httpAddr,
				Handler: redirectHandler,
			}
			go func() {
				logger.Info("status http redirector listening", "addr", httpAddr, "target", httpsAddr)
				if err := statusHTTPServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					fatal("status redirect server error", err)
				}
			}()
		}
	} else {
		// Mixed mode: serve plain HTTP on StatusAddr, and if a TLS
		// status port is configured, also serve HTTPS there, but do
		// not force redirects.
		if httpAddr != "" {
			statusHTTPServer = &http.Server{
				Addr:    httpAddr,
				Handler: mux,
			}
			go func() {
				logger.Info("status page listening (http)", "addr", httpAddr)
				if err := statusHTTPServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					fatal("status server error", err)
				}
			}()
		}
		if httpsAddr != "" {
			tlsConfig := &tls.Config{
				GetCertificate: certReloader.getCertificate,
			}
			statusHTTPSServer = &http.Server{
				Addr:      httpsAddr,
				Handler:   mux,
				TLSConfig: tlsConfig,
			}
			go func() {
				logger.Info("status page listening (https)", "addr", httpsAddr, "cert", certPath)
				if err := statusHTTPSServer.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
					fatal("status server error", err)
				}
			}()
		}
	}

	// Gracefully shut down status HTTP/HTTPS servers when a shutdown
	// signal is received.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if statusHTTPServer != nil {
			if err := statusHTTPServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("status http shutdown error", "error", err)
			}
		}
		if statusHTTPSServer != nil {
			if err := statusHTTPSServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("status https shutdown error", "error", err)
			}
		}
	}()

	// After the status page is up, derive the pool payout script and start
	// the job manager. Payout script derivation is purely local and does
	// not require RPC; a misconfigured payout address is treated as a
	// fatal startup error. The script is always derived from the configured
	// payout address at startup.
	var payoutScript []byte
	script, err := fetchPayoutScript(nil, cfg.PayoutAddress)
	if err != nil {
		fatal("payout address", err)
	}
	payoutScript = script

	// If donation is configured, derive the donation payout script.
	var donationScript []byte
	if cfg.OperatorDonationPercent > 0 && cfg.OperatorDonationAddress != "" {
		logger.Info("configuring donation payout", "address", cfg.OperatorDonationAddress, "percent", cfg.OperatorDonationPercent)
		donationScript, err = fetchPayoutScript(nil, cfg.OperatorDonationAddress)
		if err != nil {
			fatal("donation payout address", err)
		}
		logger.Info("donation script derived", "script_len", len(donationScript), "script_hex", hex.EncodeToString(donationScript))
	} else {
		logger.Info("donation not configured", "percent", cfg.OperatorDonationPercent, "address", cfg.OperatorDonationAddress)
	}

	// Once the node is reachable, derive a network-appropriate version mask
	// from bitcoind instead of relying on a manual version_mask setting.
	autoConfigureVersionMaskFromNode(ctx, rpcClient, &cfg)

	jobMgr := NewJobManager(rpcClient, cfg, payoutScript, donationScript)
	statusServer.SetJobManager(jobMgr)
	if cfg.ZMQBlockAddr != "" {
		logger.Info("block updates via zmq", "addr", cfg.ZMQBlockAddr)
	} else {
		logger.Info("block updates via longpoll")
	}
	jobMgr.Start(ctx)

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		fatal("listen error", err, "addr", cfg.ListenAddr)
	}
	defer ln.Close()

	// Optional Stratum TLS listener for miners that support TLS. When
	// configured, it shares the same auto-reloading certificate as the HTTPS status UI.
	var tlsLn net.Listener
	if strings.TrimSpace(cfg.StratumTLSListen) != "" {
		if certReloader == nil {
			// Certificate reloader wasn't initialized yet (status server didn't need TLS)
			certPath = filepath.Join(cfg.DataDir, "tls_cert.pem")
			keyPath = filepath.Join(cfg.DataDir, "tls_key.pem")
			if err := ensureSelfSignedCert(certPath, keyPath); err != nil {
				fatal("stratum tls cert", err)
			}
			certReloader, err = newCertReloader(certPath, keyPath)
			if err != nil {
				fatal("stratum tls cert reloader", err)
			}
			go certReloader.watch(ctx)
			logger.Info("tls certificate auto-reload enabled", "check_interval", "1h")
		}
		tlsCfg := &tls.Config{
			GetCertificate: certReloader.getCertificate,
		}
		tlsLn, err = tls.Listen("tcp", cfg.StratumTLSListen, tlsCfg)
		if err != nil {
			fatal("stratum tls listen error", err, "addr", cfg.StratumTLSListen)
		}
		logger.Info("stratum TLS listening", "addr", cfg.StratumTLSListen)
	}

	acceptLimiter := newAcceptRateLimiter(cfg.MaxAcceptsPerSecond, cfg.MaxAcceptBurst)

	// If steady-state throttling is configured, schedule a transition
	// from reconnection mode to steady-state mode after the configured window.
	if acceptLimiter != nil && cfg.AcceptSteadyStateRate > 0 && cfg.AcceptSteadyStateWindow > 0 {
		go func() {
			steadyStateDelay := time.Duration(cfg.AcceptSteadyStateWindow) * time.Second
			logger.Info("steady-state throttle will activate after reconnection window",
				"delay", steadyStateDelay,
				"steady_state_rate", cfg.AcceptSteadyStateRate)

			select {
			case <-ctx.Done():
				return
			case <-time.After(steadyStateDelay):
				// Transition to steady-state mode
				steadyBurst := cfg.AcceptSteadyStateRate * 2
				if steadyBurst < 20 {
					steadyBurst = 20
				}
				acceptLimiter.updateRate(cfg.AcceptSteadyStateRate, steadyBurst)
				logger.Info("transitioned to steady-state throttle mode",
					"rate", cfg.AcceptSteadyStateRate,
					"burst", steadyBurst)
			}
		}()
	}

	var connWg sync.WaitGroup

	go func() {
		<-ctx.Done()
		logger.Info("shutdown requested; closing stratum listeners")
		ln.Close()
		if tlsLn != nil {
			tlsLn.Close()
		}
	}()

	serveStratum := func(label string, l net.Listener) {
		for {
			if !acceptLimiter.wait(ctx) {
				break
			}
			conn, err := l.Accept()
			if err != nil {
				if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
					break
				}
				logger.Error("accept error", "listener", label, "error", err)
				continue
			}
			remote := conn.RemoteAddr().String()
			if reconnectLimiter != nil {
				host, _, errSplit := net.SplitHostPort(remote)
				if errSplit != nil {
					host = remote
				}
				if !reconnectLimiter.allow(host, time.Now()) {
					logger.Warn("rejecting miner for reconnect churn",
						"listener", label,
						"remote", remote,
						"host", host,
					)
					_ = conn.Close()
					continue
				}
			}
			atCapacity := cfg.MaxConns > 0 && registry.Count() >= cfg.MaxConns
			if atCapacity {
				logger.Warn("rejecting miner: at capacity", "listener", label, "remote", conn.RemoteAddr().String(), "max_conns", cfg.MaxConns)
				_ = conn.Close()
				continue
			}
			mc := NewMinerConn(ctx, conn, jobMgr, rpcClient, cfg, metrics, accounting, workerRegistry, label == "tls")
			registry.Add(mc)

			connWg.Add(1)
			go func(mc *MinerConn) {
				defer connWg.Done()
				// Always remove connection from the map when this goroutine ends.
				defer registry.Remove(mc)

				mc.handle()
			}(mc)
		}
	}

	// Plain Stratum listener runs in the main goroutine so process
	// lifetime is tied to the primary TCP listener. Optional TLS
	// listener runs in a background goroutine.
	if tlsLn != nil {
		go serveStratum("tls", tlsLn)
	}
	serveStratum("tcp", ln)

	logger.Info("shutdown requested; draining active miners")
	shutdownStart := time.Now()
	for _, mc := range registry.Snapshot() {
		mc.Close("shutdown")
	}

	done := make(chan struct{})
	go func() {
		connWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		logger.Warn("timed out waiting for miners to drain", "waited", time.Since(shutdownStart))
	}

	if accounting != nil {
		if err := accounting.Flush(); err != nil {
			logger.Error("flush accounting", "error", err)
		}
	}
	// Best-effort sync of log files on shutdown so buffered OS writes are
	// forced to disk.
	logger.Info("shutdown complete", "uptime", time.Since(startTime))
	logger.Stop()

	// Best-effort sync of log files on shutdown so buffered OS writes are
	// forced to disk.
	if err := syncFileIfExists(logPath); err != nil {
		logger.Error("sync pool log", "error", err)
	}
	if debugLogPath != "" {
		if err := syncFileIfExists(debugLogPath); err != nil {
			logger.Error("sync debug log", "error", err)
		}
	}
	if debugEnabled() && netLogPath != "" {
		if err := syncFileIfExists(netLogPath); err != nil {
			logger.Error("sync net log", "error", err)
		}
	}
	if errorLogPath != "" {
		if err := syncFileIfExists(errorLogPath); err != nil {
			logger.Error("sync error log", "error", err)
		}
	}
}

func initLogOutput(cfg Config) (string, error) {
	dir := cfg.DataDir
	if dir == "" {
		dir = defaultDataDir
	}
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(logDir, "pool.log")
	return path, nil
}

func initNetLogOutput(cfg Config) (string, error) {
	dir := cfg.DataDir
	if dir == "" {
		dir = defaultDataDir
	}
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(logDir, "net-debug.log")
	return path, nil
}

func initErrorLogOutput(cfg Config) (string, error) {
	dir := cfg.DataDir
	if dir == "" {
		dir = defaultDataDir
	}
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(logDir, "errors.log")
	return path, nil
}

func initDebugLogOutput(cfg Config) (string, error) {
	dir := cfg.DataDir
	if dir == "" {
		dir = defaultDataDir
	}
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(logDir, "debug.log")
	return path, nil
}

// sanityCheckPoolAddressRPC performs a one-shot RPC validation of the pool
// payout address using the node's validateaddress RPC. It is intended as a
// boot-time sanity check: if the call fails or the node reports the address
// as invalid, the pool exits with a clear error instead of retrying. A short
// timeout is used so startup is not blocked indefinitely on RPC issues.
