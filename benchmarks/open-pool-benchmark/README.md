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
gopool      1         1        38782   0.021   0.025   0.031   1.144
gopool      1        16       108679   0.105   0.167   0.205   3.682
gopool      4        16       342256   0.151   0.207   0.332   3.398
gopool     16        16       753410   0.163   0.539   0.909   3.777
gopool     64        16       934075   0.573   2.740   5.097  23.305
pogolo      1         1        42299   0.019   0.023   0.031   1.109
pogolo      1        16        93205   0.139   0.202   0.326   2.502
pogolo      4        16       232260   0.226   0.378   0.728  14.384
pogolo     16        16       293133   0.566   2.172   3.063  14.077
pogolo     64        16       296244   1.359  13.230  45.117 247.363
ckpool      1         1        12127   0.077   0.087   0.094   0.771
ckpool      1        16        76732   0.164   0.247   0.292   2.468
ckpool      4        16       123537   0.477   0.769   0.935   2.166
ckpool     16        16       132032   1.926   2.423   2.867   5.019
ckpool     64        16       129841   7.885   8.807   9.497  12.824
```

## New-block notify fanout

The tracked Go probe in `tools/go-notify-fanout/` measures the ZMQ/new-block
critical path from a regtest block trigger to connected Stratum clients receiving
fresh `mining.notify` work. It uses one goroutine per miner socket and records an
atomic timestamp when each client sees a new job id.

Latest unpinned run on a 32-logical-CPU host:

```text
miners    avg_ms  p50_ms  p95_ms  p99_ms  max_ms
   100     2.3     2.3     2.7     2.7     2.8
  1000     3.0     3.1     3.6     3.6     3.7
 10000    10.3    11.1    15.8    16.1    16.2
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
gopool     100     2.3     2.3     2.7     2.7     2.8
gopool    1000     3.0     3.1     3.6     3.6     3.7
gopool   10000    10.3    11.1    15.8    16.1    16.2
pogolo     100    61.3    61.3    61.7    61.7    61.7
pogolo    1000    62.9    62.9    63.2    63.3    63.3
pogolo   10000    71.8    67.8    80.9    81.4    81.5
ckpool     100    60.4    60.4    61.0    61.0    61.0
ckpool    1000    67.4    67.6    72.5    72.9    73.1
ckpool   10000   117.2   116.7   170.8   174.7   175.8
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
