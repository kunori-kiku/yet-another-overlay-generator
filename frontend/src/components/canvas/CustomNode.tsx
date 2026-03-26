import { Handle, Position } from '@xyflow/react';
import type { NodeProps } from '@xyflow/react';

const roleColors: Record<string, string> = {
  peer: 'border-green-500 bg-green-900/50',
  router: 'border-blue-500 bg-blue-900/50',
  relay: 'border-yellow-500 bg-yellow-900/50',
  gateway: 'border-purple-500 bg-purple-900/50',
  client: 'border-cyan-500 bg-cyan-900/50',
};

const roleIcons: Record<string, string> = {
  peer: '💻',
  router: '🔀',
  relay: '🔁',
  gateway: '🌐',
  client: '📱',
};

// Distinct colors for per-peer interface handles
const peerHandleColors = [
  { bg: '#f87171', border: '#dc2626' }, // red
  { bg: '#fb923c', border: '#ea580c' }, // orange
  { bg: '#facc15', border: '#ca8a04' }, // yellow
  { bg: '#4ade80', border: '#16a34a' }, // green
  { bg: '#22d3ee', border: '#0891b2' }, // cyan
  { bg: '#818cf8', border: '#6366f1' }, // indigo
  { bg: '#e879f9', border: '#c026d3' }, // fuchsia
  { bg: '#fb7185', border: '#e11d48' }, // rose
];

interface IfaceInfo {
  name: string;
  listenPort: number;
  peerName: string;
}

interface CustomNodeData {
  label: string;
  role: string;
  overlayIp: string;
  domainName: string;
  interfaces?: IfaceInfo[];
  [key: string]: unknown;
}

export function CustomNode({ data, selected }: NodeProps & { data: CustomNodeData }) {
  const role = data.role || 'peer';
  const colorClass = roleColors[role] || roleColors.peer;
  const icon = roleIcons[role] || '💻';
  const interfaces: IfaceInfo[] = (data.interfaces as IfaceInfo[]) || [];
  const hasInterfaces = interfaces.length > 0;

  return (
    <>
      {/* Target handles (top): per-interface after compilation, single default otherwise */}
      {hasInterfaces ? (
        interfaces.map((iface, i) => {
          const color = peerHandleColors[i % peerHandleColors.length];
          return (
            <Handle
              key={`target-${iface.name}`}
              type="target"
              id={iface.name}
              position={Position.Top}
              title={`${iface.name} :${iface.listenPort} (← ${iface.peerName})`}
              style={{
                left: `${((i + 1) / (interfaces.length + 1)) * 100}%`,
                backgroundColor: color.bg,
                border: `2px solid ${color.border}`,
                width: '10px',
                height: '10px',
              }}
            />
          );
        })
      ) : (
        <Handle type="target" position={Position.Top} className="!bg-gray-400 !w-2 !h-2" />
      )}

      <div
        className={`px-3 py-2 rounded-lg border-2 ${colorClass} ${
          selected ? 'ring-2 ring-white' : ''
        } min-w-[120px] text-center`}
      >
        <div className="text-lg">{icon}</div>
        <div className="text-sm font-bold text-white">{data.label}</div>
        <div className="text-xs text-gray-300">{role}</div>
        {data.overlayIp && (
          <div className="text-xs text-gray-400 font-mono">{data.overlayIp}</div>
        )}
        {data.domainName && (
          <div className="text-xs text-blue-300">{data.domainName}</div>
        )}
        {hasInterfaces && (
          <div className="mt-1 flex flex-wrap justify-center gap-1">
            {interfaces.map((iface, i) => {
              const color = peerHandleColors[i % peerHandleColors.length];
              return (
                <span
                  key={iface.name}
                  className="text-[9px] font-mono px-1 rounded"
                  style={{ backgroundColor: `${color.bg}30`, color: color.bg }}
                  title={`${iface.name} :${iface.listenPort}`}
                >
                  {iface.peerName}:{iface.listenPort}
                </span>
              );
            })}
          </div>
        )}
      </div>

      {/* Source handles (bottom): per-interface after compilation, single default otherwise */}
      {hasInterfaces ? (
        interfaces.map((iface, i) => {
          const color = peerHandleColors[i % peerHandleColors.length];
          return (
            <Handle
              key={`source-${iface.name}`}
              type="source"
              id={iface.name}
              position={Position.Bottom}
              title={`${iface.name} :${iface.listenPort} (→ ${iface.peerName})`}
              style={{
                left: `${((i + 1) / (interfaces.length + 1)) * 100}%`,
                backgroundColor: color.bg,
                border: `2px solid ${color.border}`,
                width: '10px',
                height: '10px',
              }}
            />
          );
        })
      ) : (
        <Handle type="source" position={Position.Bottom} className="!bg-gray-400 !w-2 !h-2" />
      )}
    </>
  );
}
