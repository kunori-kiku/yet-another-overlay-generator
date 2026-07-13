# YAOG browser E2E harness (plan-13 / Subject 3)

The first browser end-to-end layer for YAOG: real built panel + a live Go controller +
a real agent fixture, driven headless by Playwright. This doc is the contract later
Subject-3 plans (14–18) extend by **adding one `*.spec.ts`** — not by re-solving bring-up.

## Run it

```bash
# from the repo root: build the TEST-ONLY bring-up binaries (plain controller-only builds)
mkdir -p .e2e-bin
go build -o .e2e-bin/e2eserver ./cmd/e2eserver
go build -o .e2e-bin/e2eagent ./cmd/e2eagent

cd frontend
VITE_E2E=1 npm run build                   # produces dist/ (the panel the boots serve)
npx playwright install --with-deps chromium
npm run test:e2e                           # runs e2e/*.spec.ts headless
```

`VITE_E2E=1` compiles in the test-only ErrorBoundary render-throw probe (`App.tsx` /
`E2ERenderThrowProbe`) that `e2e/adversarial/error-render.spec.ts` drives; it is
dead-code-eliminated from any build that does not set the flag, so production/release/Docker
bundles never ship it. Omit the flag and that one spec fails (the probe is absent); every other
spec is unaffected.

CI does the same in the `frontend-e2e` job (required check), building with `VITE_E2E=1`.
Binary/dist locations are overridable via `E2E_SERVER_BIN`, `E2E_AGENT_BIN`, `E2E_WEB_DIR`; the
defaults (`.e2e-bin/*`, `frontend/dist`) match the commands above.

### Engine B (Go) — `internal/edgecase/` adversarial corpus + fuzz + DoS oracle

The Go-only adversarial layer (plan-16 / 3.4) lives under `internal/edgecase/` and ships in no
binary. From the repo root:

```bash
go test ./internal/edgecase/...                               # corpus drift guard + DoS oracle (heavy cases Short()-gated)
go test -run TestCorpusWriteOrVerify ./internal/edgecase/ -update   # regenerate the committed corpus/*.json
go test -run Fuzz -fuzztime=20s ./internal/edgecase/...        # bounded fuzz (CI); longer locally:
go test -fuzz=FuzzCompile -fuzztime=5m ./internal/edgecase/    # extended local fuzzing
```

> The durable DoS oracle (`dos_repro_test.go`) measures `compiler.Compile` directly. The former
> `-tags airgap` `/api/compile` HTTP tier (`dos_repro_http_test.go`) was retired with the anonymous
> air-gap route in framework-refactor plan-9.

> **Run ONE `npm run test:e2e` at a time per checkout.** globalSetup/teardown share a single
> handoff (`e2e/.harness/state.json`) and boot fixed processes, so two concurrent invocations in
> the same working tree clobber each other's state file + boots (ECONNREFUSED / ENOENT mid-run).
> CI runs a single invocation per job, so this is a local-dev caveat only — don't kick off a
> second suite (or a tool that runs the suite) while one is in flight.

## The boot model (and why)

`globalSetup.ts` boots `cmd/e2eserver` **twice** from one binary — two controller tenants
(framework-refactor plan-9 retired the former third, air-gap, boot):

| boot | `HarnessState` key | flags | role |
|------|--------------------|-------|------|
| **controller (keystone-OFF)** | `controller` | `--mode controller --state-dir <tmpA> …` | the default tenant; NO operator credential is ever pinned, so the keystone-OFF deploy branch + the cookie-only F1 leg stay reachable. Also serves the local-mode design specs (wasm-design, link-direction) — the SPA + wasm static assets are served identically to any boot, and those flows compute in-browser |
| **controller (keystone-ON)** | `controllerOn` | `--mode controller --state-dir <tmpB> …` | a separate tenant where the keystone spec pins an operator SIGNING credential + the WebAuthn/TOTP login legs run (self-cleaning); isolated so pinning never flips the keystone-OFF boot |

The two controller boots have separate state dirs precisely so a pinned credential on one never
flips the other. There is **no anonymous compute surface**: plan-9 deleted the air-gap
`/api/{validate,compile,export,deploy-script}` routes from every build, so the shipped controller
registers no such handler. `controller-fleet.spec.ts` asserts a POST to `/api/compile` returns
**404** (route absent — the SPA static handler 404s any unmatched `/api/` path); the authoritative
server-level proof is the perpetual Go guard `internal/api/no_anonymous_compute_test.go`
(the four routes 404 on `Server.Handler()`; no handler re-declared), run by the default
`go test ./...`. The Playwright 404 is the cross-stack echo.

The local-mode design specs (`wasm-design`, `link-direction`) run against the keystone-OFF
controller boot in `local` mode (seeded client-side by `seedLocalMode`, so no login gate) and
compute entirely in-browser through `web/yaog.wasm` — they never touch a server API, so the
controller's operator auth is irrelevant to them. `wasm-design` needs `web/yaog.wasm` +
`web/wasm_exec.js` in `dist/` (`npm run build:wasm` before `npm run build`).

The operator-flow suite (plan-14) adds `login`, `login-webauthn` (TOTP + passkey via a CDP
virtual authenticator), `session`, `deploy`, `deploy-keystone` (the keystone-ON signature
accepted by the Go verifier), `export-import`, `revoke`, and `fixtures` (the `leakOracle` custody
gate + `totpNow` self-checks) on top of this bring-up. See `fixtures/leakOracle.ts` for the
zero-knowledge custody checks run post-deploy / post-refresh / post-logout.

`cmd/e2eserver` builds with a plain `go build ./cmd/e2eserver` (no build tag).

## READY-line handshake (no sleeps)

Each boot binds its `:0` listeners first, then prints exactly one line to stdout:

```
E2E_READY mode=controller panel=<host:port> agent=<host:port> enroll=<token>
```

`globalSetup` parses both lines (waiting on the line, never a sleep), asserts both report
`controller`, and writes the resolved ports + enrollment tokens + child PIDs to
`e2e/.harness/state.json` (gitignored). `globalTeardown` kills the children and removes
the temp state dirs.

## Fixtures

- `fixtures/config.ts` — the single source of harness constants (tenant, operator
  user/pass, enroll node). The Go side hard-codes nothing; `globalSetup` passes these to the
  controller boot as flags.
- `fixtures/harness.ts` — reads `state.json` (`readHarness()`), plus `httpURL()` and the
  path constants. The `HarnessState` shape: `{ controller:{panel,agent,enrollToken},
  controllerOn:{panel,agent,enrollToken}, agentBin, pids, tmpDirs }`.
- `fixtures/seedStore.ts` — **the `controller-storage` seed contract**. Writes the Zustand
  `persist` envelope `{ state:{…}, version:0 }` with ONLY allowlist keys
  (`mode`/`baseURL`/`agentBaseURL`) via `context.addInitScript`, BEFORE the page loads, so
  the store hydrates pointed at the OS-assigned `:0` ports (never the static default port).
  `seedControllerMode(context,{baseURL,agentBaseURL})` / `seedLocalMode(context)`.
- `fixtures/seed-topology.json` — a small router+peer topology that compiles cleanly; the
  design the local-mode wasm specs seed and compile in-browser.

## The agent fixture (`cmd/e2eagent`)

Reuses the REAL `internal/agent` client (EnsureKey → Enroll → Poll/Fetch/VerifyBundle →
Report); never runs `install.sh`. Spawn it from a spec as a child process:

```
e2eagent --controller http://<agent host:port> --node-id <id> --token <enrollToken> \
         [--mock] [--key <tmp.key>] [--agent-version <v>]
```

`--mock` = enroll + report a fast visible check-in (no poll/fetch); default (`--mode checkin`)
= the full wire (use after an operator deploy promotes a generation). It prints
`E2E_AGENT node=<id> reported_generation=<n> mode=<real|mock>` and exits 0.

`cmd/e2eagent` is a thin dispatcher (`main.go`) over sibling-file modes (`--mode`):

| mode | what it drives | prints |
|------|----------------|--------|
| `checkin` (default) | enroll → [poll → fetch → VerifyBundle] → report (`--mock` skips poll/fetch) | `E2E_AGENT …` |
| `rekey` | fetch `/config`, confirm `rekey_requested`, regen WG key, `(*ControllerClient).Rekey`, re-fetch+verify, report | `REKEY_DONE node=<id> newpub=<short> gen=<n>` |
| `reprovision` | keystone-rotation node half: `VerifyMembership` REFUSES under the OLD pin → `ReprovisionKeystone` → ADOPTS under NEW | `REPROVISION node=<id> refuse-before=ok adopt-after=ok` |

`--bearer-file <path>` persists/reuses the per-node bearer across invocations (the single-use
enrollment token is consumed once); `rekey`/`reprovision` reuse it. The `reprovision` mode takes
`--operator-cred <OLD.pem> --operator-cred-alg <alg> --new-cred-pem <NEW.pem> [--operator-rpid …]`.

## Fleet-lifecycle scenarios (plan-15)

On top of the operator-flow suite, the fleet-lifecycle layer (each spec self-contained, unique
node ids per run, server-side guards driven through the real product):

| spec | asserts | negative-proof (dev-only) |
|------|---------|---------------------------|
| `fleet-enroll-lifecycle` | the full real wire: enroll → deploy → poll/fetch/verify/report the applied generation (≥1) | n/a (happy path) |
| `fleet-rekey` | Roll-keys + agent rekey clears the actor; per-node "Cancel rekey" releases a straggler WITHOUT eviction/bump | skip the agent rekey → straggler never clears |
| `fleet-revoke` | the S4/S5 delta: a revoked node's live check-in is rejected + a held SECOND enrollment token cannot resurrect it | skip the purge → second token resurrects (RED) |
| `keystone-rotation` | pin OLD → sign → acked rotate to NEW → sign → node REFUSES-then-ADOPTS via reprovision; un-acked rotate = 409 | point reprovision at the wrong PEM → adopt-after RED |
| `pin-collision-heal` | a colliding topology heals on import + deploys without a duplicate-pin 4xx (R6: raw fixture collides) | bypass heal → stage 4xx (RED) |

Sibling-plan ownership of adjacent surfaces: F1 → plan-14; basic-revoke + the `virtualAuthenticator`
helper → plan-14; the TS↔Go heal byte-equality pin → plan-5's conformance harness.

## Add a scenario (plans 16–18)

Write `e2e/<name>.spec.ts`; reuse the bring-up — do NOT change it:

```ts
import { test, expect } from '@playwright/test'
import { readHarness, httpURL } from './fixtures/harness'
import { seedControllerMode } from './fixtures/seedStore'

test('…', async ({ page, context }) => {
  const h = readHarness()
  const panel = httpURL(h.controller.panel)
  await seedControllerMode(context, { baseURL: panel, agentBaseURL: httpURL(h.controller.agent) })
  await page.goto(`${panel}/`)
  // … login, drive the panel, spawn e2eagent against h.controller.agent, assert …
})
```

Notes: the suite runs **serial** (`workers:1`, `retries:0`) — the boots and the single-use
enrollment token are shared, and the required CI gate wants flakes surfaced, not masked.
Local-mode design compiles in-browser through the WASM engine — `wasm-design.spec.ts` drives the
whole validate → compile → export → deploy-script flow against `web/yaog.wasm`, served from a
controller boot (framework-refactor plan-9 deleted the former anonymous `/api/compile` oracle and
its `airgap-design.spec.ts` canary).

## Responsive / phone device-emulation layer (plan-17 / 3.5)

`e2e/responsive/**` verifies Subject 2's responsive operator surfaces at the **`lg` = 1024px**
crossover and pins them against regression. It rides this same harness (one `npm run test:e2e` at a
time) and adds a **device-projects matrix** in `playwright.config.ts`:

| Project   | Viewport     | Touch | Side of `lg` |
|-----------|--------------|-------|--------------|
| `desktop` | 1280×800     | no    | **>= lg** (docked layout) |
| `phone`   | 360×800      | yes   | < lg (narrow-edge worst case) |
| `tablet`  | 768×1024     | yes   | < lg (768 < 1024 → MOBILE side, **not** "between") |

`e2e/responsive/**` fans out across all three; the functional/adversarial specs
(`e2e/*.spec.ts`, `e2e/adversarial/**`) stay **chromium-only** (`testIgnore`), one pass. Behavior
specs branch on `testInfo.project.name` (`isDesktopProject` / `isPhoneProject` in
`e2e/responsive/responsive.ts`) — the lg-boundary PAIR is the desktop-project result **plus** the
phone/tablet result.

**Selector contract (ARIA-first — no parallel `data-testid` taxonomy):** bind to the accessible
affordances Subject 2 emits — the hamburger by `getByRole('button', { name: 'Open navigation' })`,
the off-canvas overlays by `role="dialog"`, the `CanvasGate` by its `canvasGate.title` text, fleet
rows/cards by the node's `getByRole('link', { name: <nodeId> })` + the `<table>` element. No
`data-testid` was needed.

**Blocker → spec map:** sidebar drawer → `sidebar-drawer`; Overview grid (the one `sm`=640 pair) →
`overview-grid`; fleet-table→cards → `fleet-table-reflow`; design-route gate (Subject 2's
fully-hidden-below-lg branch) → `design-route`; page padding + no-overflow → `page-padding-overflow`;
tap targets → `tap-targets`; clean login gate → `login-mobile-clean`; read-only touch pan →
`canvas-touch`. Findings: `docs/spec/rc1/3.5-findings.md`.

**Add a responsive smoke:** drop a `*.spec.ts` in `e2e/responsive/`, branch on `isDesktopProject` /
`isPhoneProject`, assert via DOM/ARIA/`localStorage` only (never import controllerStore/
controllerClient), and reuse `expectNoHorizontalPageOverflow` / `gridTrackCount` from `responsive.ts`.

### Visual-regression corpus (`snapshots.spec.ts` + `__screenshots__/`)

`toHaveScreenshot` baselines pin the **data-independent** surfaces — **Login + Settings** —
× {phone, desktop} × {light, dark} (8 baselines). Theme + controller-mode are seeded via
`addInitScript` **before navigation** (`seedTheme` / `seedControllerMode`) so the anti-FOUC paint is
correct (no `ThemeProvider` race). Baselines live under
`e2e/responsive/__screenshots__/{project}-{platform}/` (kept in git; `playwright-report/` +
`test-results/` stay ignored).

The data-bearing surfaces (Overview/Fleet/Deploy/Security/Design) are deliberately **excluded** from
the pixel corpus: this suite shares one controller boot with the enrolling behavior specs, which seed
uniquely-named (timestamped) nodes, so those surfaces' content is non-deterministic in-suite and a
pixel baseline of them would flake. Their **responsive layout** is instead pinned by the behavior
smokes (`fleet-table-reflow` / `overview-grid` / `page-padding-overflow` / `design-route`) — the pixel
corpus complements them on stable chrome, it does not duplicate them. To add a NEW surface to the
pixel corpus it must be data-independent on the keystone-OFF boot (or mask its dynamic regions); add
it to `SURFACES` in `snapshots.spec.ts` and `--update-snapshots`. See `docs/spec/rc1/3.5-findings.md`.

- **Regenerate (Linux is authoritative):** `npx playwright test e2e/responsive/snapshots.spec.ts
  --project=desktop --project=phone --update-snapshots`, then PR-review the diff.
- **CI:** the visual corpus runs as a **non-blocking** (`continue-on-error`) step in the
  `frontend-e2e` job — it uploads diff images on a mismatch but does NOT fail the required gate. The
  committed baselines are Ubuntu-24.04-generated (matching `ubuntu-latest`); if CI shows a stable
  zero-diff over a determinism run, promote the gate to required (branch protection) and drop
  `continue-on-error`. Until then, a diff is advisory — re-baseline from the CI artifact + review.
