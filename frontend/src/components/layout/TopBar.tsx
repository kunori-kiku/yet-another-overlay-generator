import { useRef } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { txt } from '../../i18n';

export function TopBar() {
  const exportProject = useTopologyStore((s) => s.exportProject);
  const importProject = useTopologyStore((s) => s.importProject);
  const viewMode = useTopologyStore((s) => s.viewMode);
  const setViewMode = useTopologyStore((s) => s.setViewMode);
  const language = useTopologyStore((s) => s.language);
  const setLanguage = useTopologyStore((s) => s.setLanguage);
  const flushWorkspace = useTopologyStore((s) => s.flushWorkspace);
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

  const handleFlushWorkspace = () => {
    const confirmed = window.confirm(
      txt(
        language,
        '⚠️ 这将清空当前 Workspace 的项目、域、节点、边、编译结果和历史记录，且无法撤销。是否继续？',
        '⚠️ This will clear project, domains, nodes, edges, compile results, and history in this workspace. This cannot be undone. Continue?'
      )
    );

    if (confirmed) {
      flushWorkspace();
    }
  };

  return (
    <header className="h-12 w-full flex items-center justify-between px-4 bg-gray-800 border-b border-gray-700 shrink-0">
      <h1 className="text-lg font-bold text-blue-400">
        🌐 {txt(language, 'Overlay 组网配置编排器', 'Overlay Network Config Orchestrator')}
      </h1>
      <div className="flex items-center gap-2">
        <div className="flex items-center bg-gray-700 rounded border border-gray-600 overflow-hidden mr-2">
          <button
            onClick={() => setLanguage('zh')}
            className={`px-2 py-1 text-xs ${language === 'zh' ? 'bg-blue-600 text-white' : 'text-gray-300 hover:bg-gray-600'}`}
          >
            中文
          </button>
          <button
            onClick={() => setLanguage('en')}
            className={`px-2 py-1 text-xs ${language === 'en' ? 'bg-blue-600 text-white' : 'text-gray-300 hover:bg-gray-600'}`}
          >
            EN
          </button>
        </div>
        <button
          onClick={() => setViewMode(viewMode === 'topology' ? 'audit' : 'topology')}
          className="px-3 py-1.5 text-sm bg-indigo-700 hover:bg-indigo-600 rounded text-white font-medium transition-colors border border-indigo-500 mr-4"
        >
          {viewMode === 'topology'
            ? txt(language, '🛡️ 审计与历史', '🛡️ Audit & History')
            : txt(language, '✏️ 编辑拓扑', '✏️ Edit Topology')}
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
          {txt(language, '导入项目', 'Import Project')}
        </button>
        <button
          onClick={exportProject}
          className="px-3 py-1.5 text-sm bg-gray-700 hover:bg-gray-600 rounded text-gray-200 transition-colors"
        >
          {txt(language, '导出项目', 'Export Project')}
        </button>
        <button
          onClick={handleFlushWorkspace}
          className="px-3 py-1.5 text-sm bg-red-700 hover:bg-red-600 rounded text-white transition-colors"
        >
          {txt(language, '清空 Workspace', 'Flush Workspace')}
        </button>
      </div>
    </header>
  );
}