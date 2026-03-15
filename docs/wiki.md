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
