import { test, expect } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { readHarness, httpURL, e2eDir } from './fixtures/harness'
import { seedLocalMode, seedCanvasTopology } from './fixtures/seedStore'

// wasm-design — the automated real-browser soak for the WASM local engine (framework-refactor
// plan-4). Since plan-4 flips the DEFAULT local engine to the in-browser Go/WASM pipeline
// (deployMode.ts), this spec drives the WHOLE local-mode design flow through the REAL UI —
// design (seeded) → validate → compile → export ZIP → deploy-script — so every compute runs
// through wasmEngine.ts against web/yaog.wasm in an ACTUAL browser, asserting real output (not a
// stub) and NO WASM instantiation / console error.
//
// It is the RUNTIME/UX counterpart to the headless node gates: the permanent three-way
// WASM-vs-golden gate (scripts/wasm-conformance-gate.mjs) already proves WASM == Go == TS
// BYTE-for-byte, and wasmEngine.test.ts instantiates the wasm in node — but only a real browser
// exercises WebAssembly.instantiate + the wasm_exec.js glue + the fetch/DOM path this spec covers.
//
// Perpetual guard vs. soak: the `chromium` project runs this as the perpetual CI guard (playwright
// runs it there via testIgnore). The `webkit` + `firefox` projects (playwright.config.ts) run ONLY
// this spec — the automated multi-browser SOAK that plan-4's stop-loss depends on (a WebKit-specific
// WASM quirk is exactly what the soak adds over the node gate). See e2e/README.md.
//
// PREREQ (invariant [9] / plan-4 change 4): the served build must INCLUDE web/yaog.wasm +
// wasm_exec.js. `npm run build:wasm` builds them into frontend/public/ so `vite build` copies them
// into dist/, which the controller cmd/e2eserver boot serves as real static files (/yaog.wasm,
// /wasm_exec.js — the SPA handler serves real files directly, only falling back to index.html for
// client routes). If they are absent, the compute assertions below fail loudly (the wasm 404s),
// which is the correct signal that the build step was skipped.
//
// framework-refactor plan-9 retired the air-gap boot; this local-mode flow now serves from the
// keystone-OFF controller boot. EnableStatic is identical across boots, and the flow runs entirely
// in-browser (no server API), so seedLocalMode's client-side mode='local' bypasses the controller
// login gate — order-independence is preserved without a dedicated boot.

const seedTopology = JSON.parse(
  fs.readFileSync(path.join(e2eDir, 'fixtures', 'seed-topology.json'), 'utf8'),
) as { project: unknown; domains: unknown[]; nodes: unknown[]; edges: unknown[] }

// Cold-load budget: the first compute instantiates the ~10 MB yaog.wasm (fetch + compile). It is
// served over loopback so this is comfortably fast, but WebKit/Firefox compile can take a beat —
// a generous ceiling absorbs it without masking a genuine hang (the stop-loss latency signal).
const WASM_COLD_MS = 30_000

test('WASM local engine: design → validate → compile → export → deploy-script produces real output in-browser', async ({
  page,
  context,
}) => {
  const h = readHarness()
  // Local mode serves the SPA (and the wasm static assets from dist) from any boot; use the
  // keystone-OFF controller boot (seedLocalMode makes the client-side mode 'local', so no login
  // gate — the flow is fully in-browser and never calls the controller API).
  const panel = httpURL(h.controller.panel)

  // Capture any WASM-instantiation / console error and any failure to serve the wasm assets —
  // registered BEFORE navigation so a load-time failure cannot slip past. The store CATCHES a
  // compute error (surfacing it in the UI, which the positive assertions below already detect), so
  // these arrays are the belt-and-suspenders "no WASM error" check plan-4 requires.
  const pageErrors: string[] = []
  const wasmConsoleErrors: string[] = []
  const wasmAssetFailures: string[] = []
  const isWasmText = (s: string): boolean => /wasm|webassembly|instantiat|yaog\.wasm/i.test(s)
  const isWasmAsset = (u: string): boolean => u.endsWith('/yaog.wasm') || u.endsWith('/wasm_exec.js')
  page.on('pageerror', (e) => pageErrors.push(e.message))
  page.on('console', (m) => {
    if (m.type() === 'error' && isWasmText(m.text())) wasmConsoleErrors.push(m.text())
  })
  page.on('response', (r) => {
    // A real serve failure is a 4xx/5xx — NOT a 304 Not Modified (a legitimate cache revalidation
    // the browser issues on a repeat navigation, which .ok() rejects and would flake this guard).
    if (isWasmAsset(r.url()) && r.status() >= 400) wasmAssetFailures.push(`${r.status()} ${r.url()}`)
  })
  page.on('requestfailed', (r) => {
    if (isWasmAsset(r.url())) wasmAssetFailures.push(`failed ${r.url()}`)
  })

  // Seed local mode + a valid 2-node design (router + peer) BEFORE the panel loads. The design is
  // the same fixture the air-gap canary compiles (a known-good topology), so a compute failure here
  // is a WASM-engine regression, never a bad design.
  await seedLocalMode(context)
  await seedCanvasTopology(context, seedTopology)

  // ── design → validate (BottomBar, /design) ──
  await page.goto(`${panel}/design`)
  await expect(page.locator('#main-content')).toBeAttached()
  // Validate is the FIRST compute → it instantiates the wasm. A clean verdict (the seed is valid)
  // proves the in-browser Go validator ran and returned real output.
  await page.getByRole('button', { name: 'Validate Topology' }).click()
  await expect(page.getByText('Topology validation passed')).toBeVisible({ timeout: WASM_COLD_MS })

  // ── compile (LocalDeploy → CompilePreview, /deploy) ──
  await page.goto(`${panel}/deploy`)
  await page.getByRole('button', { name: 'Compile', exact: false }).click()
  // CompilePreview renders the manifest header directly (not inside a collapsed <details>): a real
  // node count + a non-empty checksum prove the wasm compile produced a genuine manifest, not a stub.
  const preview = page.locator('section', { hasText: 'Compile Result' })
  await expect(preview.getByText('Node count: 2')).toBeVisible({ timeout: WASM_COLD_MS })
  await expect(preview.getByText(/Checksum:\s*\S+/)).toBeVisible()
  // A rendered per-peer WireGuard config carries the [Interface] section (mirrors the air-gap
  // canary's body assertion). It lives inside a collapsed <details>, so assert on DOM text content
  // (toContainText reads textContent, hidden or not) — the config exists ⇒ the wasm engine rendered it.
  await expect(preview).toContainText('[Interface]')

  // ── export ZIP (LocalDeploy, /deploy) ──
  const [zipDownload] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('button', { name: 'Export Artifacts' }).click(),
  ])
  const zipBuf = fs.readFileSync(await zipDownload.path())
  // A real per-node bundle ZIP: the local-file-header magic 'PK' + non-trivial size (a stub/empty
  // body would be neither). The wasm exportArtifacts builds this from the raw per-node file map.
  expect(zipBuf.length, 'export ZIP is non-trivial').toBeGreaterThan(200)
  expect(zipBuf.subarray(0, 2).toString('latin1'), 'export is a real ZIP (PK magic)').toBe('PK')

  // ── deploy-script (.sh) (LocalDeploy, /deploy) ──
  const [shDownload] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('button', { name: 'Deploy .sh' }).click(),
  ])
  const shText = fs.readFileSync(await shDownload.path(), 'utf8')
  // A real project-level deploy script starts with the bash shebang (deploy.go:160) — NOT the
  // {"error":...} envelope the wasm shim returns on failure (throwIfErrorEnvelope would have thrown
  // and the store would have surfaced an error instead of downloading).
  expect(shText.length, 'deploy .sh is non-trivial').toBeGreaterThan(200)
  expect(shText.startsWith('#!/usr/bin/env bash'), 'deploy .sh is a real bash script, not an error envelope').toBe(true)

  // ── no WASM instantiation / console error anywhere in the flow ──
  expect(wasmAssetFailures, 'the wasm assets (yaog.wasm + wasm_exec.js) all served').toEqual([])
  expect(wasmConsoleErrors, 'no WASM-related console error').toEqual([])
  expect(pageErrors, 'no uncaught page error (a wasm-instantiation throw would surface here)').toEqual([])
})
