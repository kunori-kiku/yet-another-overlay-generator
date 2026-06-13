import { useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';
import * as diff from 'diff';
import type { CompileResponse, Node as TopologyNode } from '../../types/topology';

// 文件选择器编码：把 nodeId / fileType / interfaceName 三段分别 encodeURIComponent 后用 '|'
// 连接。早先实现用 ':' 直接拼接并按 ':' 切分，含 ':' 的 ID 会破坏所有查找（修复 D58）。
const FILE_SELECTOR_DELIMITER = '|';

function encodeFileSelector(...segments: string[]): string {
  return segments.map((segment) => encodeURIComponent(segment)).join(FILE_SELECTOR_DELIMITER);
}

function decodeFileSelector(encoded: string): string[] {
  return encoded.split(FILE_SELECTOR_DELIMITER).map((segment) => decodeURIComponent(segment));
}

export function AuditView() {
  const history = useTopologyStore((s) => s.history);
  const clearHistory = useTopologyStore((s) => s.clearHistory);
  const nodes = useTopologyStore((s) => s.nodes);
  const edges = useTopologyStore((s) => s.edges);
  const language = useTopologyStore((s) => s.language);

  const [selectedHistoryId, setSelectedHistoryId] = useState<string | null>(null);
  const [selectedNodeFileId, setSelectedNodeFileId] = useState<string>('');

  const currentResult = useTopologyStore((s) => s.compileResult);

  const selectedHistory = history.find((h) => h.id === selectedHistoryId);

  // 折叠未变更区块时保留首尾各 3 行上下文，中间用计数标记折叠，便于审阅者定位改动位置
  // （修复 D77：原实现整段丢弃上下文，无法判断改动落在文件何处）。
  const DIFF_CONTEXT_LINES = 3;

  const renderDiff = (oldText: string, newText: string) => {
    const changes = diff.diffLines(oldText || '', newText || '');

    // 把一段未变更文本拆成「首部上下文 + 折叠标记 + 尾部上下文」，仅当行数超过
    // 2*上下文+1 时才折叠，否则原样显示。
    const renderUnchangedPart = (value: string, key: number) => {
      const lines = value.split('\n');
      // diff.diffLines 的块通常以换行结尾，产生末尾空串；剔除它再计数。
      const hasTrailingEmpty = lines.length > 0 && lines[lines.length - 1] === '';
      const contentLines = hasTrailingEmpty ? lines.slice(0, -1) : lines;

      const renderLines = (linesToRender: string[], keyPrefix: string) =>
        linesToRender.map((line, i) => (
          <div key={`${keyPrefix}-${i}`}>{`  ${line}`}</div>
        ));

      if (contentLines.length <= DIFF_CONTEXT_LINES * 2 + 1) {
        return (
          <span key={key} className="text-gray-300">
            {renderLines(contentLines, `u${key}`)}
          </span>
        );
      }

      const head = contentLines.slice(0, DIFF_CONTEXT_LINES);
      const tail = contentLines.slice(contentLines.length - DIFF_CONTEXT_LINES);
      const collapsedCount = contentLines.length - DIFF_CONTEXT_LINES * 2;

      return (
        <span key={key} className="text-gray-300">
          {renderLines(head, `u${key}-head`)}
          <div className="text-gray-500">
            {t(language, 'auditView.collapsedLines', { count: collapsedCount })}
          </div>
          {renderLines(tail, `u${key}-tail`)}
        </span>
      );
    };

    return (
      <div className="font-mono text-xs whitespace-pre pl-2">
        {changes.map((part, index) => {
          if (!part.added && !part.removed && index !== 0 && index !== changes.length - 1) {
            return renderUnchangedPart(part.value, index);
          }
          const color = part.added ? 'bg-green-900/40 text-green-400' : part.removed ? 'bg-red-900/40 text-red-400' : 'text-gray-300';
          const prefix = part.added ? '+ ' : part.removed ? '- ' : '  ';
          return (
            <span key={index} className={color}>
              {part.value.split('\n').map((line, i, arr) => (i === arr.length - 1 && line === '' ? null : <div key={i}>{prefix}{line}</div>))}
            </span>
          );
        })}
      </div>
    );
  };

  const getFileContent = (result: CompileResponse | null | undefined) => {
    if (!result || !selectedNodeFileId) return '';
    // 编码格式（'|' 分隔，每段 encodeURIComponent）：
    //   "nodeId|fileType"  或  "nodeId|wg|interfaceName"
    const parts = decodeFileSelector(selectedNodeFileId);
    const nodeId = parts[0];
    const fileType = parts[1];
    if (fileType === 'wg' && result.wireguard_configs) {
      // wireguard_configs 的 key 仍是后端约定的 "nodeId:interfaceName" 形式
      const interfaceName = parts.slice(2).join(':');
      return result.wireguard_configs[nodeId + ':' + interfaceName] || '';
    }
    if (fileType === 'babel' && result.babel_configs) return result.babel_configs[nodeId] || '';
    if (fileType === 'install' && result.install_scripts) return result.install_scripts[nodeId] || '';
    if (fileType === 'sysctl' && result.sysctl_configs) return result.sysctl_configs[nodeId] || '';
    return '';
  };

  const currentText = getFileContent(currentResult);
  const oldText = getFileContent(selectedHistory?.compileResult);

  // 安全审计的暴露节点列表必须基于后端推断的能力（capabilities）。角色一改，前端本地
  // store 里的能力立刻过时（后端要等到重新编译才会重新推断），直接读 store 会漏报暴露的
  // relay/inbound 节点（修复 D26）。有编译结果时以 compileResult.topology.nodes 为准，并提示
  // 「反映上次编译，重新编译刷新」；无编译结果时回退到 store 节点，并标注为编译前估计值。
  const auditNodes: TopologyNode[] = currentResult ? currentResult.topology.nodes : nodes;
  const auditNodesAreBackendInferred = currentResult !== null;
  // Security Audit list: Nodes that accept inbound or relay
  const exposedNodes = auditNodes.filter((n) => n.capabilities.can_accept_inbound || n.capabilities.can_relay);

  return (
    // Self-sizing section (was a full-screen h-full view): now stacks inside the
    // /security page, which owns the scroll.
    <div className="flex flex-col p-6 space-y-6">
      <div className="flex justify-between items-center">
        <h2 className="text-xl font-bold text-white">{t(language, 'compileHistoryTitle')}</h2>
        <button onClick={clearHistory} className="px-3 py-1.5 bg-red-800 hover:bg-red-700 text-sm rounded">{t(language, 'chClearHistory')}</button>
      </div>

      {/* Global Audit Summary */}
      <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg">
        <h3 className="text-lg font-semibold mb-3 text-orange-400">🛡️ {t(language, 'chExposureAudit')}</h3>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <h4 className="text-sm text-gray-400 mb-2">{t(language, 'chExposedNodes')}</h4>
            <p className="text-xs text-gray-500 mb-2 italic">
              {auditNodesAreBackendInferred
                ? t(language, 'auditView.basedOnBackendInferred')
                : t(language, 'auditView.preCompileEstimateFrom')}
            </p>
            {exposedNodes.length === 0 ? <span className="text-xs text-gray-500">{t(language, 'chNoExposedNodes')}</span> : (
              <ul className="text-sm space-y-1">
                {exposedNodes.map(n => {
                  const inboundEdges = edges.filter(e => e.to_node_id === n.id);
                  return (
                    <li key={n.id} className="text-gray-300">
                      <strong>{n.name}</strong> ({n.role}) - {n.overlay_ip}<br />
                      <span className="text-gray-500 text-xs pl-2">{t(language, 'chListensOnPort')}: {n.listen_port || 'Auto'} | {t(language, 'chInboundPaths')}: {inboundEdges.length}</span>
                    </li>
                  )
                })}
              </ul>
            )}
          </div>
          <div>
            <h4 className="text-sm text-gray-400 mb-2">{t(language, 'chNetworkStats')}</h4>
            <ul className="text-sm text-gray-300 space-y-1">
              <li>{t(language, 'chTotalNodes')}: {nodes.length}</li>
              <li>{t(language, 'chEncryptedEdges')}: {edges.length}</li>
              <li>{t(language, 'chCurrentChecksum')}:{currentResult ? <span className="font-mono text-xs ml-2 bg-gray-900 p-1 rounded break-all">{currentResult.manifest.checksum}</span> : ' N/A'}</li>
            </ul>
          </div>
        </div>
      </section>


      {/* History and Diff */}
      <section className="flex flex-1 gap-6 min-h-[400px]">
        {/* History List */}
        <div className="w-1/3 flex flex-col bg-gray-800 border border-gray-700 p-4 rounded-lg">
          <h3 className="text-lg font-semibold mb-3 text-blue-400">📜 {t(language, 'chCompilationHistory')}</h3>
          <div className="flex-1 overflow-y-auto space-y-2">
            {currentResult && (
              <div
                className={`p-3 rounded cursor-pointer border ${selectedHistoryId === null ? 'border-blue-500 bg-blue-900/30' : 'border-gray-600 bg-gray-700 hover:bg-gray-600'}`}
                onClick={() => setSelectedHistoryId(null)}
              >
                <div className="font-semibold text-sm">{t(language, 'chCurrentWorkingState')}</div>
                <div className="text-xs text-gray-400">{t(language, 'chReadyToExport')}</div>
              </div>
            )}
            
            {history.map((h, i) => (
              <div
                key={h.id}
                className={`p-3 rounded cursor-pointer border ${selectedHistoryId === h.id ? 'border-blue-500 bg-blue-900/30' : 'border-gray-600 bg-gray-700 hover:bg-gray-600'}`}
                onClick={() => setSelectedHistoryId(h.id)}
              >
                <div className="font-semibold text-sm">{t(language, 'chSnapshot')} {history.length - i}</div>
                <div className="text-xs text-gray-400">{new Date(h.timestamp).toLocaleString()}</div>
                <div className="text-xs text-gray-500 font-mono mt-1 w-24 truncate">{h.compileResult.manifest.checksum}</div>
              </div>
            ))}
            {history.length === 0 && !currentResult && <div className="text-sm text-gray-500 italic">{t(language, 'chNoHistory')}</div>}
          </div>
        </div>

        {/* Diff View */}
        <div className="w-2/3 flex flex-col bg-gray-800 border border-gray-700 rounded-lg overflow-hidden">
          <div className="p-3 border-b border-gray-700 bg-gray-800 flex justify-between items-center shrink-0">
            <h3 className="text-lg font-semibold text-gray-200">🔍 {t(language, 'chConfigDiff')} {selectedHistoryId && t(language, 'chCurrentVsSnapshot')}</h3>
            <select
              className="bg-gray-900 border border-gray-600 text-sm rounded px-2 py-1"
              value={selectedNodeFileId}
              onChange={(e) => setSelectedNodeFileId(e.target.value)}
            >
              <option value="">{t(language, 'chSelectFileToDiff')}</option>
              {nodes.map(n => {
                // \u4ECE\u7F16\u8BD1\u7ED3\u679C\u6536\u96C6\u6BCF\u4E2A peer \u7684 WireGuard \u63A5\u53E3\u540D\u3002wireguard_configs \u7684 key \u662F
                // \u540E\u7AEF\u7EA6\u5B9A\u7684 "nodeId:interfaceName"\uFF0C\u8FD9\u91CC\u4EE5 nodeId \u524D\u7F00\u5207\u51FA\u63A5\u53E3\u540D\u90E8\u5206\u3002
                const wgKeys = currentResult
                  ? Object.keys(currentResult.wireguard_configs)
                      .filter((key) => key.startsWith(n.id + ':'))
                      .map((key) => key.slice(n.id.length + 1))
                  : [];
                return (
                  <optgroup key={n.id} label={n.name}>
                    {wgKeys.length > 0
                      ? wgKeys.map((ifName) => (
                          <option key={encodeFileSelector(n.id, 'wg', ifName)} value={encodeFileSelector(n.id, 'wg', ifName)}>
                            {ifName}.conf
                          </option>
                        ))
                      : <option value={encodeFileSelector(n.id, 'wg', '')} disabled>({'\u00A0'}{t(language, 'chNoWgConfigs')}{'\u00A0'})</option>
                    }
                    <option value={encodeFileSelector(n.id, 'babel')}>babeld.conf</option>
                    <option value={encodeFileSelector(n.id, 'sysctl')}>sysctl.conf</option>
                    <option value={encodeFileSelector(n.id, 'install')}>install.sh</option>
                  </optgroup>
                );
              })}
            </select>
          </div>
          <div className="flex-1 p-4 overflow-y-auto bg-gray-950">
            {!selectedNodeFileId ? (
              <div className="text-center text-gray-500 mt-20">{t(language, 'chSelectFromDropdown')}</div>
            ) : selectedHistoryId === null ? (
              <pre className="font-mono text-xs text-gray-300 whitespace-pre-wrap">{currentText || t(language, 'chFileNotInCurrent')}</pre>
            ) : (
              <div>
                {!oldText && !currentText ? (
                  <div className="text-gray-500 italic">{t(language, 'chFileNotInBoth')}</div>
                ) : (
                  renderDiff(oldText, currentText)
                )}
              </div>
            )}
          </div>
        </div>
      </section>
    </div>
  );
}
