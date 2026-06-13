import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
import { t } from '../../i18n';
import { LocalDeploy } from '../deploy/LocalDeploy';
import { DeployBar } from '../deploy/DeployBar';
import { CompilePreview } from '../deploy/CompilePreview';

// /deploy — local/manual deploy actions in local mode; the controller deploy bar
// (stage/sign/promote) in controller mode. (Compile-result preview gets a stable
// home here in P3.)
export function DeployPage() {
  const language = useTopologyStore((s) => s.language);
  const mode = useControllerStore((s) => s.mode);

  return (
    <div className="h-full overflow-y-auto bg-gray-900 text-gray-100 p-6 space-y-6">
      {mode === 'local' ? (
        <LocalDeploy />
      ) : (
        <>
          <p className="text-sm text-gray-400">{t(language, 'deployControllerHint')}</p>
          <DeployBar />
        </>
      )}
      {/* 编译结果预览：从 RightPanel 迁来的稳定落点（有编译结果时才渲染）。 */}
      <CompilePreview />
    </div>
  );
}
