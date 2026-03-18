import {
  BaseEdge,
  EdgeLabelRenderer,
  getBezierPath,
  type EdgeProps,
} from '@xyflow/react';

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
  parallelIndex?: number;
  parallelCount?: number;
  sourceNodeName?: string;
  targetNodeName?: string;
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
  const edgeType = data?.edgeType || 'direct';
  const colors = edgeColors[edgeType] || defaultColor;
  const rawLabel = data?.label || edgeType;
  const srcName = data?.sourceNodeName || '';
  const tgtName = data?.targetNodeName || '';
  const namePrefix = srcName && tgtName ? `${srcName} → ${tgtName}` : '';
  const label = namePrefix ? `${namePrefix} | ${rawLabel}` : rawLabel;

  // 平行边偏移：根据 parallelIndex 和 parallelCount 计算偏移量
  const parallelIndex = data?.parallelIndex ?? 0;
  const parallelCount = data?.parallelCount ?? 1;
  const offsetStep = 30; // 每条平行边偏移 30px
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

  return (
    <>
      <BaseEdge
        id={id}
        path={edgePath}
        style={{
          stroke: colors.stroke,
          strokeWidth,
          opacity: selected ? 1 : 0.8,
          filter: selected ? `drop-shadow(0 0 4px ${colors.stroke})` : undefined,
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
          }}
        >
          <div
            style={{
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
            }}
          >
            {label}
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
