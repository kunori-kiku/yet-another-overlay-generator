import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
import { txt, STRINGS } from '../../i18n';
import { LocalDeploy } from '../deploy/LocalDeploy';
import { DeployBar } from '../deploy/DeployBar';

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
          <p className="text-sm text-gray-400">{txt(language, ...STRINGS.deployControllerHint)}</p>
          <DeployBar />
        </>
      )}
    </div>
  );
}
