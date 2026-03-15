// 前端数据模型 — 与 Go 后端 model 保持一致

export interface Topology {
  project: Project;
  domains: Domain[];
  nodes: Node[];
  edges: Edge[];
  route_policies?: RoutePolicy[];
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
}

export interface Node {
  id: string;
  name: string;
  hostname?: string;
  platform?: 'debian' | 'ubuntu';
  role: 'peer' | 'router' | 'relay' | 'gateway';
  domain_id: string;
  overlay_ip?: string;
  listen_port?: number;
  capabilities: NodeCapabilities;
  fixed_private_key?: boolean;
  wireguard_private_key?: string;
  wireguard_public_key?: string;
  public_endpoints?: PublicEndpoint[];
  extra_prefixes?: string[];
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
  endpoint_port?: number;
  priority?: number;
  weight?: number;
  transport?: 'udp' | 'tcp';
  is_enabled: boolean;
  notes?: string;
}

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
  manifest: CompileManifest;
}

export interface CompileHistoryEntry {
  id: string;
  timestamp: string;
  topology: Topology;
  compileResult: CompileResponse;
}
