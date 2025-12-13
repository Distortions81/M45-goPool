#!/bin/bash
#
# Link Let's Encrypt Certificates to goPool TLS Directory
#
# This script creates symbolic links from Let's Encrypt certificate files
# to the goPool data directory. Using symlinks instead of copies means
# goPool automatically picks up renewed certificates without manual intervention.
#
# The certReloader in goPool checks for certificate file changes hourly,
# so renewed certificates will be loaded within 1 hour of renewal.
#
# Usage:
#   sudo ./link-certs.sh [DOMAIN] [DATA_DIR]
#
# Arguments:
#   DOMAIN   - Your domain name (e.g., pool.example.com)
#   DATA_DIR - Path to goPool data directory (default: ./data)
#

set -e

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${BLUE}======================================"
echo "goPool TLS Certificate Linker"
echo -e "======================================${NC}"
echo

# Check if running as root
if [[ $EUID -ne 0 ]]; then
   echo -e "${RED}Error: This script must be run as root (use sudo)${NC}"
   echo "Certificate files in /etc/letsencrypt are only readable by root"
   exit 1
fi

# Get domain from argument or prompt
if [[ -n "$1" ]]; then
    DOMAIN="$1"
else
    read -p "Enter your domain name (e.g., pool.example.com): " DOMAIN
fi

if [[ -z "$DOMAIN" ]]; then
    echo -e "${RED}Error: Domain name is required${NC}"
    exit 1
fi

# Get data directory from argument or prompt
if [[ -n "$2" ]]; then
    DATA_DIR="$2"
else
    read -p "Enter goPool data directory path [./data]: " DATA_DIR
    DATA_DIR=${DATA_DIR:-./data}
fi

# Convert to absolute path
DATA_DIR=$(cd "$DATA_DIR" 2>/dev/null && pwd || echo "$DATA_DIR")

# Certificate paths
CERT_DIR="/etc/letsencrypt/live/$DOMAIN"
CERT_SOURCE="$CERT_DIR/fullchain.pem"
KEY_SOURCE="$CERT_DIR/privkey.pem"

CERT_DEST="$DATA_DIR/tls_cert.pem"
KEY_DEST="$DATA_DIR/tls_key.pem"

echo "Configuration:"
echo "  Domain:          $DOMAIN"
echo "  Data Directory:  $DATA_DIR"
echo "  Certificate Dir: $CERT_DIR"
echo

# Verify Let's Encrypt certificate exists
if [[ ! -d "$CERT_DIR" ]]; then
    echo -e "${RED}Error: Certificate directory not found: $CERT_DIR${NC}"
    echo
    echo "Make sure certbot has generated certificates for this domain."
    echo "You can run certbot with:"
    echo "  sudo certbot certonly --standalone -d $DOMAIN"
    echo "or use the certbot-setup-manual.sh script"
    exit 1
fi

if [[ ! -f "$CERT_SOURCE" ]]; then
    echo -e "${RED}Error: Certificate file not found: $CERT_SOURCE${NC}"
    exit 1
fi

if [[ ! -f "$KEY_SOURCE" ]]; then
    echo -e "${RED}Error: Private key file not found: $KEY_SOURCE${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Let's Encrypt certificates found${NC}"
echo

# Create data directory if it doesn't exist
if [[ ! -d "$DATA_DIR" ]]; then
    echo -e "${YELLOW}Creating data directory: $DATA_DIR${NC}"
    mkdir -p "$DATA_DIR"
fi

# Backup existing files if they exist and are not symlinks
if [[ -f "$CERT_DEST" && ! -L "$CERT_DEST" ]]; then
    BACKUP_CERT="${CERT_DEST}.backup.$(date +%Y%m%d-%H%M%S)"
    echo -e "${YELLOW}Backing up existing certificate to: $BACKUP_CERT${NC}"
    mv "$CERT_DEST" "$BACKUP_CERT"
fi

if [[ -f "$KEY_DEST" && ! -L "$KEY_DEST" ]]; then
    BACKUP_KEY="${KEY_DEST}.backup.$(date +%Y%m%d-%H%M%S)"
    echo -e "${YELLOW}Backing up existing private key to: $BACKUP_KEY${NC}"
    mv "$KEY_DEST" "$BACKUP_KEY"
fi

# Remove existing symlinks if they exist
if [[ -L "$CERT_DEST" ]]; then
    echo "Removing existing certificate symlink"
    rm -f "$CERT_DEST"
fi

if [[ -L "$KEY_DEST" ]]; then
    echo "Removing existing private key symlink"
    rm -f "$KEY_DEST"
fi

# Create symbolic links
echo -e "${GREEN}Creating symbolic links...${NC}"
ln -s "$CERT_SOURCE" "$CERT_DEST"
ln -s "$KEY_SOURCE" "$KEY_DEST"

# Verify links were created successfully
if [[ ! -L "$CERT_DEST" ]] || [[ ! -f "$CERT_DEST" ]]; then
    echo -e "${RED}Error: Failed to create certificate symlink${NC}"
    exit 1
fi

if [[ ! -L "$KEY_DEST" ]] || [[ ! -f "$KEY_DEST" ]]; then
    echo -e "${RED}Error: Failed to create private key symlink${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Certificate: $CERT_DEST -> $CERT_SOURCE${NC}"
echo -e "${GREEN}✓ Private Key: $KEY_DEST -> $KEY_SOURCE${NC}"
echo

# Display certificate information
echo -e "${BLUE}Certificate Information:${NC}"
openssl x509 -in "$CERT_SOURCE" -noout -subject -dates -issuer 2>/dev/null || echo "Could not parse certificate"
echo

# Set ownership to match the user who invoked sudo
if [[ -n "$SUDO_USER" ]]; then
    echo "Setting ownership to $SUDO_USER..."
    # We can't change ownership of the symlink itself, but we can ensure
    # the data directory is accessible
    chown -h "$SUDO_USER:$SUDO_USER" "$DATA_DIR" 2>/dev/null || true
fi

echo -e "${GREEN}======================================"
echo "Setup Complete!"
echo -e "======================================${NC}"
echo
echo "The symbolic links have been created successfully."
echo
echo -e "${BLUE}How automatic renewal works:${NC}"
echo "1. Let's Encrypt renews certificates in /etc/letsencrypt/live/$DOMAIN"
echo "2. The symlinks always point to the latest certificates"
echo "3. goPool's certReloader checks for changes every hour"
echo "4. New certificates are loaded automatically (no restart needed)"
echo
echo -e "${BLUE}Next steps:${NC}"
echo "1. Ensure goPool config.toml has status_tls_listen set (e.g., \":443\")"
echo "2. Start or restart goPool to load the certificates"
echo "3. Test your HTTPS endpoint: https://$DOMAIN"
echo
echo -e "${BLUE}Manual certificate renewal:${NC}"
echo "  sudo certbot renew --force-renewal --cert-name $DOMAIN"
echo
echo -e "${BLUE}Check certificate renewal status:${NC}"
echo "  sudo certbot certificates -d $DOMAIN"
echo
echo -e "${GREEN}Note: Certbot automatically renews certificates before expiration.${NC}"
echo "      No manual intervention is needed for renewals."
echo
