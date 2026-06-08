import { createBrowserRouter, RouterProvider, Navigate } from 'react-router-dom';
import { ReactFlowProvider } from '@xyflow/react';
import { Shell } from './components/shell/Shell';
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
      { path: 'overview', element: <OverviewPage /> },
      { path: 'fleet', element: <FleetPage /> },
      { path: 'fleet/nodes/:id', element: <FleetNodeDetailPage /> },
      { path: 'deploy', element: <DeployPage /> },
      { path: 'security', element: <SecurityPage /> },
      { path: 'settings', element: <SettingsPage /> },
      { path: '*', element: <IndexRedirect /> },
    ],
  },
]);

function App() {
  return (
    <ThemeProvider>
      <RouterProvider router={router} />
    </ThemeProvider>
  );
}

export default App;
