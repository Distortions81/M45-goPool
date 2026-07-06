#!/bin/sh
set -eu

rendered=/openbench/gopool.toml
config_dir=/app/data/config

if [ ! -f "$rendered" ]; then
  echo "missing rendered openbench config: $rendered" >&2
  exit 1
fi

mkdir -p "$config_dir" /app/data/logs /app/data/state

cp "$rendered" "$config_dir/config.toml"
cp "$rendered" "$config_dir/policy.toml"
cp "$rendered" "$config_dir/tuning.toml"
cp "$rendered" "$config_dir/secrets.toml"
chmod 600 "$config_dir/secrets.toml"

exec /app/goPool \
  -network regtest \
  -allow-rpc-creds \
  -secrets "$config_dir/secrets.toml" \
  -status-tls off \
  -stratum-tls off \
  -stdout
