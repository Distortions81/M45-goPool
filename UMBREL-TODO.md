# Umbrel App - TODO

## Before Testing
- [x] ZMQ disabled by default (Umbrel's Bitcoin Core doesn't expose ZMQ). goPool falls back to RPC polling.
- [ ] Build and test the Docker image locally on both amd64 and arm64.
- [ ] Set a payout address — users need to set `PAYOUT_ADDRESS` env var or edit `config.toml` after first boot.

## Before Submitting to Umbrel App Store
- [x] GHCR image auto-built via `.github/workflows/docker.yml` on tagged releases (`ghcr.io/distortions81/gopool`).
- [x] `@sha256:` digest and version auto-pinned in `docker-compose.yml` and `umbrel-app.yml` by the workflow after each build.
- [x] Landing page with pool info, admin credentials, miner connection, docs, and license.
- [x] Home/Umbrel branding applied (goPool Home).
- [ ] Prepare a 256x256 SVG icon (no rounded corners — Umbrel applies its own). Can convert from M45CORE-1024.PNG.
- [ ] Prepare 3-5 gallery screenshots at 1440x900 PNG.
- [ ] Fork `getumbrel/umbrel-apps`, add the app directory, and open a PR.

## Optional Enhancements
- [ ] Enable ZMQ support if Umbrel adds ZMQ to their Bitcoin Core app.
- [ ] Add `exports.sh` if other Umbrel apps need to consume goPool's Stratum endpoint.
- [ ] Add admin panel auto-setup (generate random password, expose via Umbrel auth).
- [ ] Consider adding a `services.toml` and `policy.toml` generation to `docker-entrypoint.sh` for more env var customization.
