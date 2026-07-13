/// <reference types="vite/client" />

// Typed Vite env contract (plan-6, R8). tsconfig.app.json's `types: ["vite/client"]` leaves every
// VITE_* key as `string | undefined` — untyped and undiscoverable. Declaring the build flags here
// makes them a first-class contract enforced under `tsc -b` (project memory: bare `tsc --noEmit`
// misses TS2352; the `tsc -b` CI path is the strict gate these declarations satisfy).
//
// The former VITE_YAOG_LOCAL_ENGINE local-engine selector was removed: framework-refactor plan-5
// deleted the hand-mirrored TS compiler and plan-9 retired the Go air-gap `backend` escape hatch,
// so local-mode compute is always the in-browser Go/WASM pipeline (see lib/deployMode.ts and
// lib/localEngine.ts). A stale VITE_YAOG_LOCAL_ENGINE value in a build env is simply ignored.
interface ImportMetaEnv {
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
