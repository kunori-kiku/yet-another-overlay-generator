// 共享的「每条 edge ↔ 已编译 WG 接口」解析器（Decisions #12），供 RightPanel 与 CustomNode 共用，
// 避免两处各自实现而漂移。纯函数，不读 store；类型从 ../types/topology 引入。
//
// 关键约束（绝不通过剥离 'wg-' 前缀来反推 peerName）：
// 备份链路接口名形如 wg-<clean8><hash4>（如 wg-betaa3f2），剥前缀只会得到垃圾 chip。
// 正确做法是按「pinned 端口」把接口匹配回它所属的 edge —— 每个节点上接口的 ListenPort 唯一，
// 因此 (pinned_from_port===P && from_node_id===N) 或 (pinned_to_port===P && to_node_id===N)
// 能确定性地定位到 edge，再取「对端节点名」作为 peerName，并复用 edge.role 决定 ★/bN 标记。
// 接口命名权威在后端（docs/spec/artifacts/naming.md）；前端只消费，不重算接口名。
import type { Node, Edge } from '../types/topology';

export interface CompiledInterfaceInfo {
  interfaceName: string; // 真实接口名（如 "wg-beta" / "wg-betaa3f2"），用于 tooltip，绝不剥 'wg-'
  listenPort: number;    // 从配置正文解析出的 ListenPort
  peerName: string;      // 对端节点名（匹配到 edge 时）；未匹配时回退为接口名本身
  edgeId?: string;       // 匹配到的 edge id（未匹配则缺省）
  role: 'primary' | 'backup' | 'unknown'; // backup→'backup'；匹配到非 backup→'primary'；未匹配→'unknown'
}

// 从 WG 配置正文解析 ListenPort（端口分配权威在后端）。无法解析时返回 null（上层据此跳过该条目）。
function parseListenPort(config: string | undefined): number | null {
  if (!config) return null;
  const m = config.match(/ListenPort\s*=\s*(\d+)/);
  if (!m) return null;
  const port = parseInt(m[1], 10);
  return Number.isFinite(port) ? port : null;
}

// 按 pinned 端口把「节点 N 上监听端口 P 的接口」匹配回它所属的 edge：
//   (pinned_from_port===P && from_node_id===N) 或 (pinned_to_port===P && to_node_id===N)。
// 节点内端口唯一，因此匹配是确定性的。返回 undefined 表示该接口尚未编译 / 缺 pin / 无对应 edge。
function matchEdgeByPinnedPort(
  nodeId: string,
  listenPort: number,
  edges: Edge[]
): Edge | undefined {
  return edges.find(
    (e) =>
      (e.pinned_from_port === listenPort && e.from_node_id === nodeId) ||
      (e.pinned_to_port === listenPort && e.to_node_id === nodeId)
  );
}

// 解析某个节点上全部已编译接口为带角色的展示信息。
// 配置 key 形如 "<nodeID>:<interfaceName>"；只处理属于 nodeId 的条目。
// 优雅降级：无 ListenPort（无法解析）→ 跳过该条目；缺 pin / 无对应 edge → role:'unknown'，
// peerName 原样回退为接口名（绝不剥 'wg-'）。
export function resolveNodeInterfaces(
  nodeId: string,
  wireguardConfigs: Record<string, string>,
  nodes: Node[],
  edges: Edge[]
): CompiledInterfaceInfo[] {
  const out: CompiledInterfaceInfo[] = [];
  if (!wireguardConfigs) return out;

  for (const [key, config] of Object.entries(wireguardConfigs)) {
    const colonIdx = key.indexOf(':');
    if (colonIdx < 0) continue;
    const keyNodeId = key.slice(0, colonIdx);
    if (keyNodeId !== nodeId) continue;
    const interfaceName = key.slice(colonIdx + 1);

    const listenPort = parseListenPort(config);
    if (listenPort === null) continue; // 无法解析端口 → 跳过

    const edge = matchEdgeByPinnedPort(nodeId, listenPort, edges);
    if (!edge) {
      // 未匹配到 edge（缺 pin / 尚未编译 / 无对应 edge）：role 未知，peerName 回退为接口名。
      out.push({
        interfaceName,
        listenPort,
        peerName: interfaceName,
        role: 'unknown',
      });
      continue;
    }

    // 对端节点 = edge 的另一端（接口所在节点的对侧）。
    const otherNodeId =
      edge.from_node_id === nodeId ? edge.to_node_id : edge.from_node_id;
    const otherNode = nodes.find((n) => n.id === otherNodeId);
    const peerName = otherNode?.name || interfaceName;
    const role: CompiledInterfaceInfo['role'] =
      edge.role === 'backup' ? 'backup' : 'primary';

    out.push({
      interfaceName,
      listenPort,
      peerName,
      edgeId: edge.id,
      role,
    });
  }

  return out;
}

// 解析单条 edge 某一侧的已编译接口（RightPanel 的「每条边已编译值」面板用）。
// fromSide=true → 在 from_node_id 上找监听 pinned_from_port 的接口；
// fromSide=false → 在 to_node_id 上找监听 pinned_to_port 的接口。
// 缺 pin（尚未编译）/ 无法解析 ListenPort / 找不到对应接口 → 返回 null。
export function resolveEdgeInterface(
  edge: Edge,
  fromSide: boolean,
  wireguardConfigs: Record<string, string>
): { interfaceName: string; listenPort: number } | null {
  if (!wireguardConfigs) return null;
  const nodeId = fromSide ? edge.from_node_id : edge.to_node_id;
  const pinnedPort = fromSide ? edge.pinned_from_port : edge.pinned_to_port;
  if (pinnedPort === undefined || pinnedPort === null) return null;

  for (const [key, config] of Object.entries(wireguardConfigs)) {
    const colonIdx = key.indexOf(':');
    if (colonIdx < 0) continue;
    const keyNodeId = key.slice(0, colonIdx);
    if (keyNodeId !== nodeId) continue;

    const listenPort = parseListenPort(config);
    if (listenPort === null) continue;
    if (listenPort !== pinnedPort) continue;

    return { interfaceName: key.slice(colonIdx + 1), listenPort };
  }

  return null;
}
