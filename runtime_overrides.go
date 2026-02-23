package main

import (
	"fmt"
	"net"
	"strings"
)

type runtimeOverrides struct {
	bind                string
	listenAddr          string
	statusAddr          string
	statusTLSAddr       string
	stratumTLSListen    string
	safeMode            *bool
	ckpoolEmulate       *bool
	stratumFastDecode   *bool
	stratumFastEncode   *bool
	stratumTCPReadBuf   *int
	stratumTCPWriteBuf  *int
	rpcURL              string
	rpcCookiePath       string
	dataDir             string
	maxConns            int
	allowPublicRPC      bool
	allowRPCCredentials bool
	flood               bool
	mainnet             bool
	testnet             bool
	signet              bool
	regtest             bool
}

func applyRuntimeOverrides(cfg *Config, overrides runtimeOverrides) error {
	if overrides.flood {
		cfg.MinDifficulty = 0.0000001
		cfg.MaxDifficulty = 0.0000001
	}

	selectedNetworks := 0
	if overrides.mainnet {
		selectedNetworks++
	}
	if overrides.testnet {
		selectedNetworks++
	}
	if overrides.signet {
		selectedNetworks++
	}
	if overrides.regtest {
		selectedNetworks++
	}
	if selectedNetworks > 1 {
		return fmt.Errorf("only one of -mainnet, -testnet, -signet, -regtest may be set")
	}

	if overrides.rpcURL != "" {
		cfg.RPCURL = overrides.rpcURL
	} else if cfg.RPCURL == "http://127.0.0.1:8332" {
		switch {
		case overrides.testnet:
			cfg.RPCURL = "http://127.0.0.1:18332"
		case overrides.signet:
			cfg.RPCURL = "http://127.0.0.1:38332"
		case overrides.regtest:
			cfg.RPCURL = "http://127.0.0.1:18443"
		}
	}

	if overrides.rpcCookiePath != "" {
		cfg.RPCCookiePath = overrides.rpcCookiePath
	}
	if strings.TrimSpace(overrides.dataDir) != "" {
		cfg.DataDir = strings.TrimSpace(overrides.dataDir)
	}
	if overrides.maxConns >= 0 {
		cfg.MaxConns = overrides.maxConns
	}
	if overrides.allowPublicRPC {
		cfg.AllowPublicRPC = true
	}
	if overrides.bind != "" {
		_, port, err := net.SplitHostPort(cfg.ListenAddr)
		if err != nil {
			cfg.ListenAddr = net.JoinHostPort(overrides.bind, strings.TrimPrefix(cfg.ListenAddr, ":"))
		} else {
			cfg.ListenAddr = net.JoinHostPort(overrides.bind, port)
		}

		if cfg.StatusAddr != "" {
			_, port, err = net.SplitHostPort(cfg.StatusAddr)
			if err != nil {
				cfg.StatusAddr = net.JoinHostPort(overrides.bind, strings.TrimPrefix(cfg.StatusAddr, ":"))
			} else {
				cfg.StatusAddr = net.JoinHostPort(overrides.bind, port)
			}
		}

		if cfg.StatusTLSAddr != "" {
			_, port, err = net.SplitHostPort(cfg.StatusTLSAddr)
			if err != nil {
				cfg.StatusTLSAddr = net.JoinHostPort(overrides.bind, strings.TrimPrefix(cfg.StatusTLSAddr, ":"))
			} else {
				cfg.StatusTLSAddr = net.JoinHostPort(overrides.bind, port)
			}
		}

		if cfg.StratumTLSListen != "" {
			_, port, err = net.SplitHostPort(cfg.StratumTLSListen)
			if err != nil {
				cfg.StratumTLSListen = net.JoinHostPort(overrides.bind, strings.TrimPrefix(cfg.StratumTLSListen, ":"))
			} else {
				cfg.StratumTLSListen = net.JoinHostPort(overrides.bind, port)
			}
		}
	}

	// Explicit listener overrides win over global bind rewrites.
	if strings.TrimSpace(overrides.listenAddr) != "" {
		cfg.ListenAddr = strings.TrimSpace(overrides.listenAddr)
	}
	if strings.TrimSpace(overrides.statusAddr) != "" {
		cfg.StatusAddr = strings.TrimSpace(overrides.statusAddr)
	}
	if strings.TrimSpace(overrides.statusTLSAddr) != "" {
		cfg.StatusTLSAddr = strings.TrimSpace(overrides.statusTLSAddr)
	}
	if strings.TrimSpace(overrides.stratumTLSListen) != "" {
		cfg.StratumTLSListen = strings.TrimSpace(overrides.stratumTLSListen)
	}
	if overrides.ckpoolEmulate != nil {
		cfg.CKPoolEmulate = *overrides.ckpoolEmulate
	}
	if overrides.safeMode != nil {
		cfg.SafeMode = *overrides.safeMode
	}
	if overrides.stratumFastDecode != nil {
		cfg.StratumFastDecodeEnabled = *overrides.stratumFastDecode
	}
	if overrides.stratumFastEncode != nil {
		cfg.StratumFastEncodeEnabled = *overrides.stratumFastEncode
	}
	if overrides.stratumTCPReadBuf != nil {
		cfg.StratumTCPReadBufferBytes = *overrides.stratumTCPReadBuf
	}
	if overrides.stratumTCPWriteBuf != nil {
		cfg.StratumTCPWriteBufferBytes = *overrides.stratumTCPWriteBuf
	}

	if cfg.ZMQHashBlockAddr == "" && cfg.ZMQRawBlockAddr == "" {
		if overrides.mainnet || overrides.testnet || overrides.signet || overrides.regtest {
			cfg.ZMQHashBlockAddr = defaultZMQHashBlockAddr
			cfg.ZMQRawBlockAddr = defaultZMQRawBlockAddr
		}
	}

	if cfg.ZMQHashBlockAddr == "" && cfg.ZMQRawBlockAddr == "" {
		logger.Warn("zmq is not configured; using RPC/longpoll-only mode", "hint", "set node.zmq_hashblock_addr/node.zmq_rawblock_addr in config.toml to enable ZMQ (legacy node.zmq_block_addr is read-only for migration)")
	}
	if cfg.SafeMode {
		applySafeModeProfile(cfg)
	}

	return nil
}

func applySafeModeProfile(cfg *Config) {
	if cfg == nil {
		return
	}
	cfg.SafeMode = true

	// Conservative compatibility/safety defaults for troubleshooting and broad miner support.
	cfg.CKPoolEmulate = true
	cfg.StratumFastDecodeEnabled = false
	cfg.StratumFastEncodeEnabled = false
	cfg.StratumTCPReadBufferBytes = 0
	cfg.StratumTCPWriteBufferBytes = 0

	cfg.ShareRequireAuthorizedConnection = true
	cfg.ShareCheckParamFormat = true
	cfg.ShareCheckDuplicate = true
	cfg.ShareRequireJobID = true
	cfg.SubmitProcessInline = false

	// Prefer widest miner compatibility over stricter policy checks.
	cfg.ShareCheckNTimeWindow = false
	cfg.ShareCheckVersionRolling = false
	cfg.ShareRequireWorkerMatch = false

	// Disable automatic temporary bans in safe mode to avoid false positives while troubleshooting.
	cfg.BanInvalidSubmissionsAfter = 0
	cfg.ReconnectBanThreshold = 0

	cfg.DisableConnectRateLimits = true
}
