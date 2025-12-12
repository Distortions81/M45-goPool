#!/usr/bin/env bash
set -euo pipefail

# Run the full goPool test suite with verbose output.
# Additional arguments are passed through to `go test`.

cd "$(dirname "${BASH_SOURCE[0]}")/.."

echo "Running go test -v -count=1 ./... $*"
go test -v -count=1 ./... "$@"
