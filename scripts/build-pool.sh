#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "Building pool with buildTime=${BUILD_TIME}"

go build -ldflags="-X main.buildTime=${BUILD_TIME}" -o gopool .

echo "Built ./gopool"

