import type { ReactNode } from 'react';

interface AppLayoutProps {
  topBar: ReactNode;
  leftPanel: ReactNode;
  canvas: ReactNode;
  rightPanel: ReactNode;
  bottomBar: ReactNode;
}

export function AppLayout({ topBar, leftPanel, canvas, rightPanel, bottomBar }: AppLayoutProps) {
  return (
    <div className="h-screen flex flex-col bg-gray-900 text-gray-100">
      {topBar}

      {/* 主内容区 */}
      <div className="flex flex-1 overflow-hidden">
        {/* 左侧面板 */}
        {leftPanel && (
          <aside className="w-72 bg-gray-800 border-r border-gray-700 overflow-y-auto shrink-0">
            {leftPanel}
          </aside>
        )}

        {/* 中央画布 */}
        <main className="flex-1 relative overflow-auto bg-gray-900">
          {canvas}
        </main>

        {/* 右侧面板 */}
        {rightPanel && (
          <aside className="w-80 bg-gray-800 border-l border-gray-700 overflow-y-auto shrink-0">
            {rightPanel}
          </aside>
        )}
      </div>

      {/* 底部栏 */}
      {bottomBar && (
        <footer className="h-40 bg-gray-800 border-t border-gray-700 overflow-y-auto shrink-0">
          {bottomBar}
        </footer>
      )}
    </div>
  );
}
