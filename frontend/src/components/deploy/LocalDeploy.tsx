import { useTopologyStore } from '../../stores/topologyStore';
import { txt } from '../../i18n';

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
        {txt(language, '本地 / 手动部署', 'Local / Manual Deploy')}
      </h3>
      <p className="text-sm text-gray-400">
        {txt(
          language,
          '在浏览器内生成密钥与配置，下载安装产物包或部署脚本，然后手动在目标主机上执行。',
          'Keys and configs are generated in your browser. Download the install bundle or deploy script, then run it on each target host manually.',
        )}
      </p>
      {noNodes && (
        <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
          {txt(
            language,
            '当前拓扑没有节点，请先在「拓扑设计」中添加节点。',
            'The current topology has no nodes. Add nodes in Design first.',
          )}
        </p>
      )}
      <div className="space-y-2">
        <button
          onClick={() => compile()}
          disabled={isCompiling || noNodes}
          className="w-full py-1.5 bg-green-600 hover:bg-green-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
        >
          {isCompiling
            ? txt(language, '编译中...', 'Compiling...')
            : txt(language, '🔨 编译', '🔨 Compile')}
        </button>
        <button
          onClick={() => exportArtifacts()}
          disabled={noNodes}
          className="w-full py-1.5 bg-indigo-600 hover:bg-indigo-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
        >
          {txt(language, '📦 导出产物包', '📦 Export Artifacts')}
        </button>
        <div className="flex gap-2">
          <button
            onClick={() => downloadDeployScript('sh')}
            disabled={noNodes}
            className="flex-1 py-1.5 bg-orange-600 hover:bg-orange-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
          >
            {txt(language, '🚀 部署脚本 .sh', '🚀 Deploy .sh')}
          </button>
          <button
            onClick={() => downloadDeployScript('ps1')}
            disabled={noNodes}
            className="flex-1 py-1.5 bg-orange-600 hover:bg-orange-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
          >
            {txt(language, '🚀 部署脚本 .ps1', '🚀 Deploy .ps1')}
          </button>
        </div>
      </div>
    </section>
  );
}
