# Releasing YAOG

This document describes how YAOG cuts a release. It is the process-of-record
referenced by `CHANGELOG.md` and the `implementation_plans/` closure rituals.

## Version ramp

YAOG uses [Semantic Versioning](https://semver.org/). A major version stabilizes
through an ordered pre-release ramp before the final tag:

```
vX.Y.Z-preview.N  →  vX.Y.Z-beta.N  →  vX.Y.Z-rc.N  →  vX.Y.Z
```

- **preview.N** — incremental, possibly-breaking previews of in-progress work.
  Breaking changes are allowed between previews (call them out in the notes).
- **beta.N** — feature-complete for the target, RC blockers closed, intended for
  wider testing. The first beta is set as the GitHub *latest* release.
- **rc.N** — release candidate; no new features, only fixes. Cut once the beta
  soak is clean and any owed hardware smokes have passed.
- **vX.Y.Z** — the GA tag.

Pre-release identifiers sort per SemVer (`-beta.2` < `-beta.10`); never use a bare
numeric suffix without the dotted identifier.

## Cutting a release

1. **Roll the changelog.** Move the `## [Unreleased]` entries into a new
   `## [vX.Y.Z-...] - YYYY-MM-DD` section and add the version's compare link at the
   bottom of `CHANGELOG.md`. Leave a fresh empty `## [Unreleased]`.
2. **Verify the gates are green on `main`** (CI): `go build/vet/test ./...` and the
   frontend `lint` + `build`. Releases are cut from `main` only.
3. **Run (or record as owed) any gating hardware smokes.** Some validation requires
   real hosts / a browser authenticator and cannot run in CI; if hardware is
   unavailable, record the smoke as *owed (owner-accepted risk)* in `STATUS.md` and
   the release notes, per the convention used by prior releases.
4. **Create an annotated tag** with the project commit identity:

   ```bash
   GIT_AUTHOR_NAME=kunori-kiku GIT_AUTHOR_EMAIL=rokuyanlin@gmail.com \
   GIT_COMMITTER_NAME=kunori-kiku GIT_COMMITTER_EMAIL=rokuyanlin@gmail.com \
   git tag -a vX.Y.Z-beta.1 -m "vX.Y.Z-beta.1 — <one-line summary>"
   git push origin vX.Y.Z-beta.1
   ```

   Always annotated (`-a`), never lightweight — `release.yml` and `git describe`
   depend on annotated tags.
5. **Publish the GitHub release.** The push triggers `.github/workflows/release.yml`,
   which builds the binaries; create/edit the release with notes:

   ```bash
   gh release create vX.Y.Z-beta.1 --title "vX.Y.Z-beta.1" --notes-file <notes> --latest
   ```

   Use `--latest` for the current stable-most release; use `--prerelease` (and omit
   `--latest`) for a release that should not be advertised as latest yet.

## Build-version injection

Release binaries embed their version via linker flags (live since beta.1), so a deployed
agent/server reports exactly which tag it was built from. The convention (consumed by
`release.yml` and the `Dockerfile`) is to **extend** the existing `-ldflags "-s -w"`
string rather than replace it:

```bash
go build -trimpath -ldflags "-s -w -X main.BuildVersion=${TAG}" -o yaog-agent ./cmd/agent/
```

Each `main` package declares `var BuildVersion = "dev"`; the `-X` flag overwrites it
at link time, and a non-release build keeps `dev`. A `version` subcommand
(`yaog-agent version`, `yaog-server version`, `yaog-compiler version`) prints it.
This rail underpins the controller's per-node version reporting and the signed agent
self-update floor.

## Release-pipeline invariant: `release.yml` gates must mirror `ci.yml`

`release.yml` re-runs the CI gate set (Go test, frontend lint/build, **frontend E2E**,
real-tunnel netns) on the tag before it builds and publishes. Its `gate-e2e` job is a
**separate copy** of `ci.yml`'s `frontend-e2e` job, and the two must build the panel
**identically**. The trap: **the release E2E gate only runs on a tag push**, so any drift
between the two E2E jobs stays invisible on PRs and first surfaces as a failed release build
(which skips build + publish → no release).

Concretely: whenever you add a build step to `ci.yml`'s `frontend-e2e` (dependency install,
a codegen step, `VITE_E2E=1`, **`npm run build:wasm`**, a dist assertion), add the twin step
to `release.yml`'s `gate-e2e` **in the same PR**. This has bitten releases twice — the
`VITE_E2E` panel build (#173) and the WASM engine build (`v2.0.0-rc.6`: the release E2E panel
had no `/yaog.wasm`, so every in-browser-validate spec timed out; fixed in #295, and the
tag was moved to the fixed commit — moving a tag whose release never published is the correct
recovery, not a force-push of published history). **A green PR does not prove the release
pipeline is green.** Treat the first tag after any FE-build/CI change as a real risk.

Related operational invariant: a CI job **display-name** change silently orphans its required
branch-protection context — update branch protection in the same PR as any `name:` edit in
`ci.yml`.
