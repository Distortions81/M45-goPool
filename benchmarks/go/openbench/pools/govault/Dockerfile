# syntax=docker/dockerfile:1

FROM golang:1.24-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/govault-openbench ./cmd/openbench

FROM debian:bookworm-slim

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/govault-openbench /usr/local/bin/govault-openbench

EXPOSE 10333

ENTRYPOINT ["/usr/local/bin/govault-openbench", "-config", "/openbench/govault.json"]
