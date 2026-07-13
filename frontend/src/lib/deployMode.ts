// deployMode.ts — the single deploy-mode descriptor (plan-0, FE ratchet-hygiene).
//
// Collapses the deployment-shaping build flags into ONE typed, validated descriptor so the former
// single-seam modules (lib/localOnly.ts and lib/localEngine.ts) — and any future reader — share a
// SINGLE source of truth for "what kind of build is this?" instead of each re-reading
// import.meta.env with its own ad-hoc truthiness rule. The flag name, default, and semantics are
// pinned by deployMode.test.ts.
//
//   VITE_LOCAL_ONLY  (was read in lib/localOnly.ts) — static-local-design SPA pin. A truthy
//     literal ⇒ the backend-free LOCAL-design build (controller mode unreachable). A falsy
//     literal — unset, '', '0', 'false' — ⇒ the default all-in-one controller panel.
//     → descriptor.localOnly (boolean).
//
// The local compute engine is no longer a build choice. framework-refactor plan-4/5 made the
// in-browser Go/WASM pipeline the sole in-browser engine, and plan-9 retired the `backend` air-gap
// escape hatch (with the anonymous /api/{validate,compile,export,deploy-script} routes it POSTed to),
// so descriptor.localEngine is fixed to 'wasm'. The former VITE_YAOG_LOCAL_ENGINE selector is gone;
// a stale value in someone's .env is simply ignored — local-mode compute is always WASM.
//
// Behaviour contract (pinned by deployMode.test.ts): deployMode() is a PURE per-call read of
// import.meta.env for VITE_LOCAL_ONLY — NOT a memoized module-load const — matching the former
// per-call predicate exactly. That keeps vitest's vi.stubEnv working and guarantees no module-load
// capture silently changes semantics. The two former predicates (localOnly(), localEngineEnabled())
// now merely project fields off this descriptor, so every downstream caller is behaviour-identical.

// LocalEngine enumerates the compute back-ends the build can select. Since framework-refactor
// plan-5 deleted the hand-mirrored TS compiler and plan-9 retired the Go air-gap `backend` escape
// hatch, 'wasm' — the in-browser Go pipeline compiled to GOOS=js GOARCH=wasm — is the only member.
export type LocalEngine = 'wasm';

export interface DeployMode {
  // true ⇒ the static-local-design SPA (VITE_LOCAL_ONLY truthy); controller mode is unreachable.
  // false ⇒ the default all-in-one controller panel.
  readonly localOnly: boolean;
  // Which engine runs LOCAL-mode compute. Always 'wasm' (the in-browser Go/WASM pipeline) — the
  // sole engine since plan-9 retired the air-gap backend hatch.
  readonly localEngine: LocalEngine;
}

// localOnlyFlag applies the EXACT truthiness rule the former localOnly() used: a literal that is
// undefined / '' / '0' / 'false' is falsy (default controller build); any other value is truthy.
function localOnlyFlag(v: string | undefined): boolean {
  return v !== undefined && v !== '' && v !== '0' && v !== 'false';
}

// deployMode reads VITE_LOCAL_ONLY from import.meta.env and returns the validated descriptor. It is
// the ONE place that touches import.meta.env for that flag; call it per-invocation (do not hoist to
// a module-load const) so it stays behaviour-identical to the former per-call predicate and remains
// stub-able under vitest. localEngine is the constant 'wasm' (the backend escape hatch is retired).
export function deployMode(): DeployMode {
  return {
    localOnly: localOnlyFlag(import.meta.env.VITE_LOCAL_ONLY),
    localEngine: 'wasm',
  };
}
