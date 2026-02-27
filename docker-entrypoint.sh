#!/bin/bash
set -e

# Sync web assets, templates, and example configs from the image on every boot.
# This ensures updates are picked up after image upgrades without overwriting
# user-edited config/state/logs (those are mounted volumes).
cp -a /app/defaults/www/. /app/data/www/
cp -a /app/defaults/templates/. /app/data/templates/
mkdir -p /app/data/config/examples
cp -a /app/defaults/config/examples/. /app/data/config/examples/

CONFIG_DIR="/app/data/config"
CONFIG_FILE="${CONFIG_DIR}/config.toml"
SECRETS_FILE="${CONFIG_DIR}/secrets.toml"

# Generate config.toml from environment on first run
if [ ! -f "${CONFIG_FILE}" ]; then
    echo "Generating config.toml from environment..."

    PAYOUT_ADDR="${PAYOUT_ADDRESS:-YOUR_POOL_WALLET_ADDRESS_HERE}"
    RPC_URL="${BITCOIN_RPC_URL:-http://127.0.0.1:8332}"
    RPC_COOKIE="${BITCOIN_RPC_COOKIE_PATH:-}"
    ZMQ_HASH="${ZMQ_HASHBLOCK_ADDR:-tcp://127.0.0.1:28334}"
    ZMQ_RAW="${ZMQ_RAWBLOCK_ADDR:-tcp://127.0.0.1:28332}"
    POOL_PORT="${POOL_LISTEN:-:3333}"
    STATUS_PORT="${STATUS_LISTEN:-:8580}"
    POOL_NAME="${POOL_BRAND_NAME:-goPool}"
    POOL_FEE="${POOL_FEE_PERCENT:-2.0}"

    cat > "${CONFIG_FILE}" <<EOF
[branding]
  fiat_currency = "usd"
  pool_donation_address = ""
  server_location = ""
  status_brand_domain = ""
  status_brand_name = "${POOL_NAME}"
  status_tagline = "Solo Mining Pool"

[logging]
  debug = false
  net_debug = false

[mining]
  pool_fee_percent = ${POOL_FEE}

[node]
  payout_address = "${PAYOUT_ADDR}"
  rpc_cookie_path = "${RPC_COOKIE}"
  rpc_url = "${RPC_URL}"
  zmq_hashblock_addr = "${ZMQ_HASH}"
  zmq_rawblock_addr = "${ZMQ_RAW}"

[server]
  pool_listen = "${POOL_PORT}"
  status_listen = "${STATUS_PORT}"
  status_public_url = ""
  status_tls_listen = ""

[stratum]
  safe_mode = false
  stratum_tls_listen = ""
EOF

    echo "Config generated at ${CONFIG_FILE}"
fi

# Generate secrets.toml from environment on first run
if [ ! -f "${SECRETS_FILE}" ]; then
    RPC_USER="${BITCOIN_RPC_USER:-}"
    RPC_PASS="${BITCOIN_RPC_PASS:-}"

    if [ -n "${RPC_USER}" ] && [ -n "${RPC_PASS}" ]; then
        echo "Generating secrets.toml with RPC credentials..."
        cat > "${SECRETS_FILE}" <<EOF
rpc_user = "${RPC_USER}"
rpc_pass = "${RPC_PASS}"
EOF
        chmod 600 "${SECRETS_FILE}"
        echo "Secrets generated at ${SECRETS_FILE}"
    fi
fi

# Run with automatic restart on crash
RESTART_DELAY="${RESTART_DELAY:-5}"
while true; do
    echo "Starting goPool..."
    "$@" && exit 0
    EXIT_CODE=$?
    echo "goPool exited with code ${EXIT_CODE}, restarting in ${RESTART_DELAY}s..."
    sleep "${RESTART_DELAY}"
done
