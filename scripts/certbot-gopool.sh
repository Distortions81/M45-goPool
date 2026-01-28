#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

usage() {
  cat <<'EOF'
Usage:
  ./scripts/certbot-gopool.sh [options] --domain example.com [--domain www.example.com]

What it does:
  1) Runs certbot to obtain/renew an HTTP-01 certificate (webroot or standalone)
  2) Links (or copies) certbot output into goPool's expected paths:
       <data-dir>/tls_cert.pem  (from fullchain.pem)
       <data-dir>/tls_key.pem   (from privkey.pem)

Options:
  --www-dir PATH        webroot dir for ACME (default: ./data/www)
  -d, --domain DOMAIN   domain name (repeatable; first domain is the default cert name)
  --cert-name NAME      certbot "cert-name" (default: first --domain)

  --email EMAIL         ACME account email (recommended)
  --no-email            use certbot --register-unsafely-without-email

  --webroot             use certbot --webroot (default)
  --standalone          use certbot --standalone (requires port 80 and no other listener)

  --staging             use Let's Encrypt staging environment
  --dry-run             run a simulated renewal
  --force-renewal       force renewal even if not due

  --link-mode MODE      symlink (default) or copy
  --owner USER:GROUP    chown the resulting ./data/tls_*.pem (copy mode only; symlink uses -h)
  --restart-cmd CMD     run CMD after updating tls_*.pem (e.g. "systemctl restart gopool")
  --sync-only           skip certbot; only link/copy from /etc/letsencrypt/live/<cert-name>

Notes:
  - For HTTP-01, the domain must reach your server on port 80. If goPool isn't
    directly on :80, put a reverse proxy in front or use a different challenge.
  - goPool reads ./data/tls_cert.pem and ./data/tls_key.pem.
EOF
}

log() { echo "certbot-gopool: $*" >&2; }
die() { log "ERROR: $*"; exit 1; }

DATA_DIR="${DATA_DIR:-$(pwd)/data}"
WWW_DIR=""
DOMAINS=()
CERT_NAME=""
EMAIL=""
NO_EMAIL=0
MODE="webroot"
STAGING=0
DRY_RUN=0
FORCE_RENEWAL=0
LINK_MODE="symlink"
OWNER=""
RESTART_CMD=""
SYNC_ONLY=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --www-dir)
      WWW_DIR="${2:-}"; shift 2 ;;
    -d|--domain)
      DOMAINS+=("${2:-}"); shift 2 ;;
    --cert-name)
      CERT_NAME="${2:-}"; shift 2 ;;
    --email)
      EMAIL="${2:-}"; shift 2 ;;
    --no-email)
      NO_EMAIL=1; shift ;;
    --webroot)
      MODE="webroot"; shift ;;
    --standalone)
      MODE="standalone"; shift ;;
    --staging)
      STAGING=1; shift ;;
    --dry-run)
      DRY_RUN=1; shift ;;
    --force-renewal)
      FORCE_RENEWAL=1; shift ;;
    --link-mode)
      LINK_MODE="${2:-}"; shift 2 ;;
    --owner)
      OWNER="${2:-}"; shift 2 ;;
    --restart-cmd)
      RESTART_CMD="${2:-}"; shift 2 ;;
    --sync-only)
      SYNC_ONLY=1; shift ;;
    -h|--help)
      usage; exit 0 ;;
    *)
      die "unknown argument: $1 (try --help)" ;;
  esac
done

if [[ -z "${WWW_DIR}" ]]; then
  WWW_DIR="${DATA_DIR%/}/www"
fi

if [[ "${SYNC_ONLY}" -eq 0 ]]; then
  if [[ ${#DOMAINS[@]} -eq 0 ]]; then
    die "at least one --domain is required (try --help)"
  fi
  if [[ -z "${CERT_NAME}" ]]; then
    CERT_NAME="${DOMAINS[0]}"
  fi
  if [[ -z "${EMAIL}" && "${NO_EMAIL}" -eq 0 ]]; then
    die "provide --email (recommended) or --no-email"
  fi
else
  if [[ -z "${CERT_NAME}" ]]; then
    if [[ ${#DOMAINS[@]} -gt 0 ]]; then
      CERT_NAME="${DOMAINS[0]}"
    else
      die "--sync-only requires --cert-name (or at least one --domain)"
    fi
  fi
fi

TARGET_CERT="${DATA_DIR%/}/tls_cert.pem"
TARGET_KEY="${DATA_DIR%/}/tls_key.pem"

cleanup_paths=()
cleanup() {
  for p in "${cleanup_paths[@]}"; do
    rm -f -- "${p}" 2>/dev/null || true
  done
}
trap cleanup EXIT

if [[ "${SYNC_ONLY}" -eq 0 ]]; then
  if ! command -v certbot >/dev/null 2>&1; then
    die "certbot not found in PATH"
  fi

  mkdir -p "${WWW_DIR%/}/.well-known/acme-challenge"

  certbot_args=(certonly --non-interactive --agree-tos --preferred-challenges http --cert-name "${CERT_NAME}")
  if [[ -n "${EMAIL}" ]]; then
    certbot_args+=(--email "${EMAIL}")
  else
    certbot_args+=(--register-unsafely-without-email)
  fi

  case "${MODE}" in
    webroot)
      certbot_args+=(--webroot -w "${WWW_DIR}")
      ;;
    standalone)
      certbot_args+=(--standalone)
      ;;
    *)
      die "invalid mode: ${MODE} (expected webroot or standalone)"
      ;;
  esac

  if [[ "${STAGING}" -eq 1 ]]; then
    certbot_args+=(--staging)
  fi
  if [[ "${DRY_RUN}" -eq 1 ]]; then
    certbot_args+=(--dry-run)
  fi
  if [[ "${FORCE_RENEWAL}" -eq 1 ]]; then
    certbot_args+=(--force-renewal)
  fi

  for d in "${DOMAINS[@]}"; do
    if [[ -z "${d}" ]]; then
      die "empty --domain value"
    fi
    certbot_args+=(-d "${d}")
  done

  log "running: certbot ${certbot_args[*]}"
  certbot "${certbot_args[@]}"
fi

LIVE_DIR="/etc/letsencrypt/live/${CERT_NAME}"
SRC_CERT="${LIVE_DIR}/fullchain.pem"
SRC_KEY="${LIVE_DIR}/privkey.pem"

if [[ ! -r "${SRC_CERT}" ]]; then
  die "cannot read ${SRC_CERT} (wrong --cert-name, missing cert, or permissions)"
fi
if [[ ! -r "${SRC_KEY}" ]]; then
  die "cannot read ${SRC_KEY} (wrong --cert-name, missing cert, or permissions)"
fi

mkdir -p "${DATA_DIR}"

case "${LINK_MODE}" in
  symlink)
    tmp_cert="${TARGET_CERT}.tmp"
    tmp_key="${TARGET_KEY}.tmp"
    cleanup_paths+=("${tmp_cert}" "${tmp_key}")
    ln -sfn "${SRC_CERT}" "${tmp_cert}"
    ln -sfn "${SRC_KEY}" "${tmp_key}"
    mv -Tf "${tmp_cert}" "${TARGET_CERT}"
    mv -Tf "${tmp_key}" "${TARGET_KEY}"
    if [[ -n "${OWNER}" ]]; then
      chown -h "${OWNER}" "${TARGET_CERT}" "${TARGET_KEY}"
    fi
    ;;
  copy)
    install -m 0644 "${SRC_CERT}" "${TARGET_CERT}"
    install -m 0600 "${SRC_KEY}" "${TARGET_KEY}"
    if [[ -n "${OWNER}" ]]; then
      chown "${OWNER}" "${TARGET_CERT}" "${TARGET_KEY}"
    fi
    ;;
  *)
    die "invalid --link-mode: ${LINK_MODE} (expected symlink or copy)"
    ;;
esac

log "updated:"
log "  ${TARGET_CERT} -> ${SRC_CERT}"
log "  ${TARGET_KEY}  -> ${SRC_KEY}"

if [[ -n "${RESTART_CMD}" ]]; then
  log "running restart cmd: ${RESTART_CMD}"
  bash -lc "${RESTART_CMD}"
fi
