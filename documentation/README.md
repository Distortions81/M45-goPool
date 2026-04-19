# Documentation Index


Start with [README.md](../README.md) for quick setup and feature overview, including Docker/Compose containerization instructions. See below for deeper operations:

- `operations.md` - Build/run procedures, config files, runtime flags, listeners/TLS, backups, signals, and operational guidance.
- `json-apis.md` - Public and authenticated `/api/*` endpoint reference (methods, response fields, auth expectations, caching headers).
- `stratum-v1.md` - Stratum v1 method compatibility and behavior notes (supported, compatibility-acknowledged, and not implemented).
- `version-bits.md` - `version_bits.toml` format, precedence, and known bit usage in goPool.
- `TESTING.md` - Unit/compat/fuzz/benchmark workflows, profiling commands, and coverage commands.
- `systemd.service` - Example service unit for long-running deployments.

- **Containerization**: See the [Containerization section in the main README](../README.md#containerization-dockercompose) for Docker and Compose usage, environment variables, and persistent data setup.
