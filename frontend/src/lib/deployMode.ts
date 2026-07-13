// deployMode.ts — the single deploy-mode descriptor (plan-0, FE ratchet-hygiene).
//
// Collapses the TWO build-time deployment env flags into ONE typed, validated descriptor so the
// former single-seam modules (lib/localOnly.ts and compiler/localEngine.ts) — and any future
// reader — share a SINGLE source of truth for "what kind of build is this?" instead of each
// re-reading import.meta.env with its own ad-hoc truthiness rule. This is a PURE refactor: the
// flag names, defaults, and semantics are unchanged (pinned byte-for-byte by deployMode.test.ts).
//
// The two flags and their EXACT current semantics:
//
//   VITE_LOCAL_ONLY  (was read in lib/localOnly.ts) — static-local-design SPA pin. A truthy
//     literal ⇒ the backend-free LOCAL-design build (controller mode unreachable). A falsy
//     literal — unset, '', '0', 'false' — ⇒ the default all-in-one controller panel.
//     → descriptor.localOnly (boolean).
//
//   VITE_YAOG_LOCAL_ENGINE  (was read in compiler/localEngine.ts) — LOCAL-mode compute-engine
//     selector, default-ON: anything OTHER than the exact literal 'backend' (unset / 'local' /
//     any stray value) ⇒ the in-browser plan-4 TS compiler; ONLY 'backend' opts back out to the
//     Go air-gap fetch path (functional only against a `-tags airgap` server). This value is
//     typed as 'local' | 'backend' in vite-env.d.ts, so `tsc -b` already rejects stray literals;
//     the normalization here is the runtime belt-and-suspenders. → descriptor.localEngine.
//
// Behaviour contract (pinned by deployMode.test.ts): deployMode() is a PURE per-call read of
// import.meta.env — NOT a memoized module-load const — matching the former per-call predicates
// exactly. That keeps vitest's vi.stubEnv working and guarantees no module-load capture silently
// changes semantics. The two former predicates (localOnly(), localEngineEnabled()) now merely
// project fields off this descriptor, so every downstream caller is behaviour-identical.

// LocalEngine enumerates the two compute back-ends the build can select. There is deliberately NO
// 'wasm' member: the codebase ships only the in-browser TS compiler ('ts') and the retained Go
// air-gap escape hatch ('backend'). Add 'wasm' here ONLY once a wasm engine actually exists.
export type LocalEngine = 'ts' | 'backend';

export interface DeployMode {
  // true ⇒ the static-local-design SPA (VITE_LOCAL_ONLY truthy); controller mode is unreachable.
  // false ⇒ the default all-in-one controller panel.
  readonly localOnly: boolean;
  // Which engine runs LOCAL-mode compute: 'ts' = the in-browser plan-4 compiler (default),
  // 'backend' = the Go air-gap fetch escape hatch (VITE_YAOG_LOCAL_ENGINE === 'backend').
  readonly localEngine: LocalEngine;
}

// localOnlyFlag applies the EXACT truthiness rule the former localOnly() used: a literal that is
// undefined / '' / '0' / 'false' is falsy (default controller build); any other value is truthy.
function localOnlyFlag(v: string | undefined): boolean {
  return v !== undefined && v !== '' && v !== '0' && v !== 'false';
}

// localEngineFlag mirrors the former localEngineEnabled() decision, projected onto the union: ONLY
// the exact literal 'backend' selects the Go air-gap path; unset / 'local' / anything else stays
// on the in-browser TS compiler (default-ON).
function localEngineFlag(v: 'local' | 'backend' | undefined): LocalEngine {
  return v === 'backend' ? 'backend' : 'ts';
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
