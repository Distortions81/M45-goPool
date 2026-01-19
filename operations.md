# Configuration & Operations

> **Quick Start:** See the [main README](README.md) for initial setup and build instructions.

This document covers detailed configuration options, operational procedures, and runtime management for goPool.

## Configuration Files

goPool uses three TOML configuration files located in `data/config/`:

- **`config.toml`** (required) - Primary pool configuration including ports, branding, payout address, RPC URL, difficulty, and fee settings
- **`secrets.toml`** (optional) - Sensitive credentials such as Discord tokens, Clerk keys, and optionally `rpc_user`/`rpc_pass` when using `-allow-rpc-credentials`
- **`tuning.toml`** (optional) - Advanced tuning and operational limits. Deleting this file reverts to built-in defaults. See [data/config/examples/tuning.toml.example](data/config/examples/tuning.toml.example)

**Branding Options:** The `config.toml` includes `discord_url` and `github_url` settings that control header and About page links.

## Database Backups

### Local Snapshots

goPool automatically creates periodic snapshots of `data/state/workers.db` for safe backups. These local snapshots work independently of cloud uploads.

**Configuration:**
- `keep_local_copy` (default `true`) - Stores a local snapshot in `data/state/`
- `snapshot_path` (default empty) - Override snapshot location (relative to `data_dir` if not absolute)
- `interval_seconds` (default `43200`) - Snapshot frequency (12 hours)

**Important:** When backing up outside of goPool (rsync/tar/system backups), always back up the snapshot file (`data/state/workers.db.bak`) rather than the live database. Copying a live SQLite database can produce corrupt backups. If you must back up `workers.db` directly, stop goPool first.

### Backblaze B2 Cloud Uploads (Optional)

Configure Backblaze B2 integration via the `[backblaze_backup]` section in `data/config/config.toml`. Uploads remain disabled until you set `enabled = true` and populate the required fields.

**Configuration Structure:**

In `config.toml`:
```toml
[backblaze_backup]
enabled = true
account_id = "your_account_id"
bucket = "your-bucket-name"  # must be lowercase
prefix = "gopool/"           # optional namespace prefix
interval_seconds = 43200     # 12 hours
```

In `secrets.toml`:
```toml
backblaze_application_key = "your_master_application_key"
backblaze_account_id = "key_id_from_backblaze"
```

**Important Notes:**
- Use the master application key (not the key's ID)
- The `backblaze_account_id` in secrets should match the Key ID from your Backblaze application key
- The bucket must already exist with write permissions configured
- Backblaze keeps every version unless you configure lifecycle rules
- Backup failures are logged but don't stop the pool
- Snapshot metadata is tracked in the `backup_state` table within `workers.db`

## Tuning Parameters

Configure advanced tuning in `data/config/tuning.toml`. See [data/config/examples/tuning.toml.example](data/config/examples/tuning.toml.example) for all available options.

**Key Parameters:**
- `hashrate_ema_tau_seconds` (default `600`) - Time constant in seconds for per-connection hashrate EMA. Larger values smooth reports but react slower (~10 minutes default)
- `ntime_forward_slack_seconds` (default `7000`) - How far miners may roll `ntime` beyond the template's `curtime`/`mintime`

## Command-Line Flags

### Performance Flags

- `-sha256-no-avx` (default `false`) - Disables AVX-accelerated `sha256-simd` backend, falling back to platform-independent `crypto/sha256`

### RPC Authentication Flags

- `-rpc-cookie-path <path>` - Explicitly set RPC cookie path at launch, skipping autodetection. Useful for temporary overrides or non-standard cookie locations
- `-rpc-url <url>` - Override the RPC URL from config
- `-allow-rpc-credentials` (default `false`) - Force goPool to use `rpc_user`/`rpc_pass` from `secrets.toml` instead of cookie auth. **Deprecated and insecure** - avoid when possible. Logs a warning on each launch

### ZMQ Flags

- `-no-zmq` - Disable ZMQ, use RPC/longpoll only

### HTTPS Flags

- `-https-only` (default `true`) - Enable HTTPS-first mode where HTTP serves only safe routes and redirects to HTTPS

## RPC Cookie Authentication

goPool prefers cookie-based authentication over username/password credentials.

### Cookie Path Resolution

1. **Explicit configuration:** Set `node.rpc_cookie_path` in `config.toml`
2. **Autodetection:** If empty, goPool searches in order:
   - `$BITCOIN_DATADIR`
   - btcd's `AppDataDir("btcd", false)/data` layout
   - Common Linux locations:
     - `~/.bitcoin/.cookie`
     - `/var/lib/bitcoin/.cookie`
     - `/home/bitcoin/.bitcoin/.cookie`
     - `/etc/bitcoin/.cookie`
   - Network-specific variants (regtest/testnet3/testnet4/signet/simnet)

3. **Directory support:** `node.rpc_cookie_path` can point to a directory; goPool will search for `.cookie` inside

4. **Persistence:** When a working cookie is found, goPool updates `config.toml` with the discovered path

### Public RPC Endpoints

Set `node.allow_public_rpc = true` to connect to intentionally unauthenticated endpoints (e.g., `https://bitcoin-rpc.publicnode.com`). When enabled with an empty `rpc_cookie_path`, goPool skips credential loading and connects without Basic auth. **Only use for public/testing nodes.**

## Bitcoin Core ZMQ Integration

goPool subscribes to Bitcoin Core ZMQ notifications via `node.zmq_block_addr` for fast block detection and status metrics. RPC longpoll handles frequent `getblocktemplate` updates (including `coinbasevalue` and transaction fees).

### ZMQ Topics

- **`rawblock`** (required) - Triggers job/template refresh on new blocks. Mining works with only this topic
- **`hashblock`** (optional) - Redundant refresh trigger, goPool can use either rawblock or hashblock
- **`rawtx` / `hashtx`** (optional) - Used for status UI metrics only

### Configuration

Bitcoin Core can publish each topic to different ZMQ endpoints. goPool expects **all subscribed topics on a single endpoint**, so configure them identically:

```conf
# bitcoin.conf
zmqpubrawblock=tcp://127.0.0.1:28332
zmqpubhashblock=tcp://127.0.0.1:28332
zmqpubrawtx=tcp://127.0.0.1:28332
zmqpubhashtx=tcp://127.0.0.1:28332
```

Then set in `config.toml`:
```toml
[node]
zmq_block_addr = "tcp://127.0.0.1:28332"
```

## Status UI & API

### Web Interface

HTML status pages are served from `data/templates/` using Go's `html/template` engine.

**Available Templates:**
- `overview.tmpl` - Main dashboard
- `worker_status.tmpl` - Per-worker statistics view
- `server.tmpl` - Server statistics
- `about.tmpl` - About page
- `pool.tmpl` - Pool information
- `node.tmpl` - Node information
- `layout.tmpl` - Shared layout wrapper

**Features:**
- Per-worker statistics: rolling hashrate, recent share window, ban status
- Pool-wide hashrate graph with client-side EMA smoothing
- Real-time metrics via JSON API endpoints

### JSON API Endpoints

- `/api/overview` - Pool overview and statistics
- `/api/pool-hashrate` - Historical hashrate data
- Additional endpoints available for monitoring and dashboard integration

## Runtime Reloading

### Template Reload (SIGUSR1)

Reload HTML templates without restarting the pool:

```bash
# Find the pool process ID
ps aux | grep goPool

# Send SIGUSR1 to reload templates
kill -SIGUSR1 <pid>

# Or if using systemd
systemctl kill -s SIGUSR1 gopool.service
```

If template parsing fails, the error is logged and old templates remain active.

### Configuration Reload (SIGUSR2)

Reload configuration files (`config.toml`, `secrets.toml`, `tuning.toml`) without restarting:

```bash
# Signal goPool to reload config
kill -SIGUSR2 <pid>

# Or via systemd
systemctl kill -s SIGUSR2 gopool.service
```

**What gets reloaded:**
- Branding settings
- Payout configuration
- Difficulty settings
- Rate-limit parameters
- Tuning parameters

**What requires a restart:**
- Network listeners (ports)
- Clerk authentication paths
- RPC connection settings

## Logging

Logs are stored in `data/logs/` (relative to `data_dir`):

- **`pool.log`** - Structured pool log. By default only errors are logged
- **`net-debug.log`** - Network traffic log (only with `-tags debug` build)
- **`errors.log`** - Error-only log for troubleshooting

### Build Tags

goPool supports multiple build tags that can be combined:

**Logging Levels:**
```bash
# Debug build with network traffic logging
go build -tags debug -o goPool

# Verbose build without network traffic logging
go build -tags verbose -o goPool
```

**Disable Hardware-Accelerated SHA256:**
```bash
# Build without sha256-simd (forces crypto/sha256)
go build -tags noavx -o goPool

# Combine with logging
go build -tags "debug noavx" -o goPool
go build -tags "verbose noavx" -o goPool
```

The `noavx` tag disables [`github.com/minio/sha256-simd`](https://github.com/minio/sha256-simd) and uses standard [`crypto/sha256`](https://pkg.go.dev/crypto/sha256) instead.

**About sha256-simd:** This library automatically detects and uses the best available instruction set:
- **x86/x64:** AVX512, SHA Extensions, AVX2
- **ARM64:** Cryptography Extensions (ARMv8)
- **Unsupported platforms:** Pure Go fallback

**When to use `noavx`:**
- Debugging or benchmarking the standard library implementation
- Working around rare sha256-simd issues
- **Not needed** for platforms lacking hardware acceleration (the library handles this automatically)

Alternatively, use the `-sha256-no-avx` runtime flag to disable hardware acceleration without rebuilding.

**Disable Hardware-Accelerated JSON:**
```bash
# Build without sonic (forces encoding/json)
go build -tags nojsonsimd -o goPool

# Combine multiple tags
go build -tags "debug noavx nojsonsimd" -o goPool
go build -tags "verbose noavx nojsonsimd" -o goPool
```

The `nojsonsimd` tag disables [`github.com/bytedance/sonic`](https://github.com/bytedance/sonic) and uses standard [`encoding/json`](https://pkg.go.dev/encoding/json) instead.

**About sonic:** Faster JSON encoding/decoding via JIT compilation and SIMD:
- **Supported:** Linux, MacOS, Windows on AMD64 or ARM64
- **Requirements:** Go 1.18+ (Go 1.20+ for ARM64)
- **Unsupported platforms:** Falls back to standard library

**When to use `nojsonsimd`:**
- Debugging JSON serialization issues
- Benchmarking standard library performance
- Working around sonic compatibility issues
