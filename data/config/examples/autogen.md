# Auto-Generated Configuration Examples

Example configuration files in this directory are **automatically generated** on first run.

## What Gets Generated

- `config.toml.example` - Complete example with all available configuration options
- `secrets.toml.example` - Example for RPC credentials (rpc_user, rpc_pass)
- `tuning.toml.example` - Advanced performance tuning and operational limits

## How to Use

Copy the example files to create your actual configuration:

```bash
cp data/config/examples/config.toml.example data/config/config.toml
cp data/config/examples/secrets.toml.example data/config/secrets.toml
# Edit with your settings
nano data/config/config.toml
nano data/config/secrets.toml
```

The `tuning.toml` file is optional - only create it if you need to override advanced settings.

## Important Notes

- Example files are regenerated on each startup
- Don't edit example files directly (your changes will be lost)
- Your actual config files in `data/config/` are gitignored
