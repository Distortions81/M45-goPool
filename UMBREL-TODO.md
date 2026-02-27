# Umbrel App - TODO

## Before Testing
- [ ] Verify Umbrel's Bitcoin Core exposes ZMQ on ports 28334/28332. If not, goPool falls back to RPC polling but ZMQ is preferred for block detection latency.
- [ ] Build and test the Docker image locally on both amd64 and arm64.
- [ ] Set a payout address — users need to set `PAYOUT_ADDRESS` env var or edit `config.toml` after first boot.

## Before Submitting to Umbrel App Store
- [x] GHCR image auto-built via `.github/workflows/docker.yml` on tagged releases (`ghcr.io/distortions81/gopool`).
- [x] `@sha256:` digest and version auto-pinned in `docker-compose.yml` and `umbrel-app.yml` by the workflow after each build.
- [ ] Prepare a 256x256 SVG icon (no rounded corners — Umbrel applies its own).
- [ ] Prepare 3-5 gallery screenshots at 1440x900 PNG.
- [ ] Fork `getumbrel/umbrel-apps`, add the app directory, and open a PR.

## Optional Enhancements
- [ ] Add `exports.sh` if other Umbrel apps need to consume goPool's Stratum endpoint.
- [ ] Add admin panel auto-setup (generate random password, expose via Umbrel auth).
- [ ] Consider adding a `services.toml` and `policy.toml` generation to `docker-entrypoint.sh` for more env var customization.
