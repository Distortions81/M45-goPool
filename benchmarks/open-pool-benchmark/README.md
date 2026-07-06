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

## Go submit hot path

The tracked Go probe in `tools/go-submit-bench/` measures post-handshake
`mining.submit` round-trip latency and throughput. It subscribes, authorizes,
receives work, then submits above-network-target nonces so pools validate and
reject shares without submitting blocks.

Latest unpinned run on a 32-logical-CPU host:

```text
  pool  conns  pipeline  validated/s  p50_ms  p95_ms  p99_ms  max_ms
gopool      1         1        38025   0.021   0.026   0.035   1.514
gopool      1        16       108958   0.103   0.164   0.212   3.688
gopool      4        16       340431   0.153   0.206   0.336   3.809
gopool     16        16       749352   0.163   0.548   0.930   4.300
gopool     64        16       917377   0.587   2.793   5.084  21.292
pogolo      1         1        42903   0.018   0.022   0.030   1.120
pogolo      1        16        91598   0.143   0.208   0.344   2.979
pogolo      4        16       232024   0.224   0.382   0.734  17.260
pogolo     16        16       298483   0.554   2.129   3.035  12.300
pogolo     64        16       296070   1.522  13.667  40.047 275.111
ckpool      1         1        11654   0.079   0.094   0.105   0.800
ckpool      1        16        76169   0.165   0.249   0.296   2.485
ckpool      4        16       123340   0.476   0.773   0.946   2.219
ckpool     16        16       131062   1.938   2.414   2.849   5.131
ckpool     64        16       127200   8.057   8.874   9.471  14.011
```

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
   100     2.4     2.4     2.7     2.7     2.8
  1000     3.0     3.0     3.5     3.6     3.6
 10000     8.9     8.7    14.7    14.9    15.0
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

With pool-side ZMQ disabled for all three pools:

```text
  pool  miners   avg_ms   p50_ms   p95_ms   p99_ms   max_ms
gopool     100      2.4      2.4      2.7      2.7      2.8
gopool    1000      3.0      3.0      3.5      3.6      3.6
gopool   10000      8.9      8.7     14.7     14.9     15.0
pogolo     100     99.2     99.0     99.8     99.9     99.9
pogolo    1000     93.1     93.1     93.9     93.9     93.9
pogolo   10000     69.2     69.2     75.6     76.2     76.4
ckpool     100  13968.0  13968.0  13968.5  13968.6  13968.6
ckpool    1000  14075.6  14075.7  14080.8  14081.2  14081.2
ckpool   10000  13600.0  13597.9  13656.4  13660.6  13661.8
```

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
