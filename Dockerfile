# syntax=docker/dockerfile:1.5
FROM --platform=$BUILDPLATFORM golang:1.26-bullseye AS builder

ARG GOPOOL_REPO=https://github.com/Distortions81/M45-Core-goPool.git
ARG GOPOOL_REF=main
ARG TARGETS="linux/amd64 linux/arm64"

WORKDIR /src

RUN git clone --depth 1 --branch ${GOPOOL_REF} ${GOPOOL_REPO} .

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    mkdir -p /outputs && \
    set -eux; \
    for target in ${TARGETS}; do \
      goos=${target%/*}; \
      goarch=${target##*/}; \
      CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
        go build -trimpath -ldflags="-s -w" -o /outputs/goPool-${goos}-${goarch} ./...; \
    done

FROM busybox:1.36.1 AS artifacts
COPY --from=builder /outputs /binaries
