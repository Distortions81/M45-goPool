package main

import (
	"path/filepath"
	"time"
)

const (
	defaultListenAddr                        = ":3333"
	defaultStatusAddr                        = ":80"
	defaultStatusTLSAddr                     = ":443"
	defaultStatusTagline                     = "Solo Mining Pool"
	defaultFiatCurrency                      = "usd"
	defaultStratumTLSListen                  = ":4333"
	defaultRPCURL                            = "http://127.0.0.1:8332"
	defaultRPCUser                           = "bitcoinrpc"
	defaultRPCPass                           = "password"
	defaultExtranonce2Size                   = 4
	defaultTemplateExtraNonce2Size           = 8
	defaultPoolFeePercent                    = 2.0
	defaultRecentJobs                        = 3
	defaultSubscribeTimeout                  = 15 * time.Second
	defaultAuthorizeTimeout                  = 15 * time.Second
	defaultStratumReadTimeout                = 5 * time.Minute
	defaultMaxAcceptsPerSecond               = 500
	defaultMaxAcceptBurst                    = 1000
	defaultAcceptReconnectWindow             = 15
	defaultAcceptBurstWindow                 = 5
	defaultAcceptSteadyStateWindow           = 100
	defaultAcceptSteadyStateRate             = 50
	defaultAcceptSteadyStateReconnectPercent = 5.0
	defaultAcceptSteadyStateReconnectWindow  = 60
	defaultCoinbaseSuffixBytes               = 4
	maxCoinbaseSuffixBytes                   = 32
	defaultCoinbaseScriptSigMaxBytes         = 100
	defaultReplayLimit                       = int64(16 << 20)
	defaultMaxConns                          = 10000
	defaultHashrateEMATauSeconds             = 600.0
	defaultNTimeForwardSlackSeconds          = 7000
	defaultBanInvalidSubmissionsAfter        = 60
	defaultBanInvalidSubmissionsWindow       = time.Minute
	defaultBanInvalidSubmissionsDuration     = 15 * time.Minute
	defaultReconnectBanThreshold             = 0
	defaultReconnectBanWindowSeconds         = 60
	defaultReconnectBanDurationSeconds       = 300
	defaultMaxDifficulty                     = 16000
	defaultMinDifficulty                     = 512
	defaultMinVersionBits                    = 1
	defaultRefreshInterval                   = 10 * time.Second
	defaultZMQReceiveTimeout                 = 15 * time.Second
)

// defaultConfig returns a Config populated with built-in defaults that act
// as the base for both runtime config loading and example config generation.
func defaultConfig() Config {
	return Config{
		ListenAddr:        defaultListenAddr,
		StatusAddr:        defaultStatusAddr,
		StatusTLSAddr:     defaultStatusTLSAddr,
		StatusBrandName:   "",
		StatusBrandDomain: "",
		StatusTagline:     defaultStatusTagline,
		FiatCurrency:      defaultFiatCurrency,
		PoolDonationAddress: "",
		DiscordURL:        "",
		// StratumTLSListen defaults to empty (disabled) so operators
		// explicitly opt in to TLS for miner connections.
		StratumTLSListen: defaultStratumTLSListen,
		RPCURL:           defaultRPCURL,
		RPCUser:          defaultRPCUser,
		RPCPass:          defaultRPCPass,
		CoinbasePoolTag:       generatePoolTag(),
		PayoutAddress:         "",
		PoolFeePercent:        defaultPoolFeePercent,
		OperatorDonationPercent: 0.0,
		OperatorDonationAddress: "",
		OperatorDonationName:    "",
		OperatorDonationURL:     "",
		// Mining / Stratum defaults.
		Extranonce2Size:                   defaultExtranonce2Size,
		TemplateExtraNonce2Size:           defaultTemplateExtraNonce2Size,
		CoinbaseSuffixBytes:               defaultCoinbaseSuffixBytes,
		CoinbaseMsg:                       poolSoftwareName,
		CoinbaseScriptSigMaxBytes:         defaultCoinbaseScriptSigMaxBytes,
		ZMQBlockAddr:                      "tcp://127.0.0.1:28332",
		DataDir:                           defaultDataDir,
		ShareLogBufferBytes:               0,
		FsyncShareLog:                     false,
		ShareLogReplayBytes:               defaultReplayLimit,
		MaxConns:                          defaultMaxConns,
		MaxAcceptsPerSecond:               defaultMaxAcceptsPerSecond,
		MaxAcceptBurst:                    defaultMaxAcceptBurst,
		AutoAcceptRateLimits:              true,
		AcceptReconnectWindow:             defaultAcceptReconnectWindow,
		AcceptBurstWindow:                 defaultAcceptBurstWindow,
		AcceptSteadyStateWindow:           defaultAcceptSteadyStateWindow,
		AcceptSteadyStateRate:             defaultAcceptSteadyStateRate,
		AcceptSteadyStateReconnectPercent: defaultAcceptSteadyStateReconnectPercent,
		AcceptSteadyStateReconnectWindow:  defaultAcceptSteadyStateReconnectWindow,
		MaxRecentJobs:                     defaultRecentJobs,
		SubscribeTimeout:                  defaultSubscribeTimeout,
		AuthorizeTimeout:                  defaultAuthorizeTimeout,
		StratumReadTimeout:                defaultStratumReadTimeout,
		VersionMask:                       defaultVersionMask,
		MinVersionBits:                    defaultMinVersionBits,
		// Default difficulty range: 512–16000 so all live targets are powers
		// of two within a practical range for typical ASICs.
		MaxDifficulty:                 defaultMaxDifficulty,
		MinDifficulty:                 defaultMinDifficulty,
		LockSuggestedDifficulty:       false,
		HashrateEMATauSeconds:         defaultHashrateEMATauSeconds,
		NTimeForwardSlackSeconds:      defaultNTimeForwardSlackSeconds,
		BanInvalidSubmissionsAfter:    defaultBanInvalidSubmissionsAfter,
		BanInvalidSubmissionsWindow:   defaultBanInvalidSubmissionsWindow,
		BanInvalidSubmissionsDuration: defaultBanInvalidSubmissionsDuration,
		ReconnectBanThreshold:         defaultReconnectBanThreshold,
		ReconnectBanWindowSeconds:     defaultReconnectBanWindowSeconds,
		ReconnectBanDurationSeconds:   defaultReconnectBanDurationSeconds,
	}
}

// defaultConfigPath returns the path for the main pool config.
func defaultConfigPath() string {
	return filepath.Join(defaultDataDir, "config", "config.toml")
}
