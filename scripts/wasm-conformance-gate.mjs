#!/usr/bin/env node
// wasm-conformance-gate.mjs — the PERMANENT three-way conformance gate
// (framework-refactor plan-3, invariant [1]: "parity is proven by execution + a permanent
// gate, never by construction").
//
// It EXECUTES the built web/yaog.wasm (the pure Go pipeline compiled to GOOS=js GOARCH=wasm)
// and asserts, byte-for-byte, that its conformance manifest equals the frozen Go golden over
// the FULL success corpus:
//
//   golden  == the Go oracle (internal/conformance TestGolden freezes conformance.BuildManifest)
//   WASM    == this gate (runs the SAME BuildManifest inside yaog.wasm) == golden   ⇒  WASM == Go
//   TS      == golden (the existing vitest conformance)                             ⇒  TS   == Go
//   ∴ WASM == Go == TS  (the three-way equality the flip in plan-4/5 depends on)
//
// A WASM-vs-golden divergence is a GOARCH=wasm determinism quirk to CHARACTERIZE (plan-3.5) —
// it is NEVER a reason to `-update` the golden. This script therefore only READS the goldens.
//
// It also PINS web/wasm_exec.js to the building Go toolchain (a rebuild+diff pin): the JS glue
// the FE ships must match the toolchain that built yaog.wasm, or a future toolchain bump could
// silently break the browser load. The gate loads the toolchain's own wasm_exec.js to run
// (so the proof always executes), and fails if the committed copy has drifted from it.
//
// Run:  node scripts/wasm-conformance-gate.mjs   (after: GOOS=js GOARCH=wasm go build -o web/yaog.wasm ./cmd/wasm)
// CI:   the `wasm-conformance` job (.github/workflows/ci.yml) builds the wasm, then runs this.

import { readFileSync, readdirSync, existsSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { resolve, dirname, join, basename } from 'node:path';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');

const CORPUS_DIR = join(repoRoot, 'internal/localcompile/testdata/contract/topologies');
const GOLDEN_DIR = join(repoRoot, 'internal/conformance/testdata/golden');
const SIGNING_PEM = join(repoRoot, 'internal/localcompile/testdata/contract/signing/test-signing-key.pem');
const WASM_PATH = join(repoRoot, 'web/yaog.wasm');
const COMMITTED_WASM_EXEC = join(repoRoot, 'web/wasm_exec.js');

function fail(msg) {
  console.error(`\n✗ wasm-conformance gate FAILED\n\n${msg}\n`);
  process.exit(1);
}

function goEnv(name) {
  try {
    return execFileSync('go', ['env', name], { encoding: 'utf8' }).trim();
  } catch (e) {
    fail(`could not run \`go env ${name}\` (is the Go toolchain on PATH?): ${e.message}`);
  }
}

// ── 1. wasm_exec.js <-> toolchain pin ─────────────────────────────────────────────────────
// The committed web/wasm_exec.js (the runtime the FE ships) must byte-match the toolchain that
// builds yaog.wasm. We load the TOOLCHAIN copy to run the gate (so the parity proof always
// executes), and separately assert the committed copy has not drifted from it.
const goroot = goEnv('GOROOT');
const goversion = goEnv('GOVERSION') || '(unknown go version)';
const toolchainWasmExecPath = join(goroot, 'lib/wasm/wasm_exec.js');
if (!existsSync(toolchainWasmExecPath)) {
  fail(`toolchain wasm_exec.js not found at ${toolchainWasmExecPath}`);
}
const toolchainWasmExec = readFileSync(toolchainWasmExecPath);

if (!existsSync(COMMITTED_WASM_EXEC)) {
  fail(`committed web/wasm_exec.js is missing — copy it:\n  cp "${toolchainWasmExecPath}" web/wasm_exec.js`);
}
const committedWasmExec = readFileSync(COMMITTED_WASM_EXEC);
if (!committedWasmExec.equals(toolchainWasmExec)) {
  fail(
    `web/wasm_exec.js has DRIFTED from the building toolchain (${goversion}).\n` +
      `The FE runtime must match the toolchain that builds yaog.wasm. Re-copy + review:\n` +
      `  cp "${toolchainWasmExecPath}" web/wasm_exec.js`,
  );
}

// ── 2. the built wasm must exist ──────────────────────────────────────────────────────────
if (!existsSync(WASM_PATH)) {
  fail(`web/yaog.wasm is missing — build it first:\n  GOOS=js GOARCH=wasm go build -o web/yaog.wasm ./cmd/wasm`);
}

// ── 3. instantiate + run yaog.wasm ────────────────────────────────────────────────────────
// wasm_exec.js is an IIFE that assigns globalThis.Go; run it in this context to define it.
vm.runInThisContext(toolchainWasmExec.toString('utf8'), { filename: 'wasm_exec.js' });
if (typeof globalThis.Go !== 'function') {
  fail('wasm_exec.js did not define globalThis.Go');
}
const go = new globalThis.Go();
const { instance } = await WebAssembly.instantiate(readFileSync(WASM_PATH), go.importObject);
// Do NOT await: main() registers globalThis.yaog synchronously, then parks on select{} forever.
go.run(instance);
const yaog = globalThis.yaog;
if (!yaog || typeof yaog.buildManifest !== 'function') {
  fail('yaog.wasm did not register yaog.buildManifest on globalThis');
}

// ── 4. iterate the corpus; assert WASM == golden byte-for-byte ─────────────────────────────
const signingPEM = readFileSync(SIGNING_PEM, 'utf8');

const corpusFiles = readdirSync(CORPUS_DIR)
  .filter((f) => f.endsWith('.json'))
  .sort();
if (corpusFiles.length === 0) fail(`no corpus fixtures found under ${CORPUS_DIR}`);

// The count assertion: every success golden must be reached (no silently-skipped fixture).
const goldenFiles = readdirSync(GOLDEN_DIR).filter((f) => f.endsWith('.json'));
if (goldenFiles.length !== corpusFiles.length) {
  fail(
    `fixture/golden COUNT mismatch: ${corpusFiles.length} corpus fixtures vs ${goldenFiles.length} success goldens.\n` +
      `Every success fixture must have exactly one golden (the golden/fail/ subset is out of the WASM gate's scope).`,
  );
}

let passed = 0;
const mismatches = [];

for (const file of corpusFiles) {
  const raw = readFileSync(join(CORPUS_DIR, file), 'utf8');
  const parsed = JSON.parse(raw);
  // Golden key mirrors conformance/golden_test.go parseFixture: the fixture's `name` field,
  // falling back to the file basename when empty. (The corpus files are numbered, e.g.
  // 01-single-primary-link.json, but the golden is keyed by name: single-primary-link.json.)
  const name = parsed.name && parsed.name.length > 0 ? parsed.name : basename(file, '.json');
  const goldenPath = join(GOLDEN_DIR, `${name}.json`);
  if (!existsSync(goldenPath)) {
    fail(`fixture ${file} (name="${name}") has no golden at ${goldenPath}`);
  }
  const golden = readFileSync(goldenPath); // Buffer, includes the canonical trailing "\n"

  // Run BuildManifest inside the wasm. Signing fixtures get the throwaway test PEM; the rest
  // get "" (the shim only reads it when the fixture's `signing` flag is set).
  const out = yaog.buildManifest(raw, parsed.signing ? signingPEM : '');
  const errFromShim = detectShimError(out);
  if (errFromShim) {
    fail(`fixture ${name}: yaog.buildManifest returned an error envelope: ${errFromShim}`);
  }
  const got = Buffer.from(out, 'utf8');

  // Cross-check the manifest's own primary key so an empty-name fixture can never silently
  // pass against the wrong golden.
  const gotFixtureName = safeParseFixtureField(out);
  if (gotFixtureName !== null && gotFixtureName !== name) {
    fail(`fixture ${name}: manifest.fixture = "${gotFixtureName}" (expected "${name}") — name resolution diverged`);
  }

  if (got.equals(golden)) {
    passed++;
  } else {
    mismatches.push(firstDivergence(name, goldenPath, golden, got));
  }
}

if (mismatches.length > 0) {
  fail(
    `${mismatches.length}/${corpusFiles.length} fixture(s) DIVERGED from golden — WASM != Go:\n\n` +
      mismatches.join('\n\n') +
      `\n\nThis is a GOARCH=wasm determinism quirk to CHARACTERIZE (plan-3.5).\n` +
      `Do NOT \`-update\` the golden — the golden is the frozen oracle.`,
  );
}

console.log(
  `✓ wasm-conformance gate PASSED — WASM == Go golden byte-for-byte over all ${passed} success fixtures.\n` +
    `  toolchain: ${goversion}   wasm_exec.js pin: OK   wasm: web/yaog.wasm`,
);
process.exit(0);

// ── helpers ───────────────────────────────────────────────────────────────────────────────

// detectShimError returns the shim's error message iff `out` is exactly the {"error":"..."}
// envelope the wasm shim emits on failure, else null (a normal canonical-manifest string).
function detectShimError(out) {
  if (out.length === 0 || out[0] !== '{') return null;
  let v;
  try {
    v = JSON.parse(out);
  } catch {
    return null;
  }
  if (v && typeof v === 'object' && typeof v.error === 'string' && Object.keys(v).length === 1) {
    return v.error;
  }
  return null;
}

// safeParseFixtureField extracts the manifest's `fixture` field, or null if `out` is not
// parseable JSON (it always is on success — this is a defensive cross-check).
function safeParseFixtureField(out) {
  try {
    const v = JSON.parse(out);
    return typeof v.fixture === 'string' ? v.fixture : null;
  } catch {
    return null;
  }
}

// firstDivergence renders a Go-FirstDivergence-style report: the first differing byte offset,
// its 1-based line/column, and a short window of context from each side.
function firstDivergence(name, goldenPath, want, got) {
  const n = Math.min(want.length, got.length);
  let off = 0;
  while (off < n && want[off] === got[off]) off++;
  let line = 1;
  let col = 1;
  for (let i = 0; i < off && i < want.length; i++) {
    if (want[i] === 0x0a) {
      line++;
      col = 1;
    } else {
      col++;
    }
  }
  const win = (buf) => (off >= buf.length ? '<EOF>' : JSON.stringify(buf.slice(off, off + 32).toString('utf8')));
  return (
    `  fixture "${name}" diverges from ${goldenPath}\n` +
    `    first divergence at byte ${off} (line ${line}, col ${col}); ` +
    `lengths want=${want.length} got=${got.length}\n` +
    `      want: ${win(want)}\n` +
    `      got:  ${win(got)}`
  );
}
