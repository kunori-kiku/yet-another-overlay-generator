import {
  BaseEdge,
  EdgeLabelRenderer,
  getBezierPath,
  type EdgeProps,
} from '@xyflow/react';
import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';

// Per edge-type stroke hue — the CATEGORICAL identity color, kept on the line, the arrow marker, and
// the label-pill border so each edge type stays recognizable. The label-pill FILL and TEXT are theme
// tokens (surface-elevated / content), not a hardcoded dark color, so the tag reads on BOTH the light
// and dark canvas (the old `bg:#164e63 / label:#cffafe` pair was dark-only and washed out on light).
const edgeStroke: Record<string, string> = {
  'direct':           '#22d3ee',    // cyan
  'public-endpoint':  '#f59e0b',    // amber
  'relay-path':       '#a78bfa',    // violet
  'candidate':        '#6b7280',    // gray
};

const defaultStroke = '#6b7280';

interface CustomEdgeData {
  edgeType?: string;
  label?: string;
  // Compile-state semantics: pending = drawn but not yet compiled (compiled_port empty); the port
  // is allocated by the backend on the next compile -> dashed line + a "pending" port chip; after
  // compile, a solid line + the actual port chip.
  pending?: boolean;
  port?: number;
  parallelIndex?: number;
  parallelCount?: number;
  sourceNodeName?: string;
  targetNodeName?: string;
  // Link-role chip (parallel-link semantics, contract item 5 / Decisions #5):
  //   'primary'             -> ★ (the primary link's representative edge)
  //   'b1' | 'b2' | ...     -> the backup link's ordinal within this node pair (by appearance order)
  //   'duplicate'           -> a redundant same-direction roleless/primary edge (mirrors backend D71 warning)
  //   undefined             -> single-edge node pair, no chip shown (keeps the look clean)
  roleChip?: 'primary' | 'duplicate' | string;
  // Focus opacity (Decisions #11, literal implementation): a deemphasized element stays mounted,
  // visible, and clickable -- it only fades to 0.15 opacity with the existing 150ms transition.
  // Never display:none / unmount / block clicks.
  deemphasized?: boolean;
  [key: string]: unknown;
}

export function CustomEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  data,
  selected,
}: EdgeProps & { data: CustomEdgeData }) {
  const language = useTopologyStore((state) => state.language);
  const edgeType = data?.edgeType || 'direct';
  const stroke = edgeStroke[edgeType] || defaultStroke;
  const rawLabel = data?.label || edgeType;
  const srcName = data?.sourceNodeName || '';
  const tgtName = data?.targetNodeName || '';
  const namePrefix = srcName && tgtName ? `${srcName} → ${tgtName}` : '';
  const label = namePrefix ? `${namePrefix} | ${rawLabel}` : rawLabel;
  const pending = data?.pending === true;
  const port = data?.port;
  const roleChip = data?.roleChip;
  const deemphasized = data?.deemphasized === true;

  // Parallel-edge offset: compute the offset from parallelIndex and parallelCount
  const parallelIndex = data?.parallelIndex ?? 0;
  const parallelCount = data?.parallelCount ?? 1;
  const offsetStep = 48; // 48px offset per parallel edge (30px arc spacing reads like a render glitch, widened to 48px)
  const totalOffset = (parallelIndex - (parallelCount - 1) / 2) * offsetStep;

  // Achieve the offset effect by adjusting curvature (springy look)
  const curvature = 0.25 + totalOffset * 0.005;

  const [edgePath, labelX, labelY] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
    curvature: Math.abs(curvature) > 0.01 ? curvature : undefined,
  });

  // Label offset: shift vertically to avoid overlap
  const labelOffsetY = totalOffset * 0.6;

  const strokeWidth = selected ? 3.5 : 2.5;
  const animated = edgeType === 'relay-path';

  // Focus deemphasis overrides everything: when deemphasized, always 0.15, regardless of selected / pending.
  const baseEdgeOpacity = selected ? 1 : pending ? 0.65 : 0.8;
  const edgeOpacity = deemphasized ? 0.15 : baseEdgeOpacity;

  return (
    <>
      <BaseEdge
        id={id}
        path={edgePath}
        style={{
          stroke: stroke,
          strokeWidth,
          opacity: edgeOpacity,
          // pending (uncompiled) edges use a dashed line to distinguish the "port not yet allocated" state
          strokeDasharray: pending ? '7 5' : undefined,
          filter: selected ? `drop-shadow(0 0 4px ${stroke})` : undefined,
          transition: 'stroke 150ms, stroke-width 150ms, opacity 150ms',
        }}
        markerEnd={`url(#marker-${edgeType})`}
        className={animated ? 'react-flow__edge-path-animated' : ''}
      />

      <EdgeLabelRenderer>
        <div
          className="nodrag nopan"
          style={{
            position: 'absolute',
            transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY + labelOffsetY}px)`,
            pointerEvents: 'all',
            cursor: 'pointer',
            // When deemphasized, fade the label out (in sync with BaseEdge) while keeping it mounted and clickable.
            opacity: deemphasized ? 0.15 : 1,
            transition: 'opacity 150ms',
          }}
        >
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: '4px',
              background: 'var(--surface-elevated)',
              color: 'var(--content)',
              border: `1.5px solid ${stroke}`,
              borderRadius: '4px',
              padding: '2px 6px',
              fontSize: '10px',
              fontWeight: selected ? 700 : 500,
              whiteSpace: 'nowrap',
              boxShadow: selected
                ? `0 0 8px ${stroke}80`
                : '0 1px 3px rgba(0,0,0,0.15)',
              transition: 'box-shadow 150ms',
            }}
          >
            {/* Link-role chip (contract item 5): ★ primary / bN backup / ⚠ same-direction duplicate.
                A single-edge node pair has no roleChip -> nothing renders, keeping it clean. */}
            {roleChip === 'primary' && (
              <span
                title="primary"
                style={{
                  color: 'var(--warning)',
                  fontSize: '12px',
                  lineHeight: 1,
                }}
              >
                ★
              </span>
            )}
            {roleChip === 'duplicate' && (
              <span
                title="duplicate"
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: '2px',
                  background: 'var(--warning-bg)',
                  color: 'var(--warning)',
                  border: '1px solid var(--warning-border)',
                  borderRadius: '3px',
                  padding: '0 3px',
                  fontSize: '9px',
                  fontWeight: 700,
                }}
              >
                ⚠ {t(language, 'duplicateChip')}
              </span>
            )}
            {roleChip !== undefined &&
              roleChip !== 'primary' &&
              roleChip !== 'duplicate' && (
                <span
                  title={`backup ${roleChip}`}
                  style={{
                    fontFamily: 'monospace',
                    background: `${stroke}30`,
                    color: 'var(--content)',
                    borderRadius: '3px',
                    padding: '0 4px',
                    fontSize: '9px',
                    fontWeight: 700,
                  }}
                >
                  {roleChip}
                </span>
              )}
            <span>{label}</span>
            {/* Port chip: compiled -> the actual listen port (the backend-allocated truth);
                uncompiled -> a "pending" placeholder, avoiding the old misleading dangling-colon "host:" form. */}
            {(port !== undefined || pending) && (
              <span
                style={{
                  fontFamily: 'monospace',
                  background: `${stroke}30`,
                  color: 'var(--content)',
                  borderRadius: '3px',
                  padding: '0 4px',
                  fontStyle: pending ? 'italic' : 'normal',
                  opacity: pending ? 0.8 : 1,
                }}
              >
                {port !== undefined ? `:${port}` : t(language, 'portPendingLabel')}
              </span>
            )}
          </div>
        </div>
      </EdgeLabelRenderer>

      {/* Custom arrow marker */}
      <svg style={{ position: 'absolute', width: 0, height: 0 }}>
        <defs>
          <marker
            id={`marker-${edgeType}`}
            viewBox="0 0 12 12"
            refX="10"
            refY="6"
            markerWidth="8"
            markerHeight="8"
            orient="auto-start-reverse"
          >
            <path d="M 0 0 L 12 6 L 0 12 z" fill={stroke} />
          </marker>
        </defs>
      </svg>
    </>
  );
}
