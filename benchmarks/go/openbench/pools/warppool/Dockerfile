# syntax=docker/dockerfile:1

FROM rust:slim-bookworm AS builder

WORKDIR /src
COPY . .

# Build the tagged source unchanged. In particular, retain the shipped
# connection-cap, persistence, rate-limit, and validation implementations.
RUN cargo build --locked --release -p dvb-warppool-daemon

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    tini \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd --system --gid 1000 warppool \
 && useradd --system --uid 1000 --gid warppool --home /opt/dvb-warppool warppool \
 && mkdir -p /config /data \
 && chown -R warppool:warppool /config /data

COPY --from=builder /src/target/release/dvb-warppool-daemon /usr/local/bin/

USER warppool
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["sh", "-ec", "umask 077; printf 'rpc_user = \"%s\"\nrpc_pass = \"%s\"\n' \"$WARPPOOL_RPC_USER\" \"$WARPPOOL_RPC_PASS\" > /tmp/secrets.toml; exec dvb-warppool-daemon --config /config/config.toml --secrets /tmp/secrets.toml --data-dir /data --stratum-listen 0.0.0.0:3333 --api-listen 0.0.0.0:18334 --no-tls"]
