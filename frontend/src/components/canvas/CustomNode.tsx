import { Handle, Position } from '@xyflow/react';
import type { NodeProps } from '@xyflow/react';

const roleColors: Record<string, string> = {
  peer: 'border-green-500 bg-green-900/50',
  router: 'border-blue-500 bg-blue-900/50',
  relay: 'border-yellow-500 bg-yellow-900/50',
  gateway: 'border-purple-500 bg-purple-900/50',
};

const roleIcons: Record<string, string> = {
  peer: '💻',
  router: '🔀',
  relay: '🔁',
  gateway: '🌐',
};

interface CustomNodeData {
  label: string;
  role: string;
  overlayIp: string;
  domainName: string;
  [key: string]: unknown;
}

export function CustomNode({ data, selected }: NodeProps & { data: CustomNodeData }) {
  const role = data.role || 'peer';
  const colorClass = roleColors[role] || roleColors.peer;
  const icon = roleIcons[role] || '💻';

  return (
    <>
      <Handle type="target" position={Position.Top} className="!bg-gray-400 !w-2 !h-2" />
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
      </div>
      <Handle type="source" position={Position.Bottom} className="!bg-gray-400 !w-2 !h-2" />
    </>
  );
}
