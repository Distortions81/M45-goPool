# <a href="https://github.com/Distortions81/M45-Core-goPool/blob/main/data/www/logo.png"><img src="https://raw.githubusercontent.com/Distortions81/M45-Core-goPool/main/data/www/logo.png" alt="goPool logo" width="32" height="32" style="vertical-align: middle;"></a> M45-Core-goPool (goPool)

[![Go CI](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/ci.yml/badge.svg)](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/ci.yml)
[![Go Vulncheck](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/govulncheck.yml/badge.svg)](https://github.com/Distortions81/M45-Core-goPool/actions/workflows/govulncheck.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/Distortions81/M45-Core-goPool)](https://goreportcard.com/report/github.com/Distortions81/M45-Core-goPool)
[![License](https://img.shields.io/github/license/Distortions81/M45-Core-goPool)](https://github.com/Distortions81/M45-Core-goPool/blob/main/LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/Distortions81/M45-Core-goPool)](https://go.dev)

> **⬇️ [Download Pre-Built Releases](https://github.com/Distortions81/M45-Core-goPool/releases)** - Ready-to-run binaries for Linux, macOS, and Windows. See [RELEASES.md](RELEASES.md) for details.

## About

goPool is a solo Bitcoin mining pool that connects directly to Bitcoin Core (`bitcoind`) over JSON-RPC and ZMQ. It serves miners via Stratum v1 (with optional TLS) and provides a web-based status UI with JSON APIs for monitoring.

**Key Features:**
- Stratum v1 mining protocol with optional TLS support
- Direct Bitcoin Core integration via JSON-RPC and ZMQ
- Configurable coinbase splits (pool fee, operator donation, miner payout)
- Web status UI with HTTPS-first design and auto-generated certificates
- Automated backups with optional Backblaze B2 cloud storage
- Rate limiting, invalid-submission bans, and reconnect churn protection
- Comprehensive test suite with compatibility checks

## Build

**Requirements:**
- Bitcoin Core with RPC enabled and ZMQ support recommended
- Go toolchain (see [go.mod](go.mod) for version)
- ZeroMQ library and headers (for `github.com/pebbe/zmq4`)

### Installing Dependencies

**Install Go (all distributions):**

goPool requires a recent Go version. Always use the official golang.org release rather than distribution packages.

```bash
# Download and install Go (check https://go.dev/dl/ for latest version)
wget https://go.dev/dl/go1.24.0.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.24.0.linux-amd64.tar.gz

# Add to PATH
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc

# Verify installation
go version
```

**Install ZeroMQ:**

**Ubuntu / Debian:**
```bash
sudo apt update
sudo apt install -y libzmq3-dev
```

**Fedora / RHEL / CentOS:**
```bash
sudo dnf install -y zeromq-devel
```

**Arch Linux:**
```bash
sudo pacman -S zeromq
```

**Alpine Linux:**
```bash
apk add --no-cache zeromq-dev build-base
```

### Build & Run

```bash
# Clone the repository (if not already cloned)
git clone https://github.com/Distortions81/M45-Core-goPool.git
cd M45-Core-goPool

# Run directly
go run .

# Or build binary
go build -o goPool
./goPool
```

On first run, goPool will generate example configuration files in `data/config/examples/` and exit with setup instructions.

### Build Tags

goPool supports several build tags for different configurations:

| Build Tag | Purpose | Default | Fallback |
|-----------|---------|---------|----------|
| `debug` | Enable debug logging + network traffic logs | Off | N/A |
| `verbose` | Enable verbose logging (no network logs) | Off | N/A |
| `noavx` | Disable hardware-accelerated SHA256 | Uses [`sha256-simd`](https://github.com/minio/sha256-simd) | [`crypto/sha256`](https://pkg.go.dev/crypto/sha256) |
| `nojsonsimd` | Disable hardware-accelerated JSON | Uses [`sonic`](https://github.com/bytedance/sonic) | [`encoding/json`](https://pkg.go.dev/encoding/json) |

**Logging Levels:**
```bash
# Debug build with network traffic logging
go build -tags debug -o goPool

# Verbose build without network traffic logging
go build -tags verbose -o goPool
```

**Disable Hardware-Accelerated SHA256:**

By default, goPool uses [`github.com/minio/sha256-simd`](https://github.com/minio/sha256-simd) which provides hardware-accelerated SHA256 hashing:
- **x86/x64:** AVX512, SHA Extensions, AVX2
- **ARM64:** Cryptography Extensions (ARMv8)
- **Fallback:** Pure Go implementation on unsupported platforms

The library automatically selects the best implementation at runtime. However, you can force the use of standard [`crypto/sha256`](https://pkg.go.dev/crypto/sha256):

1. **Build without sha256-simd:**
```bash
# Disable sha256-simd at build time
go build -tags noavx -o goPool
```

2. **Use runtime flag:**
```bash
# Disable sha256-simd at runtime
./goPool -sha256-no-avx
```

**When to disable:** Only needed for debugging, benchmarking the standard library, or if sha256-simd causes issues. The library handles unsupported platforms automatically.

**Disable Hardware-Accelerated JSON:**

By default, goPool uses [`github.com/bytedance/sonic`](https://github.com/bytedance/sonic) for faster JSON encoding/decoding with JIT compilation and SIMD:
- **Supported:** Linux, MacOS, Windows on AMD64 or ARM64
- **Fallback:** Standard [`encoding/json`](https://pkg.go.dev/encoding/json) on unsupported platforms

```bash
# Disable sonic (forces encoding/json)
go build -tags nojsonsimd -o goPool
```

**Combining Build Tags:**
```bash
# Debug build without SIMD acceleration
go build -tags "debug noavx nojsonsimd" -o goPool

# Verbose build without SIMD acceleration
go build -tags "verbose noavx nojsonsimd" -o goPool

# Disable only JSON SIMD, keep SHA256 SIMD
go build -tags nojsonsimd -o goPool

# Disable only SHA256 SIMD, keep JSON SIMD
go build -tags noavx -o goPool
```

## Configuration

### Quick Start Configuration

After your first run, goPool generates example configs in `data/config/examples/`. Copy and edit them:

```bash
cp data/config/examples/config.toml.example data/config/config.toml
nano data/config/config.toml
```

**Minimum Required Settings:**

1. **Payout Address** - Where your mining rewards go:
   ```toml
   [node]
   payout_address = "YOUR_BITCOIN_ADDRESS"
   ```

2. **Bitcoin Core Connection** - Update if not using defaults:
   ```toml
   [node]
   rpc_url = "http://127.0.0.1:8332"
   zmq_block_addr = "tcp://127.0.0.1:28332"
   ```

**Optional but Recommended:**

- Set `server.status_public_url` to your pool's public URL for proper redirects
- Configure `branding.status_brand_name` and `branding.status_tagline` for UI customization
- Set `mining.pool_fee_percent` (default 2.0%)

See [operations.md](operations.md) for detailed configuration options.

### Config Files

goPool uses three TOML configuration files in `data/config/`:

- **`config.toml`** (required) - Primary pool configuration
- **`secrets.toml`** (optional) - Sensitive credentials (Discord tokens, Clerk keys, RPC auth)
- **`tuning.toml`** (optional) - Performance tuning overrides

If `config.toml` is missing, goPool generates examples and exits. Copy and customize `data/config/examples/config.toml` to get started.

### Bitcoin Core Connection

**RPC Authentication:**
- Preferred: Cookie file via `node.rpc_cookie_path`
- Autodetection: goPool searches `$BITCOIN_DATADIR` and common locations
- Override: Use `-rpc-cookie-path` flag for single-run override
- Alternative: Use `-allow-rpc-credentials` with `rpc_user`/`rpc_pass` in `secrets.toml` (insecure, not recommended)
- Public RPC: Set `node.allow_public_rpc = true` for unauthenticated endpoints (understand security implications)

**ZMQ Configuration:**
Add to your `bitcoin.conf`:
```conf
zmqpubrawblock=tcp://127.0.0.1:28332
zmqpubhashblock=tcp://127.0.0.1:28332
zmqpubrawtx=tcp://127.0.0.1:28332
zmqpubhashtx=tcp://127.0.0.1:28332
```

Then set in `config.toml`:
```toml
[node]
zmq_block_addr = "tcp://127.0.0.1:28332"
rpc_url = "http://127.0.0.1:8332"
```

To disable ZMQ and use RPC-only mode, use the `-no-zmq` flag.

### Network Listeners

Default ports in `config.toml`:
- Stratum (plain): `:3333`
- Stratum (TLS): `:4333`
- Status UI (HTTP): `:8080`
- Status UI (HTTPS): `:4443`

**TLS Certificates:**
- Auto-generated: goPool creates self-signed certs on first run (`data/tls_cert.pem`, `data/tls_key.pem`)
- Custom certs: Replace these files with your own (e.g., from Let's Encrypt)
- Auto-reload: Certificates reload hourly automatically
- Helper script: [scripts/certbot-gopool.sh](scripts/certbot-gopool.sh) automates certbot integration

**HTTPS-First Mode:**
By default (`-https-only=true`), HTTP serves only a safe subset and redirects to HTTPS. Set `server.status_public_url` to your final public URL for proper redirects and session cookies.

## Operation

### Running the Pool

**Point miners at Stratum:**
- Plain: `stratum+tcp://<pool-host>:3333`
- TLS: `stratum+ssl://<pool-host>:4333`

**Runtime Configuration Reload:**
- `SIGUSR1` - Reload HTML templates
- `SIGUSR2` - Reload config/secrets/tuning (status pages only; restart required for listener/auth changes)

**Command-Line Flags:**
- `-rpc-cookie-path <path>` - Override RPC cookie location
- `-rpc-url <url>` - Override RPC URL
- `-allow-rpc-credentials` - Enable `rpc_user`/`rpc_pass` from secrets.toml
- `-no-zmq` - Disable ZMQ, use RPC/longpoll only
- `-https-only=false` - Disable HTTPS-first mode

### Monitoring

**Status UI:**
- Access at `https://<pool-host>:4443` (default)
- Templates in `data/templates/`
- JSON API endpoints under `/api/*`

**Data Storage:**
- Worker database: `data/state/workers.db`
- Automatic local snapshots for safe backups
- Optional Backblaze B2 uploads (configure `[backblaze_backup]` section)

### Additional Documentation

- [RELEASES.md](RELEASES.md) - Pre-built release packages and upgrade guide
- [operations.md](operations.md) - Detailed configuration, flags, API reference, logging
- [performance.md](performance.md) - Capacity planning and benchmark data
- [TESTING.md](TESTING.md) - Test suite overview and instructions
- [data/config/examples/autogen.md](data/config/examples/autogen.md) - Config generation details

### Useful Scripts

- [scripts/install-bitcoind.sh](scripts/install-bitcoind.sh) - Bootstrap local bitcoind with ZMQ
- [scripts/run-tests.sh](scripts/run-tests.sh) - Run complete Go test suite
- [scripts/profile-graph.sh](scripts/profile-graph.sh) - Generate CPU profile visualization
- [scripts/certbot-gopool.sh](scripts/certbot-gopool.sh) - Automate Let's Encrypt certificate setup
