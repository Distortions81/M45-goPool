# Auto-Generated Configuration Examples

> **Quick Start:** See the [main README](../../../README.md) for initial setup.

Configuration example files in this directory are automatically generated on first run when goPool detects missing configuration files.

## Generated Files

- **`config.toml.example`** - Complete example with all available configuration options and inline documentation
- **`secrets.toml.example`** - Example for sensitive credentials (RPC auth, Discord tokens, Clerk keys)
- **`services.toml.example`** - Optional services/integrations overrides (auth, backblaze backup, discord, status links)
- **`policy.toml.example`** - Optional policy/security overrides
- **`performance.toml.example`** - Optional performance/capacity overrides

## How to Use

### Initial Setup

```bash
# Copy example files to create your configuration
cp data/config/examples/config.toml.example data/config/config.toml
cp data/config/examples/secrets.toml.example data/config/secrets.toml

# Edit with your settings
nano data/config/config.toml
nano data/config/secrets.toml
```

### Tuning File (Optional)

Override files are optional. Only create them if you need advanced overrides:

```bash
cp data/config/examples/policy.toml.example data/config/policy.toml
cp data/config/examples/performance.toml.example data/config/performance.toml
cp data/config/examples/services.toml.example data/config/services.toml
```

## Important Notes

- **Examples are regenerated:** Example files are recreated on each startup to reflect current defaults
- **Don't edit examples:** Your changes to `.example` files will be lost on restart
- **Actual configs are protected:** Your configuration files in `data/config/` are gitignored and never overwritten
- **Cookie authentication preferred:** RPC credentials in `secrets.toml` only work with the `-allow-rpc-credentials` flag. Prefer setting `node.rpc_cookie_path` in `config.toml` for secure cookie-based authentication

## Authentication Priority

1. **Cookie file** (recommended) - Set `node.rpc_cookie_path` in `config.toml`
2. **Auto-detection** - goPool searches common locations if `rpc_cookie_path` is empty
3. **Username/password** (deprecated) - Use `rpc_user`/`rpc_pass` in `secrets.toml` with `-allow-rpc-credentials` flag

See [operations.md](../../../documentation/operations.md) for detailed configuration options.
