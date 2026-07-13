// wasmEngine.ts — the opt-in in-browser Go/WASM local engine (framework-refactor plan-3).
//
// This is the FE drop-in that runs the pure Go compile pipeline (compiled to GOOS=js
// GOARCH=wasm as web/yaog.wasm) directly in the browser, mirroring the TS engine's public
// surface (compile / validate / deployScript / export) so compiler/localEngine.ts can select
// it in place of the TS compiler. It is PREVIEW-ONLY (invariant [4]): local WASM mode never
// deploys or stages — controller compute stays server-side.
//
// Since framework-refactor plan-5 deleted the hand-mirrored TS compiler, this IS the local engine
// (reached whenever local-mode compute is enabled — see lib/localEngine.ts). It is loaded via a
// dynamic import() so its bytes (the wasm glue) are code-split out of the default bundle — a
// controller-only operator who never enables the local engine does not pay for it. The export ZIP is
// now built inside the wasm (cmd/wasm exportZip via archive/zip), so this module carries NO JS zip
// dependency.
//
// The bridge to the wasm shim (cmd/wasm/main.go): every call marshals its argument to a JSON
// STRING, calls the matching function the shim registered on globalThis.yaog (a synchronous
// syscall/js callback returning a string), and parses the string back into the FE shape. The
// shim returns a single {"error":"..."} object on failure, which this module surfaces as a
// thrown Error.
//
// Loading is async (wasm instantiation): ensureWasm() lazily fetches wasm_exec.js + yaog.wasm
// (served from frontend/public/ — copied there by scripts/build-wasm.sh) and runs the Go
// instance exactly once; every public method awaits it. If globalThis.yaog is already present
// (a prior load, or a Node test harness that instantiated the wasm itself) ensureWasm() reuses
// it and skips the browser fetch — which is what keeps this module unit-testable off the DOM.

import type { CompileResponse, Topology, ValidateResponse } from '../types/topology';

// YaogWasmApi is the JSON-string API cmd/wasm/main.go registers on globalThis.yaog. Each method
// takes/returns strings; the JSON shapes match the air-gap HTTP responses (CompileResponse /
// ValidateResponse); exportZip returns the preview ZIP bytes as a base64 string.
interface YaogWasmApi {
  compile(topoJSON: string): string;
  validate(topoJSON: string): string;
  deployScript(topoJSON: string, format: 'sh' | 'ps1'): string;
  exportZip(topoJSON: string): string;
  buildManifest(fixtureJSON: string, signingKeyPEM: string): string;
}

// GoInstance is the minimal surface of the wasm_exec.js `Go` runtime this module drives.
interface GoInstance {
  importObject: WebAssembly.Imports;
  run(instance: WebAssembly.Instance): Promise<void>;
}
type GoConstructor = new () => GoInstance;
type GlobalWithGo = typeof globalThis & { yaog?: YaogWasmApi; Go?: GoConstructor };

// Vite serves these from frontend/public/ (copied from web/ by scripts/build-wasm.sh). They are
// runtime fetches — a missing asset only 404s when the wasm engine is actually enabled, so the
// default (TS) build never depends on them being present.
const WASM_URL = '/yaog.wasm';
const WASM_EXEC_URL = '/wasm_exec.js';

// A single in-flight load promise so concurrent callers instantiate the wasm at most once.
let loadPromise: Promise<YaogWasmApi> | null = null;

// ensureWasm loads + runs the wasm instance once and resolves to its registered API. It is
// exported so callers (or a test) can pre-warm the engine; every public method awaits it.
export function ensureWasm(): Promise<YaogWasmApi> {
  if (loadPromise === null) {
    loadPromise = loadWasm();
  }
  return loadPromise;
}

async function loadWasm(): Promise<YaogWasmApi> {
  const g = globalThis as GlobalWithGo;
  // Reuse an already-instantiated instance (a prior load, or a Node test that set globalThis.yaog
  // itself). This is the seam that keeps the module testable without a DOM.
  if (!g.yaog) {
    if (typeof g.Go !== 'function') {
      await injectScript(WASM_EXEC_URL); // wasm_exec.js is an IIFE that assigns globalThis.Go
    }
    const Go = g.Go;
    if (typeof Go !== 'function') {
      throw new Error('wasm_exec.js did not define globalThis.Go');
    }
    const go = new Go();
    const bytes = await (await fetch(WASM_URL)).arrayBuffer();
    const { instance } = await WebAssembly.instantiate(bytes, go.importObject);
    // Do NOT await: the Go main() registers globalThis.yaog synchronously and then parks on
    // select{} forever, so run() never resolves — awaiting it would hang.
    void go.run(instance);
  }
  if (!g.yaog) {
    throw new Error('yaog.wasm did not register its API on globalThis');
  }
  return g.yaog;
}

function injectScript(src: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const el = document.createElement('script');
    el.src = src;
    el.onload = () => resolve();
    el.onerror = () => reject(new Error(`failed to load ${src}`));
    document.head.appendChild(el);
  });
}

// compile mirrors the TS engine's compile()+toCompileResponse(): the air-gap-shaped
// CompileResponse the store assigns into `compileResult` with no translation.
export async function compile(topo: Topology): Promise<CompileResponse> {
  const api = await ensureWasm();
  return parseResult<CompileResponse>(api.compile(JSON.stringify(topo)), 'compile');
}

// validate mirrors the TS engine's validate(): { valid, errors, warnings }. The shim omits the
// error/warning arrays when empty (the air-gap ValidateResponse shape), so normalize them back
// to arrays to match the TS engine's exact output.
export async function validate(topo: Topology): Promise<ValidateResponse> {
  const api = await ensureWasm();
  const res = parseResult<ValidateResponse>(api.validate(JSON.stringify(topo)), 'validate');
  return { valid: res.valid, errors: res.errors ?? [], warnings: res.warnings ?? [] };
}

// deployScript mirrors the TS engine's deployScript(): one project-level script as a raw string.
export async function deployScript(topo: Topology, format: 'sh' | 'ps1'): Promise<string> {
  const api = await ensureWasm();
  const out = api.deployScript(JSON.stringify(topo), format);
  throwIfErrorEnvelope(out, 'deployScript');
  return out;
}

// deployScripts renders BOTH formats, matching the localEngine adapter's { sh, ps1 } shape.
export async function deployScripts(topo: Topology): Promise<{ sh: string; ps1: string }> {
  return { sh: await deployScript(topo, 'sh'), ps1: await deployScript(topo, 'ps1') };
}

// exportArtifacts returns a downloadable ZIP Blob. The shim (cmd/wasm exportZip) builds the archive
// inside the wasm via Go's archive/zip over the per-node bundle file set and returns it as base64, so
// no JS zip library is needed. NOTE (preview-only, invariant [4]): this differs from the air-gap
// export, which wraps each node in a self-extracting installer; the WASM engine is a design PREVIEW
// and never deploys, so it ships the raw config files rather than replicating the installer wrapper
// (which lives behind //go:build airgap and is out of the wasm shim's scope). A base64 string never
// begins with '{', so throwIfErrorEnvelope distinguishes it from the {"error":...} envelope.
export async function exportArtifacts(topo: Topology): Promise<Blob> {
  const api = await ensureWasm();
  const out = api.exportZip(JSON.stringify(topo));
  throwIfErrorEnvelope(out, 'export');
  const bytes = Uint8Array.from(atob(out), (c) => c.charCodeAt(0));
  return new Blob([bytes], { type: 'application/zip' });
}

// parseResult JSON-parses a shim result string, throwing if it is the {"error":"..."} envelope.
function parseResult<T>(out: string, op: string): T {
  const parsed: unknown = JSON.parse(out);
  if (isErrorEnvelope(parsed)) {
    throw new Error(`wasm ${op} failed: ${parsed.error}`);
  }
  return parsed as T;
}

// throwIfErrorEnvelope throws when a raw-string result (e.g. a deploy script) is actually the
// {"error":"..."} envelope. A bash/PowerShell script never begins with '{', so this only fires on
// a genuine error.
function throwIfErrorEnvelope(out: string, op: string): void {
  if (out.length === 0 || out[0] !== '{') return;
  let parsed: unknown;
  try {
    parsed = JSON.parse(out);
  } catch {
    return; // not JSON — a real script body
  }
  if (isErrorEnvelope(parsed)) {
    throw new Error(`wasm ${op} failed: ${parsed.error}`);
  }
}

function isErrorEnvelope(v: unknown): v is { error: string } {
  return typeof v === 'object' && v !== null && 'error' in v && typeof (v as { error: unknown }).error === 'string';
}
