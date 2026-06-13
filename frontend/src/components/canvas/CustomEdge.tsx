import {
  BaseEdge,
  EdgeLabelRenderer,
  getBezierPath,
  type EdgeProps,
} from '@xyflow/react';
import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';

// 每种 edge type 的颜色方案
const edgeColors: Record<string, { stroke: string; label: string; bg: string }> = {
  'direct':           { stroke: '#22d3ee', label: '#cffafe', bg: '#164e63' },    // cyan
  'public-endpoint':  { stroke: '#f59e0b', label: '#fef3c7', bg: '#78350f' },    // amber
  'relay-path':       { stroke: '#a78bfa', label: '#ede9fe', bg: '#4c1d95' },    // violet
  'candidate':        { stroke: '#6b7280', label: '#e5e7eb', bg: '#374151' },    // gray
};

const defaultColor = { stroke: '#6b7280', label: '#e5e7eb', bg: '#374151' };

interface CustomEdgeData {
  edgeType?: string;
  label?: string;
  // 编译状态语义：pending = 已绘制但尚未编译（compiled_port 为空），端口由后端
  // 在下次编译时分配 → 虚线 + 「待分配」端口徽标；编译后实线 + 实际端口徽标。
  pending?: boolean;
  port?: number;
  parallelIndex?: number;
  parallelCount?: number;
  sourceNodeName?: string;
  targetNodeName?: string;
  // 链路角色徽标（并行链路语义，contract item 5 / Decisions #5）：
  //   'primary'             → ★（主链路代表边）
  //   'b1' | 'b2' | ...     → 备份链路在本节点对内的序号（按出现顺序）
  //   'duplicate'           → 同向多余的 roleless/primary 边（镜像后端 D71 告警）
  //   undefined             → 单边节点对，不显示徽标（保持简洁观感）
  roleChip?: 'primary' | 'duplicate' | string;
  // 焦点透明度（Decisions #11，逐字实现）：被弱化的元素保持挂载、可见、可点击，
  // 仅以 0.15 不透明度淡出，配合既有 150ms 过渡。绝不 display:none / 卸载 / 屏蔽点击。
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
  const colors = edgeColors[edgeType] || defaultColor;
  const rawLabel = data?.label || edgeType;
  const srcName = data?.sourceNodeName || '';
  const tgtName = data?.targetNodeName || '';
  const namePrefix = srcName && tgtName ? `${srcName} → ${tgtName}` : '';
  const label = namePrefix ? `${namePrefix} | ${rawLabel}` : rawLabel;
  const pending = data?.pending === true;
  const port = data?.port;
  const roleChip = data?.roleChip;
  const deemphasized = data?.deemphasized === true;

  // 平行边偏移：根据 parallelIndex 和 parallelCount 计算偏移量
  const parallelIndex = data?.parallelIndex ?? 0;
  const parallelCount = data?.parallelCount ?? 1;
  const offsetStep = 48; // 每条平行边偏移 48px（30px 弧间距读起来像渲染故障，加宽到 48px）
  const totalOffset = (parallelIndex - (parallelCount - 1) / 2) * offsetStep;

  // 通过调整 curvature 实现偏移效果 (弹性效果)
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

  // 标签偏移：在垂直方向上偏移，避免重叠
  const labelOffsetY = totalOffset * 0.6;

  const strokeWidth = selected ? 3.5 : 2.5;
  const animated = edgeType === 'relay-path';

  // 焦点弱化压倒一切：被弱化时一律 0.15，无论 selected / pending。
  const baseEdgeOpacity = selected ? 1 : pending ? 0.65 : 0.8;
  const edgeOpacity = deemphasized ? 0.15 : baseEdgeOpacity;

  return (
    <>
      <BaseEdge
        id={id}
        path={edgePath}
        style={{
          stroke: colors.stroke,
          strokeWidth,
          opacity: edgeOpacity,
          // pending（未编译）边用虚线区分「端口尚未分配」状态
          strokeDasharray: pending ? '7 5' : undefined,
          filter: selected ? `drop-shadow(0 0 4px ${colors.stroke})` : undefined,
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
            // 弱化时淡出标签（与 BaseEdge 同步），保持挂载与可点击。
            opacity: deemphasized ? 0.15 : 1,
            transition: 'opacity 150ms',
          }}
        >
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: '4px',
              background: colors.bg,
              color: colors.label,
              border: `1.5px solid ${colors.stroke}`,
              borderRadius: '4px',
              padding: '2px 6px',
              fontSize: '10px',
              fontWeight: selected ? 700 : 500,
              whiteSpace: 'nowrap',
              boxShadow: selected
                ? `0 0 8px ${colors.stroke}80`
                : '0 1px 3px rgba(0,0,0,0.4)',
              transition: 'box-shadow 150ms',
            }}
          >
            {/* 链路角色徽标（contract item 5）：★ 主链路 / bN 备份 / ⚠ 同向重复。
                单边节点对不设 roleChip → 不渲染，保持简洁。 */}
            {roleChip === 'primary' && (
              <span
                title="primary"
                style={{
                  color: '#fde68a',
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
                  background: '#78350f',
                  color: '#fde68a',
                  border: '1px solid #f59e0b',
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
                    background: `${colors.stroke}30`,
                    color: colors.label,
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
            {/* 端口徽标：已编译 → 实际监听端口（后端分配的真值）；
                未编译 → 「待分配」占位，避免旧版 "host:" 悬空冒号式的误导。 */}
            {(port !== undefined || pending) && (
              <span
                style={{
                  fontFamily: 'monospace',
                  background: `${colors.stroke}30`,
                  color: colors.label,
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

      {/* 自定义箭头 marker */}
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
            <path d="M 0 0 L 12 6 L 0 12 z" fill={colors.stroke} />
          </marker>
        </defs>
      </svg>
    </>
  );
}
