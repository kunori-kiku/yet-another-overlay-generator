// localEngine.ts — the local-engine seam (plan-6, milestone 1.6).
//
// This is the ONE place that (a) decides whether LOCAL-mode compute runs in the browser
// (the plan-4 TS compiler) or hits the Go air-gap routes, and (b) bridges the store's
// air-gap action shapes onto the pure compiler library's public surface
// (./index.ts). The store consults one predicate — `localEngineEnabled()` — alongside the
// controller/local mode, in ONE documented decision shape (the seam docstring in
// topologyStore.ts), and calls exactly these four adapters — never four scattered branches that
// could drift (the F3-class hazard the plan calls out). The two action kinds differ only in their
// controller-mode behavior: validate() is key-free and dispatches in-browser in controller mode
// too (browser-local verify); compile/export/deploy need private keys and refuse in controller
// mode (controller compute is server-side, on Deploy).
//
// Default-ON (plan-7 Phase 0.5): LOCAL mode runs entirely in the browser by default —
// `localEngineEnabled()` is true unless VITE_YAOG_LOCAL_ENGINE is explicitly set to
// 'backend'. This is justified by the green plan-5 conformance harness, which pins the
// in-browser TS compiler byte-for-byte against the Go pipeline (the drift guarantee that
// replaces plan-6's deferred post-soak gate). The 'backend' value is the explicit opt-out
// escape hatch: it makes the store POST to the Go air-gap routes
// (/api/validate|compile|export|deploy-script), which is functional ONLY against a
// `-tags airgap` server — plan-7 gates those routes off the default controller build, so a
// stock controller has nothing to answer them. The store retains the air-gap fetch branches
// solely as that escape-hatch path; their aggressive removal stays deferred.
//
// Controller mode reaches this module for VALIDATE only: validate() is key-free, so in controller
// mode it runs the in-browser validator (localValidate) here too — browser-local verify, so the
// controller never serves nor calls /api/validate (minimizing its attack surface). The
// compile/export/deploy controller-mode refusal guards still run before any local-engine dispatch
// and never call localCompile/localExport/localDeployScripts (controller compute is server-side).

import type {
  CompileResponse,
  Topology,
  ValidateResponse,
} from '../types/topology';

// localEngineEnabled reports whether LOCAL-mode compute should run in the browser via the
// plan-4 TS compiler instead of POSTing to the Go air-gap routes. Default-ON (plan-7 Phase
// 0.5): local mode is browser-resident unless VITE_YAOG_LOCAL_ENGINE is explicitly 'backend'
// — unset / 'local' / anything else ⇒ true ⇒ in-browser compiler; only the exact literal
// 'backend' opts back out to the air-gap fetch path (functional only against a `-tags airgap`
// server). The flag is typed as 'local' | 'backend' in vite-env.d.ts so this comparison is a
// type-checked union narrowing under `tsc -b`.
export function localEngineEnabled(): boolean {
  return import.meta.env.VITE_YAOG_LOCAL_ENGINE !== 'backend';
}

// The compiler library is loaded via a dynamic `import()` so a controller-mode-only
// operator (who never enables the local engine) never pays the @noble/curves + JSZip bytes:
// the chunk is code-split and fetched lazily, only the first time a local-engine action
// runs (R5). All four adapters share this one import so the chunk is fetched at most once.
type CompilerModule = typeof import('./index');
let compilerModulePromise: Promise<CompilerModule> | null = null;
function loadCompiler(): Promise<CompilerModule> {
  if (compilerModulePromise === null) {
    compilerModulePromise = import('./index');
  }
  return compilerModulePromise;
}

// localValidate mirrors POST /api/validate: schema-then-semantic over the topology,
// returning the exact ValidateResponse ({ valid, errors, warnings }) the store assigns into
// `validateResult` with no shape translation. The compiler's validate() is pure (never
// mutates the caller's topology).
export async function localValidate(topo: Topology): Promise<ValidateResponse> {
  const m = await loadCompiler();
  return m.validate(topo);
}

// localCompile mirrors POST /api/compile (the air-gap shape). It runs the full pure
// compile() under the default AirGap custody — so the result topology carries reconstructed
// private keys in data.topology.nodes exactly like the server (local export/deploy bundles
// need them) — then projects the rich CompileResult into the snake_case CompileResponse the
// store consumes. toCompileResponse drops the library-internal fields the wire never carried
// and, critically, leaves `skipped_unenrolled` UNDEFINED (air-gap shape — topology.ts:156;
// that field is controller-compile-preview-only and is never read on this path).
export async function localCompile(topo: Topology): Promise<CompileResponse> {
  const m = await loadCompiler();
  return m.toCompileResponse(m.compile(topo));
}

// localExport mirrors POST /api/export: the per-node bundle ZIP as a Blob, matching the
// Blob the store currently gets from res.blob(). The store names the download file itself
// (`${project.id}-artifacts.zip`, mirroring handler.go:240) on the local path, so no
// Content-Disposition is involved.
export async function localExport(topo: Topology): Promise<Blob> {
  const m = await loadCompiler();
  return m.exportArtifacts(topo);
}

// localDeployScripts mirrors POST /api/deploy-script for BOTH formats at once: the store
// picks one by `format`. deployScript() renders one project-level script (bash | PowerShell),
// matching the single-script body /api/deploy-script?format=sh|ps1 returns. Rendering both
// in one call keeps the seam a single round-trip through the (already-compiled) pipeline and
// the store free of format branching at the engine boundary.
export async function localDeployScripts(
  topo: Topology,
): Promise<{ sh: string; ps1: string }> {
  const m = await loadCompiler();
  return { sh: m.deployScript(topo, 'sh'), ps1: m.deployScript(topo, 'ps1') };
}
