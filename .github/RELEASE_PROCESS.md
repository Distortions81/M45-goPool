# Release Process

GitHub Releases contain source code only. GitHub automatically provides the
source ZIP and tarball for each release; the project does not publish binaries
or container images.

Create a release by tagging the current `main` commit:

```bash
git checkout main
git pull
git tag v1.0.0
git push origin v1.0.0
```

The source-release workflow creates the GitHub Release and generates release
notes. Build locally with `go build -o goPool` when a binary is needed.
