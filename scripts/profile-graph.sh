#!/usr/bin/env bash
set -euo pipefail

# Simple helper to render a CPU profile (default.pgo by default) as a
# Graphviz SVG using go tool pprof and dot.

PROFILE=${1:-default.pgo}
OUT=${2:-profile.svg}

if ! command -v go >/dev/null 2>&1; then
  echo "go not found on PATH" >&2
  exit 1
fi

if ! command -v dot >/dev/null 2>&1; then
  echo "Graphviz 'dot' binary not found on PATH" >&2
  exit 1
fi

if [ ! -f "$PROFILE" ]; then
  echo "profile file not found: $PROFILE" >&2
  exit 1
fi

echo "Rendering $PROFILE -> $OUT ..."
go tool pprof -dot "$PROFILE" ./gopool | dot -Tsvg -o "$OUT"
echo "Done. Open $OUT in a browser or SVG viewer."

