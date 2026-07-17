# plan-8 — Integrated review, exact gates, specifications, closure, and release preparation

**Outline:** [outline.md](./outline.md)
**Subject:** deployment-stability-and-charted-telemetry-2026_07_17
**Track:** all components + documentation + closure
**Depends on:** plans 1–7

## Prerequisites

- Plans 1–7 are completed, pushed, independently reviewed, fixed, re-reviewed, and closed.
- No insertion plan, unresolved reviewer finding, owner-owned worktree change, or uncommitted generated
  artifact remains.
- The active branch is `fix/rc12-telemetry-drafts`; it contains only intentional subject work and
  plan/closure bookkeeping based on current `origin/main`.
- Read `outline.md`, root `PRINCIPLES.md`, and every completed plan/closure note before review.
- This is the final `execute-implementation-plan` plan. Plan 9 is a terminal, read-only-source release
  checklist and must not be passed to `execute-implementation-plan` or `close-phase`.

## Reads from specs

Reads from specs: compiler-allocation, model-validation, controller-agent-api, controller-stage-promote, controller-store, agent, artifacts-signing, panel-deploy-fleet, panel-design

## Goal

Adversarially review the complete fixes-and-features subject, repair every actionable finding, run the
exact required CI/release-preparation gates once on the final implementation candidate, refresh the
durable architecture, prepare v2.0.0-rc.12 in candidate/ready-and-uncut language, close and archive the
subject, merge every implementation/spec/status/closure commit, and prove the resulting clean
`origin/main` commit is ready for the terminal publication checklist without creating a release tag.

## Read first

Read these authorities in order and re-grep named jobs/symbols if line numbers shifted:

1. `outline.md:3-44,46-68,87-117,119-213` — mission, minimal principles, owner decisions, milestone
   ordering, release-tag-last rule, and closure criteria.
2. `PRINCIPLES.md` — read in full; do not add URL mechanics or transient release state.
3. `CLAUDE.md` and ignored `AGENTS.md` — read in full and keep synchronized only for durable guidance.
4. `STATUS.md`, `CHANGELOG.md`, and `RELEASING.md:20-27,47-118,120-215,219-322` — current shipped state,
   release notes, exact release transaction, recovery, and verification.
5. `.github/workflows/ci.yml:8-312` — required Go, drift, frontend, WASM, E2E, real-tunnel, and security
   jobs plus advisory scans.
6. `.github/workflows/release.yml:18-301,552-950` — tag validation, seven release gates, exact 22 assets,
   private draft, versioned container, and Latest finalizers.
7. `.github/workflows/docker.yml:1-60,67-188,210-389` — candidate image, exact source/version checks,
   non-overwrite adoption, amd64/arm64 verification, and optional Docker Hub convergence.
8. `.github/actions/realtunnel-setup/action.yml:17-55` — exact Linux prerequisites and integration-test
   build used by CI/release.
9. `scripts/test-release-assets.sh`, `scripts/verify-release-assets.sh`,
   `scripts/verify-controller-image.sh`, and `scripts/verify-release-ref.sh` — read in full.
10. `specs/README.md` and `/home/kunorikiku/.codex/skills/refresh-specs/SKILL.md` — cached architecture
    reading guide and mandatory refresh workflow/diagram checkpoint.
11. `docs/spec/operations/active-telemetry.md`, `docs/spec/operations/telemetry-history.md`,
    `docs/spec/controller/deploy.md`, `docs/spec/controller/controller-api.md`,
    `docs/spec/controller/persistence.md`, `docs/spec/compiler/allocation-stability.md`, and
    `docs/spec/frontend/architecture.md` — normative descriptions this subject changes.
12. `git diff origin/main...HEAD`, `git status --short`, `git log --oneline --decorate -30`, and all
    source/test files changed by plans 1–7.

## Implementation steps

### Step 1 — Whole-subject independent review

Spawn independent read-only reviewers in parallel, each receiving `outline.md`, all plans, the complete
`origin/main...HEAD` diff, and its exact review dimension:

- **Security/agent execution:** signed successor policy, keystone coverage, installer capability gates,
  old-agent parse/apply order, URL redirect/proxy/TLS/time bounds, fixed `nvidia-smi` argv, filesystem
  parsing, opaque identifiers, command output limits, last-known-good state, and private-key custody.
- **Compiler/controller/history:** structured 422 versus operational errors, blank-draft no-mutation,
  historical client allocation stability, preview/stage parity, report-audit behavior, telemetry
  dedupe/sample-time bounds, FileStore caps, global rollup budget, and exact probe/device pushdown.
- **Frontend/product/custody:** Fleet placement, Save-versus-Deploy, two-step agent readiness, visible
  ten-second refresh feedback, URL latest status versus charts, device inventory/samples split, exact
  selectors, shared charts, a11y/i18n, and localStorage stripping.
- **Compatibility/framework/hygiene:** byte-exact v1 policy, old-agent rollout, Go/TypeScript wire drift,
  `NumericDefinitions()` exact parity, dependency direction, stale/dead/duplicate code, comments/specs,
  and minimal-test discipline.

Require findings to name severity, exact path/symbol, failure mechanism, and smallest corrective action.
Consolidate duplicates without weakening severity.

### Step 2 — Fix findings and obtain clean re-review

- Fix every blocker and meaningful medium finding. Do not improvise across a HIGH principle; draft the
  predeclared insertion plan and stop if a fix changes policy authority, custody, persisted allocation
  meaning, or the release transaction.
- Add a test only when a finding exposed an unguarded production/security contract. Extend an existing
  focused test file whenever possible; do not create another matrix for a behavior already covered.
- Run only the affected focused commands while iterating.
- Return the corrected diff to the same reviewers. No gate, documentation, closure, or release
  preparation starts until the re-review is clean.

### Step 3 — Run the exact required Go/frontend/WASM/security gates

Run from repository root in a clean shell with the `go.mod` toolchain and Node 20. These commands mirror
the required portions of `.github/workflows/ci.yml`; do not substitute `npm install`, omit the coverage
environment variable, or rely on a previous WASM binary.

```bash
set -euo pipefail

drift="$(gofmt -l ./cmd ./internal)"
if [ -n "$drift" ]; then
  echo "gofmt drift — run 'gofmt -w ./cmd ./internal':" >&2
  echo "$drift" >&2
  exit 1
fi

GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct go vet ./...
GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct go test -race ./...
if command -v pwsh >/dev/null 2>&1; then
  GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct \
    go test ./internal/renderer -run 'TestRenderDeployScripts_PowerShell' -count=1
else
  echo "local pwsh unavailable; require the PR and exact-main Go jobs to execute this gate" >&2
fi
YAOG_CONFORMANCE_COVERAGE_FLOOR=1 GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct \
  go test ./internal/localcompile/ -run TestCoverageFloor -v
GOSUMDB=sum.golang.org GOPROXY=https://proxy.golang.org,direct \
  go test ./internal/wiredrift/ -v

(
  cd frontend
  npm ci --legacy-peer-deps
  npm run lint
  npm run build
)
GOOS=js GOARCH=wasm go build -o web/yaog.wasm ./cmd/wasm
(
  cd frontend
  npm run vitest
)

GOOS=js GOARCH=wasm go build -o web/yaog.wasm ./cmd/wasm
node scripts/wasm-conformance-gate.mjs

go install golang.org/x/vuln/cmd/govulncheck@v1.6.0
"$(go env GOPATH)/bin/govulncheck" ./...
go test -tags dast ./internal/dast/ -v -timeout 180s
```

Run the advisory scans and record their output without converting an existing advisory into a required
gate:

```bash
go install github.com/securego/gosec/v2/cmd/gosec@latest
"$(go env GOPATH)/bin/gosec" -conf .gosec.json -no-fail -fmt text ./...
(
  cd frontend
  npm ci --legacy-peer-deps
  npm audit --omit=dev
) || echo "npm audit advisory reported findings; retain the output for review" >&2
```

### Step 4 — Run the exact required frontend E2E gate

Rebuild the E2E binaries and panel exactly as CI does. The required suite excludes only the explicitly
non-blocking visual corpus.

```bash
set -euo pipefail
rm -rf .e2e-bin
mkdir -p .e2e-bin
go build -o .e2e-bin/e2eserver ./cmd/e2eserver
go build -o .e2e-bin/e2eagent ./cmd/e2eagent
(
  cd frontend
  npm ci --legacy-peer-deps
  npm run build:wasm
  VITE_E2E=1 npm run build
  test -f dist/yaog.wasm
  test -f dist/wasm_exec.js
  npx playwright install --with-deps chromium
  npm run test:e2e -- --grep-invert "visual corpus"
)
```

If this changes committed `web/wasm_exec.js`, stop and diagnose a toolchain mismatch; do not include an
unexplained generated drift in the release candidate.

### Step 5 — Run the required real-tunnel gate

Run on a compatible Ubuntu host with root, WireGuard, systemd-nspawn, debootstrap, and network access.
This is the executable equivalent of `.github/actions/realtunnel-setup` plus CI's required canary:

```bash
set -euo pipefail
sudo modprobe wireguard
lsmod | grep -q wireguard

retry() {
  for attempt in 1 2 3; do
    "$@" && return 0
    echo "attempt $attempt failed: $*" >&2
    sleep $((attempt * 5))
  done
  return 1
}
retry sudo -E apt-get update
retry sudo -E apt-get install -y systemd-container debootstrap

build_rootfs() {
  sudo rm -rf /tmp/yaog-rt-rootfs
  sudo -E debootstrap --variant=minbase --components=main,universe \
    --include=systemd,systemd-sysv,udev,dbus,wireguard-tools,babeld,iproute2,iptables,nftables,openssl,iputils-ping,kmod \
    noble /tmp/yaog-rt-rootfs http://archive.ubuntu.com/ubuntu/
}
built=false
for attempt in 1 2 3; do
  if build_rootfs; then
    built=true
    break
  fi
  echo "debootstrap attempt $attempt failed" >&2
  sleep $((attempt * 10))
done
[ "$built" = true ]

go test -c -tags integration -o /tmp/realtunnel.test ./test/realtunnel/
set -o pipefail
sudo REALTUNNEL_ROOTFS=/tmp/yaog-rt-rootfs /tmp/realtunnel.test \
  -test.v -test.timeout 600s 2>&1 | tee /tmp/realtunnel-ci.log
grep -q '^--- PASS: TestSimpleMeshCanary' /tmp/realtunnel-ci.log
! grep -q '^--- SKIP: TestSimpleMeshCanary' /tmp/realtunnel-ci.log
```

If the local host cannot satisfy these prerequisites, the required PR and post-merge `realtunnel` jobs
must still pass on GitHub Actions. Do not call an unavailable local hardware/integration gate a pass.

### Step 6 — Run release-shape checks

These are the independent verifier tests prescribed by `RELEASING.md`:

```bash
set -euo pipefail
go test ./scripts
bash scripts/test-release-assets.sh
docker buildx build --check .
```

### Step 7 — Update durable docs and run a full specs refresh

- Update the normative docs named in `Read first` for:
  - draft Save versus deploy validation and compatibility-only preview fallback;
  - endpoint-specific client allocation stability;
  - routine report audit suppression and display-only names;
  - strict v1 plus successor-policy/two-deployment rollout;
  - constrained URL semantics and live-only actual status;
  - automatic device discovery/bounds, `device_inventory` versus `device_samples`, exact numeric
    definitions, selector pushdown, history gaps/valid zeros, and Fleet UI/custody.
- Keep URL mechanics and temporary rc.12 state out of root `PRINCIPLES.md`.
- Synchronize `CLAUDE.md` and ignored `AGENTS.md` with only durable architecture/operational rules.
  Verify `git check-ignore -q AGENTS.md` and inspect it for secrets/transient plan text.
- Invoke `refresh-specs` explicitly and choose the full refresh. Follow its multi-agent survey/drafting
  and quarantine rules.
- The skill-required primary Mermaid diagram verification is the one unavoidable user checkpoint in
  this otherwise autonomous subject. Present the diagram before component drafting. It must show:

```text
Fleet signed policy
  -> strict v1 or successor bundle member
  -> capability/readiness and fail-before-mutation apply
  -> URL/device collection and reliable heartbeat
  -> bounded controller latest/history/rollup
  -> Fleet live state and exact-series shared charts
```

- After diagram verification, regenerate affected components with code citations and complete the
  refresh skill's review/commit steps. Re-review the generated specs against the final code.
- Documentation-only edits do not require rerunning the entire local suite, but rerun
  `git diff --check`, link/spec checks, and any gate whose executable workflow or generated contract was
  changed. Required PR/main CI remains authoritative over the final commit.

### Step 8 — Prepare v2.0.0-rc.12 candidate state

- Move the actual entries from `Unreleased` into `## [2.0.0-rc.12] - <actual publication date>`, leave a
  fresh `Unreleased`, and update compare links.
- Explain the narrow owner-authorized RC feature addition, v1 compatibility bridge, exact agent upgrade
  sequence, URL status semantics, device support limits, and any honest real-hardware GPU validation gap.
- Update `STATUS.md` to **release-ready / uncut**, never published/shipped.
- Verify neither source docs nor release notes claim unavailable NVIDIA/AMD hardware testing passed.
- Confirm the intended tag, GitHub release/draft, and official versioned GHCR reference are absent. This
  is an early release-readiness check; the terminal plan repeats it authoritatively immediately before
  tagging.

### Step 9 — Final review, commit, and push the candidate work

Return the documentation/spec/release-prep diff to independent reviewers for accuracy, stale references,
test evidence, and candidate-versus-published wording. Fix findings and obtain clean re-review.

Stage only intentional subject files:

```bash
git add \
  cmd \
  internal \
  frontend \
  test \
  scripts \
  .github \
  docs \
  specs \
  CLAUDE.md \
  CHANGELOG.md \
  STATUS.md \
  implementation_plans/deployment-stability-and-charted-telemetry-2026_07_17
git diff --cached --check
GIT_AUTHOR_NAME='KunoriKiku' GIT_AUTHOR_EMAIL='rokuyanlin@gmail.com' \
GIT_COMMITTER_NAME='KunoriKiku' GIT_COMMITTER_EMAIL='rokuyanlin@gmail.com' \
git commit -m "$(cat <<'EOF'
chore(release): prepare v2.0.0-rc.12
EOF
)"
git push origin HEAD:fix/rc12-telemetry-drafts
```

If refresh-specs already created scoped commits, retain them; the command above commits only remaining
review/release-preparation changes. Never amend or force-push.

### Step 10 — Close and archive everything before merge

This step resolves the tag-last/close-phase boundary:

1. Let `execute-implementation-plan` mark plan 8 done in the outline and push its separate status commit.
2. In the same outline bookkeeping commit, classify plan 9 as
   `terminal checklist (not executable; publication pending)` and record that plan 8 is the last
   executable plan. Do not mark rc.12 published.
3. Invoke `close-phase` for plan 8 with **subject scope**. The implementation subject is delivered to a
   release-ready boundary; the external publication is deliberately the terminal checklist.
4. Allow close-phase to archive plan 8 and the whole subject folder, write its closure README, refresh
   `STATUS.md`, and commit its bookkeeping. The archived folder must retain plan 9 as the terminal
   checklist.
5. Inspect every close-phase commit. `STATUS.md` and the closure README must say implementation
   delivered / rc.12 ready and uncut, not published. If generic close-phase wording says shipped or
   published, correct it in a dedicated pre-merge documentation commit.
6. Keep the release branch; do not delete it during close-phase. Push every closure/status correction:

```bash
git push origin HEAD:fix/rc12-telemetry-drafts
```

There must be no closure, archive, outline, spec, guide, changelog, or status mutation left for after
the release tag.

### Step 11 — Merge the complete reviewed/closed subject and verify exact main

Open or reuse one cumulative PR containing implementation through closure. The repository uses squash
merge for these subject PRs.

```bash
set -euo pipefail
BRANCH=fix/rc12-telemetry-drafts
PR="$(gh pr list --head "$BRANCH" --base main --state open --json number --jq '.[0].number // empty')"
if [ -z "$PR" ]; then
  gh pr create --base main --head "$BRANCH" --fill
  PR="$(gh pr list --head "$BRANCH" --base main --state open --json number --jq '.[0].number')"
fi
gh pr checks "$PR" --required --watch
gh pr merge "$PR" --squash

git fetch origin main --tags
git switch main
git pull --ff-only origin main
test -z "$(git status --porcelain)"
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
```

Wait for the required push-to-main CI run on that exact squash commit:

```bash
set -euo pipefail
SOURCE_COMMIT="$(git rev-parse HEAD)"
CI_RUN=
for attempt in 1 2 3 4 5 6; do
  CI_RUN="$(gh run list --workflow ci.yml --event push --commit "$SOURCE_COMMIT" --limit 10 \
    --json databaseId,headSha --jq 'map(select(.headSha == "'"$SOURCE_COMMIT"'"))[0].databaseId // empty')"
  [ -n "$CI_RUN" ] && break
  sleep 5
done
test -n "$CI_RUN"
gh run watch "$CI_RUN" --exit-status

git fetch origin main --tags
test -z "$(git status --porcelain)"
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
```

If main advanced before tagging, the terminal checklist must restart its exact-main/CI preflight; never
tag an older reviewed ancestor.

## Tests produced by this plan

No new test file is planned. Plan 8 runs the complete existing required gate set once and adds only a
focused regression if independent review finds a genuinely unguarded production/security contract.
Any such test must be added to the existing closest domain test file, documented in the outline
decision log, and classified **perpetual** only when it directly pins a subject/root principle; otherwise
stop and draft an insertion plan rather than creating a disposable test immediately before closure.

## Definition of done

- Whole-subject security, backend/history, frontend/custody, compatibility/framework, and hygiene review
  is clean after fixes and re-review.
- Required Go, drift, frontend, WASM, security, E2E, and real-tunnel gates pass with the exact commands or
  the exact required GitHub jobs; advisory scan findings are recorded honestly.
- Release asset verifier tests and Dockerfile build check pass.
- Normative docs, synchronized local guides, and a full reviewed specs refresh describe the actual code.
- CHANGELOG and STATUS describe v2.0.0-rc.12 as ready and uncut, including residual hardware risk.
- Plan 8 and the subject are closed and archived before merge; plan 9 remains only as a terminal
  publication checklist outside execute/close-phase.
- Every implementation/spec/status/closure commit is included in the reviewed squash merge.
- Clean local `main` equals current `origin/main`, and required push-to-main CI is green on that exact
  commit.
- No v2.0.0-rc.12 tag, release/draft, or official versioned image was created by this plan.

## Out of scope

- Creating or pushing the release tag.
- Manually creating/uploading a GitHub release or mutating official container references.
- Post-tag repository commits, status edits, plan closure, archive moves, branch deletion, or release-note
  edits.
- New telemetry types, alerting, vendor daemons, or broad test suites discovered during review.
