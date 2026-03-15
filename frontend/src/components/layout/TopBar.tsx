import { useRef } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';

export function TopBar() {
  const exportProject = useTopologyStore((s) => s.exportProject);
  const importProject = useTopologyStore((s) => s.importProject);
  const viewMode = useTopologyStore((s) => s.viewMode);
  const setViewMode = useTopologyStore((s) => s.setViewMode);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const handleImportClick = () => {
    fileInputRef.current?.click();
  };

  const handleFileChange = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (file) {
      await importProject(file);
    }
    if (fileInputRef.current) {
      fileInputRef.current.value = '';
    }
  };

  return (
    <header className="h-12 w-full flex items-center justify-between px-4 bg-gray-800 border-b border-gray-700 shrink-0">
      <h1 className="text-lg font-bold text-blue-400">
        🌐 Overlay Network Config Orchestrator
      </h1>
      <div className="flex items-center gap-2">
        <button
          onClick={() => setViewMode(viewMode === 'topology' ? 'audit' : 'topology')}
          className="px-3 py-1.5 text-sm bg-indigo-700 hover:bg-indigo-600 rounded text-white font-medium transition-colors border border-indigo-500 mr-4"
        >
          {viewMode === 'topology' ? '🛡️ Audit & History' : '✏️ Edit Topology'}
        </button>
        <input
          type="file"
          accept=".json"
          ref={fileInputRef}
          className="hidden"
          onChange={handleFileChange}
        />
        <button
          onClick={handleImportClick}
          className="px-3 py-1.5 text-sm bg-gray-700 hover:bg-gray-600 rounded text-gray-200 transition-colors"
        >
          Import Project
        </button>
        <button
          onClick={exportProject}
          className="px-3 py-1.5 text-sm bg-gray-700 hover:bg-gray-600 rounded text-gray-200 transition-colors"
        >
          Export Project
        </button>
      </div>
    </header>
  );
}