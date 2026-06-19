/// <reference types="vite/client" />

// Typed Vite env contract (plan-6, R8). Today the only env typing is tsconfig.app.json's
// `types: ["vite/client"]`, which leaves every VITE_* key as `string | undefined` —
// untyped and undiscoverable. Declaring VITE_YAOG_LOCAL_ENGINE here as a literal union
// makes the local-engine flag a first-class contract enforced under `tsc -b`: a typo or a
// stray value is a build error, and localEngine.ts's `=== 'local'` check narrows against
// the union (project memory: bare `tsc --noEmit` misses TS2352; the `tsc -b` CI path is
// the strict gate this declaration must satisfy).
interface ImportMetaEnv {
  // Local-engine selector. Default-ON (plan-7 Phase 0.5): unset or any value other than
  // 'backend' (incl. 'local') ⇒ the in-browser TS compiler runs LOCAL-mode compute. Only
  // 'backend' opts back out to the Go air-gap fetch path (functional only against a
  // `-tags airgap` server, since plan-7 gates those routes off the default controller build).
  // See localEngine.ts (localEngineEnabled) and topologyStore.ts's local-engine seam.
  readonly VITE_YAOG_LOCAL_ENGINE?: 'local' | 'backend';

  // Static-local-design build pin (plan-7 Phase 3). When set (any truthy literal, e.g. '1'),
  // the panel ships as a backend-free LOCAL-design SPA: mode is forced to 'local', the mode
  // toggle + the "connect to controller" affordances are hidden, and setMode/switchToController
  // become guarded no-ops so the user cannot reach controller mode (which depends on a
  // controller backend the static site does not have). Unset/'' ⇒ the default all-in-one
  // controller panel (unchanged). Read via lib/localOnly.ts's localOnly().
  readonly VITE_LOCAL_ONLY?: string;

  // E2E test-build flag (plan-16 / 3.4). Set ONLY by the e2e CI job's `VITE_E2E=1 npm run build`
  // (never by the release/Docker builds), it gates the App's E2ERenderThrowProbe — a test-only
  // render-error seam for the ErrorBoundary adversarial spec — so the probe is dead-code-eliminated
  // from every production bundle. Read at one site (App.tsx); any truthy literal enables it.
  readonly VITE_E2E?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
