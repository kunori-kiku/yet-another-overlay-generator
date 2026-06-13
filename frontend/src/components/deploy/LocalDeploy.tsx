import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';

// 本地 / 手动部署：在浏览器内生成密钥与配置，下载安装产物包或部署脚本，手动在目标主机执行。
// （从原 DeployPanel 的 Mode A 区块原样抽出，作为 /deploy 路由在 local 模式下的内容。）
export function LocalDeploy() {
  const language = useTopologyStore((s) => s.language);
  const nodes = useTopologyStore((s) => s.nodes);
  const compile = useTopologyStore((s) => s.compile);
  const exportArtifacts = useTopologyStore((s) => s.exportArtifacts);
  const downloadDeployScript = useTopologyStore((s) => s.downloadDeployScript);
  const isCompiling = useTopologyStore((s) => s.isCompiling);
  const noNodes = nodes.length === 0;

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3 max-w-2xl">
      <h3 className="text-lg font-semibold text-blue-400">
        {t(language, 'localDeploy.localManualDeploy')}
      </h3>
      <p className="text-sm text-gray-400">
        {t(language, 'localDeploy.keysAndConfigsAre')}
      </p>
      {noNodes && (
        <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
          {t(language, 'localDeploy.theCurrentTopologyHas')}
        </p>
      )}
      <div className="space-y-2">
        <button
          onClick={() => compile()}
          disabled={isCompiling || noNodes}
          className="w-full py-1.5 bg-green-600 hover:bg-green-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
        >
          {isCompiling
            ? t(language, 'localDeploy.compiling')
            : t(language, 'localDeploy.compile')}
        </button>
        <button
          onClick={() => exportArtifacts()}
          disabled={noNodes}
          className="w-full py-1.5 bg-indigo-600 hover:bg-indigo-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
        >
          {t(language, 'localDeploy.exportArtifacts')}
        </button>
        <div className="flex gap-2">
          <button
            onClick={() => downloadDeployScript('sh')}
            disabled={noNodes}
            className="flex-1 py-1.5 bg-orange-600 hover:bg-orange-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
          >
            {t(language, 'localDeploy.deploySh')}
          </button>
          <button
            onClick={() => downloadDeployScript('ps1')}
            disabled={noNodes}
            className="flex-1 py-1.5 bg-orange-600 hover:bg-orange-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
          >
            {t(language, 'localDeploy.deployPs1')}
          </button>
        </div>
      </div>
    </section>
  );
}
