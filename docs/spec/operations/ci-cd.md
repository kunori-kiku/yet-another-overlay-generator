# CI/CD and Release

## GitHub Actions Workflow (`.github/workflows/release.yml`)

Triggered on tags matching `v*`. The validator accepts only the documented SemVer ramp, requires an
annotated tag, and requires its peeled commit to equal the then-current `origin/main`. It verifies the
remote tag object explicitly rather than trusting the local tag ref materialized by the checkout
action. Publication transactions are globally serialized; see [`RELEASING.md`](../../../RELEASING.md)
for the operator runbook and failure-boundary rules.

The release graph is:

1. **Validate the release ref.** Record the exact source commit and reject an invalid, lightweight,
   moved, or non-main-tip tag.
2. **Re-run all seven required gates on the tagged commit.** These mirror the protected-branch Go,
   wire-DTO drift, frontend, Go/WASM conformance, security/DAST, required Playwright, and real-tunnel
   jobs. Advisory audit and pixel-visual jobs remain non-blocking by policy.
3. **Build both frontend distributions.** The controller panel and backend-free local-design panel each
   include the Go/WASM engine; missing `yaog.wasm` or `wasm_exec.js` fails the release.
4. **Build seven target sets.** Linux targets are `amd64`, `arm64`, `386`, and `armv7`; Windows targets
   are `amd64`, `arm64`, and `386`. Every platform bundle contains `yaog-server`, `yaog-compiler`,
   `yaog-agent`, and the identical controller frontend. Each target also emits a standalone agent and
   its SHA-256 sidecar. The local-design panel is packaged once as a versioned ZIP.
5. **Verify the exact 22-file allowlist.** The verifier requires seven bundles, seven standalone agents,
   seven sidecars, and one local-design ZIP. It rejects unsafe or ambiguous archive trees, verifies all
   Go target/VCS/version metadata and agent byte identity, compares the complete frontend across
   bundles, executes Linux amd64 binaries, and is followed by native Windows amd64/386 execution.
6. **Publish or recover the versioned controller image.** GHCR is required; Docker Hub is mirrored only
   when both credentials are configured. The version reference is adopted only if its digest, exact
   `linux/amd64` + `linux/arm64` platform set, source/version labels, and runtime version all match.
7. **Create or recover one private GitHub Release draft.** Only an absent release or an exactly
   classified private draft containing a same-byte subset of the verified assets is accepted. The
   workflow uploads without overwriting and re-seals all 22 remote names, sizes, and SHA-256 digests.
8. **Finalize publication.** Preview/beta tags publish as non-Latest prereleases. RC/GA tags first
   converge the verified container digest to configured `latest` pointers, then publish the GitHub
   release as non-prerelease GitHub Latest, and finally verify every configured public pointer.

The versioned image, mutable registry tags, and GitHub release are separate external systems. A failed
finalizer can leave one verified pointer ahead of another; rerunning only that failed finalizer is the
normal idempotent recovery. Do not create a release manually or replace a versioned image while the
workflow is running.

## Deployment Scripts

`scripts/deploy.sh` and `scripts/deploy.ps1` pull a prebuilt bundle from GitHub Releases and set
up local startup scripts:

- Auto-detects platform and architecture
- Defaults to latest release if no `--tag` specified
- Generates `start.sh`/`stop.sh` (or `.ps1` equivalents)
