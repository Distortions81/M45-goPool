#!/usr/bin/env bash
set -e

# ===== CONFIGURATION =====
DOMAIN="yourdomain"               # <- change this to your domain
EMAIL="you@domain"          # <- change to your email
WEBROOT="$(pwd)/data/www"               # <- folder used for HTTP validation
OUTPUT_DIR="$(pwd)"                # <- where to save cert/key
CERT_OUT="$OUTPUT_DIR/tls_cert.pem"
KEY_OUT="$OUTPUT_DIR/tls_key.pem"
# ==============================

# Make sure webroot exists
if [ ! -d "$WEBROOT" ]; then
    echo "Error: webroot directory '$WEBROOT' does not exist."
    exit 1
fi

echo "Installing certbot if needed..."
sudo apt update
sudo apt install -y certbot

echo "Requesting Let's Encrypt certificate for $DOMAIN..."
sudo certbot certonly \
  --webroot \
  -w "$WEBROOT" \
  -d "$DOMAIN"

# Copy certificate/key to current directory
LE_LIVE="/etc/letsencrypt/live/$DOMAIN"

sudo cp "$LE_LIVE/fullchain.pem" "$CERT_OUT"
sudo cp "$LE_LIVE/privkey.pem" "$KEY_OUT"

# Fix permissions so your user can read them
sudo chown "$(whoami):$(whoami)" "$CERT_OUT" "$KEY_OUT"
chmod 644 "$CERT_OUT"
chmod 600 "$KEY_OUT"

echo "✅ TLS certificate generated!"
echo "Certificate: $CERT_OUT"
echo "Private Key: $KEY_OUT"`