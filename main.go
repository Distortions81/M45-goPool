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

	networkFlag := flag.String("network", "", "bitcoin network: mainnet, testnet, signet, regtest")
	bindFlag := flag.String("bind", "", "bind IP for all listeners")
	rpcURLFlag := flag.String("rpc-url", "", "override RPC URL")
	rpcCookieFlag := flag.String("rpc-cookie", "", "override RPC cookie path")
	secretsFlag := flag.String("secrets", "", "path to secrets.toml")
	stdoutLogFlag := flag.Bool("stdout", false, "mirror logs to stdout")
	profileFlag := flag.Bool("profile", false, "60s CPU profile")
	rewriteConfigFlag := flag.Bool("rewrite-config", false, "rewrite config on startup")
	floodFlag := flag.Bool("flood", false, "flood-test mode")
	disableJSONFlag := flag.Bool("no-json", false, "disable JSON API")
	allowRPCCredsFlag := flag.Bool("allow-rpc-creds", false, "allow rpc creds from secrets.toml")
	logLevelFlag := flag.String("log-level", "", "override log level (debug/info/warn/error)")
	flag.Parse()

	network := strings.ToLower(*networkFlag)

	overrides := runtimeOverrides{
		bind:                *bindFlag,
		rpcURL:              *rpcURLFlag,
		rpcCookiePath:       *rpcCookieFlag,
		allowRPCCredentials: *allowRPCCredsFlag,
		flood:               *floodFlag,
		mainnet:             network == "mainnet",
		testnet:             network == "testnet",
		signet:              network == "signet",
		regtest:             network == "regtest",
	}

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

	// Set up SIGUSR1/SIGUSR2 handler for template/config reloading
	reloadChan := make(chan os.Signal, 1)
	signal.Notify(reloadChan, syscall.SIGUSR1, syscall.SIGUSR2)

	cfgPath := defaultConfigPath()
	cfg, secretsPath := loadConfig(cfgPath, *secretsFlag)
	if err := applyRuntimeOverrides(&cfg, overrides); err != nil {
		fatal("config", err)
	}
	if err := finalizeRPCCredentials(&cfg, secretsPath, overrides.allowRPCCredentials, cfgPath); err != nil {
		fatal("rpc auth", err)
	}
	if overrides.allowRPCCredentials {
		logger.Warn("rpc credentials forced from secrets.toml instead of node.rpc_cookie_path (deprecated and insecure)", "hint", "configure bitcoind's auth cookie via node.rpc_cookie_path instead")
	}
	if err := validateConfig(cfg); err != nil {
		fatal("config", err)
	}
	adminConfigPath, err := ensureAdminConfigFile(cfg.DataDir)
	if err != nil {
		fatal("admin config", err)
	}

	logLevelName := cfg.LogLevel
	if *logLevelFlag != "" {
		logLevelName = *logLevelFlag
	}
	level, err := parseLogLevel(logLevelName)
	if err != nil {
		fatal("log level", err)
	}
	setLogLevel(level)

	// Mirror current log-level into globals used by hot paths.
	debugLogging = debugEnabled()
	verboseLogging = verboseEnabled()

	cleanBansOnStartup := cfg.CleanExpiredBansOnStartup
	if !cleanBansOnStartup {
		logger.Warn("ban cleanup on startup disabled", "tuning", "[bans].clean_expired_on_startup=false")
	}

	// Apply command-line flag for duplicate share checking (disabled by default for solo pools)
	// Select btcd network params for local address validation based on the
	// configured/selected network. Defaults to mainnet when no explicit
	// network flag is provided.
	switch {
	case overrides.regtest:
		SetChainParams("regtest")
	case overrides.testnet:
		SetChainParams("testnet3")
	case overrides.signet:
		SetChainParams("signet")
	default:
		SetChainParams("mainnet")
	}

	// Derive a concise coinbase tag like "/goPool/" or "/<prefix>-goPool/",
	// truncated to at most 40 bytes and restricted to printable ASCII so it
	// stays within standard coinbase scriptSig bounds.
	brand := poolSoftwareName
	if cfg.PoolTagPrefix != "" {
		brand = cfg.PoolTagPrefix + "-" + brand
	}
	tag := "/" + brand + "/"
	// Keep only printable ASCII bytes.
	var buf []byte
	for i := 0; i < len(tag); i++ {
		b := tag[i]
		if b >= 0x20 && b <= 0x7e {
			buf = append(buf, b)
		}
	}
	if len(buf) == 0 {
		buf = []byte("/" + brand + "/")
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
	ensureSubmissionWorkerPool()
	defer logger.Stop()

	var netLogPath string
	if debugEnabled() {
		var err error
		netLogPath, err = initNetLogOutput(cfg)
		if err != nil {
			fatal("net log file", err)
		}
		setNetLogWriter(newDailyRollingFileWriter(netLogPath))
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

	callbackPath := strings.TrimSpace(cfg.ClerkCallbackPath)
	if callbackPath == "" {
		callbackPath = defaultClerkCallbackPath
	}
	if !strings.HasPrefix(callbackPath, "/") {
		callbackPath = "/" + callbackPath
	}
	cfg.ClerkCallbackPath = callbackPath

	// Initialize shared state database connection (singleton for all components)
	if err := initSharedStateDB(cfg.DataDir); err != nil {
		fatal("initialize shared state database", err)
	}
	defer closeSharedStateDB()

	startTime := time.Now()
	metrics := NewPoolMetrics()
	metrics.SetStartTime(startTime)
	metrics.SetBestSharesDB(cfg.DataDir)
	clerkVerifier, clerkErr := NewClerkVerifier(cfg)
	if clerkErr != nil {
		logger.Warn("initialize clerk verifier", "error", clerkErr)
	}
	workerListDBPath := filepath.Join(cfg.DataDir, "state", "workers.db")
	workerLists, workerListErr := newWorkerListStore(workerListDBPath)
	if workerListErr != nil {
		logger.Warn("open saved workers store", "error", workerListErr, "path", workerListDBPath)
	} else {
		defer workerLists.Close()
	}
	if svc, err := newBackblazeBackupService(ctx, cfg, workerListDBPath); err != nil {
		logger.Warn("initialize backblaze backup service", "error", err)
	} else if svc != nil {
		if cfg.BackblazeBackupEnabled {
			logger.Info("backblaze database backups enabled", "bucket", cfg.BackblazeBucket, "interval", svc.interval.String(), "snapshot_path", svc.snapshotPath)
		} else {
			logger.Info("local database backups enabled", "interval", svc.interval.String(), "snapshot_path", svc.snapshotPath)
		}
		svc.start(ctx)
	}
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
	tuningPath := filepath.Join(cfg.DataDir, "config", "tuning.toml")
	statusServer := NewStatusServer(ctx, nil, metrics, registry, workerRegistry, accounting, rpcClient, cfg, startTime, clerkVerifier, workerLists, cfgPath, adminConfigPath, tuningPath, stop)
	statusServer.startOneTimeCodeJanitor(ctx)
	statusServer.loadOneTimeCodesFromDB(cfg.DataDir)
	statusServer.startOneTimeCodePersistence(ctx)
	// Opportunistically warm node-info cache from normal RPC traffic without
	// changing how callers issue RPCs.
	rpcClient.SetResultHook(statusServer.handleRPCResult)
	notifier := &discordNotifier{s: statusServer}
	if err := notifier.start(ctx); err != nil {
		logger.Warn("discord notifier start failed", "error", err)
	}

	// Start SIGUSR1/SIGUSR2 handler for live template/config reloading
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case sig := <-reloadChan:
				switch sig {
				case syscall.SIGUSR1:
					logger.Info("SIGUSR1 received, reloading templates")
					if err := statusServer.ReloadTemplates(); err != nil {
						logger.Error("template reload failed", "error", err)
					}
				case syscall.SIGUSR2:
					logger.Info("SIGUSR2 received, reloading config")
					reloadedCfg, err := reloadStatusConfig(cfgPath, *secretsFlag, overrides)
					if err != nil {
						logger.Error("config reload failed", "error", err)
						continue
					}
					statusServer.UpdateConfig(reloadedCfg)
					logger.Info("config reloaded", "path", cfgPath)
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
		mux.HandleFunc("/api/auth/session-refresh", statusServer.handleClerkSessionRefresh)
		mux.HandleFunc("/api/saved-workers", statusServer.withClerkUser(statusServer.handleSavedWorkersJSON))
		mux.HandleFunc("/api/saved-workers/notify-enabled", statusServer.withClerkUser(statusServer.handleSavedWorkersNotifyEnabled))
		mux.HandleFunc("/api/discord/notify-enabled", statusServer.withClerkUser(statusServer.handleDiscordNotifyEnabled))
		mux.HandleFunc("/api/saved-workers/one-time-code", statusServer.withClerkUser(statusServer.handleSavedWorkersOneTimeCode))
		mux.HandleFunc("/api/saved-workers/one-time-code/clear", statusServer.withClerkUser(statusServer.handleSavedWorkersOneTimeCodeClear))

		// Other endpoints
		mux.HandleFunc("/api/pool", statusServer.handlePoolStatsJSON)
		mux.HandleFunc("/api/blocks", statusServer.handleBlocksListJSON)
	}
	// HTML endpoints
	mux.HandleFunc("/admin", statusServer.handleAdminPage)
	mux.HandleFunc("/admin/miners", statusServer.handleAdminMinersPage)
	mux.HandleFunc("/admin/miners/disconnect", statusServer.handleAdminMinerDisconnect)
	mux.HandleFunc("/admin/miners/ban", statusServer.handleAdminMinerBan)
	mux.HandleFunc("/admin/logins", statusServer.handleAdminLoginsPage)
	mux.HandleFunc("/admin/logins/delete", statusServer.handleAdminLoginDelete)
	mux.HandleFunc("/admin/logins/ban", statusServer.handleAdminLoginBan)
	mux.HandleFunc("/admin/login", statusServer.handleAdminLogin)
	mux.HandleFunc("/admin/logout", statusServer.handleAdminLogout)
	mux.HandleFunc("/admin/apply", statusServer.handleAdminApplySettings)
	mux.HandleFunc("/admin/persist", statusServer.handleAdminPersist)
	mux.HandleFunc("/admin/reboot", statusServer.handleAdminReboot)
	mux.HandleFunc("/worker", statusServer.withClerkUser(statusServer.handleWorkerStatus))
	mux.HandleFunc("/worker/search", statusServer.withClerkUser(statusServer.handleWorkerWalletSearch))
	mux.HandleFunc("/worker/sha256", statusServer.withClerkUser(statusServer.handleWorkerStatusBySHA256))
	mux.HandleFunc("/worker/save", statusServer.withClerkUser(statusServer.handleWorkerSave))
	mux.HandleFunc("/worker/remove", statusServer.withClerkUser(statusServer.handleWorkerRemove))
	mux.HandleFunc("/saved-workers", statusServer.withClerkUser(statusServer.handleSavedWorkers))
	mux.HandleFunc("/login", statusServer.handleClerkLogin)
	mux.HandleFunc("/sign-in", statusServer.handleSignIn)
	mux.HandleFunc("/logout", statusServer.handleClerkLogout)
	mux.HandleFunc(cfg.ClerkCallbackPath, statusServer.handleClerkCallback)
	mux.HandleFunc("/node", statusServer.handleNodeInfo)
	mux.HandleFunc("/pool", statusServer.handlePoolInfo)
	mux.HandleFunc("/server", statusServer.handleServerInfoPage)
	mux.HandleFunc("/about", statusServer.handleAboutPage)
	mux.HandleFunc("/help", statusServer.handleHelpPage)
	// Static legal pages
	mux.HandleFunc("/privacy", statusServer.handleStaticFile("privacy.html"))
	mux.HandleFunc("/terms", statusServer.handleStaticFile("terms.html"))
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
	httpAddr := strings.TrimSpace(cfg.StatusAddr)
	httpsAddr := strings.TrimSpace(cfg.StatusTLSAddr)

	// TLS is optional; leaving cfg.StatusTLSAddr empty disables HTTPS for local/dev setups.
	var certPath, keyPath string
	var certReloader *certReloader
	needStatusTLS := httpsAddr != ""
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

	var statusHTTPServer *http.Server
	var statusHTTPSServer *http.Server

	// Start HTTP server.
	if httpAddr != "" {
		httpHandler := http.Handler(mux)
		httpLogMsg := "status page listening (http)"
		httpLogFields := []interface{}{"addr", httpAddr}
		if needStatusTLS {
			httpHandler = http.HandlerFunc(statusServer.redirectToHTTPS)
			httpLogMsg = "status http listener redirecting to https"
			httpLogFields = append(httpLogFields, "https_addr", httpsAddr)
		}

		statusHTTPServer = &http.Server{
			Addr:    httpAddr,
			Handler: httpHandler,
		}
		go func() {
			logger.Info(httpLogMsg, httpLogFields...)
			if err := statusHTTPServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				fatal("status server error", err)
			}
		}()
	}

	// Start HTTPS server (unless -http-only).
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

	jobMgr := NewJobManager(rpcClient, cfg, metrics, payoutScript, donationScript)
	statusServer.SetJobManager(jobMgr)
	if cfg.ZMQBlockAddr != "" {
		logger.Info("block updates via zmq + longpoll", "addr", cfg.ZMQBlockAddr)
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
			disableTCPNagle(conn)
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
			mc := NewMinerConn(ctx, conn, jobMgr, rpcClient, cfg, metrics, accounting, workerRegistry, workerLists, label == "tls")
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

func disableTCPNagle(conn net.Conn) {
	if tcp := findTCPConn(conn); tcp != nil {
		_ = tcp.SetNoDelay(true)
	}
}

func findTCPConn(conn net.Conn) *net.TCPConn {
	type netConnGetter interface {
		NetConn() net.Conn
	}

	for i := 0; i < 4 && conn != nil; i++ {
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			return tcpConn
		}
		getter, ok := conn.(netConnGetter)
		if !ok {
			return nil
		}
		next := getter.NetConn()
		if next == nil || next == conn {
			return nil
		}
		conn = next
	}
	return nil
}

func reloadStatusConfig(cfgPath, secretsPath string, overrides runtimeOverrides) (Config, error) {
	cfg, secretsPath := loadConfig(cfgPath, secretsPath)
	if err := applyRuntimeOverrides(&cfg, overrides); err != nil {
		return Config{}, err
	}
	if err := finalizeRPCCredentials(&cfg, secretsPath, overrides.allowRPCCredentials, cfgPath); err != nil {
		return Config{}, err
	}

	tag := poolSoftwareName
	brand := strings.TrimSpace(cfg.StatusBrandName)
	if brand != "" {
		tag = poolSoftwareName + "-" + brand
	}
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

	callbackPath := strings.TrimSpace(cfg.ClerkCallbackPath)
	if callbackPath == "" {
		callbackPath = defaultClerkCallbackPath
	}
	if !strings.HasPrefix(callbackPath, "/") {
		callbackPath = "/" + callbackPath
	}
	cfg.ClerkCallbackPath = callbackPath

	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
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
