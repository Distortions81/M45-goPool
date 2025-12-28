package main

import (
	"fmt"
	"net"
	"strings"
)

type runtimeOverrides struct {
	bind                string
	rpcURL              string
	rpcCookiePath       string
	allowRPCCredentials bool
	flood               bool
	noZMQ               bool
	zmqLongpollFallback bool
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

	if cfg.ZMQBlockAddr == "" {
		if overrides.mainnet || overrides.testnet || overrides.signet || overrides.regtest {
			cfg.ZMQBlockAddr = "tcp://127.0.0.1:28332"
		}
	}

	if overrides.noZMQ {
		cfg.ZMQBlockAddr = ""
	} else if cfg.ZMQBlockAddr == "" {
		return fmt.Errorf("missing zmq_block_addr; set it in config.toml or use -no-zmq to disable ZMQ")
	}

	if overrides.zmqLongpollFallback {
		cfg.ZMQLongpollFallback = true
	}

	return nil
}
