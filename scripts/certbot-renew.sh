#!/bin/bash
#
# Certbot Auto-Renewal Helper Script for goPool
#
# This script configures Certbot to automatically renew SSL/TLS certificates
# and integrates with goPool's auto-reloading certificate system.
#
# Usage:
#   ./certbot-renew.sh --domain example.com --email admin@example.com [--data-dir /path/to/data]
#
# The script will:
#   1. Set up Certbot with webroot authentication using goPool's www directory
#   2. Obtain an initial certificate (or renew existing one)
#   3. Link certificates into goPool's expected locations
#   4. Set up a cron job for automatic renewal
#

set -e

# Default values
DATA_DIR="${DATA_DIR:-./data}"
DOMAIN=""
EMAIL=""
STAGING=false
DRY_RUN=false

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Print functions
print_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

print_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Usage information
usage() {
    cat << EOF
Usage: $0 --domain DOMAIN --email EMAIL [OPTIONS]

Required arguments:
    --domain DOMAIN      Your domain name (e.g., pool.example.com)
    --email EMAIL        Email for Let's Encrypt notifications

Optional arguments:
    --data-dir PATH      Path to goPool data directory (default: ./data)
    --staging            Use Let's Encrypt staging server for testing
    --dry-run            Test the renewal process without making changes
    --help               Show this help message

Example:
    $0 --domain pool.example.com --email admin@example.com
    $0 --domain pool.example.com --email admin@example.com --data-dir /opt/gopool/data

EOF
    exit 1
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --domain)
            DOMAIN="$2"
            shift 2
            ;;
        --email)
            EMAIL="$2"
            shift 2
            ;;
        --data-dir)
            DATA_DIR="$2"
            shift 2
            ;;
        --staging)
            STAGING=true
            shift
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --help)
            usage
            ;;
        *)
            print_error "Unknown option: $1"
            usage
            ;;
    esac
done

# Validate required arguments
if [[ -z "$DOMAIN" ]]; then
    print_error "Domain is required"
    usage
fi

if [[ -z "$EMAIL" ]]; then
    print_error "Email is required"
    usage
fi

# Convert DATA_DIR to absolute path
DATA_DIR=$(cd "$DATA_DIR" 2>/dev/null && pwd || echo "$DATA_DIR")

# Define paths
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if command -v realpath >/dev/null 2>&1; then
    LINK_CERTS_SCRIPT="$(realpath "$SCRIPT_DIR/link-certs.sh")"
else
    LINK_CERTS_SCRIPT="$SCRIPT_DIR/link-certs.sh"
fi

if [[ ! -f "$LINK_CERTS_SCRIPT" ]]; then
    print_error "Link helper not found: $LINK_CERTS_SCRIPT"
    exit 1
fi

WWW_DIR="$DATA_DIR/www"
WELLKNOWN_DIR="$WWW_DIR/.well-known/acme-challenge"
CERT_PATH="$DATA_DIR/tls_cert.pem"
KEY_PATH="$DATA_DIR/tls_key.pem"
LETSENCRYPT_LIVE="/etc/letsencrypt/live/$DOMAIN"

print_info "goPool Certbot Auto-Renewal Setup"
print_info "===================================="
print_info "Domain:          $DOMAIN"
print_info "Email:           $EMAIL"
print_info "Data Directory:  $DATA_DIR"
print_info "WWW Directory:   $WWW_DIR"
print_info "Certificate:     $CERT_PATH"
print_info "Private Key:     $KEY_PATH"
echo

# Check if certbot is installed
if ! command -v certbot &> /dev/null; then
    print_error "Certbot is not installed"
    print_info "Install it with: sudo apt-get install certbot  (Ubuntu/Debian)"
    print_info "                 sudo yum install certbot      (CentOS/RHEL)"
    exit 1
fi

# Check if running as root (needed for certbot)
if [[ $EUID -ne 0 ]] && [[ "$DRY_RUN" == false ]]; then
    print_warn "This script should be run as root for certbot to work properly"
    print_info "Try: sudo $0 $@"
    exit 1
fi

# Create www directory and .well-known subdirectory if they don't exist
print_info "Creating webroot directory structure..."
mkdir -p "$WELLKNOWN_DIR"
chmod 755 "$WWW_DIR"
chmod 755 "$WWW_DIR/.well-known"
chmod 755 "$WELLKNOWN_DIR"

# Test if directory is writable
if [[ ! -w "$WELLKNOWN_DIR" ]]; then
    print_error "Directory $WELLKNOWN_DIR is not writable"
    exit 1
fi

print_info "Webroot directory ready: $WWW_DIR"

# Build certbot command
CERTBOT_CMD="certbot certonly --webroot -w $WWW_DIR -d $DOMAIN --email $EMAIL --agree-tos --non-interactive"

if [[ "$STAGING" == true ]]; then
    print_warn "Using Let's Encrypt STAGING server (certificates will not be trusted)"
    CERTBOT_CMD="$CERTBOT_CMD --staging"
fi

if [[ "$DRY_RUN" == true ]]; then
    print_info "DRY RUN mode - testing renewal without making changes"
    CERTBOT_CMD="$CERTBOT_CMD --dry-run"
fi

# Obtain/renew certificate
print_info "Running certbot..."
print_info "Command: $CERTBOT_CMD"
echo

if ! $CERTBOT_CMD; then
    print_error "Certbot failed"
    print_info ""
    print_info "Troubleshooting tips:"
    print_info "  1. Ensure port 80 is accessible from the internet (certbot needs HTTP access)"
    print_info "  2. Ensure port 443 is accessible if you're running HTTPS"
    print_info "  3. Check that DNS for $DOMAIN points to this server"
    print_info "  4. Verify goPool's status server is running and serving from $WWW_DIR"
    print_info "  5. Check firewall rules: sudo ufw status"
    print_info "  6. Use --staging flag first to test without hitting rate limits"
    exit 1
fi

# Skip the rest if this was a dry run
if [[ "$DRY_RUN" == true ]]; then
    print_info "Dry run completed successfully!"
    exit 0
fi

print_info "Certificate obtained successfully!"

# Create deployment hook script that links certs into goPool's location
DEPLOY_HOOK="/etc/letsencrypt/renewal-hooks/deploy/gopool-deploy.sh"
print_info "Creating deployment hook at $DEPLOY_HOOK..."

mkdir -p /etc/letsencrypt/renewal-hooks/deploy

cat > "$DEPLOY_HOOK" << HOOK_EOF
#!/bin/bash
set -e
LINK_CERTS_SCRIPT="$LINK_CERTS_SCRIPT"

# Certbot deployment hook for goPool
DOMAIN="$DOMAIN"
DATA_DIR="$DATA_DIR"

# Ensure the helper can run even if the hook is executed from elsewhere
bash "\$LINK_CERTS_SCRIPT" "\$DOMAIN" "\$DATA_DIR"

# Log the renewal
echo "\$(date): Certificate renewed and linked into \$DATA_DIR" >> "\$DATA_DIR/cert-renewal.log"

# goPool's certReloader will automatically detect the change within 1 hour
# No need to restart the service
HOOK_EOF

chmod +x "$DEPLOY_HOOK"

# Link initial certificates
print_info "Linking certificates to goPool data directory..."
bash "$LINK_CERTS_SCRIPT" "$DOMAIN" "$DATA_DIR"
print_info "Certificates linked successfully"

# Set up automatic renewal cron job
print_info "Setting up automatic renewal..."

# Certbot package usually installs its own cron/systemd timer, but let's verify
if systemctl list-timers | grep -q certbot; then
    print_info "Certbot systemd timer is already active"
elif [[ -f /etc/cron.d/certbot ]]; then
    print_info "Certbot cron job is already configured"
else
    print_warn "No automatic renewal found. Creating cron job..."
    # Run renewal check twice daily at random minute
    CRON_CMD="$(($RANDOM % 60)) */12 * * * root certbot renew --quiet --deploy-hook /etc/letsencrypt/renewal-hooks/deploy/gopool-deploy.sh"
    echo "$CRON_CMD" > /etc/cron.d/gopool-certbot-renew
    chmod 644 /etc/cron.d/gopool-certbot-renew
    print_info "Created cron job in /etc/cron.d/gopool-certbot-renew"
fi

# Display summary
echo
print_info "=================================="
print_info "Setup Complete!"
print_info "=================================="
echo
print_info "Your SSL/TLS certificate has been installed for: $DOMAIN"
print_info ""
print_info "Certificate location:  $CERT_PATH"
print_info "Private key location:  $KEY_PATH"
print_info ""
print_info "The certificate will automatically renew before expiration."
print_info "goPool will automatically reload new certificates within 1 hour."
print_info ""
print_info "Next steps:"
print_info "  1. Update your goPool config.toml to use HTTPS:"
print_info "     \"status_tls_listen\": \":443\""
print_info "     OR set --https-only flag when running goPool"
print_info ""
print_info "  2. Restart goPool to use the new certificate:"
print_info "     sudo systemctl restart gopool"
print_info ""
print_info "  3. Test certificate renewal (dry run):"
print_info "     sudo certbot renew --dry-run"
print_info ""
print_info "Certificate info:"
certbot certificates -d "$DOMAIN" 2>/dev/null | grep -A 10 "Certificate Name: $DOMAIN" || true
echo
