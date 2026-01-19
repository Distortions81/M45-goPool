# Release Process

This document describes how to create and publish releases for goPool.

## Automated Releases (Recommended)

The easiest way to create a release is by pushing a git tag:

```bash
# Make sure you're on the main branch with latest changes
git checkout main
git pull

# Create and push a version tag (triggers release build)
git tag v1.0.0
git push origin v1.0.0
```

This automatically:
1. Triggers the GitHub Actions release workflow
2. Builds binaries for all supported platforms (Linux, macOS, Windows; amd64/arm64)
3. Packages each binary with templates, configs, docs, and scripts
4. Creates SHA256 checksums for verification
5. Uploads everything to GitHub Releases
6. Makes the release public immediately

## Manual Test Builds

To test the release workflow without creating a public release:

1. Go to GitHub: Actions → Release Build
2. Click "Run workflow"
3. Enter a test version tag (e.g., `v1.0.0-test` or `v0.0.0-dev`)
4. Click "Run workflow"

This creates build artifacts that you can download from the workflow run, but does NOT create a GitHub Release.

## Version Numbering

Use semantic versioning (SemVer):

- **Major version** (v1.0.0 → v2.0.0): Breaking changes, major features
- **Minor version** (v1.0.0 → v1.1.0): New features, backwards compatible
- **Patch version** (v1.0.0 → v1.0.1): Bug fixes, small improvements

Examples:
- `v1.0.0` - First stable release
- `v1.1.0` - Added new features
- `v1.1.1` - Bug fixes
- `v2.0.0` - Breaking changes (config format change, API changes, etc.)

## Pre-Release Tags

For testing or early access:

- `v1.0.0-alpha` - Alpha release (early testing)
- `v1.0.0-beta` - Beta release (feature complete, testing)
- `v1.0.0-rc1` - Release candidate (nearly final)

Mark these as "pre-release" on GitHub Releases page.

## Release Checklist

Before creating a release:

- [ ] All tests pass (`go test ./...`)
- [ ] No known critical bugs
- [ ] Documentation is up to date (README.md, operations.md, etc.)
- [ ] CHANGELOG or release notes prepared
- [ ] Version number follows SemVer
- [ ] Test build succeeds (manual workflow run)

## What Gets Packaged

Each release package includes:

```
goPool-v1.0.0-linux-amd64/
├── goPool                           # Binary executable
├── README.md                        # Main documentation
├── operations.md                    # Configuration guide
├── performance.md                   # Performance benchmarks
├── TESTING.md                       # Testing documentation
├── LICENSE                          # License file
├── GETTING_STARTED.txt             # Quick start guide
├── data/
│   ├── templates/                  # All HTML templates
│   │   ├── overview.tmpl
│   │   ├── worker_status.tmpl
│   │   └── ...
│   ├── www/                        # Static web assets
│   │   ├── style.css
│   │   ├── logo.png
│   │   └── ...
│   └── config/
│       └── examples/               # Example configs
│           ├── config.toml.example
│           ├── secrets.toml.example
│           ├── tuning.toml.example
│           └── autogen.md
└── scripts/                        # Helper scripts
    ├── install-bitcoind.sh
    ├── certbot-gopool.sh
    ├── run-tests.sh
    └── ...
```

## Platform Matrix

The workflow builds for these platforms:

| Platform | Architecture | Package Format |
|----------|-------------|----------------|
| Linux | amd64 | `.tar.gz` |
| Linux | arm64 | `.tar.gz` |
| Linux | armv7 | `.tar.gz` |
| macOS | amd64 | `.tar.gz` |
| macOS | arm64 | `.tar.gz` |
| Windows | amd64 | `.zip` |
| Windows | arm64 | `.zip` |

## Build Configuration

Each binary is built with:
- **CGO_ENABLED=1** - Required for ZeroMQ
- **-trimpath** - Reproducible builds
- **-ldflags="-s -w"** - Optimized size (strips debug info)
- Hardware acceleration enabled (SHA256-SIMD, Sonic JSON)

## Troubleshooting

### Build Fails for a Platform

- Check the GitHub Actions logs for the specific platform
- Common issues: ZMQ library not found, CGO errors
- May need to update build dependencies in workflow

### Wrong Files in Package

- Edit `.github/workflows/release.yml`
- Modify the "Prepare release package" step
- Test with manual workflow run before pushing tag

### Release Not Created

- Ensure tag starts with `v` (e.g., `v1.0.0` not `1.0.0`)
- Check GitHub Actions permissions (need `contents: write`)
- Verify GITHUB_TOKEN has correct permissions

## Post-Release

After creating a release:

1. Download and test binaries on different platforms
2. Add release notes on GitHub (edit the release)
3. Announce in relevant channels (Discord, etc.)
4. Update any external documentation or websites

## Deleting/Editing Releases

To remove a bad release:

```bash
# Delete the tag locally
git tag -d v1.0.0

# Delete the tag on GitHub
git push origin :refs/tags/v1.0.0

# Delete the GitHub Release via web UI
```

Then fix issues and create a new tag.

## See Also

- [RELEASES.md](../../RELEASES.md) - User-facing release documentation
- [.github/workflows/release.yml](../workflows/release.yml) - Release workflow configuration
