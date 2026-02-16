# Repository Agent Notes

## Keep GitHub Actions current

When changing repository structure or release mechanics, update workflows in `.github/workflows/` in the same change.

At minimum, keep these in sync:

- `actions/setup-go` versions with `go.mod` (`go` directive).
- Release packaging paths in `.github/workflows/release.yml` (docs, data, scripts).
- Runtime defaults referenced in release text (ports, flags, config paths).
- Any new build/release scripts used by maintainers.

If a path is renamed (for example `documentation/` files), update workflows immediately so releases do not fail.
