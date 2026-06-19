import { useEffect } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore, selectLoggedIn } from '../../stores/controllerStore';
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
  const loggedIn = useControllerStore(selectLoggedIn);
  const refresh = useControllerStore((s) => s.refresh);
  // compileResult is set by the local air-gap compile AND (PR6) by the controller's server-side
  // compile-preview. Subscribe so the preview appears as soon as either populates it.
  const compileResult = useTopologyStore((s) => s.compileResult);

  // Refresh-on-auth (controller mode): pull fresh fleet node state when the page is shown with a
  // valid session, so the DeployBar's rekeying advisory + orphan list reflect server truth rather
  // than a stale persisted cache (the phantom "rekeying" Deploy gate). Mirrors FleetPage.
  useEffect(() => {
    if (mode === 'controller' && loggedIn) void refresh();
  }, [mode, loggedIn, refresh]);

  return (
    <div className="h-full overflow-y-auto bg-[var(--surface)] text-[var(--content)] p-3 sm:p-6 space-y-6">
      {mode === 'local' ? (
        <LocalDeploy />
      ) : (
        <>
          <p className="text-sm text-gray-400">{t(language, 'deployControllerHint')}</p>
          <DeployBar />
        </>
      )}
      {/* Compile-result preview. Local mode: the air-gap compile output. Controller mode (PR6):
          the SERVER-side compile-preview output (controller has no local compile; entering
          controller hydrates and clears any stale local result, so this only shows a fresh
          server preview). Gated on compileResult so a stale result never lingers across modes. */}
      {(mode === 'local' || compileResult) && <CompilePreview />}
    </div>
  );
}
