# syntax=docker/dockerfile:1

FROM golang:1.26.5-bookworm AS builder

WORKDIR /src

RUN apt-get update && apt-get install -y --no-install-recommends \
    libzmq3-dev \
    pkg-config \
 && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 go build \
    -trimpath \
    -ldflags="-s -w -X main.buildTime=openbench -X main.buildVersion=openbench" \
    -o /out/goPool .

FROM debian:bookworm-slim

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    libzmq5 \
 && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/goPool /app/goPool
COPY data /app/data
COPY benchmarks/go/openbench/pools/gopool/entrypoint.sh /usr/local/bin/gopool-openbench

RUN chmod +x /usr/local/bin/gopool-openbench

EXPOSE 3333 3334

ENTRYPOINT ["/usr/local/bin/gopool-openbench"]
