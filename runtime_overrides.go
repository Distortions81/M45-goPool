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

	if cfg.ZMQHashBlockAddr == "" && cfg.ZMQRawBlockAddr == "" {
		if overrides.mainnet || overrides.testnet || overrides.signet || overrides.regtest {
			cfg.ZMQHashBlockAddr = defaultZMQHashBlockAddr
			cfg.ZMQRawBlockAddr = defaultZMQRawBlockAddr
		}
	}

	if cfg.ZMQHashBlockAddr == "" && cfg.ZMQRawBlockAddr == "" {
		logger.Warn("zmq is not configured; using RPC/longpoll-only mode", "hint", "set node.zmq_hashblock_addr/node.zmq_rawblock_addr in config.toml to enable ZMQ (legacy node.zmq_block_addr is read-only for migration)")
	}

	return nil
}
