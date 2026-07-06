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

## New-block notify fanout

The tracked Go probe in `tools/go-notify-fanout/` measures the ZMQ/new-block
critical path from a regtest block trigger to connected Stratum clients receiving
fresh `mining.notify` work. It uses one goroutine per miner socket and records an
atomic timestamp when each client sees a new job id.

Latest unpinned run on a 32-logical-CPU host:

```text
miners    avg_ms  p50_ms  p95_ms  p99_ms  max_ms
   100     2.3     2.2     2.7     2.7     2.7
  1000     2.9     3.0     3.5     3.6     3.6
 10000     9.1     8.4    15.0    15.3    15.3
```

With ZMQ disabled and goPool using RPC `getblocktemplate` longpoll only on the
same local regtest setup:

```text
miners  avg_ms  p50_ms  p95_ms  p99_ms  max_ms
   100     2.3     2.4     2.5     2.7     2.7
  1000     3.1     3.0     3.7     3.7     3.7
 10000     9.1     9.4    15.5    15.7    15.8
```

Using the same Go probe against the open-pool-benchmark pool set with ZMQ
enabled:

```text
  pool  miners  avg_ms  p50_ms  p95_ms  p99_ms  max_ms
gopool     100     2.3     2.2     2.7     2.7     2.7
gopool    1000     2.9     3.0     3.5     3.6     3.6
gopool   10000     9.1     8.4    15.0    15.3    15.3
pogolo     100    61.4    61.6    61.8    61.8    61.8
pogolo    1000    61.7    61.7    62.4    62.6    62.6
pogolo   10000    69.7    69.7    75.7    76.3    76.4
ckpool     100    61.5    61.5    62.0    62.1    62.1
ckpool    1000   173.9   174.0   179.3   179.8   179.9
ckpool   10000   282.6   283.9   335.6   340.0   341.3
```

For ckpool, run the probe with `--worker-suffix=false --ordered-handshake`
because ckpool expects an exact payout-address username and rejects authorize
requests sent before the subscribe response.

One way to reproduce it is to start an unpinned goPool benchmark environment and
leave it running:

```bash
cd .benchmarks/open-pool-benchmark
docker compose run --rm openbench notify-fanout --no-pin --keep \
  --pools gopool --conns "1" --rounds 1 --out ""
```

Then build and run the Go probe from the repository root:

```bash
go build -o /tmp/go-notify-fanout ./benchmarks/open-pool-benchmark/tools/go-notify-fanout

for conns in 100 1000 10000; do
  docker run --rm --network openbench-regtest_default \
    --ulimit nofile=65535:65535 \
    -v /tmp/go-notify-fanout:/probe:ro \
    debian:bookworm-slim /probe \
    --host openbench-pool --port 3333 \
    --address bcrt1qlk935ze2fsu86zjp395uvtegztrkaezawxx0wf \
    --rpc http://bitcoind:18443 \
    --rpc-user openbench --rpc-pass openbenchpass \
    --connections "$conns" --rounds 5 --batch 500
done
```

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
