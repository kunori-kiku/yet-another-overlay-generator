import { ReactFlowProvider } from '@xyflow/react';
import { AppLayout } from './components/layout/AppLayout';
import { LeftPanel } from './components/layout/LeftPanel';
import { RightPanel } from './components/layout/RightPanel';
import { BottomBar } from './components/layout/BottomBar';
import { TopologyCanvas } from './components/canvas/TopologyCanvas';

function App() {
  return (
    <ReactFlowProvider>
      <AppLayout
        leftPanel={<LeftPanel />}
        canvas={<TopologyCanvas />}
        rightPanel={<RightPanel />}
        bottomBar={<BottomBar />}
      />
    </ReactFlowProvider>
  );
}

export default App;
