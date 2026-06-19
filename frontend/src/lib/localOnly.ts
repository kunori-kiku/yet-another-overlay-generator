// localOnly.ts — the static-local-design build flag (plan-7 Phase 3, milestone 1.7).
//
// VITE_LOCAL_ONLY pins the panel to a backend-free LOCAL-design site: a build produced with
// the flag set ships ONLY the local (in-browser) design workflow, with controller mode
// unreachable — no "connect to controller" affordance, no mode toggle, no controller-only
// nav. This is the SPA half of the two-deployment story plan-7 establishes (the standalone
// static site vs. the all-in-one controller image); the controller routes
// (/api/validate|compile|export|deploy-script) are gated off the default Go controller build
// by the //go:build airgap tag, and this flag is the matching frontend pin so the static
// site never offers a path that depends on a controller backend (R7).
//
// This module is the ONE place that reads the flag, so both the store (mode default + the
// setMode/switchToController guards) and the chrome (nav/mode-toggle visibility) share a
// SINGLE decision point rather than each re-reading import.meta.env (the same single-seam
// discipline as localEngine.ts's localEngineEnabled()). Keeping it out of controllerStore
// also lets nav.ts and the Sidebar read it without importing the store (no cycle).
//
// The DEFAULT build (flag unset) is the all-in-one controller panel — unchanged: localOnly()
// is false, mode is operator-selectable, and the controller affordances render exactly as
// before. Only an explicit VITE_LOCAL_ONLY=1 build flips this.

// localOnly reports whether this build is the static-local-design SPA (VITE_LOCAL_ONLY set).
// Truthiness of the literal env value decides it: '1'/'true'/any non-empty string ⇒ true;
// unset or empty ⇒ false (the default controller all-in-one panel). The flag is typed in
// vite-env.d.ts so this read is a checked contract under `tsc -b`.
export function localOnly(): boolean {
  const v = import.meta.env.VITE_LOCAL_ONLY;
  return v !== undefined && v !== '' && v !== '0' && v !== 'false';
}
