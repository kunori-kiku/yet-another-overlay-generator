import { Handle, Position } from '@xyflow/react';
import type { NodeProps } from '@xyflow/react';
import { useTopologyStore } from '../../stores/topologyStore';
import { txt } from '../../i18n';

const roleColors: Record<string, string> = {
  peer: 'border-green-500 bg-green-900/50',
  router: 'border-blue-500 bg-blue-900/50',
  relay: 'border-yellow-500 bg-yellow-900/50',
  gateway: 'border-purple-500 bg-purple-900/50',
  client: 'border-cyan-500 bg-cyan-900/50',
};

// 连接手柄：节点级、角色配色、永久存在（编译前后形态一致）。
// 接口是编译产物而非绘图原语 —— onConnect 只用节点 ID（忽略 handle id），端口由
// 后端在编译时分配；因此不再渲染「每接口一个手柄」（旧形态在编译后只剩被占用的
// 接口手柄，看起来像"端口已饱和、无法再连线"，且暗示新连线会复用既有端口）。
const handleClassBase =
  '!w-3.5 !h-3.5 !border-2 transition-all duration-150 hover:!w-4 hover:!h-4';

// react-flow 的 `.react-flow__handle` 自带默认背景；用 `!` important 前缀让角色色生效。
const roleHandleColorClass: Record<string, string> = {
  peer: '!border-green-500 !bg-green-500',
  router: '!border-blue-500 !bg-blue-500',
  relay: '!border-yellow-500 !bg-yellow-500',
  gateway: '!border-purple-500 !bg-purple-500',
  client: '!border-cyan-500 !bg-cyan-500',
};

const roleIcons: Record<string, string> = {
  peer: '💻',
  router: '🔀',
  relay: '🔁',
  gateway: '🌐',
  client: '📱',
};

// 接口详情徽标的区分配色（展示用）
const ifaceChipColors = [
  { bg: '#f87171', border: '#dc2626' }, // red
  { bg: '#fb923c', border: '#ea580c' }, // orange
  { bg: '#facc15', border: '#ca8a04' }, // yellow
  { bg: '#4ade80', border: '#16a34a' }, // green
  { bg: '#22d3ee', border: '#0891b2' }, // cyan
  { bg: '#818cf8', border: '#6366f1' }, // indigo
  { bg: '#e879f9', border: '#c026d3' }, // fuchsia
  { bg: '#fb7185', border: '#e11d48' }, // rose
];

// 节点接口徽标的数据形态：由 TopologyCanvas 通过共享解析器 resolveNodeInterfaces
// （edge-aware，Decisions #12）算好后传入。绝不在前端重算接口名 / 反推 peer 名。
interface IfaceChip {
  name: string;        // 真实接口名（tooltip 用，永不剥离 'wg-'）
  listenPort: number;  // 后端分配的监听端口
  peerName: string;    // 对端节点名（resolver 解析；'unknown' 时回退为接口名）
  // 角色标记，与边扇形序号一致：'★' 主链路 / 'b1','b2',... 备份 / undefined 未知或单边。
  roleMarker?: string;
}

interface CustomNodeData {
  label: string;
  role: string;
  overlayIp: string;
  domainName: string;
  interfaces?: IfaceChip[];
  // 焦点透明度（Decisions #11）：被弱化的节点保持挂载、可见、可点击，仅淡出到 0.15。
  deemphasized?: boolean;
  [key: string]: unknown;
}

export function CustomNode({ data, selected }: NodeProps & { data: CustomNodeData }) {
  const language = useTopologyStore((state) => state.language);
  // 「显示接口详情」画布开关：接口/端口信息按需展开，默认收起。
  const showInterfaces = useTopologyStore((state) => state.showInterfaces);
  const role = data.role || 'peer';
  const colorClass = roleColors[role] || roleColors.peer;
  const icon = roleIcons[role] || '💻';
  const interfaces: IfaceChip[] = (data.interfaces as IfaceChip[]) || [];
  const handleClass = `${roleHandleColorClass[role] || roleHandleColorClass.peer} ${handleClassBase}`;
  const dragHint = txt(language, '拖拽以连接', 'drag to connect');
  const deemphasized = data.deemphasized === true;

  return (
    // 根容器包裹焦点透明度：弱化时整张卡片淡出到 0.15，但手柄仍渲染且可点击
    //（透明度不影响命中测试 → 点击透明节点照样把焦点切过去）。
    <div
      style={{
        opacity: deemphasized ? 0.15 : 1,
        transition: 'opacity 150ms',
      }}
    >
      <Handle
        type="target"
        position={Position.Top}
        title={dragHint}
        className={handleClass}
      />

      <div
        className={`px-3 py-2 rounded-lg border-2 ${colorClass} ${
          selected ? 'ring-2 ring-white' : ''
        } min-w-[120px] text-center transition-shadow duration-150 ${
          selected ? 'shadow-lg' : 'shadow'
        }`}
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
        {/* 已编译接口详情（纯展示，开关控制）：wg-<peer> 接口名 + 监听端口。
            手柄不再与接口绑定，这里是接口信息的唯一画布载体。 */}
        {showInterfaces && interfaces.length > 0 && (
          <div className="mt-1 flex flex-wrap justify-center gap-1">
            {interfaces.map((iface, i) => {
              const color = ifaceChipColors[i % ifaceChipColors.length];
              // roleMarker 与边扇形序号一致（★ / bN）：可解析的链路显示角色标记；
              // 'unknown' 由 TopologyCanvas 让 peerName 回退为接口名（永不剥离 'wg-'），
              // 此时不带 roleMarker。tooltip 始终展示真实接口名 + 端口。
              const marker = iface.roleMarker;
              return (
                <span
                  key={iface.name}
                  className="text-[9px] font-mono px-1 rounded"
                  style={{ backgroundColor: `${color.bg}30`, color: color.bg }}
                  title={`${iface.name} :${iface.listenPort}`}
                >
                  {marker ? `${marker} ` : ''}
                  {iface.peerName}:{iface.listenPort}
                </span>
              );
            })}
          </div>
        )}
      </div>

      <Handle
        type="source"
        position={Position.Bottom}
        title={dragHint}
        className={handleClass}
      />
    </div>
  );
}
