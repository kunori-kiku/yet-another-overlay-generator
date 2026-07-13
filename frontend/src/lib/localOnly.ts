// localOnly.ts — the static-local-design build flag (plan-7 Phase 3, milestone 1.7).
//
// VITE_LOCAL_ONLY pins the panel to a backend-free LOCAL-design site: a build produced with
// the flag set ships ONLY the local (in-browser) design workflow, with controller mode
// unreachable — no "connect to controller" affordance, no mode toggle, no controller-only
// nav. This is the SPA half of the two-deployment story (the standalone static site vs. the
// all-in-one controller image). The former anonymous compute routes
// (/api/{validate,compile,export,deploy-script}) were deleted in framework-refactor plan-9, so
// LOCAL design compiles in-browser (the WASM engine) on both deployments; this flag keeps the
// static site from offering any controller affordance that depends on a backend (R7).
//
// localOnly() is the store/chrome-facing projection of the shared deploy-mode descriptor
// (lib/deployMode.ts is now the ONE place that reads import.meta.env for the build flags). Both
// the store (mode default + the setMode/switchToController guards) and the chrome (nav/mode-toggle
// visibility) call localOnly() for a SINGLE decision point rather than each re-reading
// import.meta.env (the same single-seam discipline as localEngine.ts's localEngineEnabled()).
// Keeping it out of controllerStore also lets nav.ts and the Sidebar read it without importing
// the store (no cycle).
//
// The DEFAULT build (flag unset) is the all-in-one controller panel — unchanged: localOnly()
// is false, mode is operator-selectable, and the controller affordances render exactly as
// before. Only an explicit VITE_LOCAL_ONLY=1 build flips this.

import { deployMode } from './deployMode';

// localOnly reports whether this build is the static-local-design SPA (VITE_LOCAL_ONLY truthy).
// The exact truthiness rule ('1'/'true'/any non-empty non-'0'/'false' literal ⇒ true; unset, '',
// '0', 'false' ⇒ false = the default all-in-one controller panel) now lives in deployMode.ts; this
// is a thin projection of descriptor.localOnly, behaviour-identical to the former direct read.
export function localOnly(): boolean {
  return deployMode().localOnly;
}
