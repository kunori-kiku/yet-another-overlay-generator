import type { ReactNode } from 'react';

interface AppLayoutProps {
  leftPanel: ReactNode;
  canvas: ReactNode;
  rightPanel: ReactNode;
  bottomBar: ReactNode;
}

export function AppLayout({ leftPanel, canvas, rightPanel, bottomBar }: AppLayoutProps) {
  return (
    <div className="h-screen flex flex-col bg-gray-900 text-gray-100">
      {/* 顶部标题栏 */}
      <header className="h-12 flex items-center px-4 bg-gray-800 border-b border-gray-700 shrink-0">
        <h1 className="text-lg font-bold text-blue-400">
          🌐 Overlay Network Config Orchestrator
        </h1>
      </header>

      {/* 主内容区 */}
      <div className="flex flex-1 overflow-hidden">
        {/* 左侧面板 */}
        <aside className="w-72 bg-gray-800 border-r border-gray-700 overflow-y-auto shrink-0">
          {leftPanel}
        </aside>

        {/* 中央画布 */}
        <main className="flex-1 relative">
          {canvas}
        </main>

        {/* 右侧面板 */}
        <aside className="w-80 bg-gray-800 border-l border-gray-700 overflow-y-auto shrink-0">
          {rightPanel}
        </aside>
      </div>

      {/* 底部栏 */}
      <footer className="h-40 bg-gray-800 border-t border-gray-700 overflow-y-auto shrink-0">
        {bottomBar}
      </footer>
    </div>
  );
}
