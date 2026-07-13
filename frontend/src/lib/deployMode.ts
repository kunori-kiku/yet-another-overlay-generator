// deployMode.ts — the single deploy-mode descriptor (plan-0, FE ratchet-hygiene).
//
// Collapses the TWO build-time deployment env flags into ONE typed, validated descriptor so the
// former single-seam modules (lib/localOnly.ts and lib/localEngine.ts) — and any future reader —
// share a SINGLE source of truth for "what kind of build is this?" instead of each re-reading
// import.meta.env with its own ad-hoc truthiness rule. The flag names, defaults, and semantics are
// pinned by deployMode.test.ts.
//
// The two flags and their EXACT current semantics:
//
//   VITE_LOCAL_ONLY  (was read in lib/localOnly.ts) — static-local-design SPA pin. A truthy
//     literal ⇒ the backend-free LOCAL-design build (controller mode unreachable). A falsy
//     literal — unset, '', '0', 'false' — ⇒ the default all-in-one controller panel.
//     → descriptor.localOnly (boolean).
//
//   VITE_YAOG_LOCAL_ENGINE  (was read in lib/localEngine.ts) — LOCAL-mode compute-engine
//     selector, default-ON: anything OTHER than the exact literal 'backend' (unset / 'local' /
//     'wasm' / any stray value, including a stale 'ts' from before framework-refactor plan-5
//     deleted the TS compiler) ⇒ the in-browser Go/WASM pipeline (the DEFAULT and sole in-browser
//     engine); ONLY 'backend' opts back out to the Go air-gap fetch path (functional only against
//     a `-tags airgap` server). This value is typed as 'local' | 'backend' | 'wasm' in
//     vite-env.d.ts, so `tsc -b` rejects stray literals; the normalization here is the runtime
//     belt-and-suspenders. → descriptor.localEngine.
//
// Behaviour contract (pinned by deployMode.test.ts): deployMode() is a PURE per-call read of
// import.meta.env — NOT a memoized module-load const — matching the former per-call predicates
// exactly. That keeps vitest's vi.stubEnv working and guarantees no module-load capture silently
// changes semantics. The two former predicates (localOnly(), localEngineEnabled()) now merely
// project fields off this descriptor, so every downstream caller is behaviour-identical.

// LocalEngine enumerates the compute back-ends the build can select. 'wasm' is the in-browser Go
// pipeline compiled to GOOS=js GOARCH=wasm (the DEFAULT and, since framework-refactor plan-5 deleted
// the hand-mirrored TS compiler, the ONLY in-browser engine), and 'backend' is the Go air-gap fetch
// escape hatch (VITE_YAOG_LOCAL_ENGINE='backend').
export type LocalEngine = 'backend' | 'wasm';

export interface DeployMode {
  // true ⇒ the static-local-design SPA (VITE_LOCAL_ONLY truthy); controller mode is unreachable.
  // false ⇒ the default all-in-one controller panel.
  readonly localOnly: boolean;
  // Which engine runs LOCAL-mode compute: 'wasm' = the in-browser Go/WASM pipeline (the default and
  // sole in-browser engine), 'backend' = the Go air-gap fetch escape hatch (VITE_YAOG_LOCAL_ENGINE
  // === 'backend').
  readonly localEngine: LocalEngine;
}

// localOnlyFlag applies the EXACT truthiness rule the former localOnly() used: a literal that is
// undefined / '' / '0' / 'false' is falsy (default controller build); any other value is truthy.
function localOnlyFlag(v: string | undefined): boolean {
  return v !== undefined && v !== '' && v !== '0' && v !== 'false';
}

// localEngineFlag projects the raw VITE_YAOG_LOCAL_ENGINE literal onto the LocalEngine union: only
// the exact literal 'backend' selects the Go air-gap fetch path; unset / 'local' / 'wasm' / any other
// value (including a stale 'ts' from before framework-refactor plan-5 deleted the TS compiler)
// defaults to the in-browser Go/WASM pipeline. The 'wasm' default (not 'backend') keeps
// localEngineEnabled() true for the in-browser engine, so the store's browser-vs-air-gap decision is
// unchanged, and a retired 'ts' value degrades gracefully rather than crashing.
function localEngineFlag(v: 'local' | 'backend' | 'wasm' | undefined): LocalEngine {
  if (v === 'backend') return 'backend';
  return 'wasm';
}

// deployMode reads both flags from import.meta.env and returns the validated descriptor. It is the
// ONE place that touches import.meta.env for these flags; call it per-invocation (do not hoist to a
// module-load const) so it stays behaviour-identical to the former per-call predicates and remains
// stub-able under vitest.
export function deployMode(): DeployMode {
  return {
    localOnly: localOnlyFlag(import.meta.env.VITE_LOCAL_ONLY),
    localEngine: localEngineFlag(import.meta.env.VITE_YAOG_LOCAL_ENGINE),
  };
}
