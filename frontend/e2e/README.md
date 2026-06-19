# YAOG browser E2E harness (plan-13 / Subject 3)

The first browser end-to-end layer for YAOG: real built panel + a live Go controller +
a real agent fixture, driven headless by Playwright. This doc is the contract later
Subject-3 plans (14–18) extend by **adding one `*.spec.ts`** — not by re-solving bring-up.

## Run it

```bash
# from the repo root: build the TEST-ONLY bring-up binaries (e2eserver MUST be -tags airgap)
mkdir -p .e2e-bin
go build -tags airgap -o .e2e-bin/e2eserver ./cmd/e2eserver
go build -o .e2e-bin/e2eagent ./cmd/e2eagent

cd frontend
npm run build                              # produces dist/ (the panel the boots serve)
npx playwright install --with-deps chromium
npm run test:e2e                           # runs e2e/*.spec.ts headless
```

CI does the same in the `frontend-e2e` job (required check). Binary/dist locations are
overridable via `E2E_SERVER_BIN`, `E2E_AGENT_BIN`, `E2E_WEB_DIR`; the defaults
(`.e2e-bin/*`, `frontend/dist`) match the commands above.

> **Run ONE `npm run test:e2e` at a time per checkout.** globalSetup/teardown share a single
> handoff (`e2e/.harness/state.json`) and boot fixed processes, so two concurrent invocations in
> the same working tree clobber each other's state file + boots (ECONNREFUSED / ENOENT mid-run).
> CI runs a single invocation per job, so this is a local-dev caveat only — don't kick off a
> second suite (or a tool that runs the suite) while one is in flight.

## The boot model (and why)

`globalSetup.ts` boots `cmd/e2eserver` **three times** from one binary (the operator-flow suite,
plan-14, added the keystone-ON tenant on top of plan-13's controller + airgap boots):

| boot | `HarnessState` key | flags | role |
|------|--------------------|-------|------|
| **controller (keystone-OFF)** | `controller` | `--mode controller --state-dir <tmpA> …` | the default tenant; NO operator credential is ever pinned, so the keystone-OFF deploy branch + the cookie-only F1 leg stay reachable |
| **controller (keystone-ON)** | `controllerOn` | `--mode controller --state-dir <tmpB> …` | a separate tenant where the keystone spec pins an operator SIGNING credential + the WebAuthn/TOTP login legs run (self-cleaning); isolated so pinning never flips the keystone-OFF boot |
| **air-gap** | `airgap` | `--mode airgap --web-dir dist` | serves the unauthenticated `/api/compile` oracle + the panel SPA |

The two controller boots have separate state dirs precisely so a pinned credential on one never
flips the other (`EnableController` arms `operatorAuth` **unconditionally**, so a single
controller boot cannot also serve the *unauthenticated* `/api/compile` the air-gap oracle
exposes). The controller-vs-airgap auth split is observable at the HTTP layer:
`airgap-design.spec.ts` asserts the air-gap `/api/compile` is **open (200)** and
`controller-fleet.spec.ts` asserts the controller boot's is **gated (401)**. The authoritative
server-level assertion lives in the required Go gate test
`internal/api/airgap_auth_gate_test.go` (`TestAirgapRoutes_GatedInControllerMode`, run by CI's
`go test -tags airgap ./...`); the Playwright assertions are the cross-stack echo.

The operator-flow suite (plan-14) adds `login`, `login-webauthn` (TOTP + passkey via a CDP
virtual authenticator), `session`, `deploy`, `deploy-keystone` (the keystone-ON signature
accepted by the Go verifier), `export-import`, `revoke`, and `fixtures` (the `leakOracle` custody
gate + `totpNow` self-checks) on top of this bring-up. See `fixtures/leakOracle.ts` for the
zero-knowledge custody checks run post-deploy / post-refresh / post-logout.

`cmd/e2eserver` MUST be built `-tags airgap`: the four air-gap routes live behind
`//go:build airgap` (plan-7), so a default build's air-gap boot would 404 `/api/compile`.

## READY-line handshake (no sleeps)

Each boot binds its `:0` listener(s) first, then prints exactly one line to stdout:

```
E2E_READY mode=<controller|airgap> panel=<host:port> [agent=<host:port>] [enroll=<token>]
```

`globalSetup` parses both lines (waiting on the line, never a sleep), asserts exactly one
`controller` + one `airgap`, and writes the resolved ports + enrollment token + child PIDs
to `e2e/.harness/state.json` (gitignored). `globalTeardown` kills the children and removes
the temp state dir.

## Fixtures

- `fixtures/config.ts` — the single source of harness constants (tenant, operator
  user/pass, enroll node). The Go side hard-codes nothing; `globalSetup` passes these to the
  controller boot as flags.
- `fixtures/harness.ts` — reads `state.json` (`readHarness()`), plus `httpURL()` and the
  path constants. The `HarnessState` shape: `{ controller:{panel,agent,enrollToken},
  airgap:{panel}, agentBin, pids, tmpDir }`.
- `fixtures/seedStore.ts` — **the `controller-storage` seed contract**. Writes the Zustand
  `persist` envelope `{ state:{…}, version:0 }` with ONLY allowlist keys
  (`mode`/`baseURL`/`agentBaseURL`) via `context.addInitScript`, BEFORE the page loads, so
  the store hydrates pointed at the OS-assigned `:0` ports (never the static default port).
  `seedControllerMode(context,{baseURL,agentBaseURL})` / `seedLocalMode(context)`.
- `fixtures/seed-topology.json` — a small router+peer topology that compiles cleanly; the
  body for `/api/compile` canaries.

## The agent fixture (`cmd/e2eagent`)

Reuses the REAL `internal/agent` client (EnsureKey → Enroll → Poll/Fetch/VerifyBundle →
Report); never runs `install.sh`. Spawn it from a spec as a child process:

```
e2eagent --controller http://<agent host:port> --node-id <id> --token <enrollToken> \
         [--mock] [--key <tmp.key>] [--agent-version <v>]
```

`--mock` = enroll + report a fast visible check-in (no poll/fetch); default = the full wire
(use after an operator deploy promotes a generation, plans 14+). It prints
`E2E_AGENT node=<id> reported_generation=<n> mode=<real|mock>` and exits 0.

## Add a scenario (plans 14–18)

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
The two existing specs are canaries only (smoke proof); scenario depth belongs in plans
14–18. The air-gap design canary's `/api/compile` round-trip pins the retained air-gap
compute oracle; post-Subject-1 local mode compiles in-browser, so the panel UI no longer
hits that route (see `airgap-design.spec.ts`).
