# syntax=docker/dockerfile:1.7
FROM --platform=$TARGETPLATFORM golang:1.26-bookworm AS builder

ARG TARGETOS
ARG TARGETARCH
ARG BUILD_TIME=unknown
ARG BUILD_VERSION=dev

WORKDIR /src

RUN apt-get update && \
    apt-get install -y --no-install-recommends gcc pkg-config libzmq3-dev ca-certificates && \
    rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags="-s -w -X main.buildTime=${BUILD_TIME} -X main.buildVersion=${BUILD_VERSION}" \
    -o /out/goPool .

FROM debian:bookworm-slim

WORKDIR /app

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates libzmq5 && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/goPool /usr/local/bin/goPool
COPY data /app/data

EXPOSE 3333 8080

ENTRYPOINT ["goPool"]
