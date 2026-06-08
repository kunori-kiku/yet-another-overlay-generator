import type { ReactNode } from 'react';

interface AppLayoutProps {
  leftPanel: ReactNode;
  canvas: ReactNode;
  rightPanel: ReactNode;
  bottomBar: ReactNode;
}

// The Design scene layout: left panel + canvas + right panel + bottom bar. The
// app-shell (Shell/Topbar) owns the top bar and viewport height, so this fills
// its routed MAIN region with h-full. (P3 restructures the panels into an aside.)
export function AppLayout({ leftPanel, canvas, rightPanel, bottomBar }: AppLayoutProps) {
  return (
    <div className="h-full flex flex-col bg-gray-900 text-gray-100">
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
