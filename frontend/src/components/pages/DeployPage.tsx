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
      {/* 编译结果预览（本地编译产物）：仅本地模式渲染（plan-11 / T4）。控制器模式不本地编译，
          预览只会显示切换前残留的陈旧本地编译结果——与服务端权威设计无关，故隐藏。 */}
      {mode === 'local' && <CompilePreview />}
    </div>
  );
}
