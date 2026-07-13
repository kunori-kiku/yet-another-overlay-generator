// localEngine.ts — the local-engine seam.
//
// This is the ONE place that (a) reports whether LOCAL-mode compute runs in the browser and (b)
// bridges the store's compute action shapes onto the WASM engine's public surface
// (../wasm/wasmEngine). The store consults one predicate — `localEngineEnabled()` — alongside the
// controller/local mode, in ONE documented decision shape (the seam docstring in topologyStore.ts),
// and calls exactly these four adapters — never four scattered branches that could drift. The two
// action kinds differ only in their controller-mode behavior: validate() is key-free and dispatches
// in-browser in controller mode too (browser-local verify); compile/export/deploy need private keys
// and refuse in controller mode (controller compute is server-side, on Deploy).
//
// Since framework-refactor plan-5 deleted the hand-mirrored TS compiler and plan-9 retired the Go
// air-gap `backend` escape hatch (with the anonymous /api/{validate,compile,export,deploy-script}
// routes it POSTed to), local mode is ALWAYS the in-browser Go/WASM engine (web/yaog.wasm) — there
// is no longer an engine choice. The permanent WASM-vs-golden gate (scripts/wasm-conformance-gate.mjs)
// proves it byte-equals the Go controller pipeline, so `localEngineEnabled()` is always true.
//
// Controller mode reaches this module for VALIDATE only: validate() is key-free, so in controller
// mode it runs the in-browser validator (localValidate) here too — browser-local verify, so the
// controller never serves nor calls a validate endpoint (minimizing its attack surface). The
// compile/export/deploy controller-mode refusal guards still run before any local-engine dispatch and
// never call localCompile/localExport/localDeployScripts (controller compute is server-side).

import type { CompileResponse, Topology, ValidateResponse } from '../types/topology';
import { deployMode } from './deployMode';

// localEngineEnabled reports whether LOCAL-mode compute runs in the browser (the WASM engine). Since
// framework-refactor plan-9 retired the Go air-gap `backend` escape hatch, local-mode compute is
// ALWAYS the in-browser Go/WASM pipeline — so this is always true. It still projects the shared
// deploy-mode descriptor (lib/deployMode.ts, the single source of truth, where descriptor.localEngine
// is fixed to 'wasm') so the store keeps ONE browser-vs-server decision seam.
export function localEngineEnabled(): boolean {
  return deployMode().localEngine === 'wasm';
}

// The WASM engine (web/yaog.wasm) is loaded via a dynamic import() so a controller-mode-only operator
// (who never enables the local engine) never pays for the wasm glue: the chunk is code-split and
// fetched lazily, only the first time a local-engine action runs. All four adapters share this one
// import so the chunk is fetched at most once.
type WasmEngineModule = typeof import('../wasm/wasmEngine');
let wasmEngineModulePromise: Promise<WasmEngineModule> | null = null;
function loadWasmEngine(): Promise<WasmEngineModule> {
  if (wasmEngineModulePromise === null) {
    wasmEngineModulePromise = import('../wasm/wasmEngine');
  }
  return wasmEngineModulePromise;
}

// localValidate mirrors POST /api/validate: schema-then-semantic over the topology, returning the
// exact ValidateResponse ({ valid, errors, warnings }) the store assigns into `validateResult` with no
// shape translation. The engine's validate() is pure (never mutates the caller's topology).
export async function localValidate(topo: Topology): Promise<ValidateResponse> {
  return (await loadWasmEngine()).validate(topo);
}

// localCompile mirrors POST /api/compile (the air-gap shape). It runs the full compile under the
// default AirGap custody — so the result topology carries reconstructed private keys in
// data.topology.nodes exactly like the server (local export/deploy bundles need them) — and returns
// the snake_case CompileResponse the store consumes.
export async function localCompile(topo: Topology): Promise<CompileResponse> {
  return (await loadWasmEngine()).compile(topo);
}

// localExport mirrors POST /api/export: the per-node bundle ZIP as a Blob, matching the Blob the store
// currently gets from res.blob(). The store names the download file itself
// (`${project.id}-artifacts.zip`) on the local path, so no Content-Disposition is involved.
export async function localExport(topo: Topology): Promise<Blob> {
  return (await loadWasmEngine()).exportArtifacts(topo);
}

// localDeployScripts mirrors POST /api/deploy-script for BOTH formats at once: the store picks one by
// `format`. deployScripts() renders both project-level scripts (bash + PowerShell) in one call, so the
// seam is a single round-trip through the (already-compiled) pipeline and the store is free of format
// branching at the engine boundary.
export async function localDeployScripts(
  topo: Topology,
): Promise<{ sh: string; ps1: string }> {
  return (await loadWasmEngine()).deployScripts(topo);
}
