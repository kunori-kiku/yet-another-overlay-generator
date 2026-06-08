import { createBrowserRouter, RouterProvider, Navigate } from 'react-router-dom';
import { ReactFlowProvider } from '@xyflow/react';
import { Shell } from './components/shell/Shell';
import { ThemeProvider } from './theme/ThemeProvider';
import { DesignPage } from './components/pages/DesignPage';
import { OverviewPage } from './components/pages/OverviewPage';
import { FleetPage } from './components/pages/FleetPage';
import { DeployPage } from './components/pages/DeployPage';
import { SecurityPage } from './components/pages/SecurityPage';
import { SettingsPage } from './components/pages/SettingsPage';

// Deep-linkable routes under the persistent app-shell. ReactFlowProvider is
// scoped to /design so the canvas only initializes on that route. The index and
// unknown paths redirect to /design (mode-aware landing arrives in P4).
const router = createBrowserRouter([
  {
    element: <Shell />,
    children: [
      { index: true, element: <Navigate to="/design" replace /> },
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
      { path: 'deploy', element: <DeployPage /> },
      { path: 'security', element: <SecurityPage /> },
      { path: 'settings', element: <SettingsPage /> },
      { path: '*', element: <Navigate to="/design" replace /> },
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
