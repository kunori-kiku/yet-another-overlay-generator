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

// viewMode 路由：topology → 编辑画布 + 左右面板；audit → 审计视图；deploy → 部署面板。
// audit/deploy 都是占满中央画布、隐藏左右/底部面板的全屏视图（与 AuditView 的接线一致）。
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

function App() {
  const viewMode = useTopologyStore(s => s.viewMode);

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

export default App;
