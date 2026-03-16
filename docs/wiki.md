# Yet Another Overlay Generator Wiki

## 1. 项目简介

Yet Another Overlay Generator 是一个基于 Web 的交互式组网设计与配置生成系统。用户通过图形化拓扑界面定义节点、网络域和可达关系，系统自动分配地址、生成 WireGuard + Babel 配置文件及一键安装脚本。

### 设计哲学

系统遵循**设计 → 编译 → 部署**三层架构：

```text
[Web 前端 / CLI]
        │  Topology JSON
        ▼
[编译器]
  ├─ Schema 校验
  ├─ 语义校验
  ├─ IP 分配器
  ├─ Peer 推导器
  └─ 配置渲染器
        │  ├─ WireGuard 配置
        │  ├─ Babel 配置
        │  ├─ sysctl 内核参数
        │  └─ 安装脚本
        ▼
[产物导出器]
        │  每节点部署包
        ▼
[目标主机]
        └─ 执行 install.sh → 网络上线
```

核心原则：
- **拓扑即代码**：JSON 拓扑是唯一真相源，所有配置确定性推导。
- **离线编译**：密钥和配置在本地可信主机生成，不依赖在线控制面。
- **幂等部署**：安装脚本可安全重复执行。

---

## 2. 核心概念

### 2.1 网域（Domain）

网域是一个 Overlay 地址空间，定义了可分配 IP 的范围。

| 字段 | 说明 |
|------|------|
| 名称 | 显示名与逻辑标识 |
| CIDR | 网段范围，如 `10.11.0.0/24` |
| 分配模式 | `auto`（自动分配）/ `manual`（手工指定） |
| 路由模式 | `babel`（动态路由）/ `static`（静态路由）/ `none`（不生成） |

### 2.2 节点（Node）与角色

节点代表一台机器（云主机、物理机、容器宿主）。

**基础字段：**
- 节点名称、主机名（可选）、平台（`debian` / `ubuntu`）
- 所属网域、Overlay IP（可选手动指定）
- WireGuard 监听端口（默认 51820）、MTU（可选）

**角色与能力：**

| 角色 | 转发 | 中继 | Babel 通告 | 典型用途 |
|------|------|------|-----------|---------|
| `peer` | ✗ | ✗ | 仅自身 IP | 终端客户端 |
| `router` | ✓ | ✗ | 自身 IP + Domain CIDR | 骨干转发节点 |
| `relay` | ✓ | ✓ | 自身 IP + Domain CIDR（cost 96） | NAT 场景中继 |
| `gateway` | ✓ | ✗ | 自身 IP + Domain CIDR + 额外前缀 + 默认路由 | 桥接外部网段 |

**能力字段：**
- 公网可达：节点是否可被外部路径访问
- 可入站：外部流量能否到达此节点
- 可转发：是否可转发他人流量
- 可中继：是否作为中继角色运行

**多公网映射：** 节点支持配置多组 `Host:Port` 公网端点（支持域名），用于多出口、多 ISP、NAT 多重映射等场景。

### 2.3 连线（Edge）与有向语义

有向连线 `A → B` 的含义：**A 主动去连 B**。

| 字段 | 说明 |
|------|------|
| 类型 | `direct`（直连）/ `public-endpoint`（公网端点）/ `relay-path`（中继路径）/ `candidate`（候选） |
| Endpoint | 目标 Host:Port，可从目标节点的公网映射下拉选择 |
| Transport | `udp` / `tcp` 元数据 |
| Priority / Weight | 路径偏好权重 |
| Is Enabled | 该连线是否参与编译 |

### 2.4 两层地址分离

系统使用两个独立的 IP 地址池，避免链路地址与节点身份地址冲突：

| | Overlay IP（业务地址） | Transit IP（链路地址） |
|---|---|---|
| 地址池 | 每个 Domain 定义（如 `10.11.0.0/24`） | `10.10.0.0/24`（全局共用） |
| 分配到 | `dummy0` 接口 | 每个 per-peer WireGuard 接口 |
| 用途 | 节点稳定身份地址（DNS、应用、监控） | 隧道点对点寻址 |
| Babel 通告 | ✓ `redistribute local` | ✗ 内部使用 |
| 稳定性 | 不随拓扑变化 | 随链路增删变化 |

另外，每条链路还分配一对 IPv6 link-local 地址（`fe80::X`），用于 Babel 邻居发现。

### 2.5 Per-Peer WireGuard 接口模型

**为什么不用单个 wg0 + 多 Peer？**

传统 WireGuard 单接口多 Peer 模型与 Babel 动态路由不兼容：
- Babel 需要**每个邻居一个独立接口**才能独立跟踪链路质量
- 单 wg0 多 peer 在 Babel 看来是一个广播域，无法区分各链路
- 多 peer 的 `AllowedIPs` 容易产生地址冲突

**Per-peer 设计：** 每条 peer 连接使用独立的 WireGuard 接口：

```
Node alpha:
  wg-node-beta   ← 到 beta 的隧道 (port 51820)
  wg-node-gamma  ← 到 gamma 的隧道 (port 51821)
  dummy0         ← 稳定 overlay 地址
```

每个接口特点：
- 独立监听端口（基础端口 + 偏移量递增）
- 独立 transit IP（/32 点对点）+ IPv6 link-local
- 仅一个 `[Peer]` 段
- `Table = off`（阻止 wg-quick 添加路由，由 Babel 管理）
- `AllowedIPs = 0.0.0.0/0, ::/0`（每接口仅一个 peer，安全）

**接口命名规则：** `wg-<对端名称>`，小写、特殊字符替换为 `-`，Linux 15 字符限制截断。

---

## 3. 使用指南

### 3.1 拓扑编辑工作流

标准操作顺序：

1. **创建网域** — 定义地址空间（CIDR）、分配模式、路由模式
2. **创建节点** — 设置名称、平台、角色，分配到网域
3. **添加公网映射**（可选）— 为有公网入口的节点配置 Host:Port
4. **画连线** — 在画布上从源节点拖向目标节点，设置 endpoint
5. **校验** — 检查拓扑完整性和语义错误
6. **编译** — 生成所有配置文件
7. **导出** — 下载每节点部署包

**界面布局：**
- 中央画布：可视化节点与有向连线
- 左侧面板：创建并排序网域、节点（支持拖拽排序）
- 右侧面板：编辑当前选中的网域/节点/连线属性
- 底部面板：校验结果与诊断信息

### 3.2 参数全解

#### 网域参数

| 参数 | 必填 | 说明 |
|------|------|------|
| Name | ✓ | 显示名与逻辑标识 |
| CIDR | ✓ | Overlay 地址池，如 `10.11.0.0/24` |
| Allocation Mode | ✓ | `auto` 自动分配 / `manual` 手动指定 |
| Routing Mode | ✓ | `babel` 动态路由 / `static` 静态 / `none` 不生成 |

#### 节点参数

| 参数 | 必填 | 说明 |
|------|------|------|
| Name | ✓ | 画布与列表显示名 |
| Hostname | ✗ | 真实 hostname 或域名标签 |
| Platform | ✓ | `debian` / `ubuntu` |
| Domain | ✓ | 所属网域 |
| Role | ✓ | `peer` / `router` / `relay` / `gateway` |
| Overlay IP | ✗ | 手工指定时使用，否则自动分配 |
| Listen Port | ✗ | WireGuard 基础监听端口，默认 51820 |
| MTU | ✗ | WireGuard 接口 MTU，0 = 系统默认 |
| Router ID | ✗ | Babel router-id（MAC-48），留空自动生成 |

**能力字段：**

| 参数 | 说明 |
|------|------|
| 公网可达 | 节点是否可从公网访问 |
| 可入站 | 外部流量是否能到达 |
| 可转发 | 是否转发他人流量 |
| 可中继 | 是否作为中继节点 |

**公网映射（每组）：**

| 参数 | 说明 |
|------|------|
| Host | 公网 IP 或域名 |
| Port | 公网端口 |
| Note | 备注（如 "电信出口A"、"东京入口"） |

#### 连线参数

| 参数 | 必填 | 说明 |
|------|------|------|
| Type | ✓ | `direct` / `public-endpoint` / `relay-path` / `candidate` |
| Endpoint Host | ✗ | 目标 IP 或域名（可从目标节点公网映射下拉选择） |
| Endpoint Port | ✗ | 目标端口 |
| Transport | ✗ | `udp` / `tcp` 元数据 |
| Priority | ✗ | 路径优先级 |
| Weight | ✗ | 路径权重 |
| Is Enabled | ✓ | 是否参与编译 |

### 3.3 校验、编译与导出

**校验** 检查两类问题：
- **Schema 校验**：必填字段、类型正确性、引用有效性（如节点的 domain_id 指向已有网域）
- **语义校验**：IP 是否重复、节点是否孤立、CIDR 是否合法

**编译** 从拓扑 JSON 确定性生成：
- 每个 per-peer WireGuard 配置文件
- 每节点 Babel 路由配置
- 每节点 sysctl 内核参数
- 每节点一键安装脚本

**导出** 打包为每节点独立的部署目录，包含所有配置文件、install.sh、manifest.json 和 checksums.sha256。

---

## 4. 编译器工作原理

### 4.1 编译流水线

编译器（`internal/compiler/compiler.go`）按 5 个阶段处理拓扑：

**Pass 1：Schema 校验** — 校验 JSON 结构正确性：必填字段、类型、引用有效性。

**Pass 2：语义校验** — 检查逻辑一致性：IP 冲突、孤立节点、非法边引用、CIDR 合法性。

**Pass 3：IP 分配 + Peer 推导**
- **IP 分配器**（`internal/allocator/ip.go`）：为无手动 IP 的节点从 Domain CIDR 池顺序分配，跳过网络地址/广播地址/保留区间
- **能力推导**（`internal/compiler/roles.go`）：根据角色推导能力字段（如 `router` → `can_forward=true`）
- **Peer 推导**（`internal/compiler/peers.go`）：处理 Edge 生成每对节点的 PeerInfo（详见 4.2）

**Pass 4：配置渲染** — 四个独立渲染器：

| 渲染器 | 输出 | 源码位置 |
|--------|------|----------|
| WireGuard | 每 peer 一个 `.conf` | `internal/renderer/wireguard.go` |
| Babel | 每节点 `babeld.conf` | `internal/renderer/babel.go` |
| sysctl | `99-overlay.conf` | `internal/renderer/sysctl.go` |
| 安装脚本 | `install.sh` | `internal/renderer/script.go` |

**Pass 5：产物导出**（`internal/artifacts/export.go`）— 组织为每节点独立目录。

### 4.2 Peer 推导逻辑

Peer 推导器是编译器中最复杂的部分，负责将拓扑 Edge 转换为具体的 WireGuard Peer 配置。

**输入 → 输出：**
- 输入：Topology（节点 + 边）+ 密钥对
- 输出：`map[nodeID][]PeerInfo` — 每节点的 peer 接口配置列表

**Edge 处理规则：**
1. 按顺序遍历所有启用的 edge
2. 去重：某对节点 `A→B` 已处理过则跳过后续同对 edge
3. 每对新节点同时生成正向 peer 和自动反向 peer

**Endpoint 解析：**
- **正向 peer**：直接使用 edge 的 `endpoint_host:endpoint_port`
- **反向 peer**：查找是否存在反向 edge（`B→A`），如有则使用其 endpoint；如无则反向 peer 没有 endpoint（依赖正向侧发起连接）

```
示例（双向 edge）:
  Edge: node-1 → node-2, endpoint=203.0.113.2:51820
  Edge: node-2 → node-1, endpoint=203.0.113.1:51820

  结果:
    node-1 的 peer 配置: Endpoint = 203.0.113.2:51820
    node-2 的 peer 配置: Endpoint = 203.0.113.1:51820  ← 反向 edge 查找
```

**PersistentKeepalive 判定：**

| 条件 | Keepalive |
|------|-----------|
| 节点可入站 且 存在反向 edge | 0（不启用） |
| 节点不可入站（NAT 后） | 25 秒 |
| 无反向 edge（单向连接） | 25 秒 |

**Transit IP 分配：** 每对节点从 `10.10.0.0/24` 顺序分配一对地址：
- Link 0: `10.10.0.1` ↔ `10.10.0.2`
- Link N: `10.10.0.(2N+1)` ↔ `10.10.0.(2N+2)`

**IPv6 Link-Local 分配：** 同步分配，Link 0: `fe80::1` ↔ `fe80::2`，依此类推。

**监听端口分配：** 每节点从 `listen_port`（默认 51820）开始，每增加一个 peer 接口递增 1。

### 4.3 Babel 路由集成

Babel 是使多跳 overlay 网络运转的动态路由守护程序。

**何时运行 Babel？** 当节点所属 Domain 的 `routing_mode` 为 `"babel"` 时生成 Babel 配置。

**Router-ID 生成：**
1. 计算 `SHA-256(node_id)`
2. 取前 6 字节作为 MAC-48 地址
3. 设置 locally administered bit（`| 0x02`），清除 multicast bit（`& 0xFE`）
4. 保证稳定性（同节点同 ID）和唯一性（SHA-256 分布均匀）
5. 用户可手动指定 `router_id` 覆盖

**接口声明：** 每个 per-peer WireGuard 接口声明为 Babel tunnel 接口：
```
interface wg-node-beta type tunnel hello-interval 4 update-interval 16
```
- `type tunnel`：声明为点对点隧道
- `hello-interval 4`：每 4 秒发送 hello
- `update-interval 16`：每 16 秒发送完整路由更新

**路由重分发策略（按角色）：**

| 角色 | 通告内容 | 默认 Cost |
|------|---------|-----------|
| `peer` | 自身 overlay IP | 0 |
| `router` | 自身 overlay IP + Domain CIDR | 0 |
| `relay` | 自身 overlay IP + Domain CIDR | 96（优先直连） |
| `gateway` | 自身 overlay IP + Domain CIDR + 额外前缀 + 默认路由 | 0 |

末尾的 `redistribute local deny` 至关重要——防止意外通告 transit IP 池或系统路由。

**全局设置：**
- `local-port 33123`：Babel 管理端口
- `skip-kernel-setup false`：让 Babel 管理内核路由表

---

## 5. 生成产物

### 5.1 产物目录结构

每个节点的部署包包含上线所需的全部文件：

```
node-alpha/
  ├── wireguard/
  │   ├── wg-node-beta.conf      # 到 beta 的 WireGuard 隧道配置
  │   └── wg-node-gamma.conf     # 到 gamma 的 WireGuard 隧道配置
  ├── babel/
  │   └── babeld.conf            # Babel 路由守护程序配置
  ├── sysctl/
  │   └── 99-overlay.conf        # 内核参数（转发、rp_filter）
  ├── install.sh                 # 一键安装脚本
  ├── manifest.json              # 构建元信息与文件清单
  ├── checksums.sha256           # SHA-256 完整性校验
  └── README.txt                 # 快速上手说明
```

### 5.2 WireGuard 配置详解

生成的 per-peer WireGuard 配置示例：

```ini
# WireGuard per-peer interface: wg-node-beta
# Node: node-alpha -> Peer: node-beta

[Interface]
PrivateKey = <private_key>
Address = 10.10.0.1/32
Table = off
ListenPort = 51820

PostUp = ip -6 addr add fe80::1/64 dev %i 2>/dev/null || true
PostDown = ip -6 addr del fe80::1/64 dev %i 2>/dev/null || true

[Peer]
PublicKey = <public_key>
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 203.0.113.2:51820
```

**关键设计点：**

- **`Table = off`**：阻止 `wg-quick` 添加内核路由。由于 `AllowedIPs = 0.0.0.0/0`，不加此选项每个接口都会尝试添加默认路由、相互冲突。路由完全交给 Babel。
- **`AllowedIPs = 0.0.0.0/0, ::/0`**：在 per-peer 模型中是安全的——每个接口仅一个 peer，允许任何流量通过隧道，由 Babel 决定使用哪条隧道。
- **`PostUp`/`PostDown`**：添加 Babel 邻居发现所需的 IPv6 link-local 地址。

### 5.3 安装脚本三阶段逻辑

`install.sh` 遵循幂等的分阶段部署：

**Phase 0 — 清理**
- 停止并移除现有的 WireGuard 接口和旧配置
- 清理遗留的 `wg0` 单接口配置
- 停止 Babel 守护程序
- 移除旧 sysctl 配置

**Phase 1 — 环境准备**
- 校验文件完整性（checksums.sha256）
- 检查 root 权限、检测 OS（Debian / Ubuntu）
- 安装依赖包（`wireguard`、`wireguard-tools`、`babeld`）
- 创建 `dummy0` 接口并分配 overlay IP
- 安装 systemd 服务使 `dummy0` 在重启后持久化

**Phase 2 — 部署配置**
- 复制 WireGuard 配置到 `/etc/wireguard/`
- 复制 Babel 配置到 `/etc/babel/`
- 复制 sysctl 配置到 `/etc/sysctl.d/`

**Phase 3 — 激活验证**
- 应用 sysctl 设置
- 启动所有 `wg-quick@<interface>` 服务
- 配置 babeld systemd override（声明依赖所有 WireGuard 服务）
- 启动并启用 babeld
- 显示状态摘要

### 5.4 dummy0 + Table=off 设计

这个组合是 per-peer 接口与 Babel 协同工作的关键：

```
┌─────────────────────────────────────────┐
│              Node alpha                   │
│                                           │
│  dummy0: 10.11.0.1/32  ← Overlay IP      │
│  (稳定地址，Babel 通告)                     │
│                                           │
│  wg-node-beta:  10.10.0.1/32 (Table=off) │
│  wg-node-gamma: 10.10.0.3/32 (Table=off) │
│                                           │
│  Babel 管理所有路由决策                      │
│  - 从邻居学习路由                           │
│  - 在内核路由表中安装/移除路由                │
│  - 自动处理链路故障切换                      │
└─────────────────────────────────────────┘
```

- `dummy0` 提供 Babel 通告的稳定地址——应用和 DNS 始终指向这里
- 每个 WireGuard 接口 `Table = off`，`wg-quick` 不触碰路由表
- Babel 将每个 `wg-*` 接口视为独立隧道链路，独立跟踪可达性
- 某条链路故障时，Babel 自动通过存活链路重新路由——无需手动调整
