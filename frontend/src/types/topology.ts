// 前端数据模型 — 与 Go 后端 model 保持一致

export interface Topology {
  project: Project;
  domains: Domain[];
  nodes: Node[];
  edges: Edge[];
  route_policies?: RoutePolicy[];
  // 分配方案版本号：由编译器写入并原样回传，使将来 pin 格式变更可以检测/迁移旧分配。
  // 参见 docs/spec/compiler/allocation-stability.md（不变量 I10）。
  alloc_schema_version?: number;
}

export interface Project {
  id: string;
  name: string;
  description?: string;
  version?: string;
}

export interface Domain {
  id: string;
  name: string;
  cidr: string;
  description?: string;
  allocation_mode: 'auto' | 'manual';
  routing_mode: 'static' | 'babel' | 'none';
  reserved_ranges?: string[];
  // 每个 domain 的 transit 地址池，缺省 10.10.0.0/24；transit IP 现按 CIDR 逐域分配。
  // 对应 Go 模型的 Domain.TransitCIDR；参见 docs/spec/api/wire-contract.md（Domain 字段表）。
  transit_cidr?: string;
}

export interface Node {
  id: string;
  name: string;
  hostname?: string;
  platform?: 'debian' | 'ubuntu';
  role: 'peer' | 'router' | 'relay' | 'gateway' | 'client';
  domain_id: string;
  overlay_ip?: string;
  listen_port?: number;
  mtu?: number;
  capabilities: NodeCapabilities;
  fixed_private_key?: boolean;
  wireguard_private_key?: string;
  wireguard_public_key?: string;
  public_endpoints?: PublicEndpoint[];
  extra_prefixes?: string[];
  // SSH connection details (for auto-deploy)
  ssh_alias?: string;
  ssh_host?: string;
  ssh_port?: number;
  ssh_user?: string;
  ssh_key_path?: string;
}

export interface PublicEndpoint {
  id: string;
  host: string;
  port: number;
  note?: string;
}

export interface NodeCapabilities {
  can_accept_inbound: boolean;
  can_forward: boolean;
  can_relay: boolean;
  has_public_ip: boolean;
}

export interface Edge {
  id: string;
  from_node_id: string;
  to_node_id: string;
  type: 'direct' | 'public-endpoint' | 'relay-path' | 'candidate';
  endpoint_host?: string;
  endpoint_port?: number;  // user input: 0 = auto, nonzero = NAT override
  compiled_port?: number;  // read-only: actual port set by compiler
  priority?: number;
  weight?: number;
  // 平行链路的链路角色：'backup' 自成一条独立链路（独立 WG 接口/端口/transit/链路本地地址），
  // 无角色或 'primary' 归入「主链路类」（同一节点对的 A→B + B→A 仍合并为单条双向隧道）。
  // 参见 docs/spec/data-model/edge.md（§Parallel links）。
  role?: 'primary' | 'backup';
  transport?: 'udp' | 'tcp';
  is_enabled: boolean;
  notes?: string;
  // 分配 pin：由编译器写入，并原样回传，使重新编译时保留既有分配（端口 / transit IP /
  // 链路本地地址），从而新增节点不会扰动既有链路。参见
  // docs/spec/compiler/allocation-stability.md。
  pinned_from_port?: number;
  pinned_to_port?: number;
  pinned_from_transit_ip?: string;
  pinned_to_transit_ip?: string;
  pinned_from_link_local?: string;
  pinned_to_link_local?: string;
}

// 保留特性（RESERVED）：route_policies 目前未接入任何 renderer，语义校验会拒绝非空数组。
// 该类型仅为线缆（wire）兼容保留——请勿基于它构建功能。
// 参见 docs/spec/api/wire-contract.md（“route_policies is RESERVED”）。
export interface RoutePolicy {
  id: string;
  domain_id: string;
  destination_cidr: string;
  next_hop_node_id?: string;
  metric?: number;
  notes?: string;
  source_selector?: string;
  action?: 'allow' | 'deny' | 'metric-override';
  apply_to_node_id?: string;
}

// API 响应类型
export interface ValidationError {
  field: string;
  message: string;
  level: 'error' | 'warning';
}

export interface ValidateResponse {
  valid: boolean;
  errors?: ValidationError[];
  warnings?: ValidationError[];
}

export interface CompileManifest {
  project_id: string;
  project_name: string;
  version: string;
  compiled_at: string;
  node_count: number;
  checksum: string;
}

export interface CompileResponse {
  topology: Topology;
  wireguard_configs: Record<string, string>;
  babel_configs: Record<string, string>;
  sysctl_configs: Record<string, string>;
  install_scripts: Record<string, string>;
  deploy_scripts: Record<string, string>;
  manifest: CompileManifest;
  // 非致命提示（双重 NAT、缺少端点的边、孤立节点等）。
  // 编译路径会运行语义校验并把这些 warning 一并返回，UI 在编译成功后展示。
  warnings?: ValidationError[];
}

export interface CompileHistoryEntry {
  id: string;
  timestamp: string;
  topology: Topology;
  compileResult: CompileResponse;
}
