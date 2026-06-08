import { createBrowserRouter, RouterProvider } from 'react-router-dom';
import { ReactFlowProvider } from '@xyflow/react';
import { AppLayout } from './components/layout/AppLayout';
import { TopBar } from './components/layout/TopBar';
import { LeftPanel } from './components/layout/LeftPanel';
import { RightPanel } from './components/layout/RightPanel';
import { BottomBar } from './components/layout/BottomBar';
import { TopologyCanvas } from './components/canvas/TopologyCanvas';
import { useTopologyStore } from './stores/topologyStore';
import { AuditView } from './components/audit/AuditView';
import { DeployPanel } from './components/deploy/DeployPanel';
import { Shell } from './components/shell/Shell';
import { ThemeProvider } from './theme/ThemeProvider';

// viewMode 路由：topology → 编辑画布 + 左右面板；audit → 审计视图；deploy → 部署面板。
// audit/deploy 都是占满中央画布、隐藏左右/底部面板的全屏视图（与 AuditView 的接线一致）。
// P1：仍由 viewMode 分发，整体被嵌入新的 Shell 内作为首页路由（P2 拆成真实路由）。
function mainView(viewMode: ReturnType<typeof useTopologyStore.getState>['viewMode']) {
  switch (viewMode) {
    case 'audit':
      return <AuditView />;
    case 'deploy':
      return <DeployPanel />;
    default:
      return <TopologyCanvas />;
  }
}

// The existing topology/audit/deploy scene, rendered unchanged as the shell's
// index route. ReactFlowProvider stays scoped to this scene (P2 narrows it to
// the /design route).
function DesignScene() {
  const viewMode = useTopologyStore((s) => s.viewMode);

  return (
    <ReactFlowProvider>
      <AppLayout
        topBar={<TopBar />}
        leftPanel={viewMode === 'topology' ? <LeftPanel /> : null}
        canvas={mainView(viewMode)}
        rightPanel={viewMode === 'topology' ? <RightPanel /> : null}
        bottomBar={viewMode === 'topology' ? <BottomBar /> : null}
      />
    </ReactFlowProvider>
  );
}

const router = createBrowserRouter([
  {
    element: <Shell />,
    children: [{ index: true, element: <DesignScene /> }],
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
