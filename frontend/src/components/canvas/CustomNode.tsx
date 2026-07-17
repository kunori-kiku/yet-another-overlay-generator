import { Handle, Position } from '@xyflow/react';
import type { NodeProps } from '@xyflow/react';
import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';
import { roleHue } from './roleHue';

// Connection handles: node-level, role-colored, always present (same shape before and after
// compile). Interfaces are a compile product, not a drawing primitive -- onConnect uses only the
// node ID (ignoring handle id), and ports are allocated by the backend at compile time; so we no
// longer render "one handle per interface" (the old shape left only occupied interface handles
// after compile, which looked like "ports saturated, cannot connect anymore" and implied a new
// connection would reuse an existing port).
// The card border/fill AND handle colors both come from the shared ROLE_HUE map (roleHue.ts) so they
// stay in lockstep with the MiniMap. react-flow's `.react-flow__handle` ships a default background;
// ROLE_HUE.handle is `!`-important-prefixed so the role color wins.
const handleClassBase =
  '!w-3.5 !h-3.5 !border-2 transition-all duration-150 hover:!w-4 hover:!h-4';

const roleIcons: Record<string, string> = {
  peer: '💻',
  router: '🔀',
  relay: '🔁',
  gateway: '🌐',
  client: '📱',
};

// Distinguishing colors for interface-detail chips (display only)
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

// Data shape for node interface chips: computed by TopologyCanvas via the shared resolver
// resolveNodeInterfaces (edge-aware, Decisions #12) and passed in. Never recompute interface
// names / back-derive peer names on the frontend.
interface IfaceChip {
  name: string;        // real interface name (for the tooltip; never strips 'wg-')
  listenPort: number;  // backend-allocated listen port
  peerName: string;    // peer node name (resolved by the resolver; falls back to the interface name when 'unknown')
  // Role marker, consistent with the edge fan ordinal: '★' primary / 'b1','b2',... backup / undefined unknown or single-edge.
  roleMarker?: string;
}

interface CustomNodeData {
  label: string;
  role: string;
  overlayIp: string;
  showOverlayIps?: boolean;
  domainName: string;
  interfaces?: IfaceChip[];
  // Focus opacity (Decisions #11): a deemphasized node stays mounted, visible, and clickable -- it only fades to 0.15.
  deemphasized?: boolean;
  [key: string]: unknown;
}

export function CustomNode({ data, selected }: NodeProps & { data: CustomNodeData }) {
  const language = useTopologyStore((state) => state.language);
  // The "show interface details" canvas toggle: interface/port info expands on demand, collapsed by default.
  const showInterfaces = useTopologyStore((state) => state.showInterfaces);
  const role = data.role || 'peer';
  const hue = roleHue(role);
  const colorClass = `${hue.border} ${hue.fill}`;
  const icon = roleIcons[role] || '💻';
  const interfaces: IfaceChip[] = (data.interfaces as IfaceChip[]) || [];
  const handleClass = `${hue.handle} ${handleClassBase}`;
  const dragHint = t(language, 'customNode.dragToConnect');
  const deemphasized = data.deemphasized === true;

  return (
    // The root container wraps focus opacity: when deemphasized the whole card fades to 0.15, but
    // the handles still render and stay clickable (opacity does not affect hit testing -> clicking
    // a transparent node still switches focus to it).
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
          selected ? 'ring-2 ring-[var(--cta)]' : ''
        } min-w-[120px] text-center transition-shadow duration-150 ${
          selected ? 'shadow-lg' : 'shadow'
        }`}
      >
        <div className="text-lg">{icon}</div>
        <div className="text-sm font-bold text-[var(--content)]">{data.label}</div>
        <div className="text-xs text-[var(--content)]">{role}</div>
        {data.showOverlayIps !== false && data.overlayIp && (
          <div className="text-xs text-[var(--content-muted)] font-mono">{data.overlayIp}</div>
        )}
        {data.domainName && (
          <div className="text-xs text-[var(--info)]">{data.domainName}</div>
        )}
        {/* Compiled interface details (display only, toggle-gated): wg-<peer> interface name +
            listen port. Handles are no longer bound to interfaces, so this is the only canvas
            carrier of interface info. */}
        {showInterfaces && interfaces.length > 0 && (
          <div className="mt-1 flex flex-wrap justify-center gap-1">
            {interfaces.map((iface, i) => {
              const color = ifaceChipColors[i % ifaceChipColors.length];
              // roleMarker is consistent with the edge fan ordinal (★ / bN): resolvable links show
              // the role marker; for 'unknown', TopologyCanvas falls peerName back to the interface
              // name (never stripping 'wg-') and there is no roleMarker. The tooltip always shows
              // the real interface name + port.
              const marker = iface.roleMarker;
              return (
                <span
                  key={iface.name}
                  className="text-[9px] font-mono px-1 rounded"
                  style={{ backgroundColor: `${color.bg}30`, color: 'var(--content)' }}
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
