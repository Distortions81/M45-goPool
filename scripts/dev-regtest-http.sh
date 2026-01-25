#!/usr/bin/env bash
set -euo pipefail

# Convenience wrapper for HTTP-only local dev.
# - Sets server.status_tls_listen = "" (disables HTTPS listener)
# - Runs goPool with -http-only so JSON endpoints work over HTTP
#
# Usage:
#   ./scripts/dev-regtest-http.sh [regtest|regnet]

HTTP_ONLY=1 exec ./scripts/dev-regtest.sh "${1:-regtest}"
