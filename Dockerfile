# syntax=docker/dockerfile:1

FROM golang:1.26.1-bookworm AS builder

WORKDIR /src

# Install ZeroMQ build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    libzmq3-dev \
    pkg-config \
 && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG BUILD_TIME=unknown
ARG BUILD_VERSION=v0.0.0-dev

RUN CGO_ENABLED=1 go build \
    -trimpath \
    -ldflags="-s -w -X main.buildTime=${BUILD_TIME} -X main.buildVersion=${BUILD_VERSION}" \
    -o /out/goPool .

FROM debian:bookworm-slim

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    libzmq5 \
 && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/goPool /app/goPool
COPY data /app/data
COPY documentation /app/documentation

EXPOSE 3333 80 443

ENTRYPOINT ["/app/goPool"]