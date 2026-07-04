# open-pool-benchmark overlay

This directory contains the goPool overlay for
[`eandersson/open-pool-benchmark`](https://github.com/eandersson/open-pool-benchmark).
The benchmark harness itself is cloned by `scripts/open-pool-benchmark.sh`; this
repo keeps only the reproducible pool registry and goPool-specific config.

## Quick start

Docker with the Compose plugin is required.

```bash
./scripts/open-pool-benchmark.sh list
./scripts/open-pool-benchmark.sh suite --pools gopool,pogolo,ckpool
./scripts/open-pool-benchmark.sh report
```

For the standard comparison table, run:

```bash
./scripts/open-pool-benchmark-report.sh
```

It runs the sweep matrix `1x1x1`, then `1/4/16/64 connections` with
`workers=4,pipeline=16`, and writes a timestamped `.txt` table plus `.csv` under
`reports/open-pool-benchmark/`.

The script clones the upstream harness into `.benchmarks/open-pool-benchmark`,
copies this overlay into it, and forwards all arguments to:

```bash
docker compose run --rm openbench "$@"
```

Results are written under `.benchmarks/open-pool-benchmark/results/`.

## goPool profile

`pools/gopool/bench.toml` is intentionally tuned for low latency and a lean local
benchmark:

- Stratum binds on `:3333`; status HTTP/API and TLS listeners are disabled by
  runtime flags during benchmark runs.
- RPC and ZMQ point at the shared regtest bitcoind from open-pool-benchmark.
- VarDiff is enabled with the active profile's min/default/max difficulty.
- Submit processing is inline to remove worker-queue latency from the hot path.
- Duplicate-share checks stay enabled so the validation path remains realistic.
- Connection rate limits are disabled for trusted local benchmark runs.
- Socket buffers are set explicitly to small, low-latency values.
- The benchmark wrapper starts goPool with `-status off`, `-status-tls off`,
  `-stratum-tls off`, and `-no-json`; readiness uses the Stratum TCP socket.

The wrapper image copies the single rendered TOML file to goPool's
`config.toml`, `policy.toml`, `tuning.toml`, and `secrets.toml`. Each loader reads
only the sections it understands, while `secrets.toml` reads the top-level
`rpc_user` and `rpc_pass` needed for the regtest node.
