#!/usr/bin/env bash
set -euo pipefail

# install-bitcoind.sh
# Helper script to install a *local, portable* Bitcoin Core (bitcoind) under
# ./bitcoin-node and write a basic bitcoin.conf tuned for goPool on
# the chosen network. Intended for testing/development only on generic Linux.
#
# Usage:
#   ./scripts/install-bitcoind.sh [mainnet|testnet|signet|regtest|regnet]
# Defaults to regtest when no network is provided.

NETWORK="${1:-regtest}"
if [ "${NETWORK}" = "regnet" ]; then
  NETWORK="regtest"
fi
case "${NETWORK}" in
  mainnet|testnet|signet|regtest) ;;
  *)
    echo "Usage: $0 [mainnet|testnet|signet|regtest|regnet]" >&2
    exit 1
    ;;
esac

# Install into a local node directory under the current repo.
NODE_ROOT="$(pwd)/bitcoin-node"
NODE_DATA="${NODE_ROOT}/data/${NETWORK}"
CONF_DIR="${NODE_DATA}"
CONF_FILE="${CONF_DIR}/bitcoin.conf"
mkdir -p "${CONF_DIR}"

# Bitcoin Core stores chain-specific state (and the RPC cookie) under a
# network subdirectory (e.g. regtest/, signet/, testnet3/).
case "${NETWORK}" in
  mainnet)
    CHAIN_DIR="${NODE_DATA}"
    ;;
  testnet)
    CHAIN_DIR="${NODE_DATA}/testnet3"
    ;;
  signet)
    CHAIN_DIR="${NODE_DATA}/signet"
    ;;
  regtest)
    CHAIN_DIR="${NODE_DATA}/regtest"
    ;;
esac
COOKIE_PATH="${CHAIN_DIR}/.cookie"

# Download a portable Bitcoin Core tarball into ./bitcoin-node if it is not
# already present, and expose bitcoind under ./bitcoin-node/bin.
BITCOIN_VERSION="${BITCOIN_VERSION:-27.0}"
ARCH="$(uname -m)"
case "${ARCH}" in
  x86_64|amd64)
    PKG_ARCH="x86_64-linux-gnu"
    ;;
  aarch64|arm64)
    PKG_ARCH="aarch64-linux-gnu"
    ;;
  *)
    echo "Unsupported architecture '${ARCH}'. Please install Bitcoin Core manually from https://bitcoincore.org/en/download/ and re-run this script." >&2
    exit 1
    ;;
esac

BIN_DIR="${NODE_ROOT}/bin"
BITCOIND="${BIN_DIR}/bitcoind"

if [ ! -x "${BITCOIND}" ]; then
  mkdir -p "${BIN_DIR}"
  TMP_DIR="${NODE_ROOT}/tmp"
  mkdir -p "${TMP_DIR}"
  TARBALL="bitcoin-${BITCOIN_VERSION}-${PKG_ARCH}.tar.gz"
  URL="https://bitcoincore.org/bin/bitcoin-core-${BITCOIN_VERSION}/${TARBALL}"
  echo "Downloading Bitcoin Core ${BITCOIN_VERSION} (${PKG_ARCH}) from:"
  echo "  ${URL}"
  if command -v curl >/dev/null 2>&1; then
    curl -L "${URL}" -o "${TMP_DIR}/${TARBALL}"
  elif command -v wget >/dev/null 2>&1; then
    wget -O "${TMP_DIR}/${TARBALL}" "${URL}"
  else
    echo "ERROR: Neither curl nor wget is available. Please install one of them or download Bitcoin Core manually." >&2
    exit 1
  fi

  echo "Extracting Bitcoin Core into ${NODE_ROOT}..."
  tar -xzf "${TMP_DIR}/${TARBALL}" -C "${TMP_DIR}"
  EXTRACTED_DIR="$(find "${TMP_DIR}" -maxdepth 1 -type d -name "bitcoin-${BITCOIN_VERSION}*" | head -n1)"
  if [ -z "${EXTRACTED_DIR}" ]; then
    echo "ERROR: Unable to locate extracted Bitcoin Core directory under ${TMP_DIR}" >&2
    exit 1
  fi
  cp -f "${EXTRACTED_DIR}/bin/"* "${BIN_DIR}/"
  chmod +x "${BIN_DIR}/bitcoind" "${BIN_DIR}/bitcoin-cli" || true
fi

if [ -f "${CONF_FILE}" ]; then
  backup="${CONF_FILE}.$(date +%Y%m%d-%H%M%S).bak"
  echo "Existing bitcoin.conf found; backing up to ${backup}"
  cp "${CONF_FILE}" "${backup}"
fi

AUTH_MODE="${BITCOIND_AUTH:-cookie}" # cookie (recommended) | userpass
RPC_USER="${BITCOIND_RPC_USER:-poolrpc}"
RPC_PASS="${BITCOIND_RPC_PASS:-}"
if [ "${AUTH_MODE}" = "userpass" ] && [ -z "${RPC_PASS}" ]; then
  if command -v openssl >/dev/null 2>&1; then
    RPC_PASS="$(openssl rand -hex 16)"
  else
    RPC_PASS="$(tr -dc 'A-Za-z0-9' </dev/urandom | head -c 32)"
  fi
fi

echo "Writing ${CONF_FILE} for ${NETWORK}..."
cat >"${CONF_FILE}" <<EOF
server=1
daemon=1

# Development-friendly RPC tuning: goPool can burst RPC calls (template refresh,
# longpoll, submitblock racing). Defaults are small; increase to avoid local 503s.
rpcthreads=16
rpcworkqueue=256

rpcallowip=127.0.0.1

	# ZMQ (miner-safe when bound to localhost). goPool uses block notifications to refresh templates quickly.
	zmqpubhashblock=tcp://127.0.0.1:28332
	zmqpubrawblock=tcp://127.0.0.1:28332
	
	EOF

case "${AUTH_MODE}" in
  cookie)
    # Default (recommended): cookie-based auth (writes ${COOKIE_PATH} after first start).
    ;;
  userpass)
    cat >>"${CONF_FILE}" <<EOF
rpcuser=${RPC_USER}
rpcpassword=${RPC_PASS}

EOF
    ;;
  *)
    echo "ERROR: Unknown BITCOIND_AUTH='${AUTH_MODE}' (expected 'cookie' or 'userpass')." >&2
    exit 1
    ;;
esac

case "${NETWORK}" in
  mainnet)
    # Mainnet uses default ports: 8332 (RPC), 8333 (P2P).
    cat >>"${CONF_FILE}" <<EOF
rpcbind=127.0.0.1
EOF
    ;;
  testnet)
    cat >>"${CONF_FILE}" <<EOF
[test]
rpcbind=127.0.0.1
rpcport=18332
EOF
    ;;
  signet)
    cat >>"${CONF_FILE}" <<EOF
[signet]
rpcbind=127.0.0.1
rpcport=38332
EOF
    ;;
  regtest)
    cat >>"${CONF_FILE}" <<EOF
[regtest]
rpcbind=127.0.0.1
rpcport=18443
wallet=testwallet
fallbackfee=0.0002
EOF
    ;;
esac

echo
echo "Bitcoin Core installed and configured."
echo "Node root: ${NODE_ROOT}"
echo "Data dir:  ${NODE_DATA}"
echo "Config:    ${CONF_FILE}"
echo "Auth:      ${AUTH_MODE}"
echo "Cookie:    ${COOKIE_PATH}"
if [ "${AUTH_MODE}" = "userpass" ]; then
  echo "RPC_USER=${RPC_USER}"
  echo "RPC_PASS=${RPC_PASS}"
fi

case "${NETWORK}" in
  mainnet)
    echo
    echo "Start mainnet node:"
    echo "  \"${BITCOIND}\" -daemon -datadir=\"${NODE_DATA}\""
    echo
    echo "Example pool config:"
    echo "  rpc_url:  \"http://127.0.0.1:8332\""
    echo "  node.rpc_cookie_path = \"${COOKIE_PATH}\""
    echo "  # If you used BITCOIND_AUTH=userpass, set data/config/secrets.toml and launch goPool with -allow-rpc-credentials."
    ;;
  testnet)
    echo
    echo "Start testnet node:"
    echo "  \"${BITCOIND}\" -daemon -testnet -datadir=\"${NODE_DATA}\""
    echo
    echo "Example pool config:"
    echo "  rpc_url:  \"http://127.0.0.1:18332\""
    echo "  node.rpc_cookie_path = \"${COOKIE_PATH}\""
    echo "  # If you used BITCOIND_AUTH=userpass, set data/config/secrets.toml and launch goPool with -allow-rpc-credentials."
    ;;
  signet)
    echo
    echo "Start signet node:"
    echo "  \"${BITCOIND}\" -daemon -signet -datadir=\"${NODE_DATA}\""
    echo
    echo "Example pool config:"
    echo "  rpc_url:  \"http://127.0.0.1:38332\""
    echo "  node.rpc_cookie_path = \"${COOKIE_PATH}\""
    echo "  # If you used BITCOIND_AUTH=userpass, set data/config/secrets.toml and launch goPool with -allow-rpc-credentials."
    ;;
  regtest)
    echo
    echo "Start regtest node (with wallet 'testwallet' auto-selected):"
    echo "  \"${BITCOIND}\" -daemon -regtest -datadir=\"${NODE_DATA}\""
    echo
    echo "Example pool config (matches config.toml.example in this repo):"
    echo "  rpc_url:  \"http://127.0.0.1:18443\""
    echo "  node.rpc_cookie_path = \"${COOKIE_PATH}\""
    echo "  # If you used BITCOIND_AUTH=userpass, set data/config/secrets.toml and launch goPool with -allow-rpc-credentials."
    ;;
esac

echo
echo "To run the pool against this node:"
case "${NETWORK}" in
  mainnet)
    echo "  go run main.go -mainnet -verbose"
    ;;
  testnet)
    echo "  go run main.go -testnet -verbose"
    ;;
  signet)
    echo "  go run main.go -signet -verbose"
    ;;
  regtest)
    echo "  go run main.go -regtest -verbose"
    ;;
esac
echo "  # Ensure node.rpc_cookie_path in data/config/config.toml points to ${COOKIE_PATH}."
echo "  # If you used BITCOIND_AUTH=userpass, fill data/config/secrets.toml and launch with -allow-rpc-credentials."
echo
echo "Remember to generate a payout address from the chosen network's wallet"
echo "and set it as PAYOUT_ADDRESS / payout_address in the pool config."
