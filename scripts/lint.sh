#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

fail=0

echo "go vet ./..."
if ! go vet ./...; then
  fail=1
fi

echo "staticcheck ./..."
if ! go run honnef.co/go/tools/cmd/staticcheck@latest ./...; then
  fail=1
fi

echo "unparam ./..."
if ! go run mvdan.cc/unparam@latest ./...; then
  fail=1
fi

exit "$fail"
