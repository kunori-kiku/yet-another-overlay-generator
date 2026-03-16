# Yet Another Overlay Generator Wiki

This wiki is split into two separate language sections.

- English: product behavior + full option reference
- 中文: 功能分层介绍 + 全量参数解释

## Language Navigation
- [English Documentation](#english-documentation)
- [中文文档](#中文文档)

---

# English Documentation

## 1. Product Feature Map

### 1.1 Topology Workspace
- Visual canvas for nodes and directed edges.
- Left panel for creating and sorting domains/nodes.
- Right panel for editing selected domain/node/edge properties.
- Bottom panel for validation diagnostics.

### 1.2 Editing Workflow
- Create domains first.
- Create nodes and assign them to a domain.
- Connect nodes with directed edges.
- Compile, inspect configs, then export artifacts.

### 1.3 Drag-and-Drop Ordering
- Domains can be reordered by drag in the left panel.
- Nodes can be reordered by drag in the left panel.
- Order is persisted in local storage (through the topology store).
- Selecting an item in the list opens its editable properties in the right panel.

---

## 2. Domain Reference (All Options)

A domain represents one overlay address space.

### 2.1 Domain Fields
- Name: Display and semantic identifier.
- CIDR: Overlay subnet pool, for example `10.30.0.0/24`.
- Allocation Mode:
  - `auto`: overlay IP auto assignment.
  - `manual`: node overlay IP can be manually set.
- Routing Mode:
  - `babel`: dynamic route exchange.
  - `static`: static route strategy.
  - `none`: no routing generation.

### 2.2 Domain Editing UX
- Click a domain in the left list to edit in the right panel.
- You can edit name, CIDR, allocation mode, routing mode.
- You can drag domains to reorder them.

---

## 3. Node Reference (All Options)

A node is one machine in the overlay network.

### 3.1 Node Fields
- Node Name: Friendly name shown on canvas/list.
- Hostname (optional): OS hostname or DNS/FQDN label.
- Platform: `ubuntu` or `debian`.
- Domain: which domain this node belongs to.
- Role: `peer`, `router`, `gateway`, `relay`.
- Overlay IP (optional): manual override in manual allocation scenarios.
- Listen Port: local WireGuard UDP port.

### 3.2 Node Role Meanings
- peer: endpoint client, no transit responsibility by default.
- router: routing backbone node for multi-hop forwarding.
- relay: transit helper node for difficult NAT environments.
- gateway: bridge node for announcing extra non-overlay prefixes.

### 3.3 Capability Fields
- Publicly Reachable: whether this node is reachable from public network paths.
  - UI text was updated from “Public IP” semantics to “Publicly Reachable”.
- Can Accept Inbound: whether inbound packets can reach this node.
- Can Forward: whether node can forward traffic for others.
- Can Relay: whether node is intended as relay transit.

### 3.4 Multiple Public Endpoint Mappings
Node now supports multiple externally reachable endpoint tuples.

Each mapping includes:
- Host: public IP or DNS.
- Port: mapped public port.
- Note (optional): description, ISP label, region, etc.

Use cases:
- Multi-NAT mapping.
- Multi-ISP or multi-region publication.
- Migration windows with old and new mappings coexisting.

### 3.5 Node Editing UX
- Click a node in left list or canvas to edit in right panel.
- Add/remove/edit multiple endpoint mappings in the node section.
- Drag node items in left list to reorder.

---

## 4. Edge/Link Engine (All Options)

An edge means: source node initiates connection to target node.

### 4.1 Edge Fields
- Type:
  - `direct`: direct connectivity model.
  - `public-endpoint`: public endpoint initiation model.
  - `relay-path`: relay-oriented path declaration.
  - `candidate`: planned or candidate path.
- Endpoint Host / Port: concrete target endpoint for this link.
- Transport: `udp` or `tcp` metadata field.
- Priority / Weight: path preference metadata.
- Is Enabled: compile-time enable/disable switch.

### 4.2 New Endpoint Dropdown Behavior
When creating an edge to a target node:
- The target node's endpoint mappings are available as selectable options.
- You can pick one mapping directly from the dropdown in the right panel.
- You can still switch to manual input or clear endpoint fields.

This makes “which public IP:port to use” explicit and fast, especially for multi-mapping targets.

---

## 5. Compile, Validate, Export
- Validate checks semantic and schema consistency.
- Compile generates WireGuard/Babel/sysctl/install outputs.
- Export creates installable artifacts.

---

# 中文文档

## 1. 功能总览（按功能分类）

### 1.1 拓扑工作区
- 中央画布：可视化节点与有向连线。
- 左侧面板：创建并排序网域、节点。
- 右侧面板：编辑当前选中的网域/节点/连线属性。
- 底部面板：校验结果与错误告警。

### 1.2 标准操作流
- 先建网域。
- 再建节点并分配到网域。
- 再连线形成拓扑。
- 最后校验、编译、导出。

### 1.3 拖动排序能力（新增）
- 网域支持左侧列表拖动排序。
- 节点支持左侧列表拖动排序。
- 顺序变更会持久化。
- 点击列表项即可在右侧进入编辑。

---

## 2. 网域（Domain）参数全解

网域是一个 Overlay 地址池，决定该域可分配 IP 的范围。

### 2.1 字段说明
- 名称（Name）：显示名与逻辑标识。
- CIDR：网段范围，如 `10.30.0.0/24`。
- 分配模式（Allocation Mode）：
  - `auto`：自动分配 Overlay IP。
  - `manual`：允许手工控制节点 Overlay IP。
- 路由模式（Routing Mode）：
  - `babel`：动态路由。
  - `static`：静态路由。
  - `none`：不生成路由。

### 2.2 编辑方式
- 左侧点击某个网域，右侧即可编辑。
- 可编辑名称、CIDR、分配模式、路由模式。
- 可拖动调整网域顺序。

---

## 3. 节点（Node）参数全解

节点代表一台机器（云主机/物理机/容器宿主）。

### 3.1 基础字段
- 节点名称：画布与列表显示名。
- 主机名（可选）：真实 hostname 或域名标签。
- 平台：`ubuntu` / `debian`。
- 所属网域：节点归属的 Domain。
- 角色：`peer` / `router` / `gateway` / `relay`。
- Overlay IP（可选）：手工指定时使用。
- 监听端口：WireGuard 本地监听 UDP 端口。

### 3.2 角色差异
- peer：终端节点，默认不承担转发。
- router：骨干转发节点，适合多跳网络。
- relay：中继转发节点，适合复杂 NAT 场景。
- gateway：网关节点，用于桥接额外网段。

### 3.3 能力字段
- 公网可达（新增命名）：
  - 已将“公网IP”语义统一为“公网可达”。
  - 表示这个节点可被外部路径访问。
- 可入站：外部是否能打进来。
- 可转发：是否可转发他人流量。
- 可中继：是否作为中继角色运行。

### 3.4 多公网映射（新增）
节点现在支持配置多组 `公网IP:端口`（也可为域名:端口）。

每组映射包含：
- Host：公网 IP 或域名。
- Port：公网端口。
- Note（可选）：备注，例如“电信出口A”“东京入口”。

典型场景：
- 同一节点有多条公网出口。
- NAT 多重映射需要按场景切换。
- 新旧映射并存，平滑迁移。

### 3.5 编辑方式
- 点左侧节点列表，或点画布节点，右侧编辑。
- 右侧可新增/编辑/删除多组公网映射。
- 左侧节点支持拖动改顺序。

---

## 4. 连线引擎（Edge）参数全解

有向连线 `A -> B` 的含义：A 主动去连 B。

### 4.1 连线字段
- 类型（Type）：
  - `direct`：直连模型。
  - `public-endpoint`：公网端点连接模型。
  - `relay-path`：中继路径模型。
  - `candidate`：候选路径模型。
- Endpoint Host / Port：该连线使用的目标端点。
- Transport：`udp` / `tcp` 元数据。
- Priority / Weight：路径偏好权重元数据。
- Is Enabled：该连线是否参与编译。

### 4.2 端点下拉选择（新增）
当连线指向某个节点后：
- 右侧连线属性会出现“目标节点公网映射”下拉。
- 下拉数据来自目标节点配置的多组 `Host:Port`。
- 可直接选某一组映射作为该连线 endpoint。
- 也可切换回手工输入，或清空 endpoint。

这使“到底用哪个公网 IP:端口去访问目标节点”变成显式、可控、可复用的配置动作。

---

## 5. 校验、编译与导出
- 校验：检查拓扑结构和语义错误。
- 编译：生成 WireGuard/Babel/sysctl/install 配置。
- 导出：打包为可部署产物。

---

# Backend Architecture / 后端架构

## 6. Architecture Overview / 架构总览

### English

The system follows a **Design → Compile → Deploy** pipeline:

```text
[Web Frontend / CLI]
        │  Topology JSON
        ▼
[Compiler]
  ├─ Schema Validator
  ├─ Semantic Validator
  ├─ IP Allocator
  ├─ Peer Deriver
  └─ Renderers
        │  ├─ WireGuard configs
        │  ├─ Babel configs
        │  ├─ sysctl configs
        │  └─ Install scripts
        ▼
[Artifact Exporter]
        │  Per-node deployment packages
        ▼
[Target Hosts]
        └─ Run install.sh → network comes up
```

Key design principles:
- **Topology is source code**: The JSON topology is the single source of truth. All configs are derived from it deterministically.
- **Offline compilation**: All keys and configs are generated on a trusted local machine. No online control plane needed.
- **Idempotent deployment**: Install scripts can be re-run safely on target hosts.

### 中文

系统遵循**设计 → 编译 → 部署**的流水线架构：

- **设计层**：用户通过 Web 前端或 CLI 定义拓扑（节点、网域、连线），输出标准 Topology JSON。
- **编译层**：后端编译器接收 JSON，经过校验、IP 分配、Peer 推导、配置渲染，生成每节点的完整部署包。
- **部署层**：操作者手动将产物分发到目标主机，执行 `install.sh` 即可上线。

核心原则：
- 拓扑 JSON 是唯一真相源，所有配置均由此确定性推导。
- 编译在本地可信主机完成，不依赖在线控制面。
- 安装脚本幂等可重复执行。

---

## 7. Per-Peer WireGuard Interface Model / Per-Peer WireGuard 接口模型

### English

#### Why not a single `wg0` with multiple peers?

The traditional WireGuard setup uses one interface (`wg0`) with multiple `[Peer]` sections. This works for static routing but **breaks with Babel dynamic routing**:

- Babel needs **one interface per neighbor** to track link quality, hello timers, and route metrics independently.
- A single `wg0` with multiple peers appears as one broadcast domain to Babel, preventing per-link metric differentiation.
- `AllowedIPs` conflicts arise when multiple peers need overlapping address ranges.

#### Per-peer interface design

Each WireGuard peer connection gets its own dedicated interface:

```
Node alpha:
  wg-node-beta   ← tunnel to beta  (port 51820)
  wg-node-gamma  ← tunnel to gamma (port 51821)
  dummy0         ← stable overlay address
```

Each interface has:
- Its own private key (same key reused across all interfaces on the same node)
- Its own listen port (base port + offset per peer)
- Its own transit IP address (point-to-point /32)
- Its own IPv6 link-local address (for Babel neighbor discovery)
- Exactly one `[Peer]` section
- `Table = off` to prevent `wg-quick` from adding conflicting routes
- `AllowedIPs = 0.0.0.0/0, ::/0` (safe because each interface has only one peer, and routing is delegated to Babel)

#### Interface naming

Format: `wg-<peername>`, with Linux's 15-character limit enforced:
- Lowercase conversion: `Alpha` → `wg-alpha`
- Special characters replaced with `-`: `my_server` → `wg-my-server`
- Truncation at 15 chars: `wg-abcdefghijklmnop` → `wg-abcdefghijkl`

### 中文

#### 为什么不用单个 wg0 + 多 Peer？

传统 WireGuard 部署使用一个接口（`wg0`）配置多个 `[Peer]`。这在静态路由下可行，但**与 Babel 动态路由不兼容**：

- Babel 需要**每个邻居一个独立接口**，才能独立跟踪链路质量、hello 计时器和路由 metric。
- 单个 `wg0` 多 peer 在 Babel 看来是一个广播域，无法区分各链路的质量。
- 多个 peer 的 `AllowedIPs` 容易产生地址范围冲突。

#### Per-peer 接口设计

每条 WireGuard peer 连接使用独立的接口：

- 每个接口有独立的监听端口（基础端口 + 偏移量）
- 每个接口有独立的 transit IP（点对点 /32 地址）
- 每个接口有独立的 IPv6 link-local 地址（Babel 邻居发现用）
- 每个接口只有一个 `[Peer]` 段
- 设置 `Table = off` 防止 `wg-quick` 添加冲突路由
- `AllowedIPs = 0.0.0.0/0, ::/0`（安全，因为每接口仅一个 peer，路由由 Babel 管理）

#### 接口命名规则

格式：`wg-<对端名称>`，遵循 Linux 15 字符限制：
- 大写转小写：`Alpha` → `wg-alpha`
- 特殊字符替换为 `-`：`my_server` → `wg-my-server`
- 超过 15 字符截断

---

## 8. Two-Layer Address Scheme / 两层地址分离

### English

The system uses two separate IP address pools to avoid conflicts between point-to-point link addressing and stable node identity:

#### Overlay IP (Business Address)

- **Pool**: Defined per Domain (e.g., `10.11.0.0/24`)
- **Assigned to**: `dummy0` interface on each node
- **Purpose**: Stable, routable identity address for the node
- **Allocation**: Automatic (sequential from CIDR pool) or manual override
- **Announced via**: Babel `redistribute local ip <overlay_ip>/32 allow`

Example: Node alpha gets `10.11.0.1/32` on `dummy0`, which remains stable regardless of which peer tunnels are up or down.

#### Transit IP (Link Address)

- **Pool**: `10.10.0.0/24` (hardcoded, shared across all links)
- **Assigned to**: Each per-peer WireGuard interface (`Address = 10.10.0.x/32`)
- **Purpose**: Point-to-point addressing for each WireGuard tunnel
- **Allocation**: Automatic, sequential pairs per link:
  - Link 0: `10.10.0.1` ↔ `10.10.0.2`
  - Link 1: `10.10.0.3` ↔ `10.10.0.4`
  - Link N: `10.10.0.(2N+1)` ↔ `10.10.0.(2N+2)`

#### IPv6 Link-Local (Babel Neighbor Discovery)

- **Added via**: `PostUp = ip -6 addr add fe80::X/64 dev %i`
- **Purpose**: Babel uses IPv6 link-local for neighbor discovery on each tunnel interface
- **Allocation**: Sequential pairs matching transit IP index:
  - Link 0: `fe80::1` ↔ `fe80::2`
  - Link 1: `fe80::3` ↔ `fe80::4`

#### Why two layers?

| Concern | Overlay IP | Transit IP |
|---------|-----------|------------|
| Stability | ✅ Never changes | ❌ Changes with topology |
| Scope | Global (reachable everywhere) | Local (per-link only) |
| Used by | Applications, DNS, monitoring | WireGuard tunnel plumbing |
| Announced by Babel | Yes (`redistribute local`) | No (internal) |

Without separation, adding/removing a peer link would change the node's "identity" address, breaking DNS records, monitoring, and application configs.

### 中文

系统使用两个独立的 IP 地址池，避免点对点链路地址与节点身份地址冲突：

#### Overlay IP（业务地址）

- **地址池**：每个 Domain 定义（如 `10.11.0.0/24`）
- **分配到**：每个节点的 `dummy0` 接口
- **用途**：节点的稳定、可路由身份地址
- **分配方式**：从 CIDR 池自动顺序分配，或手动指定
- **通告方式**：通过 Babel `redistribute local ip <overlay_ip>/32 allow`

#### Transit IP（链路地址）

- **地址池**：`10.10.0.0/24`（全局共用）
- **分配到**：每个 per-peer WireGuard 接口
- **用途**：每条隧道的点对点寻址
- **分配方式**：按链路自动分配一对地址

#### 为什么需要两层分离？

- Overlay IP 是节点的"身份证"，不随链路变化而改变，DNS、监控、应用都依赖它。
- Transit IP 是隧道的"门牌号"，随拓扑变化而变化，仅用于 WireGuard 内部寻址。
- 如果混用，增删一条 peer 链路就会改变节点的"身份"地址，导致连锁故障。

---

## 9. Compilation Pipeline / 编译流水线

### English

The compiler (`internal/compiler/compiler.go`) processes topology in multiple passes:

#### Pass 1: Schema Validation

Validates structural correctness of the topology JSON:
- Required fields present (project, domains, nodes, edges)
- Field types correct
- References valid (node's `domain_id` points to existing domain)

#### Pass 2: Semantic Validation

Checks logical consistency:
- No duplicate overlay IPs
- No orphan nodes (nodes without any edges)
- Edge references point to valid nodes
- CIDR ranges are valid and non-overlapping

#### Pass 3: IP Allocation + Peer Derivation

**IP Allocation** (`internal/allocator/ip.go`):
- For each node without a manual `overlay_ip`, allocates the next available IP from its domain's CIDR pool
- Skips network address, broadcast address, reserved ranges, and already-used IPs

**Capability Inference** (`internal/compiler/roles.go`):
- Derives node capabilities from role (e.g., `router` → `can_forward=true`)

**Peer Derivation** (`internal/compiler/peers.go`):
- Processes edges to generate `PeerInfo` for each node pair (see next section for details)

#### Pass 4: Config Rendering

Four independent renderers generate configs from the compiled topology:

| Renderer | Output | Key Template |
|----------|--------|-------------|
| `wireguard.go` | Per-peer `.conf` files | Interface + single Peer section |
| `babel.go` | `babeld.conf` per node | router-id, interfaces, redistribute rules |
| `sysctl.go` | `99-overlay.conf` | IP forwarding, rp_filter |
| `script.go` | `install.sh` per node | 3-phase install script |

#### Pass 5: Artifact Export

`internal/artifacts/export.go` organizes everything into per-node directories:

```
output/
  node-alpha/
    wireguard/wg-node-beta.conf
    wireguard/wg-node-gamma.conf
    babel/babeld.conf
    sysctl/99-overlay.conf
    install.sh
    manifest.json
    checksums.sha256
    README.txt
```

### 中文

编译器（`internal/compiler/compiler.go`）按多个阶段处理拓扑：

#### Pass 1：Schema 校验

校验 JSON 结构的正确性：必填字段是否存在、字段类型是否正确、引用是否有效。

#### Pass 2：语义校验

检查逻辑一致性：IP 是否重复、节点是否孤立、边的引用是否有效、CIDR 是否合法。

#### Pass 3：IP 分配 + Peer 推导

- **IP 分配器**：为没有手动 IP 的节点从 Domain CIDR 池中顺序分配地址
- **能力推导**：根据节点角色推导能力字段
- **Peer 推导**：处理 Edge 生成每对节点的 PeerInfo（详见下节）

#### Pass 4：配置渲染

四个独立渲染器从编译后的拓扑生成配置：
- `wireguard.go`：每 peer 一个 `.conf` 文件
- `babel.go`：每节点一份 `babeld.conf`
- `sysctl.go`：内核参数配置
- `script.go`：三阶段安装脚本

#### Pass 5：产物导出

将所有文件组织为每节点独立的目录结构，包含 WireGuard 配置、Babel 配置、sysctl 配置、安装脚本、manifest 和 checksums。

---

## 10. Peer Derivation Logic / Peer 推导逻辑

### English

The peer deriver (`internal/compiler/peers.go`) transforms topology edges into concrete WireGuard peer configurations. This is the most complex part of the compiler.

#### Input → Output

- **Input**: Topology (nodes + edges) + KeyPairs
- **Output**: `map[nodeID][]PeerInfo` — for each node, a list of peer interface configs

#### Edge Processing Rules

1. **Iterate all enabled edges** in order
2. **Deduplication**: If a node pair `A→B` has already been processed, skip subsequent edges for the same pair
3. **For each new pair**, generate:
   - A forward peer (from the edge's `from_node` perspective)
   - An automatic reverse peer (for the edge's `to_node`)

#### Endpoint Resolution

- **Forward peer**: Uses the edge's `endpoint_host:endpoint_port` directly
- **Reverse peer**: Looks up if a reverse edge (`B→A`) exists in the topology; if found, uses its `endpoint_host:endpoint_port`. If no reverse edge exists, the reverse peer has no endpoint (it relies on the forward side to initiate)

Example with bidirectional edges:
```
Edge: node-1 → node-2, endpoint=203.0.113.2:51820
Edge: node-2 → node-1, endpoint=203.0.113.1:51820

Result:
  node-1's peer config: Endpoint = 203.0.113.2:51820
  node-2's peer config: Endpoint = 203.0.113.1:51820  ← from reverse edge lookup
```

#### PersistentKeepalive Rules

The keepalive decision follows this logic:

| Condition | Keepalive |
|-----------|-----------|
| Node can accept inbound AND reverse edge exists | 0 (disabled) |
| Node cannot accept inbound (NAT) | 25 seconds |
| No reverse edge (unidirectional) | 25 seconds |

This ensures NAT-behind nodes and unidirectional connections maintain the tunnel by sending periodic keepalive packets.

#### Transit IP Allocation

Each node pair gets a unique transit IP pair from `10.10.0.0/24`:

```
Pair index 0: 10.10.0.1 ↔ 10.10.0.2
Pair index 1: 10.10.0.3 ↔ 10.10.0.4
Pair index N: 10.10.0.(2N+1) ↔ 10.10.0.(2N+2)
```

The forward peer gets the odd address, the reverse peer gets the even address.

#### IPv6 Link-Local Allocation

Similarly, each pair gets link-local addresses for Babel:

```
Pair index 0: fe80::1 ↔ fe80::2
Pair index 1: fe80::3 ↔ fe80::4
```

These are added via `PostUp`/`PostDown` in the WireGuard config.

#### Listen Port Allocation

Each node starts with its configured `listen_port` (default 51820) and increments for each additional peer interface:

```
Node alpha (base port 51820):
  wg-node-beta:  port 51820
  wg-node-gamma: port 51821
```

### 中文

Peer 推导器（`internal/compiler/peers.go`）将拓扑中的 Edge 转换为具体的 WireGuard Peer 配置，是编译器中最复杂的部分。

#### Edge 处理规则

1. 按顺序遍历所有启用的 edge
2. 去重：如果某对节点 `A→B` 已处理过，跳过后续同对的 edge
3. 每对新节点生成正向 peer 和自动反向 peer

#### Endpoint 解析

- **正向 peer**：直接使用 edge 的 `endpoint_host:endpoint_port`
- **反向 peer**：查找是否存在反向 edge（`B→A`），如有则使用其 endpoint；如无则反向 peer 没有 endpoint（依赖正向侧发起连接）

#### PersistentKeepalive 判定规则

| 条件 | Keepalive |
|------|-----------|
| 节点可入站 且 存在反向 edge | 0（不启用） |
| 节点不可入站（NAT 后） | 25 秒 |
| 无反向 edge（单向连接） | 25 秒 |

#### Transit IP 分配

每对节点从 `10.10.0.0/24` 分配一对唯一地址，正向 peer 得奇数地址，反向 peer 得偶数地址。

#### 监听端口分配

每个节点从配置的 `listen_port`（默认 51820）开始，每增加一个 peer 接口端口递增 1。

---

## 11. Babel Routing Integration / Babel 路由集成

### English

Babel is the dynamic routing daemon that makes multi-hop overlay networks work. The system generates `babeld.conf` for each node that participates in Babel routing.

#### When does a node run Babel?

A node runs Babel when its domain's `routing_mode` is set to `"babel"`. All nodes in a Babel-enabled domain get a Babel config, regardless of role.

#### Router-ID Generation

Each Babel node needs a unique, stable `router-id` in MAC-48 format (e.g., `36:97:1b:e6:e9:bb`).

Generation algorithm:
1. Compute `SHA-256(node_id)` — e.g., `SHA-256("node-1")`
2. Take the first 6 bytes as a MAC-48 address
3. Set the "locally administered" bit (`| 0x02`) and clear the "multicast" bit (`& 0xFE`) on byte 0

This ensures:
- **Stability**: Same node always gets the same router-id
- **Uniqueness**: SHA-256 provides excellent distribution across all nodes
- **Standards compliance**: The locally-administered bit marks it as a non-hardware MAC

Users can also manually set `router_id` on a node to override the auto-generated value.

#### Interface Declaration

Each per-peer WireGuard interface is declared as a Babel tunnel interface:

```
interface wg-node-beta type tunnel hello-interval 4 update-interval 16
interface wg-node-gamma type tunnel hello-interval 4 update-interval 16
```

- `type tunnel`: Tells Babel this is a point-to-point tunnel (not wired/wireless)
- `hello-interval 4`: Send hello packets every 4 seconds
- `update-interval 16`: Send full route updates every 16 seconds

#### Route Redistribution

Babel needs to know which local prefixes to announce to neighbors. The redistribution strategy depends on the node's role:

| Role | Announces |
|------|-----------|
| `peer` | Own overlay IP (`10.11.0.x/32`) |
| `router` | Own overlay IP + domain CIDR |
| `relay` | Own overlay IP + domain CIDR |
| `gateway` | Own overlay IP + domain CIDR + extra prefixes + default route (`0.0.0.0/0`) |

Generated redistribution rules:
```
redistribute local ip 10.11.0.1/32 allow    # own overlay IP
redistribute local ip 10.11.0.0/24 allow     # domain CIDR (routers)
redistribute local deny                       # deny everything else
```

The `redistribute local deny` at the end is critical — it prevents Babel from accidentally announcing the transit IP pool or system routes.

#### Role-Based Babel Presets

Each role has tuned Babel parameters (`internal/renderer/babel_presets.go`):

| Role | Default Cost | Notes |
|------|-------------|-------|
| `peer` | 0 (default) | Minimal participation |
| `router` | 0 (default) | Standard backbone |
| `relay` | 96 | Higher cost to prefer direct paths |
| `gateway` | 0 (default) | Announces external prefixes |

#### Global Babel Settings

```
local-port 33123         # Babel management port
skip-kernel-setup false   # Let Babel manage kernel routes
```

- `skip-kernel-setup false`: Babel will install/remove routes in the kernel routing table, which is the whole point of dynamic routing.

### 中文

Babel 是使多跳 overlay 网络运转的动态路由守护程序。系统为每个参与 Babel 路由的节点生成 `babeld.conf`。

#### 何时运行 Babel？

当节点所属 Domain 的 `routing_mode` 设为 `"babel"` 时，该域内所有节点都会生成 Babel 配置。

#### Router-ID 生成

每个 Babel 节点需要一个唯一、稳定的 `router-id`（MAC-48 格式）。

生成算法：
1. 计算 `SHA-256(node_id)`
2. 取前 6 字节作为 MAC-48 地址
3. 设置"本地管理"位（`| 0x02`），清除"组播"位（`& 0xFE`）

保证稳定性（同节点同 ID）、唯一性（SHA-256 分布均匀）和标准合规性。用户也可手动指定 `router_id` 覆盖自动值。

#### 接口声明

每个 per-peer WireGuard 接口声明为 Babel tunnel 接口，配置 `hello-interval 4`（每 4 秒发送 hello）和 `update-interval 16`（每 16 秒发送完整路由更新）。

#### 路由重分发

重分发策略取决于节点角色：
- **peer**：仅通告自身 overlay IP
- **router**：通告自身 overlay IP + Domain CIDR
- **relay**：通告自身 overlay IP + Domain CIDR（默认 cost 96，优先直连路径）
- **gateway**：通告自身 overlay IP + Domain CIDR + 额外前缀 + 默认路由

末尾的 `redistribute local deny` 至关重要——防止 Babel 意外通告 transit IP 池或系统路由。

#### 全局设置

- `local-port 33123`：Babel 管理端口
- `skip-kernel-setup false`：让 Babel 管理内核路由表

---

## 12. Generated Artifacts Structure / 产物结构

### English

Each node's deployment package contains everything needed to bring the node online.

#### Directory Structure

```
node-alpha/
  ├── wireguard/
  │   ├── wg-node-beta.conf      # WireGuard config for tunnel to beta
  │   └── wg-node-gamma.conf     # WireGuard config for tunnel to gamma
  ├── babel/
  │   └── babeld.conf            # Babel routing daemon config
  ├── sysctl/
  │   └── 99-overlay.conf        # Kernel parameters (forwarding, rp_filter)
  ├── install.sh                 # One-click install script
  ├── manifest.json              # Build metadata and file list
  ├── checksums.sha256           # SHA-256 checksums for integrity verification
  └── README.txt                 # Quick-start instructions
```

#### WireGuard Config Example

```ini
# WireGuard per-peer interface: wg-node-beta
# Node: node-alpha -> Peer: node-beta

[Interface]
PrivateKey = <private_key>
Address = 10.10.0.1/32
Table = off
ListenPort = 51820

# Add IPv6 link-local for Babel
PostUp = ip -6 addr add fe80::1/64 dev %i 2>/dev/null || true
PostDown = ip -6 addr del fe80::1/64 dev %i 2>/dev/null || true

# Peer: node-beta
[Peer]
PublicKey = <public_key>
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 203.0.113.2:51820
```

Key design points:
- **`Table = off`**: Prevents `wg-quick` from adding kernel routes. Since `AllowedIPs = 0.0.0.0/0`, without `Table = off` every interface would try to add a default route, causing conflicts. Routing is fully delegated to Babel.
- **`AllowedIPs = 0.0.0.0/0, ::/0`**: Safe in per-peer model because each interface has exactly one peer. This allows any traffic to flow through the tunnel; Babel decides which tunnel to use for each destination.
- **`PostUp`/`PostDown`**: Adds IPv6 link-local address needed for Babel neighbor discovery on this interface.

#### Install Script: Three-Phase Logic

The install script (`install.sh`) follows an idempotent three-phase approach:

**Phase 0: Cleanup**
- Stops and removes all existing WireGuard interfaces managed by this overlay
- Cleans up legacy `wg0` config if present
- Stops and disables Babel daemon
- Removes old sysctl configs

**Phase 1: Environment Preparation**
- Verifies checksums for file integrity
- Checks root privilege
- Detects OS (Debian / Ubuntu)
- Installs dependencies (`wireguard`, `wireguard-tools`, `babeld`)
- Creates `dummy0` interface with overlay IP
- Installs systemd service for persistent `dummy0`

**Phase 2: Deploy Configuration**
- Copies WireGuard configs to `/etc/wireguard/`
- Copies Babel config to `/etc/babel/`
- Copies sysctl config to `/etc/sysctl.d/`

**Phase 3: Activate and Verify**
- Applies sysctl settings
- Starts all `wg-quick@<interface>` services
- Configures babeld systemd override (depends on all WireGuard interfaces)
- Starts and enables babeld
- Displays status summary

#### The dummy0 + Table=off Design

This combination is the key to making per-peer interfaces work with Babel:

```
                 ┌─────────────────────────────────────────┐
                 │              Node alpha                   │
                 │                                           │
                 │  dummy0: 10.11.0.1/32  ← Overlay IP      │
                 │  (stable, announced by Babel)             │
                 │                                           │
                 │  wg-node-beta:  10.10.0.1/32 (Table=off) │
                 │  wg-node-gamma: 10.10.0.3/32 (Table=off) │
                 │                                           │
                 │  Babel manages ALL routing decisions       │
                 │  - Learns routes from neighbors            │
                 │  - Installs routes in kernel table          │
                 │  - Handles failover automatically           │
                 └─────────────────────────────────────────┘
```

- `dummy0` provides a stable address that Babel announces — applications and DNS always point here.
- `Table = off` on each WireGuard interface means `wg-quick` doesn't touch the routing table.
- Babel sees each `wg-*` interface as an independent tunnel link and tracks its reachability independently.
- If a link goes down, Babel automatically reroutes traffic through surviving links — no static route reconfiguration needed.

### 中文

每个节点的部署包包含上线所需的全部文件。

#### 目录结构

每个节点目录包含：`wireguard/`（per-peer 配置）、`babel/`（路由配置）、`sysctl/`（内核参数）、`install.sh`（一键安装脚本）、`manifest.json`（构建元信息）、`checksums.sha256`（完整性校验）。

#### WireGuard 配置要点

- **`Table = off`**：阻止 `wg-quick` 添加内核路由。由于 `AllowedIPs = 0.0.0.0/0`，不加此选项每个接口都会尝试添加默认路由、相互冲突。路由完全交给 Babel。
- **`AllowedIPs = 0.0.0.0/0, ::/0`**：在 per-peer 模型中是安全的，因为每个接口只有一个 peer。允许任何流量通过隧道，由 Babel 决定使用哪条隧道到达目的地。
- **`PostUp`/`PostDown`**：添加 Babel 邻居发现所需的 IPv6 link-local 地址。

#### 安装脚本：三阶段逻辑

- **Phase 0 清理**：停止并移除现有的 WireGuard 接口和旧配置，清理遗留的 `wg0` 配置，停止 Babel。
- **Phase 1 环境准备**：校验文件完整性、检查 root 权限、检测 OS、安装依赖包、创建 `dummy0` 接口并分配 overlay IP、安装 systemd 持久化服务。
- **Phase 2 部署配置**：将 WireGuard、Babel、sysctl 配置复制到系统目录。
- **Phase 3 激活验证**：应用 sysctl、启动所有 WireGuard 接口、配置并启动 babeld（依赖所有 WireGuard 服务）、显示状态摘要。

#### dummy0 + Table=off 设计

这个组合是 per-peer 接口与 Babel 配合的关键：
- `dummy0` 提供 Babel 通告的稳定地址——应用和 DNS 始终指向这里。
- 每个 WireGuard 接口的 `Table = off` 意味着 `wg-quick` 不触碰路由表。
- Babel 将每个 `wg-*` 接口视为独立的隧道链路，独立跟踪可达性。
- 如果某条链路故障，Babel 自动通过存活的链路重新路由——无需手动调整静态路由。
