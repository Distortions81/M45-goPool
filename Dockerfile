# --- Build stage ---
FROM golang:1.26-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    libzmq3-dev pkg-config gcc libc6-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG BUILD_TIME=""
ARG BUILD_VERSION=""
RUN CGO_ENABLED=1 go build \
    -ldflags="-s -w -X 'main.buildTime=${BUILD_TIME}' -X 'main.buildVersion=${BUILD_VERSION}'" \
    -trimpath -pgo=default.pgo \
    -o /goPool .

# --- Runtime stage ---
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    libzmq5 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN useradd -u 1000 -m gopool

WORKDIR /app

# Copy binary
COPY --from=builder /goPool /usr/local/bin/goPool

# Copy web assets, templates, and example configs to a staging area.
# These get synced into /app/data on each boot by the entrypoint.
COPY data/www/ /app/defaults/www/
COPY data/templates/ /app/defaults/templates/
COPY data/config/examples/ /app/defaults/config/examples/

# Copy entrypoint
COPY docker-entrypoint.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# Create data directories with correct ownership
# config, state, logs are mounted volumes; www and templates live in the image
RUN mkdir -p /app/data/config /app/data/state /app/data/logs /app/data/templates /app/data/www \
    && chown -R 1000:1000 /app

USER 1000

EXPOSE 3333 8580

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["goPool", "-data-dir", "/app/data", "-status", ":8580", "-status-tls", ""]
