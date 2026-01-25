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

set_toml_value() {
  local section="$1"
  local key="$2"
  local value_literal="$3" # TOML literal, e.g. \"foo\" or 123 or true

  if command -v python3 >/dev/null 2>&1; then
    python3 - "$CFG_FILE" "$section" "$key" "$value_literal" <<'PY'
import re
import sys

path, section, key, value = sys.argv[1:5]
lines = open(path, "r", encoding="utf-8").read().splitlines(True)

section_header = f"[{section}]"
in_section = False
section_start = None
section_end = None

for i, line in enumerate(lines):
    if re.match(r"^\s*\[.*\]\s*$", line):
        if line.strip() == section_header:
            in_section = True
            section_start = i
            continue
        if in_section:
            section_end = i
            break

if section_start is None:
    # Append missing section.
    if lines and not lines[-1].endswith("\n"):
        lines[-1] += "\n"
    if lines and lines[-1].strip() != "":
        lines.append("\n")
    lines.append(section_header + "\n")
    lines.append("\n")
    section_start = len(lines) - 2
    section_end = len(lines)

if section_end is None:
    section_end = len(lines)

key_re = re.compile(rf"^(\s*){re.escape(key)}\s*=")
for i in range(section_start + 1, section_end):
    m = key_re.match(lines[i])
    if m:
        indent = m.group(1) if m.group(1) else "  "
        lines[i] = f"{indent}{key} = {value}\n"
        open(path, "w", encoding="utf-8").write("".join(lines))
        sys.exit(0)

# Insert at end of section, before trailing blank lines.
insert_at = section_end
while insert_at > section_start + 1 and lines[insert_at - 1].strip() == "":
    insert_at -= 1

lines.insert(insert_at, f"  {key} = {value}\n")
open(path, "w", encoding="utf-8").write("".join(lines))
PY
    return
  fi

  # Fallback: best-effort in-place replace when python3 is unavailable.
  if command -v perl >/dev/null 2>&1; then
    perl -pi -e "if (\$s) { if (/^\\s*\\[/) { \$s=0 } elsif (/^\\s*${key}\\s*=/) { s|^\\s*${key}\\s*=.*\\\$|  ${key} = ${value_literal}| } } if (/^\\[${section}\\]\\s*\\\$/) { \$s=1 }" "${CFG_FILE}"
  else
    echo "WARN: python3/perl not found; unable to auto-edit ${CFG_FILE} safely." >&2
  fi
}

# Choose unprivileged local ports and avoid collisions.
STATUS_HTTP_PORT="${STATUS_HTTP_PORT:-8080}"
STATUS_HTTPS_PORT="${STATUS_HTTPS_PORT:-8443}"
HTTP_ONLY="${HTTP_ONLY:-0}"

port_free() {
  local port="$1"
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$port" <<'PY'
import socket, sys
port = int(sys.argv[1])
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
try:
    s.bind(("127.0.0.1", port))
    sys.exit(0)
except OSError:
    sys.exit(1)
finally:
    s.close()
PY
    return $?
  fi
  # Best-effort fallback: assume free if we can't test.
  return 0
}

pick_port() {
  local port="$1"
  local max_tries=50
  local i=0
  while [ $i -lt $max_tries ]; do
    if port_free "$port"; then
      echo "$port"
      return 0
    fi
    port=$((port + 1))
    i=$((i + 1))
  done
  echo "$1"
}

STATUS_HTTP_PORT="$(pick_port "${STATUS_HTTP_PORT}")"
if [ "${HTTP_ONLY}" != "1" ]; then
  STATUS_HTTPS_PORT="$(pick_port "${STATUS_HTTPS_PORT}")"
fi

# Ensure the pool points at the local regtest node and avoids privileged ports.
set_toml_value "node" "payout_address" "\"${PAYOUT_ADDRESS}\""
set_toml_value "node" "rpc_url" "\"http://127.0.0.1:18443\""
set_toml_value "node" "rpc_cookie_path" "\"${COOKIE_PATH}\""
set_toml_value "node" "zmq_block_addr" "\"tcp://127.0.0.1:28332\""

set_toml_value "server" "status_listen" "\":${STATUS_HTTP_PORT}\""
if [ "${HTTP_ONLY}" = "1" ]; then
  set_toml_value "server" "status_tls_listen" "\"\""
  STATUS_PUBLIC_URL="${STATUS_PUBLIC_URL:-http://127.0.0.1:${STATUS_HTTP_PORT}}"
else
  set_toml_value "server" "status_tls_listen" "\":${STATUS_HTTPS_PORT}\""
  STATUS_PUBLIC_URL="${STATUS_PUBLIC_URL:-https://127.0.0.1:${STATUS_HTTPS_PORT}}"
fi
set_toml_value "server" "status_public_url" "\"${STATUS_PUBLIC_URL}\""

echo "==> Building goPool"
if [ ! -x "${REPO_ROOT}/goPool" ]; then
  go build -o "${REPO_ROOT}/goPool" .
fi

echo
echo "==> Starting goPool (regtest)"
if [ "${HTTP_ONLY}" = "1" ]; then
  echo "    status UI (http):  http://127.0.0.1:${STATUS_HTTP_PORT}"
else
  echo "    status UI (https): https://127.0.0.1:${STATUS_HTTPS_PORT}"
  echo "    status UI (http):  http://127.0.0.1:${STATUS_HTTP_PORT}"
fi
echo
echo "Stop bitcoind:"
echo "  ${BITCOIN_CLI} -regtest -datadir=${NODE_DATA} stop"
echo

if [ "${HTTP_ONLY}" = "1" ]; then
  exec "${REPO_ROOT}/goPool" -regtest -stdoutlog -https-only=false
fi
exec "${REPO_ROOT}/goPool" -regtest -stdoutlog
