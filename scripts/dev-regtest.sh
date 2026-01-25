#!/usr/bin/env bash
set -euo pipefail

# dev-regtest.sh
# End-to-end local dev helper:
#   - installs a portable Bitcoin Core into ./bitcoin-node (if needed)
#   - starts bitcoind in regtest
#   - creates/loads a wallet and generates a payout address
#   - writes/patches goPool config for regtest + unprivileged ports
#   - builds and runs goPool in -regtest mode
#
# Usage:
#   ./scripts/dev-regtest.sh [regtest|regnet]

NETWORK="${1:-regtest}"
if [ "${NETWORK}" = "regnet" ]; then
  NETWORK="regtest"
fi
if [ "${NETWORK}" != "regtest" ]; then
  echo "Usage: $0 [regtest|regnet]" >&2
  exit 1
fi

REPO_ROOT="$(pwd)"
NODE_ROOT="${REPO_ROOT}/bitcoin-node"
NODE_DATA="${NODE_ROOT}/data/${NETWORK}"
CHAIN_DIR="${NODE_DATA}/regtest"
COOKIE_PATH="${CHAIN_DIR}/.cookie"
BIN_DIR="${NODE_ROOT}/bin"
BITCOIND="${BIN_DIR}/bitcoind"
BITCOIN_CLI="${BIN_DIR}/bitcoin-cli"
CONF_FILE="${NODE_DATA}/bitcoin.conf"

echo "==> Installing Bitcoin Core (local, portable)"
BITCOIND_AUTH="${BITCOIND_AUTH:-cookie}" ./scripts/install-bitcoind.sh "${NETWORK}"

if [ ! -x "${BITCOIND}" ] || [ ! -x "${BITCOIN_CLI}" ]; then
  echo "ERROR: Expected ${BITCOIND} and ${BITCOIN_CLI} to exist after install." >&2
  exit 1
fi

bitcoind_running() {
  "${BITCOIN_CLI}" -regtest -datadir="${NODE_DATA}" getblockchaininfo >/dev/null 2>&1
}

echo "==> Starting bitcoind (regtest)"
if ! bitcoind_running; then
  "${BITCOIND}" -regtest -datadir="${NODE_DATA}" -conf="${CONF_FILE}" -daemon
fi

echo "==> Waiting for RPC"
"${BITCOIN_CLI}" -regtest -datadir="${NODE_DATA}" -rpcwait getblockchaininfo >/dev/null

WALLET="testwallet"

wallet_loaded() {
  if command -v rg >/dev/null 2>&1; then
    "${BITCOIN_CLI}" -regtest -datadir="${NODE_DATA}" listwallets 2>/dev/null | rg -q "\"${WALLET}\""
  else
    "${BITCOIN_CLI}" -regtest -datadir="${NODE_DATA}" listwallets 2>/dev/null | grep -q "\"${WALLET}\""
  fi
}

wallet_exists() {
  if command -v rg >/dev/null 2>&1; then
    "${BITCOIN_CLI}" -regtest -datadir="${NODE_DATA}" listwalletdir 2>/dev/null | rg -q "\"name\"\\s*:\\s*\"${WALLET}\""
  else
    "${BITCOIN_CLI}" -regtest -datadir="${NODE_DATA}" listwalletdir 2>/dev/null | grep -q "\"name\"[[:space:]]*:[[:space:]]*\"${WALLET}\""
  fi
}

echo "==> Ensuring wallet '${WALLET}'"
if ! wallet_exists; then
  "${BITCOIN_CLI}" -regtest -datadir="${NODE_DATA}" createwallet "${WALLET}" >/dev/null
fi
if ! wallet_loaded; then
  "${BITCOIN_CLI}" -regtest -datadir="${NODE_DATA}" loadwallet "${WALLET}" >/dev/null 2>&1 || true
fi

echo "==> Generating payout address"
PAYOUT_ADDRESS="$("${BITCOIN_CLI}" -regtest -datadir="${NODE_DATA}" -rpcwallet="${WALLET}" getnewaddress "gopool" "bech32")"
echo "    payout_address=${PAYOUT_ADDRESS}"

HEIGHT="$("${BITCOIN_CLI}" -regtest -datadir="${NODE_DATA}" getblockcount)"
if [ "${HEIGHT}" -lt 101 ]; then
  TO_MINE=$((101 - HEIGHT))
  echo "==> Mining ${TO_MINE} blocks (matures coinbase)"
  "${BITCOIN_CLI}" -regtest -datadir="${NODE_DATA}" -rpcwallet="${WALLET}" generatetoaddress "${TO_MINE}" "${PAYOUT_ADDRESS}" >/dev/null
fi

echo "==> Preparing goPool config"
CFG_DIR="${REPO_ROOT}/data/config"
CFG_FILE="${CFG_DIR}/config.toml"
EXAMPLE_CFG="${CFG_DIR}/examples/config.toml.example"

mkdir -p "${CFG_DIR}"
if [ ! -f "${CFG_FILE}" ]; then
  cp "${EXAMPLE_CFG}" "${CFG_FILE}"
fi

backup_cfg() {
  local backup
  backup="${CFG_FILE}.$(date +%Y%m%d-%H%M%S).bak"
  cp "${CFG_FILE}" "${backup}"
  echo "    config backup: ${backup}"
}

backup_cfg

# Only patch the common defaults/placeholders to avoid clobbering an existing config.
perl -pi -e "s|^  payout_address = \"YOUR_POOL_WALLET_ADDRESS_HERE\"\\s*\$|  payout_address = \"${PAYOUT_ADDRESS}\"|g" "${CFG_FILE}"
perl -pi -e "s|^  rpc_url = \"http://127\\.0\\.0\\.1:8332\"\\s*\$|  rpc_url = \"http://127.0.0.1:18443\"|g" "${CFG_FILE}"
perl -pi -e "s|^  rpc_cookie_path = \"\"\\s*\$|  rpc_cookie_path = \"${COOKIE_PATH}\"|g" "${CFG_FILE}"
perl -pi -e "s|^  zmq_block_addr = \"tcp://127\\.0\\.0\\.1:28332\"\\s*\$|  zmq_block_addr = \"tcp://127.0.0.1:28332\"|g" "${CFG_FILE}"

# Unprivileged local ports (avoid :80/:443).
perl -pi -e "s|^  status_listen = \":80\"\\s*\$|  status_listen = \":8080\"|g" "${CFG_FILE}"
perl -pi -e "s|^  status_tls_listen = \":443\"\\s*\$|  status_tls_listen = \"\"|g" "${CFG_FILE}"

echo "==> Building goPool"
if [ ! -x "${REPO_ROOT}/goPool" ]; then
  go build -o "${REPO_ROOT}/goPool" .
fi

echo
echo "==> Starting goPool (regtest)"
echo "    status UI: http://127.0.0.1:8080"
echo
echo "Stop bitcoind:"
echo "  ${BITCOIN_CLI} -regtest -datadir=${NODE_DATA} stop"
echo

exec "${REPO_ROOT}/goPool" -regtest -stdoutlog -https-only=false
