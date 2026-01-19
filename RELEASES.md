# goPool Releases

This document explains the automated release builds for goPool.

## Release Packages

Each release includes pre-built binaries for multiple platforms, packaged with all necessary files to get started:

### Included in Each Package

- **Binary** - Pre-compiled goPool executable
- **Documentation** - README.md, operations.md, performance.md, TESTING.md, LICENSE
- **Templates** - All HTML templates in `data/templates/`
- **Web Assets** - CSS, images, and static files in `data/www/`
- **Config Examples** - Example configuration files in `data/config/examples/`
- **Scripts** - Helper scripts in `scripts/`
- **GETTING_STARTED.txt** - Quick start instructions

### Supported Platforms

| Platform | Architecture | Package Format | Notes |
|----------|-------------|----------------|-------|
| Linux | amd64 (x86_64) | `.tar.gz` | Most common, includes AVX/AVX2/AVX512 support |
| macOS | amd64 (Intel) | `.tar.gz` | Intel Macs |
| macOS | arm64 (Apple Silicon) | `.tar.gz` | M1/M2/M3 Macs |

**Note:** All builds include hardware-accelerated SHA256 ([sha256-simd](https://github.com/minio/sha256-simd)) and JSON ([sonic](https://github.com/bytedance/sonic)) where supported by the platform.

## Quick Start with Release Binaries

### Linux / macOS

```bash
# Download release package
wget https://github.com/Distortions81/M45-Core-goPool/releases/download/v0.1.4/goPool-v0.1.4-linux-amd64.tar.gz

# Extract
tar xzf goPool-v0.1.4-linux-amd64.tar.gz
cd goPool-v0.1.4-linux-amd64

# Run once to generate config examples
./goPool

# Copy and edit config
cp data/config/examples/config.toml.example data/config/config.toml
nano data/config/config.toml

# Set your payout address and Bitcoin Core connection
# Then run the pool
./goPool
```

## Verifying Downloads

Each release package includes a SHA256 checksum file:

```bash
# Linux
sha256sum -c goPool-v0.1.4-linux-amd64.tar.gz.sha256

# macOS
shasum -a 256 -c goPool-v0.1.4-macos-amd64.tar.gz.sha256
```

## Release Triggers

### Automatic Releases (Recommended)

Releases are automatically built when you push a git tag:

```bash
# Create and push a version tag
git tag v0.1.4
git push origin v0.1.4
```

This triggers the GitHub Actions workflow which:
1. Builds binaries for all supported platforms
2. Packages each with templates, configs, and documentation
3. Creates SHA256 checksums
4. Uploads to GitHub Releases automatically

### Manual Releases

You can also trigger a manual build from the GitHub Actions tab:
1. Go to Actions → Release Build
2. Click "Run workflow"
3. Enter a version tag (e.g., `v0.1.4-dev`)
4. Artifacts will be available for download (not uploaded to Releases)

## Build Configuration

The release workflow is defined in [.github/workflows/release.yml](.github/workflows/release.yml).

### Build Options

- **CGO_ENABLED=1** - Required for ZeroMQ support
- **-trimpath** - Removes local path information for reproducible builds
- **-ldflags="-s -w"** - Strips debug info to reduce binary size

### Hardware Acceleration

All release builds include:
- **SHA256-SIMD** - Automatic AVX/AVX2/AVX512 (x86) or ARM crypto extensions
- **Sonic JSON** - JIT-accelerated JSON on supported platforms

To disable at runtime:
```bash
# Disable SHA256 SIMD
./goPool -sha256-no-avx

# Or rebuild from source with build tags
go build -tags "noavx nojsonsimd" -o goPool
```

## Requirements

### All Platforms

- Bitcoin Core with RPC enabled and ZMQ support recommended
- ZeroMQ library (usually pre-compiled into binary where possible)

### Linux

Most distributions include ZMQ support compiled in. If you encounter issues:

```bash
# Ubuntu / Debian
sudo apt install libzmq5

# Fedora / RHEL / CentOS
sudo dnf install zeromq

# Arch Linux
sudo pacman -S zeromq

# Alpine Linux
apk add zeromq
```

### macOS

```bash
brew install zeromq
```

## Upgrading

To upgrade to a new version:

1. Stop the running goPool instance
2. Back up your `data/` directory (especially `data/config/` and `data/state/`)
3. Extract the new release package
4. Copy your `data/config/` files to the new installation
5. Review release notes for any configuration changes
6. Start the new version

**Important:** Always back up `data/state/workers.db.bak` (the snapshot) rather than the live database.

## Building from Source

If pre-built releases don't work for your platform, see [README.md](README.md) for build instructions.

## Support

- **Issues:** https://github.com/Distortions81/M45-Core-goPool/issues
- **Documentation:** See included markdown files (README.md, operations.md, performance.md)
