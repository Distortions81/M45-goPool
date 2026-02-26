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
	"strconv"
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
	listenFlag := flag.String("listen", "", "override stratum TCP listen address (e.g. :3333)")
	statusAddrFlag := flag.String("status", "", "override status HTTP listen address (e.g. :80)")
	statusTLSAddrFlag := flag.String("status-tls", "", "override status HTTPS listen address (e.g. :443)")
	stratumTLSFlag := flag.String("stratum-tls", "", "override stratum TLS listen address (e.g. :24333)")
	var safeModeFlag *bool
	flag.Func("safe-mode", "force conservative compatibility/safety profile (true/false)", func(v string) error {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			return err
		}
		safeModeFlag = &b
		return nil
	})
	var ckpoolEmulateFlag *bool
	flag.Func("ckpool-emulate", "override Stratum subscribe response shape compatibility (true/false)", func(v string) error {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			return err
		}
		ckpoolEmulateFlag = &b
		return nil
	})
	var fastDecodeFlag *bool
	flag.Func("stratum-fast-decode", "override fast-path Stratum decode/sniffing (true/false)", func(v string) error {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			return err
		}
		fastDecodeFlag = &b
		return nil
	})
	var fastEncodeFlag *bool
	flag.Func("stratum-fast-encode", "override fast-path Stratum response encoding (true/false)", func(v string) error {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			return err
		}
		fastEncodeFlag = &b
		return nil
	})
	var stratumTCPReadBufFlag *int
	flag.Func("stratum-tcp-read-buffer", "override Stratum TCP read buffer bytes (0 = OS default)", func(v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return err
		}
		if n < 0 {
			return fmt.Errorf("must be >= 0")
		}
		stratumTCPReadBufFlag = &n
		return nil
	})
	var stratumTCPWriteBufFlag *int
	flag.Func("stratum-tcp-write-buffer", "override Stratum TCP write buffer bytes (0 = OS default)", func(v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return err
		}
		if n < 0 {
			return fmt.Errorf("must be >= 0")
		}
		stratumTCPWriteBufFlag = &n
		return nil
	})
	rpcURLFlag := flag.String("rpc-url", "", "override RPC URL")
	rpcCookieFlag := flag.String("rpc-cookie", "", "override RPC cookie path")
	dataDirFlag := flag.String("data-dir", "", "override data directory")
	maxConnsFlag := flag.Int("max-conns", -1, "override max concurrent miner connections (-1 keeps config)")
	logDirFlag := flag.String("log-dir", "", "override log directory (pool/debug/net-debug logs only)")
	poolLogPathFlag := flag.String("pool-log", "", "override pool log file path")
	debugLogPathFlag := flag.String("debug-log", "", "override debug log file path")
	netDebugLogPathFlag := flag.String("net-debug-log", "", "override net-debug log file path")
	secretsFlag := flag.String("secrets", "", "path to secrets.toml")
	stdoutLogFlag := flag.Bool("stdout", false, "mirror logs to stdout")
	profileFlag := flag.Bool("profile", false, "60s CPU profile")
	rewriteConfigFlag := flag.Bool("rewrite-config", false, "rewrite config on startup")
	floodFlag := flag.Bool("flood", false, "flood-test mode")
	disableJSONFlag := flag.Bool("no-json", false, "disable JSON API")
	allowPublicRPCFlag := flag.Bool("allow-public-rpc", false, "allow unauthenticated RPC endpoint (testing only)")
	allowRPCCredsFlag := flag.Bool("allow-rpc-creds", false, "allow rpc creds from secrets.toml")
	debugFlag := flag.Bool("debug", false, "enable debug logging and detailed runtime traces")
	netDebugFlag := flag.Bool("net-debug", false, "enable raw network debug logging at startup (when supported)")
	backupOnBootFlag := flag.Bool("backup-on-boot", false, "run a forced database backup once at startup (best-effort)")
	minerProfileJSONFlag := flag.String("miner-profile-json", "", "optional path to write aggregated miner profile JSON for offline tuning")
	flag.Parse()

	network := strings.ToLower(*networkFlag)

	overrides := runtimeOverrides{
		bind:                *bindFlag,
		listenAddr:          *listenFlag,
		statusAddr:          *statusAddrFlag,
		statusTLSAddr:       *statusTLSAddrFlag,
		stratumTLSListen:    *stratumTLSFlag,
		safeMode:            safeModeFlag,
		ckpoolEmulate:       ckpoolEmulateFlag,
		stratumFastDecode:   fastDecodeFlag,
		stratumFastEncode:   fastEncodeFlag,
		stratumTCPReadBuf:   stratumTCPReadBufFlag,
		stratumTCPWriteBuf:  stratumTCPWriteBufFlag,
		rpcURL:              *rpcURLFlag,
		rpcCookiePath:       *rpcCookieFlag,
		dataDir:             *dataDirFlag,
		maxConns:            *maxConnsFlag,
		allowPublicRPC:      *allowPublicRPCFlag,
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
			logger.Warn("profile open failed", "component", "startup", "kind", "profile", "error", err)
		} else if err := pprof.StartCPUProfile(f); err != nil {
			logger.Warn("profile start failed", "component", "startup", "kind", "profile", "error", err)
			_ = f.Close()
		} else {
			logger.Info("cpu profiling started", "component", "startup", "kind", "profile", "duration", "60s", "path", "default.pgo")
			go func() {
				time.Sleep(60 * time.Second)
				pprof.StopCPUProfile()
				_ = f.Close()
				logger.Info("cpu profiling finished", "component", "startup", "kind", "profile", "path", "default.pgo")
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
		logger.Warn("rpc credentials forced from secrets.toml instead of node.rpc_cookie_path (deprecated and insecure)", "component", "startup", "kind", "config", "hint", "configure bitcoind's auth cookie via node.rpc_cookie_path instead")
	}
	if err := validateConfig(cfg); err != nil {
		fatal("config", err)
	}
	adminConfigPath, err := ensureAdminConfigFile(cfg.DataDir)
	if err != nil {
		fatal("admin config", err)
	}

	if *debugFlag {
		cfg.LogDebug = true
	}
	if *netDebugFlag {
		cfg.LogNetDebug = true
	}
	if cfg.LogDebug {
		setLogLevel(logLevelDebug)
	} else {
		setLogLevel(logLevelInfo)
	}

	// Mirror current log-level into globals used by hot paths.
	debugLogging = debugEnabled()
	verboseRuntimeLogging = verboseRuntimeEnabled()

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

	logPath, err := initLogOutput(cfg, strings.TrimSpace(*logDirFlag), strings.TrimSpace(*poolLogPathFlag))
	if err != nil {
		fatal("log file", err)
	}
	// errors.log routing is intentionally disabled: pool.log is the primary
	// application timeline (INFO/WARN/ERROR). Keep debug/net-debug as opt-in
	// detail streams.
	errorLogPath := ""
	var debugLogPath string
	if debugEnabled() {
		debugLogPath, err = initDebugLogOutput(cfg, strings.TrimSpace(*logDirFlag), strings.TrimSpace(*debugLogPathFlag))
		if err != nil {
			fatal("debug log file", err)
		}
	}
	configureFileLogging(logPath, errorLogPath, debugLogPath, *stdoutLogFlag)
	ensureSubmissionWorkerPool()
	defer logger.Stop()

	var netLogPath string
	if cfg.LogNetDebug {
		var err error
		netLogPath, err = initNetLogOutput(cfg, strings.TrimSpace(*logDirFlag), strings.TrimSpace(*netDebugLogPathFlag))
		if err != nil {
			fatal("net log file", err)
		}
		if err := setNetLogRuntime(true, newDailyRollingFileWriter(netLogPath)); err != nil {
			logger.Warn("net-debug startup enable failed", "error", err)
		}
	}
	logger.Info("log outputs configured",
		"component", "startup",
		"kind", "logging",
		"pool_log", logPath,
		"errors_log", "disabled",
		"debug_log", debugLogPath,
		"net_debug_log", netLogPath,
		"log_dir_override", strings.TrimSpace(*logDirFlag),
		"stdout", *stdoutLogFlag,
		"log_debug", cfg.LogDebug,
		"log_net_debug", cfg.LogNetDebug,
		"debug_enabled", debugLogging,
		"net_debug_enabled", netLogRuntimeEnabled(),
	)

	logger.Info("starting pool", "component", "startup", "kind", "lifecycle", "listen_addr", cfg.ListenAddr, "status_addr", cfg.StatusAddr)
	logger.Info("startup config summary",
		"component", "startup",
		"kind", "config",
		"listen_addr", cfg.ListenAddr,
		"safe_mode", cfg.SafeMode,
		"stratum_tls_listen", cfg.StratumTLSListen,
		"status_addr", cfg.StatusAddr,
		"status_tls_addr", cfg.StatusTLSAddr,
		"stratum_tls_enabled", strings.TrimSpace(cfg.StratumTLSListen) != "",
		"status_public_url_set", strings.TrimSpace(cfg.StatusPublicURL) != "",
		"vardiff_enabled", cfg.VarDiffEnabled,
		"share_checks", cfg.ShareCheckParamFormat,
		"version_rolling_checks", cfg.ShareCheckVersionRolling,
		"ntimes_window_check", cfg.ShareCheckNTimeWindow,
		"share_duplicate_check", cfg.ShareCheckDuplicate,
		"admin_config_present", strings.TrimSpace(adminConfigPath) != "",
	)
	logger.Debug("effective config", "component", "startup", "kind", "config_full", "config", cfg.Effective())
	logger.Info("sha256 implementation", "component", "startup", "kind", "crypto", "implementation", sha256ImplementationName())

	// Best-effort cleanup of legacy difficulty cache file.
	dataDir := strings.TrimSpace(cfg.DataDir)
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	difficultyCachePath := filepath.Join(dataDir, "state", "difficulty_cache.json")
	if err := os.Remove(difficultyCachePath); err != nil && !os.IsNotExist(err) {
		logger.Warn("delete difficulty cache", "error", err, "path", difficultyCachePath)
	}

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
	clerkVerifier := (*ClerkVerifier)(nil)
	if clerkConfigured(cfg) {
		var clerkErr error
		clerkVerifier, clerkErr = NewClerkVerifier(cfg)
		if clerkErr != nil {
			logger.Warn("initialize clerk verifier", "error", clerkErr)
		}
	} else {
		logger.Info("clerk auth disabled", "reason", "clerk_secret_key, clerk_publishable_key, and clerk_frontend_api_url are required")
	}
	workerListDBPath := filepath.Join(cfg.DataDir, "state", "workers.db")
	workerLists, workerListErr := newWorkerListStore(workerListDBPath)
	if workerListErr != nil {
		logger.Warn("open saved workers store", "error", workerListErr, "path", workerListDBPath)
	} else {
		defer workerLists.Close()
	}
	var backupSvc *backblazeBackupService
	if svc, err := newBackblazeBackupService(ctx, cfg, workerListDBPath); err != nil {
		logger.Warn("initialize backblaze backup service", "error", err)
	} else if svc != nil {
		backupSvc = svc
		if svc.b2Enabled {
			if svc.bucket == nil {
				logger.Warn("backblaze backups enabled but bucket is not reachable; using local snapshots only",
					"bucket", cfg.BackblazeBucket,
					"interval", svc.interval.String(),
					"force_every_interval", cfg.BackblazeForceEveryInterval,
					"snapshot_path", svc.snapshotPath,
				)
			} else {
				logger.Info("backblaze database backups enabled",
					"bucket", cfg.BackblazeBucket,
					"interval", svc.interval.String(),
					"force_every_interval", cfg.BackblazeForceEveryInterval,
					"snapshot_path", svc.snapshotPath,
				)
			}
		} else {
			logger.Info("local database backups enabled",
				"interval", svc.interval.String(),
				"force_every_interval", cfg.BackblazeForceEveryInterval,
				"snapshot_path", svc.snapshotPath,
			)
		}
		if *backupOnBootFlag {
			logger.Info("backup-on-boot enabled; forcing one backup now")
			go func() {
				runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
				defer cancel()
				svc.RunOnce(runCtx, "boot_flag", true)
			}()
		}
		svc.start(ctx)
	}
	rpcClient := NewRPCClient(cfg, metrics)
	rpcClient.StartCookieWatcher(ctx)
	// Best-effort replay of any blocks that failed submitblock while the
	// node RPC was unavailable in previous runs.
	startPendingSubmissionReplayer(ctx, rpcClient)

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
	if profiler := newMinerProfileCollector(*minerProfileJSONFlag); profiler != nil {
		setMinerProfileCollector(profiler)
		defer func() {
			if err := profiler.Flush(); err != nil {
				logger.Warn("flush miner profile json", "error", err, "path", *minerProfileJSONFlag)
			}
			setMinerProfileCollector(nil)
		}()
		go profiler.Run(ctx)
		logger.Info("miner profile collector enabled", "path", *minerProfileJSONFlag)
	}

	// Start the status webserver before connecting to the node so operators
	// can see connection state while bitcoind starts up.
	statusServer := NewStatusServer(ctx, nil, metrics, registry, workerRegistry, accounting, rpcClient, cfg, startTime, clerkVerifier, workerLists, cfgPath, adminConfigPath, stop)
	statusServer.SetBackupService(backupSvc)
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
					logger.Info("SIGUSR1 received, reloading templates and static cache")
					if err := statusServer.ReloadTemplates(); err != nil {
						logger.Error("template reload failed", "error", err)
					}
					if err := statusServer.ReloadStaticFiles(); err != nil {
						logger.Error("static cache reload failed", "error", err)
					}
				case syscall.SIGUSR2:
					logger.Info("SIGUSR2 received, reloading config")
					reloadedCfg, err := reloadStatusConfig(cfgPath, *secretsFlag, overrides)
					if err != nil {
						logger.Error("config reload failed", "error", err)
						continue
					}
					statusServer.UpdateConfig(reloadedCfg)
					if reloadedCfg.LogDebug {
						setLogLevel(logLevelDebug)
					} else {
						setLogLevel(logLevelInfo)
					}
					verboseRuntimeLogging = verboseRuntimeEnabled()
					if reloadedCfg.LogNetDebug {
						netPath := ""
						netPath, netPathErr := initNetLogOutput(reloadedCfg, strings.TrimSpace(*logDirFlag), strings.TrimSpace(*netDebugLogPathFlag))
						if netPathErr != nil {
							logger.Warn("net-debug reload init failed", "error", netPathErr)
						} else if err := setNetLogRuntime(true, newDailyRollingFileWriter(netPath)); err != nil {
							logger.Warn("net-debug reload enable failed", "error", err)
						}
					} else {
						if err := setNetLogRuntime(false, nil); err != nil {
							logger.Warn("net-debug reload disable failed", "error", err)
						}
					}
					logger.Info("config reloaded", "component", "startup", "kind", "config_reload", "path", cfgPath)
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
		mux.HandleFunc("/api/saved-workers/history", statusServer.withClerkUser(statusServer.handleSavedWorkerHistoryJSON))
		mux.HandleFunc("/api/saved-workers/notify-enabled", statusServer.withClerkUser(statusServer.handleSavedWorkersNotifyEnabled))
		mux.HandleFunc("/api/discord/notify-enabled", statusServer.withClerkUser(statusServer.handleDiscordNotifyEnabled))
		mux.HandleFunc("/api/saved-workers/one-time-code", statusServer.withClerkUser(statusServer.handleSavedWorkersOneTimeCode))
		mux.HandleFunc("/api/saved-workers/one-time-code/clear", statusServer.withClerkUser(statusServer.handleSavedWorkersOneTimeCodeClear))

		// Other endpoints
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
	mux.HandleFunc("/admin/bans", statusServer.handleAdminBansPage)
	mux.HandleFunc("/admin/bans/remove", statusServer.handleAdminBanRemove)
	mux.HandleFunc("/admin/operator", statusServer.handleAdminOperatorPage)
	mux.HandleFunc("/admin/config", statusServer.handleAdminConfigPage)
	mux.HandleFunc("/admin/logs", statusServer.handleAdminLogsPage)
	mux.HandleFunc("/admin/logs/tail", statusServer.handleAdminLogsTail)
	mux.HandleFunc("/admin/logs/flags", statusServer.handleAdminLogsSetFlags)
	mux.HandleFunc("/admin/login", statusServer.handleAdminLogin)
	mux.HandleFunc("/admin/logout", statusServer.handleAdminLogout)
	mux.HandleFunc("/admin/apply", statusServer.handleAdminApplySettings)
	mux.HandleFunc("/admin/reload-ui", statusServer.handleAdminReloadUI)
	mux.HandleFunc("/admin/persist", statusServer.handleAdminPersist)
	mux.HandleFunc("/admin/reboot", statusServer.handleAdminReboot)
	mux.HandleFunc("/worker", statusServer.withClerkUser(statusServer.handleWorkerStatus))
	mux.HandleFunc("/worker/search", statusServer.withClerkUser(statusServer.handleWorkerWalletSearch))
	mux.HandleFunc("/worker/sha256", statusServer.withClerkUser(statusServer.handleWorkerStatusBySHA256))
	mux.HandleFunc("/worker/save", statusServer.withClerkUser(statusServer.handleWorkerSave))
	mux.HandleFunc("/worker/remove", statusServer.withClerkUser(statusServer.handleWorkerRemove))
	mux.HandleFunc("/worker/reconnect", statusServer.withClerkUser(statusServer.handleWorkerReconnect))
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
	// Standard wallet lookup URLs used by miner tooling.
	mux.HandleFunc("/user/", func(w http.ResponseWriter, r *http.Request) {
		statusServer.handleWorkerLookupByWallet(w, r, "/user")
	})
	mux.HandleFunc("/users/", func(w http.ResponseWriter, r *http.Request) {
		statusServer.handleWorkerLookupByWallet(w, r, "/users")
	})
	mux.HandleFunc("/stats/", func(w http.ResponseWriter, r *http.Request) {
		statusServer.handleWorkerLookupByWallet(w, r, "/stats")
	})
	// Catch-all: try static files first, fall back to status server
	// Use os.OpenRoot for secure, chroot-like file serving that prevents path traversal.
	wwwRoot, err := os.OpenRoot(wwwDir)
	if err != nil {
		logger.Warn("open www root", "error", err, "path", wwwDir)
		// Fall back to status server only if we can't open the www directory
		mux.Handle("/", statusServer)
	} else {
		staticFiles := &fileServerWithFallback{
			fileServer: http.FileServer(http.Dir(wwwDir)),
			fallback:   statusServer,
			wwwRoot:    wwwRoot,
			wwwDir:     wwwDir,
		}
		if err := staticFiles.PreloadCache(); err != nil {
			logger.Warn("preload static cache failed", "error", err)
		} else {
			logger.Info("static cache preloaded", "path", wwwDir)
		}
		statusServer.SetStaticFileServer(staticFiles)
		mux.Handle("/", staticFiles)
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
		logger.Info("tls certificate auto-reload enabled", "component", "http", "kind", "tls", "check_interval", "1h")
	}

	var statusHTTPServer *http.Server
	var statusHTTPSServer *http.Server
	appHandler := statusServer.serveShortResponseCache(mux)

	// Start HTTP server.
	if httpAddr != "" {
		httpHandler := http.Handler(appHandler)
		httpLogMsg := "status page listening (http)"
		httpLogFields := []any{"addr", httpAddr}
		if needStatusTLS {
			httpHandler = http.HandlerFunc(statusServer.redirectToHTTPS)
			httpLogMsg = "status http listener redirecting to https"
			httpLogFields = append(httpLogFields, "https_addr", httpsAddr)
		}

		statusHTTPServer = &http.Server{
			Addr:              httpAddr,
			Handler:           httpHandler,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       2 * time.Minute,
		}
		go func() {
			httpLogFields = append([]any{"component", "http", "kind", "listen"}, httpLogFields...)
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
			Addr:              httpsAddr,
			Handler:           appHandler,
			TLSConfig:         tlsConfig,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       2 * time.Minute,
		}
		go func() {
			logger.Info("status page listening (https)", "component", "http", "kind", "listen", "addr", httpsAddr, "cert", certPath)
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
				logger.Error("status http shutdown error", "component", "http", "kind", "shutdown", "error", err)
			}
		}
		if statusHTTPSServer != nil {
			if err := statusHTTPSServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("status https shutdown error", "component", "http", "kind", "shutdown", "error", err)
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
		logger.Info("configuring donation payout", "component", "startup", "kind", "payout", "address", cfg.OperatorDonationAddress, "percent", cfg.OperatorDonationPercent)
		donationScript, err = fetchPayoutScript(nil, cfg.OperatorDonationAddress)
		if err != nil {
			fatal("donation payout address", err)
		}
		logger.Info("donation script derived", "component", "startup", "kind", "payout", "script_len", len(donationScript), "script_hex", hex.EncodeToString(donationScript))
	} else {
		logger.Info("donation not configured", "component", "startup", "kind", "payout", "percent", cfg.OperatorDonationPercent, "address", cfg.OperatorDonationAddress)
	}

	// Once the node is reachable, derive a network-appropriate version mask
	// from bitcoind instead of relying on a manual version_mask setting.
	autoConfigureVersionMaskFromNode(ctx, rpcClient, &cfg)

	jobMgr := NewJobManager(rpcClient, cfg, metrics, payoutScript, donationScript)
	statusServer.SetJobManager(jobMgr)
	if cfg.ZMQHashBlockAddr != "" || cfg.ZMQRawBlockAddr != "" {
		logger.Info("block updates via zmq + longpoll", "component", "startup", "kind", "job_feed", "hashblock_addr", cfg.ZMQHashBlockAddr, "rawblock_addr", cfg.ZMQRawBlockAddr)
	} else {
		logger.Info("block updates via longpoll", "component", "startup", "kind", "job_feed")
	}
	jobMgr.Start(ctx)

	// Once Stratum is live, enforce the same freshness rule at runtime:
	// - refuse new miner connections while the job feed is stale
	// - disconnect existing miners so they stop hashing stale work
	go enforceStratumFreshness(ctx, jobMgr, registry, statusServer, startTime)

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
			logger.Info("tls certificate auto-reload enabled", "component", "stratum", "kind", "tls", "check_interval", "1h")
		}
		tlsCfg := &tls.Config{
			GetCertificate: certReloader.getCertificate,
		}
		tlsLn, err = tls.Listen("tcp", cfg.StratumTLSListen, tlsCfg)
		if err != nil {
			fatal("stratum tls listen error", err, "addr", cfg.StratumTLSListen)
		}
		logger.Info("stratum TLS listening", "component", "stratum", "kind", "listen", "addr", cfg.StratumTLSListen)
	}

	var acceptLimiter *acceptRateLimiter
	if cfg.DisableConnectRateLimits {
		logger.Warn("connect rate limits disabled by config", "component", "stratum", "kind", "accept_limit")
	} else {
		acceptLimiter = newAcceptRateLimiter(cfg.MaxAcceptsPerSecond, cfg.MaxAcceptBurst)
	}

	// If steady-state throttling is configured, schedule a transition
	// from reconnection mode to steady-state mode after the configured window.
	if acceptLimiter != nil && cfg.AcceptSteadyStateRate > 0 && cfg.AcceptSteadyStateWindow > 0 {
		go func() {
			steadyStateDelay := time.Duration(cfg.AcceptSteadyStateWindow) * time.Second
			logger.Info("steady-state throttle will activate after reconnection window",
				"component", "stratum", "kind", "throttle",
				"delay", steadyStateDelay,
				"steady_state_rate", cfg.AcceptSteadyStateRate)

			select {
			case <-ctx.Done():
				return
			case <-time.After(steadyStateDelay):
				// Transition to steady-state mode
				steadyBurst := max(cfg.AcceptSteadyStateRate*2, 20)
				acceptLimiter.updateRate(cfg.AcceptSteadyStateRate, steadyBurst)
				logger.Info("transitioned to steady-state throttle mode",
					"component", "stratum", "kind", "throttle",
					"rate", cfg.AcceptSteadyStateRate,
					"burst", steadyBurst)
			}
		}()
	}

	var connWg sync.WaitGroup

	go func() {
		<-ctx.Done()
		logger.Info("shutdown requested; closing stratum listeners", "component", "stratum", "kind", "shutdown")
		ln.Close()
		if tlsLn != nil {
			tlsLn.Close()
		}
	}()

	serveStratum := func(label string, l net.Listener) {
		lastRefuseLog := time.Time{}
		unhealthySince := time.Time{}
		for {
			if !acceptLimiter.wait(ctx) {
				break
			}
			conn, err := l.Accept()
			if err != nil {
				if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
					break
				}
				logger.Error("accept error", "component", "stratum", "kind", "accept", "listener", label, "error", err)
				continue
			}
			disableTCPNagle(conn)
			curCfg := statusServer.Config()
			setTCPBuffers(conn, curCfg.StratumTCPReadBufferBytes, curCfg.StratumTCPWriteBufferBytes)
			now := time.Now()
			if now.Sub(startTime) >= stratumStartupGrace {
				if h := stratumHealthStatus(jobMgr, now); !h.Healthy {
					if unhealthySince.IsZero() {
						unhealthySince = now
					}
					if now.Sub(unhealthySince) >= stratumStaleJobGrace {
						if time.Since(lastRefuseLog) > 5*time.Second {
							fields := []any{"listener", label, "remote", conn.RemoteAddr().String(), "reason", h.Reason, "grace", stratumStaleJobGrace}
							if strings.TrimSpace(h.Detail) != "" {
								fields = append(fields, "detail", h.Detail)
							}
							fields = append([]any{"component", "stratum", "kind", "gating"}, fields...)
							logger.Warn("refusing miner connection: node updates degraded", fields...)
							lastRefuseLog = time.Now()
						}
						_ = conn.Close()
						continue
					}
				} else {
					unhealthySince = time.Time{}
				}
			}
			remote := conn.RemoteAddr().String()
			if reconnectLimiter != nil {
				host, _, errSplit := net.SplitHostPort(remote)
				if errSplit != nil {
					host = remote
				}
				if !reconnectLimiter.allow(host, time.Now()) {
					logger.Warn("rejecting miner for reconnect churn",
						"component", "stratum", "kind", "reconnect_limit",
						"listener", label,
						"remote", remote,
						"host", host,
					)
					_ = conn.Close()
					continue
				}
			}
			atCapacity := curCfg.MaxConns > 0 && registry.Count() >= curCfg.MaxConns
			if atCapacity {
				logger.Warn("rejecting miner: at capacity", "component", "stratum", "kind", "capacity", "listener", label, "remote", conn.RemoteAddr().String(), "max_conns", curCfg.MaxConns)
				_ = conn.Close()
				continue
			}
			mc := NewMinerConn(ctx, conn, jobMgr, rpcClient, curCfg, metrics, accounting, workerRegistry, workerLists, notifier, label == "tls")
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

	logger.Info("shutdown requested; draining active miners", "component", "stratum", "kind", "shutdown")
	shutdownStart := time.Now()
	for _, mc := range registry.Snapshot() {
		mc.sendClientShowMessage("Pool restarting; please reconnect.")
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
		logger.Warn("timed out waiting for miners to drain", "component", "stratum", "kind", "shutdown", "waited", time.Since(shutdownStart))
	}

	if accounting != nil {
		if err := accounting.Flush(); err != nil {
			logger.Error("flush accounting", "component", "db", "kind", "flush", "error", err)
		}
	}
	if statusServer != nil {
		if n, err := statusServer.persistSavedWorkerPeriodsSnapshot(); err != nil {
			logger.Warn("persist saved worker period history snapshot", "error", err, "path", statusServer.savedWorkerPeriodsSnapshotPath())
		} else {
			logger.Info("persisted saved worker period history snapshot", "workers", n, "path", statusServer.savedWorkerPeriodsSnapshotPath())
		}
	}

	// Best-effort checkpoint to flush WAL into the main DB on shutdown.
	checkpointSharedStateDB()
	// Best-effort sync of log files on shutdown so buffered OS writes are
	// forced to disk.
	logger.Info("shutdown complete", "component", "startup", "kind", "shutdown", "uptime", time.Since(startTime))
	logger.Stop()

	// Best-effort sync of log files on shutdown so buffered OS writes are
	// forced to disk.
	if err := syncFileIfExists(logPath); err != nil {
		logger.Error("sync pool log", "component", "startup", "kind", "log_sync", "error", err)
	}
	if debugLogPath != "" {
		if err := syncFileIfExists(debugLogPath); err != nil {
			logger.Error("sync debug log", "component", "startup", "kind", "log_sync", "error", err)
		}
	}
	if debugEnabled() && netLogPath != "" {
		if err := syncFileIfExists(netLogPath); err != nil {
			logger.Error("sync net log", "component", "startup", "kind", "log_sync", "error", err)
		}
	}
	if errorLogPath != "" {
		if err := syncFileIfExists(errorLogPath); err != nil {
			logger.Error("sync error log", "component", "startup", "kind", "log_sync", "error", err)
		}
	}
}

func enforceStratumFreshness(ctx context.Context, jobMgr *JobManager, registry *MinerRegistry, statusServer *StatusServer, start time.Time) {
	if ctx == nil || jobMgr == nil || registry == nil {
		return
	}

	wasHealthy := true
	unhealthySince := time.Time{}
	lastLog := time.Time{}
	for {
		if ctx.Err() != nil {
			return
		}

		now := time.Now()
		if !start.IsZero() && now.Sub(start) < stratumStartupGrace {
			// During boot grace window, do not treat missing/degraded node state as actionable.
			unhealthySince = time.Time{}
			wasHealthy = true
			time.Sleep(500 * time.Millisecond)
			continue
		}
		h := stratumHealthStatus(jobMgr, now)
		if !h.Healthy {
			if unhealthySince.IsZero() {
				unhealthySince = now
			}
			// Require a long continuous unhealthy window before disconnecting miners.
			if wasHealthy && now.Sub(unhealthySince) >= stratumStaleJobGrace {
				miners := registry.Snapshot()
				for _, mc := range miners {
					mc.sendClientShowMessage("Pool paused: node updates degraded. Reconnecting when ready.")
					mc.Close("node updates degraded")
				}
				eventCountTotal := uint64(0)
				if statusServer != nil && len(miners) > 0 {
					eventCountTotal = statusServer.recordStratumSafeguardDisconnectEvent(now, len(miners), h.Reason, h.Detail)
				}
				if time.Since(lastLog) > 2*time.Second {
					fs := jobMgr.FeedStatus()
					fields := []any{"disconnected", len(miners), "reason", h.Reason, "heartbeat_interval", stratumHeartbeatInterval, "grace", stratumStaleJobGrace}
					if eventCountTotal > 0 {
						fields = append(fields, "safeguard_disconnect_events_total", eventCountTotal)
					}
					if strings.TrimSpace(h.Detail) != "" {
						fields = append(fields, "detail", h.Detail)
					}
					job := jobMgr.CurrentJob()
					if job != nil && !job.CreatedAt.IsZero() {
						fields = append(fields, "job_age", now.Sub(job.CreatedAt))
					} else {
						fields = append(fields, "job_age", "(none)")
					}
					if !fs.LastSuccess.IsZero() {
						fields = append(fields, "last_success", fs.LastSuccess, "last_success_age", now.Sub(fs.LastSuccess))
					}
					if fs.LastError != nil {
						fields = append(fields, "last_error", fs.LastError.Error())
					}
					fields = append([]any{"component", "stratum", "kind", "gating"}, fields...)
					logger.Warn("stratum gated: node updates degraded; disconnected miners", fields...)
					lastLog = now
				}
				wasHealthy = false
			}
		} else {
			unhealthySince = time.Time{}
			if !wasHealthy {
				logger.Info("stratum ungated: node updates healthy again", "component", "stratum", "kind", "gating")
				wasHealthy = true
			}
		}

		time.Sleep(500 * time.Millisecond)
	}
}

func disableTCPNagle(conn net.Conn) {
	if tcp := findTCPConn(conn); tcp != nil {
		if err := tcp.SetNoDelay(true); err != nil {
			logger.Debug("set tcp no-delay failed (ignored)", "error", err)
		}
	}
}

func setTCPBuffers(conn net.Conn, readBytes, writeBytes int) {
	if readBytes <= 0 && writeBytes <= 0 {
		return
	}
	if tcp := findTCPConn(conn); tcp != nil {
		if readBytes > 0 {
			if err := tcp.SetReadBuffer(readBytes); err != nil {
				logger.Debug("set tcp read buffer failed", "error", err, "bytes", readBytes)
			}
		}
		if writeBytes > 0 {
			if err := tcp.SetWriteBuffer(writeBytes); err != nil {
				logger.Debug("set tcp write buffer failed", "error", err, "bytes", writeBytes)
			}
		}
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

func initLogOutput(cfg Config, logDirOverride, pathOverride string) (string, error) {
	return initNamedLogOutput(cfg, logDirOverride, pathOverride, "pool.log")
}

func initNetLogOutput(cfg Config, logDirOverride, pathOverride string) (string, error) {
	return initNamedLogOutput(cfg, logDirOverride, pathOverride, "net-debug.log")
}

func initDebugLogOutput(cfg Config, logDirOverride, pathOverride string) (string, error) {
	return initNamedLogOutput(cfg, logDirOverride, pathOverride, "debug.log")
}

func initNamedLogOutput(cfg Config, logDirOverride, pathOverride, baseName string) (string, error) {
	if strings.TrimSpace(pathOverride) != "" {
		path := strings.TrimSpace(pathOverride)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		return path, nil
	}
	if strings.TrimSpace(logDirOverride) != "" {
		logDir := strings.TrimSpace(logDirOverride)
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return "", err
		}
		return filepath.Join(logDir, baseName), nil
	}
	dir := cfg.DataDir
	if dir == "" {
		dir = defaultDataDir
	}
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(logDir, baseName), nil
}

// sanityCheckPoolAddressRPC performs a one-shot RPC validation of the pool
// payout address using the node's validateaddress RPC. It is intended as a
// boot-time sanity check: if the call fails or the node reports the address
// as invalid, the pool exits with a clear error instead of retrying. A short
// timeout is used so startup is not blocked indefinitely on RPC issues.
