#!/usr/bin/env bash
set -euo pipefail

# Convenience wrapper for HTTP-only local dev.
# - Disables the HTTPS status listener (server.status_tls_listen = "")
# - Runs goPool with -https-only=false so JSON endpoints work over HTTP
#
# Usage:
#   ./scripts/dev-regtest-http.sh [regtest|regnet]

HTTP_ONLY=1 exec ./scripts/dev-regtest.sh "${1:-regtest}"

