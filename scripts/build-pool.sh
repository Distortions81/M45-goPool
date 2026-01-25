#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
BUILD_VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
echo "Building pool with buildVersion=${BUILD_VERSION} buildTime=${BUILD_TIME}"

go build -ldflags="-X main.buildTime=${BUILD_TIME} -X main.buildVersion=${BUILD_VERSION}" -o gopool .

echo "Built ./gopool"
