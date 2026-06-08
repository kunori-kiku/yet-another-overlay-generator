import { AppLayout } from '../layout/AppLayout';
import { LeftPanel } from '../layout/LeftPanel';
import { RightPanel } from '../layout/RightPanel';
import { BottomBar } from '../layout/BottomBar';
import { TopologyCanvas } from '../canvas/TopologyCanvas';

// /design — the topology editing scene (left panel + canvas + right panel +
// validation footer). Mounted under a route-scoped ReactFlowProvider (App.tsx),
// so the canvas only initializes here. P3 restructures the panels into an aside.
export function DesignPage() {
  return (
    <AppLayout
      leftPanel={<LeftPanel />}
      canvas={<TopologyCanvas />}
      rightPanel={<RightPanel />}
      bottomBar={<BottomBar />}
    />
  );
}
