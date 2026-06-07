# CI/CD and Release

## GitHub Actions Workflow (`.github/workflows/release.yml`)

Triggered on tags matching `v*`.

**Jobs:**
0. **gate-go / gate-frontend** (release gate): `go vet ./... && go test ./...` and frontend
   `npm run lint` re-run on the tagged commit before any bundle is built — a tag must never
   ship untested code, even if it points at a commit that skipped PR CI
1. **build-frontend** (needs both gates): Builds React frontend (`npm ci && npm run build`),
   uploads `frontend/dist` as artifact
2. **build-bundles**: Matrix build across 7 platform targets:
   - Linux: `amd64`, `arm64`, `386`, `armv7`
   - Windows: `amd64`, `arm64`, `386`
   - Builds two Go binaries: `yaog-server` and `yaog-compiler`
   - Assembles bundle: `bin/` + `frontend/`
   - Archives: `.tar.gz` (Linux) or `.zip` (Windows)
3. **release**: Creates GitHub Release with all bundle archives

## Deployment Scripts

`scripts/deploy.sh` and `scripts/deploy.ps1` pull a prebuilt bundle from GitHub Releases and set
up local startup scripts:
- Auto-detects platform and architecture
- Defaults to latest release if no `--tag` specified
- Generates `start.sh`/`stop.sh` (or `.ps1` equivalents)
