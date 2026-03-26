import { useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import * as diff from 'diff';

export function AuditView() {
  const history = useTopologyStore((s) => s.history);
  const clearHistory = useTopologyStore((s) => s.clearHistory);
  const nodes = useTopologyStore((s) => s.nodes);
  const edges = useTopologyStore((s) => s.edges);

  const [selectedHistoryId, setSelectedHistoryId] = useState<string | null>(null);
  const [selectedNodeFileId, setSelectedNodeFileId] = useState<string>('');

  const currentResult = useTopologyStore((s) => s.compileResult);

  const selectedHistory = history.find((h) => h.id === selectedHistoryId);

  const renderDiff = (oldText: string, newText: string) => {
    const changes = diff.diffLines(oldText || '', newText || '');
    return (
      <div className="font-mono text-xs whitespace-pre pl-2">
        {changes.map((part, index) => {
          const color = part.added ? 'bg-green-900/40 text-green-400' : part.removed ? 'bg-red-900/40 text-red-400' : 'text-gray-300';
          const prefix = part.added ? '+ ' : part.removed ? '- ' : '  ';
          if (!part.added && !part.removed && part.value.split('\n').length > 5 && index !== 0 && index !== changes.length - 1) {
            return (
              <span key={index} className="text-gray-500">
                {`\n... unchanged lines skipped ...\n`}
              </span>
            );
          }
          return (
            <span key={index} className={color}>
              {part.value.split('\n').map((line, i, arr) => (i === arr.length - 1 && line === '' ? null : <div key={i}>{prefix}{line}</div>))}
            </span>
          );
        })}
      </div>
    );
  };

  const getFileContent = (result: any) => {
    if (!result || !selectedNodeFileId) return '';
    // Format: "nodeId:fileType" or "nodeId:wg:interfaceName"
    const parts = selectedNodeFileId.split(':');
    const nodeId = parts[0];
    const fileType = parts[1];
    if (fileType === 'wg' && result.wireguard_configs) {
      // per-peer key format: "nodeId:interfaceName"
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

  // Security Audit list: Nodes that accept inbound or relay
  const exposedNodes = nodes.filter((n) => n.capabilities.can_accept_inbound || n.capabilities.can_relay);

  return (
    <div className="h-full flex flex-col p-6 space-y-6 overflow-y-auto">
      <div className="flex justify-between items-center">
        <h2 className="text-xl font-bold text-white">Security Audit & Compilation History</h2>
        <button onClick={clearHistory} className="px-3 py-1.5 bg-red-800 hover:bg-red-700 text-sm rounded">Clear History</button>
      </div>

      {/* Global Audit Summary */}
      <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg">
        <h3 className="text-lg font-semibold mb-3 text-orange-400">🛡️ Global Exopsure Audit</h3>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <h4 className="text-sm text-gray-400 mb-2">Exposed Nodes (Public / Relays)</h4>
            {exposedNodes.length === 0 ? <span className="text-xs text-gray-500">No exposed nodes</span> : (
              <ul className="text-sm space-y-1">
                {exposedNodes.map(n => {
                  const inboundEdges = edges.filter(e => e.to_node_id === n.id);
                  return (
                    <li key={n.id} className="text-gray-300">
                      <strong>{n.name}</strong> ({n.role}) - {n.overlay_ip}<br />
                      <span className="text-gray-500 text-xs pl-2">Listens on port: {n.listen_port || 'Auto'} | Inbound allowed paths: {inboundEdges.length}</span>
                    </li>
                  )
                })}
              </ul>
            )}
          </div>
          <div>
            <h4 className="text-sm text-gray-400 mb-2">Network Statistics</h4>
            <ul className="text-sm text-gray-300 space-y-1">
              <li>Total Nodes: {nodes.length}</li>
              <li>Encrypted Edges: {edges.length}</li>
              <li>Current Checksum:{currentResult ? <span className="font-mono text-xs ml-2 bg-gray-900 p-1 rounded break-all">{currentResult.manifest.checksum}</span> : ' N/A'}</li>
            </ul>
          </div>
        </div>
      </section>


      {/* History and Diff */}
      <section className="flex flex-1 gap-6 min-h-[400px]">
        {/* History List */}
        <div className="w-1/3 flex flex-col bg-gray-800 border border-gray-700 p-4 rounded-lg">
          <h3 className="text-lg font-semibold mb-3 text-blue-400">📜 Compilation History</h3>
          <div className="flex-1 overflow-y-auto space-y-2">
            {currentResult && (
              <div
                className={`p-3 rounded cursor-pointer border ${selectedHistoryId === null ? 'border-blue-500 bg-blue-900/30' : 'border-gray-600 bg-gray-700 hover:bg-gray-600'}`}
                onClick={() => setSelectedHistoryId(null)}
              >
                <div className="font-semibold text-sm">Current Working State</div>
                <div className="text-xs text-gray-400">Ready to export</div>
              </div>
            )}
            
            {history.map((h, i) => (
              <div
                key={h.id}
                className={`p-3 rounded cursor-pointer border ${selectedHistoryId === h.id ? 'border-blue-500 bg-blue-900/30' : 'border-gray-600 bg-gray-700 hover:bg-gray-600'}`}
                onClick={() => setSelectedHistoryId(h.id)}
              >
                <div className="font-semibold text-sm">Snapshot {history.length - i}</div>
                <div className="text-xs text-gray-400">{new Date(h.timestamp).toLocaleString()}</div>
                <div className="text-xs text-gray-500 font-mono mt-1 w-24 truncate">{h.compileResult.manifest.checksum}</div>
              </div>
            ))}
            {history.length === 0 && !currentResult && <div className="text-sm text-gray-500 italic">No history available. Compile the project first.</div>}
          </div>
        </div>

        {/* Diff View */}
        <div className="w-2/3 flex flex-col bg-gray-800 border border-gray-700 rounded-lg overflow-hidden">
          <div className="p-3 border-b border-gray-700 bg-gray-800 flex justify-between items-center shrink-0">
            <h3 className="text-lg font-semibold text-gray-200">🔍 Configuration Diff {selectedHistoryId && '(Current vs Snapshot)'}</h3>
            <select
              className="bg-gray-900 border border-gray-600 text-sm rounded px-2 py-1"
              value={selectedNodeFileId}
              onChange={(e) => setSelectedNodeFileId(e.target.value)}
            >
              <option value="">Select a file to diff...</option>
              {nodes.map(n => {
                // Collect per-peer WireGuard interface names from compile result
                const wgKeys = currentResult
                  ? Object.keys(currentResult.wireguard_configs)
                      .filter((key) => key.startsWith(n.id + ':'))
                      .map((key) => key.split(':').slice(1).join(':'))
                  : [];
                return (
                  <optgroup key={n.id} label={n.name}>
                    {wgKeys.length > 0
                      ? wgKeys.map((ifName) => (
                          <option key={`${n.id}:wg:${ifName}`} value={`${n.id}:wg:${ifName}`}>
                            {ifName}.conf
                          </option>
                        ))
                      : <option value={`${n.id}:wg:`} disabled>({'\u00A0'}no wg configs{'\u00A0'})</option>
                    }
                    <option value={`${n.id}:babel`}>babeld.conf</option>
                    <option value={`${n.id}:sysctl`}>sysctl.conf</option>
                    <option value={`${n.id}:install`}>install.sh</option>
                  </optgroup>
                );
              })}
            </select>
          </div>
          <div className="flex-1 p-4 overflow-y-auto bg-gray-950">
            {!selectedNodeFileId ? (
              <div className="text-center text-gray-500 mt-20">Select a file from the dropdown to view differences.</div>
            ) : selectedHistoryId === null ? (
              <pre className="font-mono text-xs text-gray-300 whitespace-pre-wrap">{currentText || 'File not found in current compilation.'}</pre>
            ) : (
              <div>
                {!oldText && !currentText ? (
                  <div className="text-gray-500 italic">File does not exist in both states.</div>
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
