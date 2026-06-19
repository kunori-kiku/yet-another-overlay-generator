import type { ReactNode } from 'react';
import { createBrowserRouter, RouterProvider, Navigate } from 'react-router-dom';
import { ReactFlowProvider } from '@xyflow/react';
import { Shell } from './components/shell/Shell';
import { ErrorBoundary } from './components/shell/ErrorBoundary';
import { E2ERenderThrowProbe } from './components/shell/E2ERenderThrowProbe';
import { ThemeProvider } from './theme/ThemeProvider';
import { useControllerStore } from './stores/controllerStore';
import { landingPathForMode } from './components/shell/nav';
import { DesignPage } from './components/pages/DesignPage';
import { OverviewPage } from './components/pages/OverviewPage';
import { FleetPage } from './components/pages/FleetPage';
import { FleetNodeDetailPage } from './components/pages/FleetNodeDetailPage';
import { DeployPage } from './components/pages/DeployPage';
import { SecurityPage } from './components/pages/SecurityPage';
import { SettingsPage } from './components/pages/SettingsPage';

// Mode-aware landing: controller → /overview, local → /design (P4).
function IndexRedirect() {
  const mode = useControllerStore((s) => s.mode);
  return <Navigate to={landingPathForMode(mode)} replace />;
}

// Controller-only route guard (plan-11 / T5): Overview/Fleet/fleet-detail are controller
// constructs (fleet registry, enrollment, deploy summary). nav.ts hides them in local mode, but
// a DEEP LINK bypasses nav — so in local mode they'd render stale/empty controller UI (cached
// fleet rows, enrollment-token mint affordances). Redirect to the local landing instead, making
// reachability match nav visibility. (Local-internal routes — design/deploy/security/settings —
// stay reachable in controller mode; each is mode-gated internally.)
function RequireControllerMode({ children }: { children: ReactNode }) {
  const mode = useControllerStore((s) => s.mode);
  if (mode !== 'controller') return <Navigate to={landingPathForMode('local')} replace />;
  return <>{children}</>;
}

// Deep-linkable routes under the persistent app-shell. ReactFlowProvider is
// scoped to /design so the canvas only initializes on that route. The index
// redirects to the mode's landing; unknown paths fall back to /design.
const router = createBrowserRouter([
  {
    element: <Shell />,
    children: [
      { index: true, element: <IndexRedirect /> },
      {
        path: 'design',
        element: (
          <ReactFlowProvider>
            <DesignPage />
          </ReactFlowProvider>
        ),
      },
      { path: 'overview', element: <RequireControllerMode><OverviewPage /></RequireControllerMode> },
      { path: 'fleet', element: <RequireControllerMode><FleetPage /></RequireControllerMode> },
      { path: 'fleet/nodes/:id', element: <RequireControllerMode><FleetNodeDetailPage /></RequireControllerMode> },
      { path: 'deploy', element: <DeployPage /> },
      { path: 'security', element: <SecurityPage /> },
      { path: 'settings', element: <SettingsPage /> },
      { path: '*', element: <IndexRedirect /> },
    ],
  },
]);

function App() {
  return (
    <ErrorBoundary>
      {/* Test-only render-error probe, dead-code-eliminated from any build that does not set
          VITE_E2E (i.e. all production builds). Drives the ErrorBoundary adversarial spec. */}
      {import.meta.env.VITE_E2E ? <E2ERenderThrowProbe /> : null}
      <ThemeProvider>
        <RouterProvider router={router} />
      </ThemeProvider>
    </ErrorBoundary>
  );
}

export default App;
