// E2ERenderThrowProbe is a TEST-ONLY render-error trigger for the ErrorBoundary adversarial spec
// (plan-16 / 3.4, error-render.spec.ts). App mounts it inside <ErrorBoundary> ONLY when
// import.meta.env.VITE_E2E is set, so it is dead-code-eliminated from every build that does not set
// that flag — i.e. it ships in NO production bundle (the release/Docker builds never set VITE_E2E;
// only the e2e CI job's `VITE_E2E=1 npm run build` does). Even in the e2e bundle it renders nothing
// and has no effect until a spec sets window.__E2E_RENDER_THROW__ before the app mounts; then it
// throws during render, exercising the boundary's recoverable fallback instead of a white screen.
//
// This is a build-flag-gated test seam, not a runtime feature: there is no UI, no route, and no
// way to reach the throw without both the build flag AND a same-origin script setting the window
// sentinel (a same-origin script already has full page control, so this grants no new capability).

declare global {
  interface Window {
    __E2E_RENDER_THROW__?: boolean;
  }
}

export function E2ERenderThrowProbe(): null {
  if (typeof window !== 'undefined' && window.__E2E_RENDER_THROW__) {
    throw new Error('E2E forced render error (ErrorBoundary probe)');
  }
  return null;
}
