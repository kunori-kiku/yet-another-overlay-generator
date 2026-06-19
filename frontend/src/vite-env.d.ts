/// <reference types="vite/client" />

// Typed Vite env contract (plan-6, R8). Today the only env typing is tsconfig.app.json's
// `types: ["vite/client"]`, which leaves every VITE_* key as `string | undefined` —
// untyped and undiscoverable. Declaring VITE_YAOG_LOCAL_ENGINE here as a literal union
// makes the local-engine flag a first-class contract enforced under `tsc -b`: a typo or a
// stray value is a build error, and localEngine.ts's `=== 'local'` check narrows against
// the union (project memory: bare `tsc --noEmit` misses TS2352; the `tsc -b` CI path is
// the strict gate this declaration must satisfy).
interface ImportMetaEnv {
  // Local-engine selector. Default-OFF: unset or any value other than 'local' ⇒ the
  // proven backend (air-gap fetch) path. 'local' opts into the in-browser TS compiler.
  // See localEngine.ts (localEngineEnabled) and topologyStore.ts's local-engine seam.
  readonly VITE_YAOG_LOCAL_ENGINE?: 'local' | 'backend';
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
