import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';

// LocalDeploy is local / manual deploy: generate keys and configs in the browser, download the install
// artifact bundle or deploy scripts, and run them manually on the target hosts.
// (Extracted verbatim from the Mode A section of the original DeployPanel, serving as the /deploy route
// content in local mode.)
export function LocalDeploy() {
  const language = useTopologyStore((s) => s.language);
  const nodes = useTopologyStore((s) => s.nodes);
  const compile = useTopologyStore((s) => s.compile);
  const exportArtifacts = useTopologyStore((s) => s.exportArtifacts);
  const downloadDeployScript = useTopologyStore((s) => s.downloadDeployScript);
  const isCompiling = useTopologyStore((s) => s.isCompiling);
  const noNodes = nodes.length === 0;

  return (
    <section className="bg-[var(--surface-elevated)] border border-[var(--hairline)] p-4 rounded-lg space-y-3 max-w-2xl">
      <h3 className="text-lg font-semibold text-[var(--info)]">
        {t(language, 'localDeploy.localManualDeploy')}
      </h3>
      <p className="text-sm text-[var(--content-muted)]">
        {t(language, 'localDeploy.keysAndConfigsAre')}
      </p>
      {noNodes && (
        <p className="text-xs text-[var(--warning)] bg-[var(--warning-bg)] px-2 py-1 rounded">
          {t(language, 'localDeploy.theCurrentTopologyHas')}
        </p>
      )}
      <div className="space-y-2">
        <button
          onClick={() => compile()}
          disabled={isCompiling || noNodes}
          className="w-full py-1.5 bg-[var(--accent)] hover:bg-[var(--accent-hover)] text-[var(--accent-fg)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-sm"
        >
          {isCompiling
            ? t(language, 'localDeploy.compiling')
            : t(language, 'localDeploy.compile')}
        </button>
        <button
          onClick={() => exportArtifacts()}
          disabled={noNodes}
          className="w-full py-1.5 bg-[var(--control)] hover:bg-[var(--control-hover)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-sm"
        >
          {t(language, 'localDeploy.exportArtifacts')}
        </button>
        <div className="flex gap-2">
          <button
            onClick={() => downloadDeployScript('sh')}
            disabled={noNodes}
            className="flex-1 py-1.5 bg-[var(--control)] hover:bg-[var(--control-hover)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-sm"
          >
            {t(language, 'localDeploy.deploySh')}
          </button>
          <button
            onClick={() => downloadDeployScript('ps1')}
            disabled={noNodes}
            className="flex-1 py-1.5 bg-[var(--control)] hover:bg-[var(--control-hover)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-sm"
          >
            {t(language, 'localDeploy.deployPs1')}
          </button>
        </div>
      </div>
    </section>
  );
}
