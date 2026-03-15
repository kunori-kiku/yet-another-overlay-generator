import { ReactFlowProvider } from '@xyflow/react';
import { AppLayout } from './components/layout/AppLayout';
import { TopBar } from './components/layout/TopBar';
import { LeftPanel } from './components/layout/LeftPanel';
import { RightPanel } from './components/layout/RightPanel';
import { BottomBar } from './components/layout/BottomBar';
import { TopologyCanvas } from './components/canvas/TopologyCanvas';
import { useTopologyStore } from './stores/topologyStore';
import { AuditView } from './components/audit/AuditView';

function App() {
  const viewMode = useTopologyStore(s => s.viewMode);

  return (
    <ReactFlowProvider>
      <AppLayout
        topBar={<TopBar />}
        leftPanel={viewMode === 'topology' ? <LeftPanel /> : null}
        canvas={viewMode === 'topology' ? <TopologyCanvas /> : <AuditView />}
        rightPanel={viewMode === 'topology' ? <RightPanel /> : null}
        bottomBar={viewMode === 'topology' ? <BottomBar /> : null}
      />
    </ReactFlowProvider>
  );
}

export default App;
