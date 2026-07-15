# Releasing YAOG

This document is the release process of record referenced by `CHANGELOG.md` and the
`implementation_plans/` closure rituals. A tag push starts publication; do not create a GitHub
release by hand while the workflow is running.

## Version ramp and publication policy

YAOG uses [Semantic Versioning](https://semver.org/) and the ordered ramp:

```text
vX.Y.Z-preview.N -> vX.Y.Z-beta.N -> vX.Y.Z-rc.N -> vX.Y.Z
```

- `preview.N` may include explicitly documented breaking changes.
- `beta.N` is feature-complete and intended for wider testing.
- `rc.N` admits fixes only and is the stable-most candidate for the target.
- `vX.Y.Z` is the GA release.

The tag-time validator accepts only `vMAJOR.MINOR.PATCH` or that version followed by exactly
`-preview.N`, `-beta.N`, or `-rc.N`, with no leading zeroes except the number zero itself. Every
release tag must be annotated and resolve to the current `origin/main` tip. An older main ancestor
is rejected: a tag may publish only the exact commit whose current main checks were reviewed.

The current workflow deliberately makes RC and GA releases GitHub **Latest** and publishes their
container `latest` tag. Preview and beta releases do neither. This project policy is independent of
SemVer's ordering rules; do not let GitHub's default newest-release heuristic decide it.

## Release graph and gates

`.github/workflows/release.yml` owns the complete release transaction:

```text
validate annotated tag + current main tip
  -> seven tag-time gates
  -> build controller/local frontend distributions
  -> build seven platform bundles + standalone agents, and package local-design
  -> verify the exact 22-file allowlist, safe archive trees, all target metadata,
     and native Linux amd64 + Windows amd64/386 execution
  -> publish/recover and verify the policy-non-overwritten multi-arch controller image
  -> upload the exact assets to a GitHub release draft
  -> preview/beta: publish the draft as a non-Latest prerelease
  -> RC/GA: promote the verified image digest, then publish the draft as Latest
```

The seven gates mirror the required behavior in `.github/workflows/ci.yml`:

1. Go format, vet, race tests, and the per-package coverage floor.
2. Wire DTO and `omitempty` drift.
3. Frontend lint, TypeScript/Vite build, WASM prebuild, and Vitest.
4. Go/WASM-to-golden conformance.
5. Required security checks: `govulncheck` and the live-wire DAST suite.
6. Required Playwright functional/adversarial/responsive behavior tests (excluding the non-blocking
   pixel visual corpus).
7. The required real-tunnel systemd-nspawn canary, including a positive assertion that it ran rather
   than skipped.

The advisory CI steps (gosec, npm audit, pixel snapshots, and additive real-tunnel scenarios) remain
advisory and are not silently promoted to tag blockers. If required behavior changes in `ci.yml`,
update its release twin in the same PR. In particular, keep the E2E binary/WASM/panel build sequence
identical. A required job display-name change also changes the branch-protection context and must be
coordinated with branch protection.

The release asset verifier requires exactly seven platform bundles, seven standalone agents, seven
matching raw-agent SHA-256 sidecars, and the versioned local-design ZIP. It snapshots each bounded
outer archive before inspecting it, rejects links/special members and ambiguous/colliding paths,
checks the complete member tree and payload integrity, and extracts only required regular files.
Every bundled binary and standalone agent must have the exact main package, target GOOS/GOARCH/GOARM,
clean source revision, `CGO_ENABLED=0`, trimpath setting, executable archive mode, and one exact static
release stamp. Every bundled agent is byte-compared with its standalone asset. The complete controller
frontend member set and bytes must match across all seven bundles; local-design has its own exact safe
tree policy. Linux amd64 is executed natively, and a Windows runner separately executes amd64 and 386
PE binaries. The upload and both finalizers re-download and repeat the verifier. Immediately before
and after publication they also verify the release ID/tag/classification and all 22 remote names,
sizes, and SHA-256 digests.

Container publication is an explicit transaction phase through reusable `.github/workflows/docker.yml`;
it no longer triggers independently on a tag push. A manual Docker run can publish only `edge`.
The called workflow publishes a version reference (the release tag without its leading `v`) under a
non-overwrite policy, verifies its digest, exact runtime platforms (`linux/amd64` and `linux/arm64`),
source/version labels, and embedded server version. A failed post-push run may adopt an existing
reference only when both platform configs/runtimes and all of those properties match; a missing
optional mirror is repaired from that
verified digest, while any different existing bytes fail closed. GHCR is required. Docker Hub is
included only when both credentials are configured.

GitHub upload accepts only an absent release or one exact private draft whose existing assets are a
same-byte subset of the verified set. The pinned action fills that selected draft without overwriting
assets. Preview/beta publish it explicitly as a non-Latest prerelease. RC/GA converge Docker Hub
`latest` when configured, GHCR `latest`, and GitHub Latest to the verified transaction. Those are
separate external systems and cannot change atomically: a failed finalizer can temporarily leave one
verified mutable pointer ahead of another. Rerunning **only that failed finalizer job** is the normal
recovery and idempotently converges every pointer to the same digest/release; its last step verifies
all configured pointers. Do not describe this as an all-or-nothing cross-registry transaction.

Maintainers can exercise the verifier independently with:

```bash
go test ./scripts
bash scripts/test-release-assets.sh
```

The focused Go tests synthesize traversal, repeated separators, case/prefix collisions, ZIP links,
TAR links/devices/FIFOs, member-count/size limits, and outer-file attacks. The end-to-end script
builds a clean seven-target positive set and proves wrong-architecture, dev/wrong-version,
missing/extra asset, unexpected-root, non-executable-mode, and complete-frontend-drift failures.

## Cutting a release

### 1. Prepare and merge the release state

In a reviewed PR:

1. Move the intended entries from `## [Unreleased]` into
   `## [X.Y.Z-...] - YYYY-MM-DD` in `CHANGELOG.md` (the heading omits the tag's `v`).
2. Leave a fresh `## [Unreleased]` section and add/update the compare link at the bottom.
3. Update `STATUS.md` and the release-note source in candidate/ready-and-uncut tense, including
   residual risks. Reserve **shipped**, **published**, and equivalent claims for post-publication
   verification.
4. Merge the PR and wait for all required `main` checks to pass.

Releases are cut from committed `main`, never from a dirty worktree or an unmerged release branch.

### 2. Resolve hardware-only validation

Run the applicable real-host/browser-authenticator smokes. If required hardware is unavailable,
record the check as **owed (owner-accepted risk)** in `STATUS.md` and in the release notes. Do not
silently translate an unavailable hardware check into a pass.

### 3. Synchronize and preflight

```bash
git fetch origin main --tags
git switch main
git pull --ff-only origin main
git status --short

TAG=vX.Y.Z-rc.N
IMAGE_VERSION=${TAG#v}
git rev-parse HEAD
git rev-parse origin/main
git tag -l "$TAG"
git ls-remote --tags origin "refs/tags/$TAG" "refs/tags/$TAG^{}"

IMAGE_REF="ghcr.io/kunori-kiku/yaog-controller:$IMAGE_VERSION"
if output=$(docker buildx imagetools inspect "$IMAGE_REF" 2>&1); then
  echo "version reference already exists; inspect it as recovery state: $IMAGE_REF" >&2
  false
elif ! grep -Fqx "ERROR: $IMAGE_REF: not found" <<<"$output" \
  && ! grep -Eqi '(manifest unknown|name unknown|no such manifest|HTTP 404)' <<<"$output"; then
  echo "$output" >&2
  false  # registry/auth failure is not proof of absence
fi
```

The worktree must be clean, local `HEAD` must equal the verified `origin/main`, and both tag lookups
must be empty for a new release. The GHCR version reference should also be absent. If it exists because
a prior run crossed the push boundary, do not delete or overwrite it: the workflow will adopt it only
after proving the exact source/version labels, runtime version, platform set, and digest. Confirm the
successful `main` CI run refers to the same commit. The workflow requires the tag to equal the main tip
at transaction start, records that commit, and revalidates that the annotated remote tag still peels to
it before draft creation and final publication. `main` may advance while the long release run executes.

Optionally preflight the same strict tag grammar before creating it:

```bash
[[ "$TAG" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(preview|beta|rc)\.(0|[1-9][0-9]*))?$ ]]
```

### 4. Create and push one annotated tag

Use the project release identity and an annotated tag:

```bash
GIT_AUTHOR_NAME=kunori-kiku GIT_AUTHOR_EMAIL=rokuyanlin@gmail.com \
GIT_COMMITTER_NAME=kunori-kiku GIT_COMMITTER_EMAIL=rokuyanlin@gmail.com \
git tag -a "$TAG" -m "$TAG — <one-line summary>"

git cat-file -t "refs/tags/$TAG"   # must print: tag
git push origin "refs/tags/$TAG"
```

Never use a lightweight tag. The release validator rejects one before running any build.

### 5. Watch the workflow; do not race it

The tag push is the publication trigger. The global concurrency group prevents two release
transactions from racing mutable pointers. Push only one release tag at a time and wait for it to
finish: GitHub keeps at most one pending run in a basic concurrency group, so a third queued tag can
replace the older pending run rather than form an unbounded queue. Find the `Release` run for `$TAG`
in Actions (or with `gh run list --workflow release.yml`), then wait for the entire graph, including
versioned-image publication/recovery, draft upload, pointer convergence, and final verification:

```bash
gh run list --workflow release.yml --limit 10
gh run watch <run-id> --exit-status
```

Do **not** run `gh release create`. After all gates and the versioned-image check, the workflow selects
one absent-or-exact private draft, creates it explicitly when absent, and uses a commit-SHA-pinned
release action only to upload the allowlisted files. A recovery draft may contain only a same-byte
subset; public, misclassified, unexpected, or different-byte state fails closed. Both finalizers
re-download the artifacts and re-seal the remote object immediately before publication. A concurrent
manual create/edit defeats those rails.

Failure recovery depends on whether either publication boundary was crossed. Check both:

```bash
IMAGE_VERSION=${TAG#v}
gh api "repos/kunori-kiku/yet-another-overlay-generator/releases?per_page=100" \
  | jq -e --arg tag "$TAG" 'any(.[]; .tag_name == $tag)'  # includes authenticated draft listings
docker buildx imagetools inspect "ghcr.io/kunori-kiku/yaog-controller:$IMAGE_VERSION"
```

- If both lookups are absent, the tag is still unpublished. Fix and merge `main`, delete the remote
  and local tag, and recreate the annotated tag at the newly verified tip. This is the only case in
  which moving the tag is permitted.
- If a versioned container or GitHub release object exists, the version has crossed a publication
  boundary. Do not move the Git tag, overwrite the image with different bytes, delete the recovery
  draft, or reuse the version. The Docker job can recover a matching partial registry push and rejects
  a mismatch. A failed RC/GA finalizer may have moved Docker Hub `latest`, GHCR `latest`, or GitHub
  Latest before a later system failed. Rerun only that failed job: it is designed to converge the same
  verified digest/draft and re-check every pointer. If the existing state is not an exact recovery
  state, preserve the evidence and cut the next release number.

If a deterministic defect in the tagged workflow makes that ordinary rerun impossible, do not replace
it with manual `gh release create` or registry mutation. First fix and merge the recovery tooling, pass
exact-commit `main` CI, and independently review `.github/workflows/recover-release.yml`. That workflow
is deliberately narrower than a release build: RC/GA and GHCR only, default-branch dispatch only, and
the same `yaog-release-publication` lock. It requires the exact immutable tag/revision, failed Release
run, version-image digest, and an explicit publication confirmation. It proves the source run's complete
pre-draft job graph, binds the digest to the successful source build log, resolves the exact unexpired
artifact IDs, re-seals all 22 files, and adopts—but never builds or overwrites—the versioned image before
running the ordinary private-draft and Latest finalizers.

Dispatch it only after verifying that Docker Hub was disabled in the source run (the workflow also
enforces that condition):

```bash
gh workflow run recover-release.yml --ref main \
  -f release_tag="$TAG" \
  -f source_revision="$SOURCE_COMMIT" \
  -f source_run_id="$FAILED_RELEASE_RUN_ID" \
  -f expected_ghcr_digest="$IMAGE_DIGEST" \
  -F confirm_publication=true
```

Watch and verify the recovery run exactly like the ordinary Release run. If its immutable run, artifact,
tag, image, or draft checks disagree, preserve the state and advance the release number; do not loosen
the allowlists to force publication.

### 6. Edit notes after automation finishes

Once the workflow has completed, edit only the release title/body in place when richer notes are
needed:

```bash
gh release edit "$TAG" --title "$TAG" --notes-file <notes-file>
```

The workflow sets preview/beta prerelease state explicitly and keeps them off Latest. It sets RC/GA
Latest only after container promotion succeeds. Do not manually change that classification as part
of an ordinary notes edit. Editing after the workflow is safe; creating before or during it is not.

### 7. Verify the published result

Verify, do not infer, the final state:

- `gh release view "$TAG" --json tagName,name,isDraft,isPrerelease,assets` shows the intended release
  state and all assets. Compare `gh api repos/{owner}/{repo}/releases/latest --jq .tag_name` with
  `$TAG` to verify RC/GA Latest explicitly; preview/beta must remain a prerelease and must not replace
  the prior Latest. The current graph produces 22 files: seven platform bundles, seven standalone
  agents, seven `.sha256` sidecars, and one versioned local-design ZIP.
- Download all assets and repeat the exact local verifier. The workflow has already run Windows
  amd64 and 386 binaries natively, but post-publication checks should still confirm the download:

  ```bash
  rm -rf "/tmp/yaog-release-$TAG"
  gh release download "$TAG" --dir "/tmp/yaog-release-$TAG"
  SOURCE_COMMIT=$(git rev-list -n 1 "refs/tags/$TAG")
  bash scripts/verify-release-assets.sh "/tmp/yaog-release-$TAG" "$TAG" "$SOURCE_COMMIT"
  ```

- Confirm the Release workflow and its called Docker job are green.
- Inspect the GHCR manifest (and Docker Hub when configured): the policy-non-overwritten version must include
  `linux/amd64` and `linux/arm64`; RC/GA must also have `latest`, while preview/beta must not move it.
- Confirm `CHANGELOG.md`, `STATUS.md`, release notes, tag target, and published asset set all describe
  the same commit and version.

After those checks pass, update any status ledger or operator-facing notes that need to move from
candidate/ready-and-uncut wording to **published** or **shipped**. That factual transition belongs
after publication verification, never in the pre-tag release-state PR.

## Build-version injection

Every release main package declares `var BuildVersion = "dev"`. The release bundle matrix extends
the stripping flags with the tag stamp:

```bash
LDFLAGS="-s -w -X main.BuildVersion=${TAG}"
go build -trimpath -ldflags "$LDFLAGS" -o yaog-agent ./cmd/agent/
```

The same convention is applied to `yaog-server` and `yaog-compiler`. Docker passes the tag as the
`BUILD_VERSION` build argument and the Dockerfile applies the same linker stamp. Non-release builds
retain `dev`; `yaog-agent version`, `yaog-server version`, and `yaog-compiler version` expose the
embedded value. This version feeds controller reporting and the signed agent self-update floor, so
never replace the linker flags in a way that drops either `-s -w` or `-X main.BuildVersion`.
