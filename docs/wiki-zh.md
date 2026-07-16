# Yet Another Overlay Generator — Wiki

> 其他语言版本：[English](wiki.md)

本 Wiki 是 **YAOG** 的完整用户文档，覆盖项目运行的**两种方式**：

- **本地生成器（air-gap）：** 在浏览器中设计拓扑、**完全在浏览器内编译**、导出各节点的可部署配置包，再直接
  或通过 SSH 安装这些由本地信任的 AirGap 产物。全程不涉及任何后端。
- **控制器（Agent 拉取式）：** 将 YAOG 作为长期运行的服务运行，每个节点**主动拉取**自己那份经 keystone 签名
  的配置，并把实时健康状态回报给运营者面板。

两种模式执行同一条 Go 编译路径：本地面板以 Go/WebAssembly 加载它，控制器和离线 CLI 则以
本地 Go 方式调用同一个 `internal/localcompile` 门面。必需的 WASM-vs-golden 门禁会将浏览器构建与
Go 语料库对照；项目不再维护一个手写的 TypeScript 编译器副本。架构层面的权威说明位于
[`docs/spec/`](spec/)——本 Wiki 是在其之上的叙述式指南。

---

## 目录

1. [项目概览](#1-项目概览)
2. [核心概念](#2-核心概念)
3. [两种模式与构建边界](#3-两种模式与构建边界)
4. [本地模式 — 设计、编译、导出、部署](#4-本地模式--设计编译导出部署)
5. [控制器模式 — Agent 拉取式控制平面](#5-控制器模式--agent-拉取式控制平面)
6. [编译器工作原理](#6-编译器工作原理)
7. [生成产物](#7-生成产物)
8. [安全模型](#8-安全模型)
9. [HTTP API 参考](#9-http-api-参考)
10. [调试与故障排查](#10-调试与故障排查)
11. [术语表](#11-术语表)

---

## 1. 项目概览

Yet Another Overlay Generator 是一个基于 Web 的网络设计与配置生成系统。你通过可视化拓扑编辑器定义节点、网域和
连通关系；系统自动分配地址，并确定性地生成 **WireGuard**（三层加密隧道）+ **Babel**（动态路由）配置，以及一键
安装脚本。

### 设计哲学

系统遵循 **设计 → 编译 → 部署** 的三层架构：

```text
[Web 画布  /  CLI]
        │  拓扑 JSON
        ▼
[编译器]                          ← 在浏览器内运行（本地模式）或在控制器上运行
  ├─ 模式（Schema）校验
  ├─ 语义校验
  ├─ IP 分配
  ├─ 能力推导
  ├─ Peer 推导
  └─ 配置渲染器
        │  ├─ WireGuard 配置
        │  ├─ Babel 配置
        │  ├─ sysctl 内核参数
        │  ├─ 安装脚本
        │  └─ 部署脚本
        ▼
[产物导出器]                       ← 各节点配置包（本地）或各节点签名配置包（控制器）
        ▼
[目标主机]
        └─ AirGap：受信任的 install.sh；AgentHeld：已注册 agent / 经验证的 kit apply
```

核心原则：

- **拓扑即代码。** 拓扑 JSON 是唯一事实来源；每一份配置都由它确定性派生而来。
- **确定性编译。** 相同的显式输入产生相同的字节（编译门面接收时钟、密钥生成器、签名器与获取设置——
  见[第 6 节](#6-编译器工作原理)）。因此 CI 可将 Go/WASM 构建与本地 Go 运行在同一份冻结 golden 语料上对照。
- **幂等部署。** 安装脚本可安全地重复运行；新增一个节点不会改动其他无关节点的配置包字节。
- **密钥各居其所。** 本地模式下密钥在你的设计主机上生成并保存；控制器模式下每个节点持有自己的私钥，控制器
  永远看不到它（见[第 8 节](#8-安全模型)）。

---

## 2. 核心概念

这些概念在两种模式下完全一致——它们描述的是编译器消费的拓扑模型。

### 2.1 网域（Domain）

**网域**是定义可分配 IP 范围的 overlay 地址空间。

| 字段 | 说明 |
|-------|-------------|
| Name | 显示名与逻辑标识 |
| CIDR | 地址范围，例如 `10.11.0.0/24` |
| Allocation Mode（分配模式） | `auto`（编译器分配）/ `manual`（按节点手动指定） |
| Routing Mode（路由模式） | `babel`（动态路由）—— 目前唯一已实现的模式；`static` / `none` 为保留值，会在**校验时被拒绝**（留空则归一化为 `babel`） |

### 2.2 节点（Node）与角色

**节点**代表一台机器（云主机、裸金属服务器、容器宿主机）。

**基本字段：** Name、Hostname（可选）、Platform（`debian` / `ubuntu`）、所属网域、Overlay IP（可选手动覆盖）、
WireGuard 基础监听端口（默认 51820）、MTU（可选）、Router ID（可选的 Babel MAC-48；留空则自动生成）。

**角色与能力**（权威来源：`internal/compiler/roles.go`）：

| 角色 | IP 转发 | 接受入站 | 运行 Babel | Babel 通告 | 典型用途 |
|------|-----------|----------------|------------|-----------------|-------------|
| `peer` | 否 | 否 | 是 | 仅自身 overlay `/32` | 终端用户节点 |
| `router` | 是 | 仅当具备公网 IP | 是 | 自身 `/32` + 网域 CIDR + extra prefixes（设置时） | 骨干转发节点 |
| `relay` | 是 | **始终接受** | 是 | 自身 `/32` + 网域 CIDR + extra prefixes（设置时） | NAT 穿透中继 |
| `gateway` | 是 | 仅当具备公网 IP | 是 | 自身 `/32` + 网域 CIDR + extra prefixes + **默认路由 `0.0.0.0/0`** | 通往外部网络的桥接 |
| `client` | 否 | 否 | **否** | 无（不运行 Babel） | 轻量端点（手机、笔记本） |

> **接受入站是有条件的。** `router` 与 `gateway` 只有在节点可被公网访问时才接受入站；`relay` 始终接受入站。
> 拥有任意公网端点的节点即被视为可公网访问，即便未显式设置该标志（当 `PublicEndpoints` 非空时，`roles.go`
> 会把 `HasPublicIP` 归一化为真）。

> **Extra prefixes（额外前缀）。** `router` 与 `relay` 仅在 `extra_prefixes` 字段非空时通告它（例如节点背后的
> 一段 LAN）；`gateway` 则无条件通告。Extra prefixes 与网关默认路由通过内核路由机制通告
> （`redistribute ip <prefix> allow`，匹配真实的 connected/WAN 内核路由），而非 `redistribute local`。
> 见 [spec/roles/roles.md](spec/roles/roles.md)。

> **链路开销（Babel `rxcost`）—— 按角色的默认值加按边的覆盖。** 默认值按*节点角色*而定：`relay` 会被写入一个
> 显式的 `rxcost 96`（一种开销偏置，使在有直连链路时路径尽量避开中继），而 `router` / `gateway` / `peer` 省略
> 该 token，交由 babeld 套用其自身的内建默认值。边的 `priority`（>0 时）、否则 `weight` 会覆盖该默认值；**备份**
> 边带有预设开销（384），使 Babel 在主链路在线时优先选择主链路。见[第 2.3 节](#23-连线edge有向连接)、
> [spec/compiler/routing-modes.md](spec/compiler/routing-modes.md) 与 [spec/artifacts/babel.md](spec/artifacts/babel.md)。

> **Client 角色。** Client 是最轻量的角色，面向不参与动态路由的设备。Client 使用单个 `wg0` 接口连接到一个
> router/relay/gateway。它不运行 Babel、不使用 `dummy0`、也不使用 per-peer 接口模型。Client 的可达性通过
> router 侧的内核路由注入（`PostUp = ip route add <client_ip>/32 dev %i`）加上 Babel 再分发实现。

**能力字段**（由角色推导，可覆盖）：可公网访问、可接受入站、可转发、可中继。

**多个公网端点。** 一个节点可携带多个 `Host:Port` 公网端点映射（允许主机名），用于多出口 / 多 ISP / NAT
多映射场景。

**SSH 连接（自动部署）。** 节点可选地存储供生成的部署脚本使用的 SSH 连接信息（本地模式）：

| 字段 | 说明 |
|-------|-------------|
| SSH Alias | 来自 `~/.ssh/config` 的主机别名；设置后覆盖下方手动字段 |
| SSH Host | SSH 目标 IP 或主机名 |
| SSH Port | SSH 端口（默认 22） |
| SSH User | SSH 登录用户名（默认 root） |
| SSH Key Path | **你本机上**的 SSH 私钥路径 |

> 不支持密码认证——请使用基于密钥的认证。SSH 信息在节点属性面板中默认折叠，且绝不是 WireGuard 密钥材料。

### 2.3 连线（Edge，有向连接）

有向边 `A → B` 表示 **A 主动连接到 B**。

| 字段 | 说明 |
|-------|-------------|
| Type | `direct` / `public-endpoint` / `relay-path` / `candidate` |
| Endpoint Host | 目标公网 IP 或主机名；从目标节点的公网端点中选择或手动填写 |
| Endpoint Port | 运营者 NAT / 端口转发覆盖：`0`（默认）= 编译器自动分配；非零 = from 侧逐字拨号的外部端口 |
| Compiled Port | 只读：from 侧实际拨号的端口，编译后填入 |
| Transport | `udp` = 普通 WireGuard。`tcp` = 该链路由 [mimic](https://github.com/hack3ric/mimic) 包裹（eBPF UDP→伪 TCP），用于限速或封锁 UDP 的网络。两端都须为带 eBPF 的 Linux；MTU 会被自动下调；安装器从发行版仓库装配 mimic。这**不是**审查规避 / DPI 绕过功能。见 [spec/artifacts/mimic.md](spec/artifacts/mimic.md) |
| 链路方向 | `A ⇄ B`（双向连接，默认）= 两端均可发起握手。`A → B`（单向连接）= 仅 A 拨号；B 保持路由但从不发起。第三个选项 `B → A` 会**翻转该边**（可见：箭头反向，分配值跟随各自节点）然后单向连接。见下方说明 |
| Priority / Weight | 链路开销偏好（越低越优先）；输入到 Babel 的 `rxcost` |
| Is Enabled | 该边是否参与编译 |

> **端口归属。** 编译器是 WireGuard 监听端口的唯一权威。`endpoint_port` *不是*所分配端口的副本——把它保留为
> `0`，编译器就会拨号对端接口自动分配的监听端口，并把结果写入只读的 `compiled_port`。仅当需要显式的
> NAT / 端口转发覆盖时（例如某 router 将外部 `:51900` DNAT 到节点内部 `:51820`）才把 `endpoint_port` 设为非
> 零值；该覆盖会被逐字尊重并在重编译间保留。完整契约见 [spec/data-model/edge.md](spec/data-model/edge.md)。

> **并行链路与故障切换。** 一对节点之间可携带一条主链路外加一条或多条**备份**链路，每条都是独立的 WireGuard
> 接口。Babel 按每条链路的开销选择并自动故障切换——例如一条普通 UDP 主链路配一条 `TCP (mimic)` 备份链路。
> 备份链路具有更高的默认开销（384），使主链路在线时被优先选用。

> **何时使用单向连接（加速器与中继）。** 当 from 节点带有公网端点（或存在填写了主机的显式反向边）时，一条双向
> 连接的边其实悄悄创建了**两条**拨号路径：from 侧拨号你填写的 Endpoint Host，而 to 侧会自动拨号 from 节点的
> 第一个公网端点（它的直连地址）。WireGuard 每个 peer 只保留一个*运行时*端点，并跟随最近一次完成握手的一方
> ——因此若你把 `A → B` 经由 UDP 加速器转发、而 B 先启动，B 会**直连**拨号 A，加速器路径就被永久绕过。把方向
> 设为 `A → B`（单向连接）即可消除竞争：B 保留隧道、保持路由，但从不发起——它只应答 A 经加速器到达的握手。
> 单向连接的边**必须**填写 Endpoint Host（否则没有任何一方能拨号——校验会大声拒绝），且编辑器会显示双向模式下
> *反向*拨号的解析来源，让这种不对称一目了然。客户端链路不能单向连接；主链路类的边在其节点对还有另一条已启用
> 主链路类边时也不能单向连接（它们会折叠为一条隧道——方向设置将被静默忽略）；备份链路是独立隧道，可以单向连接
> （校验会分别说明原因）。

> **mimic 需要直连路径（不能经 L7 中继）。** `tcp`（mimic）把 UDP 整形为伪 TCP，需要**端到端的 L3/L4 报文
> 透明**。一个会终止并重新发起连接的 L7 / UDP 加速中继（例如做 DNAT+SNAT 的 gost/realm 式中继）会破坏它——反向
> 伪 TCP 那一段会被 `RST`——因此经由此类中继的链路必须使用 **`transport: udp`** 而非 `tcp`。YAOG 会在设计期
> 警告：一条 type 为 `relay-path` 的已启用 `tcp` 边会触发 `validation_edge_mimic_relay_path` 警告，建议改用
> `udp`（这是警告，不是阻断）。

### 2.4 两层地址分离

系统使用两个独立的 IP 地址池，使链路地址永远不与节点身份地址冲突：

| | Overlay IP（身份地址） | Transit IP（链路地址） |
|---|---|---|
| 地址池 | 各网域 CIDR（例如 `10.11.0.0/24`） | 各网域的 `transit_cidr`（默认 `10.10.0.0/24`） |
| 分配到 | `dummy0` 接口 | 每个 per-peer WireGuard 接口 |
| 用途 | 稳定的节点身份（DNS、应用、监控） | 隧道点对点编址 |
| Babel 通告 | 是（`redistribute local`） | 否——仅内部使用 |
| 稳定性 | 不随拓扑变化 | 随链路增删而变化 |

每条链路还获得一对 IPv6 链路本地地址（`fe80::X`），供 Babel 邻居发现使用。

### 2.5 Per-Peer WireGuard 接口模型

**为什么不用带多个 Peer 的单个 `wg0`？** 传统的单接口多 peer 模型与 Babel 动态路由不兼容：Babel 需要**每个
邻居一个独立接口**以分别跟踪各链路的质量；单个 `wg0` 在 Babel 看来像一个广播域；多个 peer 的 `AllowedIPs`
还会互相冲突。

**Per-peer 设计**——每个 peer 连接使用一个专属的 WireGuard 接口：

```
节点 alpha：
  wg-beta    ← 通往 beta 的隧道  （端口 51820）
  wg-gamma   ← 通往 gamma 的隧道 （端口 51821）
  dummy0     ← 稳定的 overlay 地址
```

每个接口具备：独立的监听端口（基础端口 + 递增偏移）、独立的 transit IP（`/32` 点对点）+ IPv6 链路本地地址、
恰好一个 `[Peer]` 段、`Table = off`（wg-quick 不添加路由——由 Babel 管理路由），以及
`AllowedIPs = 0.0.0.0/0, ::/0`（每接口仅一个 peer，因此安全）。

**接口命名。** `wg-<peer-name>`，全部小写，`[a-z0-9-]` 之外的字符（包括 `_`）替换为 `-`。Linux 内核将接口名
限制在 15 个字符，因此：若 `wg-<clean-name>` ≤ 15 字符则原样使用；否则算法返回 `wg-` + 清洗后名称的前 8 个字符
+ `sha256(peer-name)` 的前 4 个十六进制字符（3 + 8 + 4 = 15）。哈希后缀可避免两个共享前缀的不同长名冲突。后端
是该名称的唯一权威（`internal/naming`）；前端始终消费已编译的名称，绝不自行重新推导。完整算法见
[spec/artifacts/naming.md](spec/artifacts/naming.md)。

---

## 3. 两种模式与构建边界

YAOG 发布一个静态本地设计站点、一种控制器服务器构建，以及一个离线 CLI。静态站点没有后端监听器；
`yaog-server` 始终是控制器，不会变成匿名编译服务。权威说明见
[spec/operations/deployment-topology.md](spec/operations/deployment-topology.md)。

### 3.1 本地生成器（air-gap）——在浏览器内计算

本地生成器是一个**静态前端包**：面板加载 `yaog.wasm`（`cmd/wasm` 的 Go/WebAssembly 构建），
并使用共享的 `internal/localcompile` 流水线执行校验 / 编译 / 导出。它不向任何后端发起 POST——根本没有
服务器监听，因此你可以把它托管在任意静态文件服务器或 CDN 上。发布物中提供一个自包含的
`yaog-local-design-<version>.zip`；你也可以直接运行前端开发服务器（见[第 4 节](#4-本地模式--设计编译导出部署)）。

一个构建期标志 `VITE_LOCAL_ONLY` 会产出一个**模式锁定**的静态站点：控制器模式被设为不可达（隐藏切换开关与
控制器专属导航，并把已持久化的控制器模式强制扳回本地）。`yaog-local-design` 资产正是以此标志构建的。

### 3.2 控制器——长期运行的 Go 后端

构建 `./cmd/server/` 会产出 `yaog-server`，即**控制器**（面板 + API）。它服务面板 SPA、公开的
`GET /api/health` 探针、`:8080` 上的运营者路由，以及 `:9090` 上的 agent 路由。控制器的编译路径是**经运营者
鉴权的**服务端渲染（`compile-preview` / `stage`），而非匿名计算端点。

> **单一服务器二进制是“仅控制器”且会高声失败（fail loud）。** 在**未**同时设置 `YAOG_CONTROLLER_STATE_DIR` 与
> `YAOG_TENANT_ID` 的情况下运行 `yaog-server`，会**以一条醒目的错误退出**，而不会去启动一个匿名计算监听器。
> 旧的 `POST /api/{validate,compile,export,deploy-script}` 路由已被移除；它们在所有服务器构建中都不存在并返回
> 404。离线工作请使用静态 Go/WASM 站点或 `yaog-compiler`。

### 3.3 第三条路径——`cmd/compiler` CLI

`cmd/compiler` 是离线 CLI。它读取一份拓扑 JSON 并写出一个配置包目录，完全不需要服务器，使用与浏览器及
控制器相同的 `internal/localcompile` 门面：

```bash
go run ./cmd/compiler/ -input topology.json   # -input 必填；-output 默认为 ./output
```

### 3.4 计算在何处运行（一览）

| 产物 | 构建 | 计算面 |
|---|---|---|
| 静态本地设计站点（`yaog-local-design-<ver>.zip`） | `npm run build:local` | 浏览器内 Go/WASM 流水线；**无**后端监听 |
| 控制器 `yaog-server`（发布二进制 + Docker 镜像） | `go build ./cmd/server/` | `/api/health` + 运营者/agent 路由；编译需运营者鉴权。已退役的匿名路由始终不存在。缺少控制器环境变量时高声失败。 |
| `yaog-compiler` CLI | `go build ./cmd/compiler/` | 离线共享 Go `render → export`；无监听器 |

必需的 **WASM 一致性门禁**（`scripts/wasm-conformance-gate.mjs`）会在冻结的 `internal/localcompile` golden 语料上
执行编译后的 Go/WASM 引擎，并将其 manifest 与本地 Go 比较。这是通过执行同一实现来验证一致性，而非维护
两个编译器（[spec/compiler/io-contract.md](spec/compiler/io-contract.md)）。

---

## 4. 本地模式 — 设计、编译、导出、部署

本地模式下一切都在浏览器中发生；你唯一需要运行的就是前端。

```bash
cd frontend
npm install --legacy-peer-deps
npm run dev          # Vite 开发服务器，端口 :5173 —— 打开 http://localhost:5173
```

（`./dev.sh start` 是贡献者便捷脚本，会同时启动 Go 服务器，但 Go 服务器是仅控制器构建，只有设置了控制器环境
变量时才会保持运行——纯本地设计只需上面的前端即可。）

### 4.1 拓扑编辑工作流

所有编辑都在 **Design（设计）** 页面进行（本地模式的默认落地页）：

1. **新增网域** —— 定义地址空间（CIDR）、分配模式、路由模式。
2. **新增节点** —— 设置名称、平台、角色，并指派到某网域。
3. **添加公网端点**（可选）—— 为有公网入站的节点配置 `Host:Port`。
4. **配置 SSH**（可选）—— 自动部署所需的连接信息（默认折叠）。
5. **绘制连线** —— 在画布上从源拖到目标；设置端点主机（除非需要 NAT 覆盖，否则把端口保留为 `0`）。
6. **校验** —— 检查完整性与语义错误（在浏览器内运行）。
7. **编译** —— 分配 IP 与端口、推导 peer 配置、渲染全部产物（在浏览器内运行；无后端往返）。画布随后会显示
   彩色编码的 per-peer 句柄，以及每条边只读的 `compiled_port`。
8. **导出与部署** —— 切到 **Deploy（部署）** 页面查看已编译产物并下载产物 ZIP。ZIP 根目录中的
   `deploy-all.sh` / `deploy-all.ps1` 与全部节点配置包由浏览器内的**同一次编译原子地产出**。

**界面布局：** 中央画布以彩色编码的 per-peer 句柄可视化节点与有向边；画布工具栏创建网域/节点；右侧侧栏编辑
所选的网域/节点/边；底栏显示校验结果。

### 4.2 校验、编译与导出

**校验**检查两类内容：

- **Schema（模式）** —— 必填字段、类型正确性、引用有效性（例如节点的 `domain_id` 指向一个存在的网域）。
- **语义** —— IP 冲突、孤立节点、非法 CIDR、被破坏的 NAT 可达性。

**编译**确定性地产出各 peer 的 WireGuard 配置、各节点的 Babel 配置、各节点的 sysctl 参数、各节点的安装脚本，
以及项目级部署脚本。

**导出**将以稳定 node ID 为键的完整节点目录打包，其中包含所有配置文件、`install.sh`、`manifest.json` 与
`checksums.sha256`；ZIP 根目录同时包含匹配的两个项目级部署脚本。完整目录结构与安装脚本各阶段见
[第 7 节](#7-生成产物)。

### 4.3 部署配置包

这条直接路径**仅适用于 AirGap**。对于本地信任的 AirGap 导出，可把完整节点目录拷到主机上并运行
`sudo bash install.sh`；对于整个集群，ZIP 根目录中的 `deploy-all.sh`（Bash）/
`deploy-all.ps1`（PowerShell）会把每个完整目录上传到配置了 SSH 的节点。见
[第 7.5 节](#75-自动部署脚本)。

AgentHeld/控制器产物不得使用这些捷径。受管节点只能通过已注册 agent 应用；手动 AgentHeld 节点必须使用
`sudo yaog-agent kit apply`，且运营者凭据须经另一条受信任通道提供。见
[第 5.4 节](#54-注册并部署到节点agent-拉取)。

---

## 5. 控制器模式 — Agent 拉取式控制平面

> **2.0（beta）新增。** 你不必再导出 air-gap 配置包，而可以把 YAOG 作为长期运行的**控制器**运行，让每个节点
> **主动拉取**自己那份签名配置。控制器是单个 Docker 镜像（SPA 面板 + API）；各节点上的 agent 是一个小型主机
> 二进制，控制器会给你一行安装命令。上文经典的生成/导出流程保持不变。

### 5.1 启动控制器（Docker）

需要带 Compose 插件的 Docker Engine（`docker compose`，v2）。

```bash
# 获取 compose 文件（或直接用仓库根目录的那一份）
curl -fsSLO https://raw.githubusercontent.com/kunori-kiku/yet-another-overlay-generator/main/docker-compose.yml

# 仅适用于未启用 userns-remap 的 rootful Docker：创建私有的 ./data bind mount，
# 并将其所有者设为容器 uid 65532：
sudo install -d -m 0700 -o 65532 -g 65532 data

docker compose up -d
```

上述字面 uid 只适用于未启用用户命名空间映射的 rootful Docker。对于 rootless Docker 或启用了
`userns-remap` 的守护进程，不要预先创建或 `chown ./data`；请把 Docker 管理的命名卷选项持久化到
`.env`，确保之后每条 Compose 命令都使用同一份状态：

```dotenv
YAOG_DATA_SOURCE=controller-data
```

然后照常运行 `docker compose up -d`。

使用默认 bind mount 时，控制器状态持久化到 `./data`；使用 `controller-data` 时，状态位于 Compose
项目作用域的 Docker 卷中，须通过 Docker 的卷工具备份。默认 bind mount 模式无需 `.env`；只有选择上述便携命名卷
时才需要该设置。默认两个端口都**仅绑定到 loopback**（`127.0.0.1`），因为登录表单携带明文密码；请从同一主机
访问面板，或通过隧道（`ssh -L 8080:127.0.0.1:8080 <host>`）。

> **镜像可见性。** compose 拉取 `ghcr.io/kunori-kiku/yaog-controller:latest`。若拉取被拒（GHCR 包为私有），
> 要么先 `docker login ghcr.io`，要么本地构建——在 `docker-compose.yml` 中注释掉 `image:` 并取消注释
> `build: .`。

### 5.2 创建运营者并登录

```bash
docker compose run --rm controller create-operator \
    --state-dir /data --tenant default --username admin
```

会提示你输入密码（不回显）；加 `--force` 可重置已有运营者。面板 + 运营者 API 在 **`http://localhost:8080`**
（面向节点的 agent API 在 **`:9090`**）。控制器模式下，在任何面板界面之前你会先看到一个**全屏登录页**——以
`admin` 登录。

密码以 **argon2id** 哈希；登录成功后会签发一个存放在 **httpOnly cookie** 中的会话，因此刷新页面后登录依然有效，
且 **`localStorage` 中不保存任何 token**。登出在右上角账户菜单；可选的应急（break-glass）运营者令牌从登录页的
**Recovery（恢复）** 展开项输入。可选的第二因子是 **TOTP**（RFC 6238）和/或 **passkey**；passkey 还支持无密码
登录。完整鉴权模型见[第 8.4 节](#84-运营者认证)。

> **Passkey/WebAuthn 在 `http://localhost` 上可用**（浏览器将 loopback 视为安全上下文）。⚠️ 请使用主机名
> **`localhost`** 而非 `127.0.0.1`——WebAuthn 禁止 IP 地址域名，因此在 `127.0.0.1` 上注册 passkey 会以
> *“invalid domain”* 失败。任何**远程**访问都请在控制器前置一个终止 TLS 的反向代理（compose 文件中注释了一个
> 示例 `caddy` 服务）。

### 5.3 服务器是权威

控制器模式下，**控制器存储的设计是唯一事实来源**；浏览器缓存只是一份可丢弃的镜像。每次登录（以及 cookie 会话
恢复）时，面板都会拉取控制器存储的拓扑并覆盖本地画布。

如果你的浏览器持有一份非空且与服务器副本**不同**的本地设计，面板会在覆盖**之前**下载一份新的
`pre-hydration-backup-<date>.json` 并显示提示。这在*每一次*会丢弃分歧本地工作的覆盖时都会触发（不仅是第一次），
因此未部署的本地工作永远不会被悄然丢失。在稳态（本地 == 服务器）下不会下载备份。

登录后你会落在 **Overview（概览）**。面板分区（可折叠侧栏；路由可深链接）：

| 分区 | 路由 | 模式 | 用途 |
|---|---|---|---|
| Overview | `/overview` | 仅控制器 | 拓扑 + 集群一览 |
| Design | `/design` | 两种 | React Flow 拓扑画布 |
| Fleet | `/fleet` | 仅控制器 | 节点注册 + 各节点详情（孤立行标记“not in design”） |
| Deploy | `/deploy` | 两种 | 编译预览 + 一键 Deploy（带缩减确认护栏） |
| Security | `/security` | 两种 | TOTP/passkey 注册、审计日志、编译历史 |
| Settings | `/settings` | 两种 | 模式、连接、bootstrap、外观 |

控制器模式落地于 `/overview`；本地模式落地于 `/design` 并隐藏 Overview/Fleet。

> **升级既有控制器？** 本次发布重命名了密钥路径前缀环境变量并改变了登录/水合流程——部署前请阅读
> [`docs/MIGRATION-controller-server-authority.md`](MIGRATION-controller-server-authority.md)。

### 5.4 注册并部署到节点（Agent 拉取）

要让远程节点拉取配置，先暴露 agent 端口（`:9090`）——实验环境用
`YAOG_BIND_ADDR=0.0.0.0 docker compose up -d`；生产环境用上文的 TLS 代理。然后：

1. 在 **Settings → Bootstrap Settings** 中，把 **Public Agent URL** 设为节点访问控制器的地址（例如
   `https://overlay.example.com` 或 `http://<host>:9090`）。
2. 把节点加入你的拓扑（**Design**），然后在 **Fleet** 页面为它铸造一个单次使用的**注册令牌
   （enrollment token）**。
3. 在目标主机（Linux + systemd）上以 root 身份：

```bash
bash <(curl -fsSL https://<public-agent-url>/api/v1/agent/bootstrap) \
     --token <enrollment-token> --node-id <id>
```

这会下载 `yaog-agent` 二进制、注册该节点、应用当前 generation，并安装一个 `yaog-agent.service` systemd
守护进程，使日后的 Deploy 自动应用。各节点的 bearer 令牌落在 `/etc/wireguard/agent-controller.token`
（权限 0600）；启用 keystone 时，运营者的验证凭据落在 `/etc/wireguard/operator-cred.pem`。除必填的
`--token` / `--node-id` 外，这行命令还接受 `--controller`、`--gh-proxy`、`--release-base` 覆盖项。

**注册仪式**（仅用标准库密码学——无 CA、无 CSR、无 mTLS）：面板铸造一个**单次使用、短 TTL** 的注册令牌（以哈希
存储）；节点向 `/enroll` 出示它一次，连同自己的 WireGuard **公**钥，换得一个常驻的**各节点 bearer 令牌**（仅
返回一次，此后作为 `Authorization: Bearer …` 发送）。注册令牌在签发任何身份**之前**就被原子地销毁，因此一个
令牌永远无法供给两个节点。一个已批准的公钥恰好绑定一个 node-id（重复则拒绝，409）。吊销某节点会清除其 bearer
令牌（下一次调用起即失效）并把它从未来所有渲染中剔除。

**手动节点（AgentHeld、无守护进程）。** 在目标主机运行 `yaog-agent kit --node-id <id>`，然后把输出的公开
描述符粘贴到部署模式为 **Manual（手动）** 的拓扑节点中。Deploy 后，从 **Fleet** 下载该节点的配置包。必须通过
另一条受信任通道把已固定（pinned）的运营者**公开**凭据交给主机；绝不能把下载的配置包本身当作信任锚。随后按
凭据算法运行对应命令：

```bash
# 原始 Ed25519 运营者凭据
sudo yaog-agent kit apply --bundle <node-bundle.zip> --node-id <id> \
  --operator-cred <trusted-public-key.pem> --operator-cred-alg ed25519

# 浏览器/WebAuthn 运营者凭据；必须保留注册时的准确 RP 绑定
sudo yaog-agent kit apply --bundle <node-bundle.zip> --node-id <id> \
  --operator-cred <trusted-public-key.pem> \
  --operator-cred-alg <webauthn-es256|webauthn-eddsa> \
  --operator-rpid <rp-id> --operator-origin <origin>
```

`kit apply` 会先捕获下载目录/ZIP 的完整不可变快照，验证全部文件以及（配置时）已签名的成员关系，再暂存到受信任
的临时副本，最后才调用该副本中的 `install.sh`。卸载时使用相同命令并加 `--uninstall`。只有**从未**启用过
keystone 的遗留集群才可改传 `--dangerously-allow-no-keystone`；若配置包携带信任列表，或节点的持久状态记录了过去已验证的
keystone，这项显式风险确认仍会被拒绝。绝不能直接运行下载的 AgentHeld `install.sh`，也不要使用
`deploy-all`——生成的 AgentHeld helper 会以 fail-closed 指引退出。

### 5.5 部署生命周期 — 编译 → 暂存（stage）→ 提升（promote）

一次 Deploy 是对每租户单调递增的 **generation** 计数器进行的两阶段、经运营者鉴权的转换：

1. **编译 + 暂存**（`Deploy` → stage）。控制器加载存储的拓扑，选取**就绪子图**（已注册的受管节点加上有效的
   手动节点），运行与本地模式相同的冻结流水线，并把各节点的签名配置包**暂存**在 `generation + 1`。暂存是
   可逆的、对 agent 不可见的（尚未成为 `current`）。重新暂存会替换之前的暂存集合；它不会推进计数器。
2. **提升**（原子翻转）。所有已暂存的配置包变为 `current`，generation 自增，所有正在长轮询的 agent 被唤醒。
   控制器从不自行提升。

**只渲染就绪的部分（render-what's-ready）。** 受管节点只有在已批准且已登记公钥时才被纳入；手动节点则使用
运营者在拓扑中提供的公钥（且永不注册）。一条边只有在两端都就绪时才被保留。这让你可以提前设计整个集群，再
增量地把受管节点拉上线；通往尚未注册对端的边，会在远端注册并重新 Deploy 后重新出现在双方的配置包中。分配
pin（overlay IP、transit IP、端口——绝不含密钥材料）会在每次 stage 后写回，因此增量注册永远不会给已上线
节点重新编号。

**增量部署（delta deploy）—— 只重新暂存有变更的节点。** 一次 Deploy 会跳过那些其新编译出的配置包与它
正在服务的那份**字节完全一致**的节点，从而完全不去动未变更的节点。其身份标识是对配置包
`checksums.sha256` 求 SHA-256（该文件覆盖 `install.sh` 与每个配置文件，但**不**含 manifest 中易变的
`compiled_at`），因此只要真实配置未变，它在多次重编译间保持稳定，一旦任何一个渲染字节发生
变化便立即改变。被跳过的节点**保持其当前 generation**——它的 agent 永远看不到更新的 generation，
也就永不重新拉取——于是整个集群停在一个**混合 generation**（只有变更过的节点前进），且逐链路的
重新握手只发生在端点确实发生变化的链路上（一次空操作的 Deploy 不再引发全集群的无谓搅动/churn）。
该跳过遵循 **fail-open**：若控制器读不到某节点正在服务的配置包，就把它视为已变更并重新暂存，绝不
在存疑时跳过。

**部署前预览。** Deploy 对话框会基于你**当前的画布**给出一个预览——“N updated, M unchanged”（N 个
更新、M 个不变）——作为一次**不暂存任何东西**的只读空跑（dry-run）。它与真实 Deploy 共用完全相同的
跳过判定与身份标识，因此这个计数与实际结果一致。若预览无法获取（例如较新的面板对接较旧的控制器），
面板会显示该错误，但仍允许你 **Deploy anyway（照常部署）**——预览绝不会硬性阻断部署。

**强制重新部署（Force redeploy）。** 你可以覆盖这个跳过：**Force redeploy** 会重新暂存某个节点（或整个
集群），即便它并无变更；节点详情页上还有一个针对单节点的 **Force redeploy this node**。Force 仅用于
**主机侧配置漂移/抢修**——某节点本地配置被篡改或丢失、必须重新推送的情形。它**不**用于普通改动
（真实的配置变更会自行重新暂存），也**不**用于 keystone 密钥轮换或首次签名部署（这两者会自动强制全量
重新暂存，使签名的信任列表在每个节点上重新固定（re-pin））。

**安全护栏。** 一次会清空设计或丢弃 ≥ 50% 节点的 Deploy 需要键入项目名以确认。控制器保留**最近 10 个拓扑
版本**用于恢复，并用一份只追加、哈希链式的**审计日志**记录每次 enroll/revoke/stage/promote/rekey
（`/telemetry` 心跳刻意**不**审计——30 秒一跳会淹没链）。

**集群级密钥轮换（Roll keys）。** 轮换复用同一模型，分四步：(1) `rekey-all` 标记每个已批准节点并推进 generation
以*唤醒*停泊的 agent；(2) 每个被唤醒的 agent 重新生成自己的私钥并登记新的**公**钥（跳过那份陈旧的唤醒配置包）；
(3) 你等待每个“rotating keys（轮换密钥中）”徽章清除（其间 Deploy 被禁用）；(4) 一次普通的 Deploy 用新公钥重新
编译。代价是一次短暂的滚动式逐链路抖动。

### 5.6 节点 agent — 拉取、验证、应用

agent（`cmd/agent`）是 `install.sh` 之上的一层薄薄的“先验证再应用”包装，而非一个 reconciler：

1. **keygen**（一次性）在本地生成一对 WireGuard 密钥；**私钥**留在 `/etc/wireguard/agent.key`（权限 0600），
   永不离开主机。
2. **poll/pull** —— 长轮询 `GET /poll?after=<watermark>` 会阻塞直到存在更新的 generation（204 表示“无变化”）；
   随后 `GET /config` 返回已提升的配置包。
3. **verify** —— agent 重算规范化的 `checksums.sha256`，用运营者预置（pinned）的公钥凭据验证 keystone 签名，
   并复核每个文件的哈希。任何不匹配都会在任何东西以 root 运行**之前**被**硬性拒绝**。
4. **apply** —— 对一份已验证、未回滚的配置包，agent 运行配置包自带的 `install.sh`，由它把本地持有的私钥拼接进
   被拷贝配置中的占位符（见[第 8.3 节](#83-零知识密钥托管)）。
5. **report** —— `POST /report` 记录已应用的 generation/校验和/健康（尽力而为）。

在第 4 步之前，Linux agent 会把候选配置包、动作与信任锚精确写入独立的 `PendingApply` 预写记录并持久同步，
同时保留上一次成功状态。安装器 guardian 会继承状态目录的同一把锁，因此即使 Go 父进程被杀死，重启后的 daemon
或手动 `kit apply` 也不能与仍在运行的 root 脚本重叠。失败或中断后只能重新同步并精确重试同一候选，或使用经过
完整验证且严格更新的同类动作；成员关系/防回滚下限和信任锚都不能被静默重置。root 应用仅支持 Linux；Windows
仍可使用跨平台的验证和密钥命令，但会拒绝执行 `install.sh`。

`run --controller` 默认是单次的（一次 poll→apply→report 循环）；`--daemon` 让它持续循环，这也是 bootstrap 所
安装的形态。一个防回滚高水位线会拒绝 `manifest.json` 构建时间不晚于上次已应用的配置包。

### 5.7 签名的 agent 自更新 + 版本感知滚动发布

agent 可以把**自己的二进制**替换为已验证配置包中 `artifacts.json` 所固定（pinned）的版本（该文件本身被配置包
签名覆盖）。下载的二进制会针对**签名内的 SHA-256 pin** 验证（绝不针对上游 `.sha256` 旁文件），并在 exec 前
通过一次**自检**（`<新二进制> version` 必须等于目标版本），且整个替换是有崩溃上限的：`Restart=always` 循环被
限制在 3 次尝试，之后 agent 回滚到保存的 `.bak`。一次健康检查把新版本标记为**试用期（probationary）**，且
**防降级下限**只有在一次完整、干净的循环之后才推进。

从面板：一键 **“将所有 agent 更新到 {version}”** 以控制器自身版本为目标，装配一次**金丝雀-然后-全量
（canary-then-fleet）** 滚动发布，且控制器**拒绝比自己更新的目标**。卡住的滚动发布会以 `selfupdate: Blocked`
状态浮现，并附带可操作的原因。

### 5.8 实时集群健康 — Node Conditions + `/telemetry` 心跳

agent 在一个专用的 `POST /telemetry` 心跳上回报结构化的 **Node Conditions**——Kubernetes 风格的
`{type, status, reason, message}`（默认 **30 秒**，由 agent 的 `--telemetry-interval` 标志设置；`0` 关闭；
心跳仅在 daemon 模式下进行）。心跳实时刷新这些状态，使面板反映*当前*健康，而非应用时定格的快照。它携带 conditions
外加一个可扩展的 `metrics` 映射，并刻意**绝不**触碰部署托管字段（已应用 generation/校验和）——可观测性与部署
状态严格分离，且心跳不被审计。

**短时中断使用有界重放，而不是第二套传输。** JSON 正文与旧控制器保持兼容；可选的 v2 协议头添加每进程 boot ID、
序号、采样时间与节奏。上传遇到传输错误、408/429 或 5xx 重试时，采样仍会继续，样本保存在一个易失的 32 项队列中。
控制器在内存中去重精确重试，并始终以控制器接收时间作为 `LastSeen` 的权威。如果 CDN/代理剥离这些扩展头，请求会
安全降级为旧版投递，只是失去精确去重。不需要新增 WebSocket 或 gRPC 端点。

四个 condition **类型（type）**（小写、闭集）及其 `status`（`ok`/`warn`/`error`/`unknown`）：

| 类型 | 报告内容 | 主要原因（reason） |
|---|---|---|
| `configapply` | 最近一次配置应用 | `Applied`（ok）、`DegradedKeepingLastGood`（warn） |
| `selfupdate` | 自更新状态 | `Active`、`HealthConfirmedProbationary`、`Updated`、`Abandoned`、`Blocked` |
| `wireguard` | 链路健康 | `AllPeersUp`（ok）、`PeerHandshakeStale`、`SomePeersDown`、`LinkDown`、`NoInterfaces` |
| `mimic` | mimic shaper 状态 | 面包屑（breadcrumb）+ 每次心跳实时重探（`systemctl is-active`）：若应当在运行的单元自部署后已停止则报 `Stopped`（warn），否则为部署结果（`Active` / `FellBackToUDP` / `ModuleUnavailable` / `NativeDowngradedSkb`） |

> **`SomePeersDown` 对比 `LinkDown`（beta.12）。** 网格中单个离线的 peer（一条 Babel 会绕过的链路）现在读作
> **`SomePeersDown`**（“1/3 peers down”），而非令人惊慌的整机 **`LinkDown`**；`LinkDown` 保留给*所有* peer
> 都掉线（或首次握手前的全新应用）的情形。

**各 Peer 的“WireGuard links”面板（beta.12）。** 节点详情页显示一个**可折叠**的“WireGuard links”面板——它是
聚合 `wireguard` 状态背后的逐链路细节。每一行是一个 peer，带一个状态点（绿 = up / 黄 = stale / 红 = never）和
一个相对的、实时跳动的最近握手时间。仅当某链路掉线/陈旧时它才自动展开；全部在线的节点保持折叠。数据搭载在心跳的
`metrics["wireguard_peers"]` 映射上（peer / interface / endpoint / last_handshake / status——无密钥材料）。
这份遥测是**仅实时（live-only）**的：它在刷新时重新获取，并刻意**不**持久化到浏览器（定格的握手时间会误导，
而原始端点属于集群机密）。

**主机资源指标（`resource`）。** 每次心跳还携带一个 `resource` 指标：`cpu_pct`（可选）、`load1`/`load5`/
`load15` 三个负载均值（load average），以及内存总量/可用量。`cpu_pct` 是把 CPU 利用率测为**相邻两次心跳
之间的差值**，因此 agent（重）启动后的**第一跳**不带 CPU 值——这是一处刻意的**空缺，而非 0**（真实的
0% 与“尚无法测量”绝不能看起来一样）；此后每一跳都带有它。

**CPU/内存/负载历史图表。** 控制器为每个节点保留一份有界的 `resource` 指标**历史**，节点详情页把它随时间
绘成图表：CPU %、内存使用率 %，以及负载均值。你选择一个**时间范围**与一个**粒度**（步长 step），服务器把
原始样本聚合进各个桶（每桶给出平均/最小/最大），并**略去空桶**——数据中的空缺（节点离线、历史刚启用）在
图上仍是空缺，绝不伪造 0。过细的步长会被限制在约 1 秒的下限，而在大范围上过细的步长会被自动放宽，使图表
保持有界；实际生效的步长会被回传，因此坐标轴标注的是你真正得到的粒度。
选择**自动（Auto）**粒度时，服务器优先采用最近一次上报的心跳间隔；若没有，则从观测时间差中取抗离线空档的较低
中位数（并避免把部署后立即推动的短间隔误认为长期节奏），下限为 30 秒。缺失某项指标的桶始终显示为空缺。为避免
健康样本恰好落在相邻桶边界两侧时造成假断线，允许跨过一个空的有效桶；更长的连续空缺会让曲线断开，不会跨过离线
区间强行连线。

**保留量可配置。** 控制器 **Settings（设置）** 中的每节点样本上限限定历史长度；默认 ≈ 20160 个样本 ≈
**30 秒心跳下的 7 天**。把上限设为 **0 即禁用**历史——此时图表显示“历史已关闭”状态。历史是只追加的，且
**绝不阻塞心跳**（心跳绝不写盘）。它也绝不定格：刚部署的节点其图表会**及时**更新（不只在部署时），因为
心跳是唯一的数据来源，且 agent 会在每次 apply 之后立即推动一次新样本。

**签名主动遥测。** 在 **Fleet（集群）** 的节点详情页中，可以配置多条 ICMP Echo 或 TCP 连接检测，并在同一位置
查看最近结果。每条只需一个必填的目标 `host`，可填写 IP 字面量或 DNS 主机名；没有单独或强制的 DNS 字段。TCP
另需端口。保存只更新控制器设计草稿，**Deploy（部署）** 才是签名与启用边界。带主动探测的部署必须有已固定的离线
Keystone，agent 在启用前还会再次验证成员资格。DNS 名称由节点在每次尝试时重新解析。ICMP 需要原始套接字权限；若
不可用会明确报告权限失败。TCP 与 ICMP 均为进程内实现，不依赖外部 `ping`/`tcping` 包。未来 URL 探测仍应是独立的
类型。详见[主动遥测规范](spec/operations/active-telemetry.md)。

### 5.9 Mimic `.deb` 目录

对于不打包 mimic 的发行版（Debian 12 / Ubuntu 24.04），面板按 `<codename>-<arch>` 以 SHA-256 固定（pin）
mimic 的 `.deb` 包。上游发布了**两个**必须一起固定的包：`mimic`（工具本体）与 `mimic-dkms`（其内核模块——
缺了它 `mimic` 包无法安装）。**Discover from release（从发布发现）** 会列出某个 GitHub release 的 `.deb` 资产，
并把同一 `<codename>-<arch>` 的 `mimic` 与 `mimic-dkms` **配对**到一行；**Assist（辅助）** 会填入两者的
SHA-256（若代理漏取某个 sidecar，会重试直连 GitHub）。安装时会下载、按签名 pin 校验每个包，并把**两者一起**
安装后再 `dpkg`。

若某节点内核过旧、无法编译模块（其精确的 `linux-headers` 已不在仓库中——在数月前启动的 VPS 上很常见），节点编辑器
会**提前警告**（"此内核无法构建 mimic 模块——请重启进入当前内核"）；在你重启之前，该链路会按其 **Mimic 回退（Mimic
fallback）** 策略处理：*回退到 UDP* 会以纯 UDP 建立链路，*失败即关闭* 会让链路保持断开并显示清晰的 `mimic` 健康标签
——绝不静默断连。修复：在该节点上执行
`apt-get update && apt-get install -y linux-image-cloud-amd64 linux-headers-cloud-amd64 && reboot`，然后重新部署。

**XDP 模式（native 与 skb）。** mimic 链路默认使用通用的 **skb** XDP（任何网卡都可用）。你可以在节点编辑器里把某
节点切到 **native** XDP（更快）——但很多 VPS 网卡不支持，因此当 native attach 失败时 YAOG 会自动降级为 skb
（链路照常建立）。节点编辑器会**始终**显示每个节点的 native-XDP 支持情况（便于你在选择 native 之前就能看到），节点的
`mimic` 健康标签会显示实际生效的模式。

**出口网卡。** 默认情况下 mimic 绑定到节点的默认路由网卡（自动检测）。在多网卡/策略路由、WireGuard 出口不是默认路由的
节点上，可在节点编辑器里设置该节点的 **Mimic 出口网卡**（如 `wan0`）；留空 = 自动检测。

### 5.10 配置参考

控制器行为通过容器上的环境变量配置（在 `docker-compose.yml` 中设置），外加少量在面板中编辑的服务器存储设置。

| 变量 | 默认 | 作用 |
|---|---|---|
| `YAOG_BIND_ADDR` | `127.0.0.1` | 仅 compose：两个发布端口绑定的宿主机接口。`0.0.0.0` 可暴露到 loopback 之外。 |
| `YAOG_PANEL_PORT` | `8080` | 仅 compose：运营者/面板 API 在宿主机上发布的端口。 |
| `YAOG_AGENT_PORT` | `9090` | 仅 compose：agent API 在宿主机上发布的端口。 |
| `YAOG_CONTROLLER_STATE_DIR` | 未设置 | 控制器状态目录。与 `YAOG_TENANT_ID` 一起，是开启控制器模式的开关（镜像设为 `/data`）。 |
| `YAOG_TENANT_ID` | 未设置 | 限定所有控制器状态的租户标识（目前单租户）。 |
| `YAOG_CONTROLLER_AGENT_ADDR` | `:9090` | 面向节点的 agent API 的监听地址。 |
| `YAOG_OPERATOR_PATH_PREFIX` | 空 | 运营者 API（`:8080`）的可选密钥路径前缀。 |
| `YAOG_AGENT_PATH_PREFIX` | 空 | agent API（`:9090`）的可选密钥路径前缀，与运营者前缀相互独立；bootstrap 命令会把它烘焙进 agent 的 URL。 |
| `YAOG_PANEL_ORIGIN` | 空 | 允许携带凭据跨源访问面板的源（origin）逗号分隔白名单（仅当面板来自不同源时需要；需 HTTPS）。 |
| `YAOG_SECURE_COOKIE` | `true` | 会话/CSRF cookie 的 `Secure` 属性。仅本地非 TLS 开发时设为 `false`。 |
| `YAOG_CONTROLLER_OPERATOR_TOKEN` | 未设置 | 可选的应急运营者令牌（恢复通道）。仅保存其 SHA-256。 |
| `YAOG_BUNDLE_SIGNING_KEY` | 未设置 | 指向 Ed25519 PKCS#8 PEM 的路径。设置后每个配置包都携带分离签名，且 `install.sh` 固定该公钥；加载是 fail-closed。 |
| `YAOG_WEB_DIR` | 未设置 | 服务器据以服务面板 SPA 的目录（镜像设为 `/app/web`）。 |

> **密钥路径前缀**把两类受众挂在不同命名空间下——运营者在 `/<operator-prefix>/api/v1/operator/`、agent 在
> `/<agent-prefix>/api/v1/agent/`——因此基于路径的代理可把各自路由到各自端口，你也可以只公开 agent 端点。这是
> 纵深防御式的**隐蔽**，**不是**安全边界；真正的边界是 bearer 令牌与 keystone 签名。旧的单个
> `YAOG_CONTROLLER_PATH_PREFIX` 已移除——若仍设置则服务器拒绝启动。面板的“Secret Path Prefix”字段只镜像
> **运营者**前缀。

完整参考：[spec/controller/](spec/controller/)（从 `controller-api.md` 与 `agent.md` 开始）。

---

## 6. 编译器工作原理

两种模式使用同一套 Go 编译器代码。`internal/localcompile` 拥有规范的
`GenerateKeys → CompileAt → render.AllWith → artifacts` 序列；`cmd/wasm`、`cmd/compiler` 与控制器子图编译都从
这个门面进入。它是显式输入的**纯函数**：门面内不读时钟、不访问文件系统，也不依赖全局状态。这使本地 Go 与
Go/WASM 的执行都可复现并可直接比对。

### 6.1 编译流水线

编译器分多趟处理拓扑：

1. **Schema 校验** —— JSON 结构：必填字段、类型、引用有效性。
2. **语义校验** —— 逻辑一致性：IP 冲突、孤立节点、非法引用、CIDR 有效性。
3. **IP 分配 + 能力推导 + Peer 推导** ——
   - *IP 分配器*（`internal/allocator/ip.go`）：为没有手动 IP 的节点从网域 CIDR 顺序分配 overlay IP，跳过
     网络/广播/保留地址。
   - *能力推导*（`internal/compiler/roles.go`）：从角色派生能力字段。
   - *Peer 推导*（`internal/compiler/peers.go`）：把边转换为各节点的 `PeerInfo`（见[第 6.2 节](#62-peer-推导)）。
4. **配置渲染** —— 四个渲染器外加部署脚本：

   | 渲染器 | 输出 | 源文件 |
   |----------|--------|--------|
   | WireGuard | 每个 peer 一份 `.conf`（client 为单个 `wg0`） | `internal/renderer/wireguard.go` |
   | Babel | 每节点一份 `babeld.conf` | `internal/renderer/babel.go` |
   | sysctl | `99-overlay.conf` | `internal/renderer/sysctl.go` |
   | 安装脚本 | `install.sh` | `internal/renderer/script.go` |
   | 部署脚本 | AirGap `deploy-all.sh` + `.ps1`；AgentHeld fail-closed 指引 | `internal/renderer/deploy.go` |

5. **产物导出**（`internal/artifacts/export.go`）—— 把一切组织进各节点目录，附带 manifest 与校验和。

### 6.2 Peer 推导

Peer 推导把拓扑的边转换为具体的 WireGuard peer 配置。

- **输入 → 输出：** 拓扑（节点 + 边）+ 密钥对 → `map[nodeID][]PeerInfo`。
- **两趟算法。** 第一趟按节点对预分配：监听端口（各节点递增偏移）、transit IP、IPv6 链路本地地址，双向存储。
  第二趟再次遍历边，查出预分配的资源，并用正确的已分配端口构建 `PeerInfo`。
- **端点解析。** 正向 peer 使用边的 `endpoint_host` + 目标侧已分配端口。反向 peer 若存在反向边（`B→A`）则用之；
  否则没有端点，依赖正向侧发起。
- **PersistentKeepalive。**

  | 条件 | Keepalive |
  |-----------|-----------|
  | 节点可接受入站 且 存在反向边 | 0（关闭） |
  | 节点在 NAT 之后（无法接受入站） | 25 秒 |
  | 无反向边（单向） | 25 秒 |

- **Transit IP 分配。** 每对节点从其网域的 `transit_cidr`（默认 `10.10.0.0/24`）取得一对：链路 0 →
  `10.10.0.1` ↔ `10.10.0.2`；链路 N → `10.10.0.(2N+1)` ↔ `10.10.0.(2N+2)`。
- **监听端口分配。** 每个节点从 `listen_port`（默认 51820）起步，为每个额外 peer 接口向上补空隙分配。
- **固定（sticky）分配。** 一旦某链路的端口、transit IP 与链路本地地址选定，编译器就把它们作为 `pinned_*` 字段
  写回边，并在下次编译时逐字复用。这使你新增节点时既有服务器保持字节稳定。先预留 pin、再补空隙的完整契约与
  不变式见 [spec/compiler/allocation-stability.md](spec/compiler/allocation-stability.md)。

### 6.3 Babel 路由集成

Babel 是让多跳 overlay 网络得以工作的动态路由守护进程；当某节点的网域设置了 `routing_mode = "babel"` 时它便
运行。

**Router-ID 生成：** `SHA-256(node_id)` → 取前 6 字节作为 MAC-48；置本地管理位（`| 0x02`）、清组播位
（`& 0xFE`）。稳定（同节点 → 同 ID）且分布良好；手动的 `router_id` 可覆盖它。

**接口声明：** 每个 per-peer WireGuard 接口被声明为一个 Babel tunnel 接口，例如
`interface wg-beta type tunnel hello-interval 4 update-interval 16`。hello/update 间隔与 `rxcost` 来自按角色的
Babel 预设（`internal/renderer/babel_presets.go`）；边的 `priority`/`weight` 覆盖链路开销。

**再分发（redistribution）** 使用两种机制（`internal/renderer/babel.go`）：

- `redistribute local ip <prefix> allow` —— 用于由 `dummy0` connected 路由支撑的前缀：节点自身的 overlay
  `/32`，以及（router 侧）注入的 client `/32`。
- `redistribute ip <prefix> allow`（无 `local`）—— 用于由真实内核路由（而非 `dummy0` connected 路由）支撑的
  前缀：`extra_prefixes`（LAN 段）与网关的 `0.0.0.0/0` 默认路由。正是这种无 `local` 的形式使它们能匹配内核
  路由并传播。

末尾的 `redistribute local deny` 可防止误通告 transit IP 或系统路由。

### 6.4 密钥管理与持久化

WireGuard 密钥是**持久的**，并非每次编译都重新生成。

- **本地 / air-gap（AirGap 托管）。** 新节点首次编译会生成一对密钥，并把**两把**密钥写回拓扑 JSON 中的该节点
  （私钥按设计往返，使无状态编译器能重新渲染该节点自己的 `Interface PrivateKey`）。之后每次编译复用这对密钥，
  因此新增无关节点永不轮换某把密钥。轮换是显式的：清空**两个**密钥字段（强制重新生成）或粘贴一把不同的私钥。
  一个携带公钥但无私钥的节点是硬错误。由于拓扑（以及浏览器 localStorage）携带活私钥，须将其视为机密材料。
- **控制器（AgentHeld 托管）。** 控制器仅从**公钥**渲染——每个节点的 `[Interface] PrivateKey =` 行渲染为占位
  符，由已注册 agent 或经验证的手动 `kit apply` 路径在安装时把节点本地持有的私钥拼接进去。控制器永远看不到
  私钥。见[第 8.3 节](#83-零知识密钥托管)。

完整契约：[spec/data-model/node.md](spec/data-model/node.md) 与
[spec/compiler/allocation-stability.md](spec/compiler/allocation-stability.md)。

---

## 7. 生成产物

### 7.1 配置包目录结构

AirGap 导出在 CLI、浏览器/WASM ZIP 与部署 helper 之间统一使用稳定 node ID 作为目录键。浏览器把两个项目级
helper 与节点目录写入**同一个 ZIP、同一次编译**：

```
artifacts.zip/
  ├── deploy-all.sh           # AirGap 集群 helper；AgentHeld 中为 fail-closed 指引
  ├── deploy-all.ps1          # 与下列所有目录来自同一次编译
  └── node-alpha/             # 稳定 node ID（不是显示名称）
      ├── wireguard/
      │   ├── wg-beta.conf    # 通往 beta 的 WireGuard 隧道配置
      │   └── wg-gamma.conf   # 通往 gamma 的 WireGuard 隧道配置
      ├── babel/
      │   └── babeld.conf     # Babel 路由守护进程配置
      ├── sysctl/
      │   └── 99-overlay.conf # 内核参数（转发、rp_filter）
      ├── install.sh          # 带完整性门禁的安装脚本
      ├── manifest.json       # 构建元数据与文件清单
      ├── checksums.sha256    # SHA-256 完整性校验
      └── README.txt          # 针对托管模式的快速上手说明
```

控制器保存相同的规范化各节点文件集。手动节点端点一次只服务一个已选节点，所以下载名为
`<node-id>-bundle.zip`，并把该节点文件直接放在归档根目录，而不再套一层冗余目录。设置
`YAOG_BUNDLE_SIGNING_KEY` 时，可选的配置包签名会加入 `bundle.sig` + `signing-pubkey.pem`；启用 keystone 的
AgentHeld served snapshot 还携带 `trustlist.json` + `trustlist.sig`；配置了自更新时，其 pin 位于
`artifacts.json`。agent/kit 会在应用前验证每一个适用层。

### 7.2 WireGuard 配置详解

一份 per-peer 配置（服务器类角色）：

```ini
# WireGuard per-peer interface: wg-beta
# Node: node-alpha -> Peer: node-beta

[Interface]
PrivateKey = <private_key>          # AgentHeld 占位符；由 agent / 经验证的 kit apply 拼接
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

`client` 角色的节点改为获得一个单一的 `wg0`，只有一个 peer（它的上游 router/relay/gateway），没有 `dummy0`、
也没有 Babel。

关键设计点：

- **`Table = off`** —— wg-quick 不添加内核路由；若 `AllowedIPs = 0.0.0.0/0`，每个接口本会争抢默认路由。路由
  全部由 Babel 管理。
- **`AllowedIPs = 0.0.0.0/0, ::/0`** —— 在 per-peer 模型下安全（每接口一个 peer）；由 Babel 决定走哪条隧道。
- **`PostUp`/`PostDown`** —— 添加 Babel 邻居发现所需的 IPv6 链路本地地址。

### 7.3 安装脚本逻辑

`install.sh` 是一个幂等的、分阶段的部署。下列直接命令仅适用于**本地信任的 AirGap** 目录：

```bash
sudo bash install.sh              # 安装 / 升级 overlay
sudo bash install.sh --uninstall  # 从本节点彻底移除 overlay
```

**`--uninstall` / `-u`** 会彻底拆除：停止并禁用所有受管及遗留 WireGuard 接口、移除 `/etc/wireguard/` 配置、
停止 Babel 并移除其配置/override、移除 overlay SNAT 规则与 `overlay-snat.service`、还原 sysctl 默认值、移除
`dummy0` 及其 `overlay-dummy.service`，并重载 systemd。

**正常安装阶段：**

- **阶段 0 — 清理。** 停止/移除既有 WireGuard 接口与旧配置。一次全面的遗留清扫会扫描所有 `wg*` 接口与
  `/etc/wireguard/*.conf`，移除一切不归当前 overlay 管理的内容（捕获 `wg0`、`wg1`、`wg-overlay` 等残留）。
  停止 Babel；移除旧 sysctl。
- **阶段 1 — 环境准备。** 校验 checksum；检查 root；探测 OS；安装依赖（`wireguard`、`wireguard-tools`、
  `babeld`）；创建 `dummy0` 并分配 overlay IP；安装一个 systemd 单元以持久化 `dummy0`；配置 overlay SNAT
  （见[第 7.4 节](#74-dummy0--tableoff--snat-修正)）。
- **阶段 2 — 部署配置。** 把 WireGuard 配置拷到 `/etc/wireguard/`、Babel 配置拷到 `/etc/babel/`、sysctl 配置
  拷到 `/etc/sysctl.d/`。
- **阶段 3 — 激活与验证。** 应用 sysctl；启动各 `wg-quick@<iface>`；安装 babeld systemd override（依赖所有
  WireGuard 服务）；启动并启用 babeld；打印状态摘要。

AgentHeld 配置包必须通过已注册 agent，或第 5.4 节所述、经验证的手动 `yaog-agent kit apply` 路径到达此脚本；
直接执行会绕过离机成员关系门禁与持久防回滚状态。

当通过 `YAOG_BUNDLE_SIGNING_KEY` 配置了配置包签名时，脚本会在运行 `sha256sum -c` **之前**用内嵌公钥校验
`bundle.sig`；签名构建的 `install.sh` 若缺少签名会被视为篡改并拒绝。这层配置包签名与 AgentHeld keystone
成员关系检查不同；后者由 agent/kit 在脚本运行前完成。

### 7.4 dummy0 + Table=off + SNAT 修正

`dummy0` 承载 Babel 通告的稳定 overlay IP（应用与 DNS 始终指向这里）。每个 `wg-*` 接口都是 `Table = off`，
因此由 Babel——而非 wg-quick——安装与移除内核路由并处理链路故障切换。

**源地址问题。** 每个 `wg-*` 接口都有一个 transit IP（例如 `10.10.0.3/32`）。当内核向某 overlay 目标发包时，
Babel 经某个 `wg-*` 接口路由它，而内核挑选了 **transit IP** 作为源地址——而非 `dummy0` 上的 overlay IP。发往
transit IP 的回包不可路由（transit IP 未被通告），因此 `ping 10.111.0.3` 会静默失败，而
`ping -I 10.111.0.2 10.111.0.3` 正常。

**修正。** 安装器添加一条 SNAT 规则，把从 `wg-*` 接口外发、源为 transit（`10.10.0.0/24`）的包改写为节点的
overlay IP：

```
# nftables（优先）：
table inet overlay-snat {
    chain postrouting {
        type nat hook postrouting priority srcnat; policy accept;
        oifname "wg-*" ip saddr 10.10.0.0/24 snat to <overlay_ip>
    }
}

# iptables（回退）：
iptables -t nat -A POSTROUTING -o wg-+ -s 10.10.0.0/24 -j SNAT --to-source <overlay_ip>
```

安装器自动探测 `nft` 并回退到 `iptables`；一个持久的 `overlay-snat.service` 使该规则跨重启存活。要手动修正既有
部署，运行等价规则并把 `<overlay_ip>` 替换为该节点的 overlay IP 即可。

### 7.5 自动部署脚本

一次 **AirGap** 编译会生成两个项目级部署脚本：`deploy-all.sh`（Bash）与 `deploy-all.ps1`（PowerShell）。
浏览器/WASM 导出会把这些准确脚本与其所期待的完整、node-ID-keyed 目录一同放在 ZIP 根目录，因此之后的另一轮
编译不会让 helper 与归档互相不匹配。

```bash
bash deploy-all.sh path/to/artifacts.zip            # 部署
bash deploy-all.sh --clean path/to/artifacts.zip    # 先清掉既有 WG 配置
bash deploy-all.sh --uninstall                      # 在所有节点上拆除 overlay（无需 ZIP）
```

```powershell
.\deploy-all.ps1 -ArtifactsZip path\to\artifacts.zip
.\deploy-all.ps1 -ArtifactsZip path\to\artifacts.zip -Clean
.\deploy-all.ps1 -Uninstall
```

它们会在解压前拒绝不安全、含歧义、符号链接或特殊文件的 ZIP 条目。对每个配置了 SSH 的节点，helper 查找
`<node-id>/install.sh` 与必需的 `<node-id>/checksums.sha256`，在目标上创建全新的
`/tmp/yaog-<node-id>-*` 目录，递归上传**完整**配置包，调用其带完整性门禁的安装脚本，清理暂存目录，并记录
结果。`--clean` 只会转交给通过签名/checksum 门禁后的 `install.sh`；helper 不会先摧毁 last-good 布局。

未配置 SSH 的节点会被跳过。SSH 在设置了别名时用 `ssh <alias>`，否则用
`ssh -p <port> -i <key> <user>@<host>`；不支持密码认证。这些 helper 在 AgentHeld/控制器渲染中会被刻意
禁用：受管节点使用 stage/promote 加 agent，手动节点使用经验证的 `kit apply`（卸载时也加 `--uninstall`）。

### 7.6 画布可视化

编译后画布会显示：**多接口句柄**（上 = 入站，下 = 出站），每个 per-peer 接口一个，循环配色，带悬停提示（接口名、
监听端口、peer 名）；**节点信息卡**带与句柄配色一致的 `<peerName>:<port>` 彩色标签；以及**边标签**
`<source> → <target> | <endpoint>`，按类型配色（direct = 青，public-endpoint = 琥珀，relay-path = 紫，
candidate = 灰）。

---

## 8. 安全模型

YAOG 的安全建立在一个刻意的拆分上：一次无人值守的控制器沦陷绝不能 (a) 伪造集群**成员关系**，或 (b) 窃取任何
WireGuard **私钥**。运营者面板鉴权只守护面板——它**不是**网络信任锚。权威说明见
[spec/security/security.md](spec/security/security.md) 与 [spec/controller/](spec/controller/)。

### 8.1 两个截然不同的签名角色（切勿混淆）

| | **离线部署 keystone** | **配置包签名密钥**（`YAOG_BUNDLE_SIGNING_KEY`） |
|---|---|---|
| 持有者 | 运营者持有的浏览器 WebAuthn 凭据，或原始 Ed25519 CLI 密钥 | **服务器侧**的 Ed25519 PEM 文件（或 air-gap 导出主机） |
| 签什么 | 规范化的**信任列表字节**（谁被信任），通过内容绑定的 WebAuthn 断言或原始 Ed25519 签名 | 每个节点配置包规范化的 `checksums.sha256` 字节 |
| 关闭的威胁 | 被沦陷的控制器**单凭自身无法向集群推送成员变更** | 已渲染配置包的真实性/完整性（前提是有带外 pin） |
| 服务器持久化 | 仅**非机密的公开**运营者凭据（描述符 + 公开 PEM） | **绝不**持久化私钥——只持久化公钥，作为每租户的 pin |

浏览器 keystone 与登录 passkey 共用底层 WebAuthn 断言验证器：keystone 用内容哈希，登录 passkey 用随机
nonce。原始 Ed25519 CLI keystone 与配置包签名密钥使用 Ed25519，而非 WebAuthn。

### 8.2 离线签名 keystone

改变**谁被信任**（准入、驱逐或为节点轮换密钥）需要一份覆盖*变更内容本身*的签名——规范化信任列表字节加一个
单调版本号的哈希。面板产生 WebAuthn 断言；CLI 兼容的原始 Ed25519 路径产生分离签名。控制器只持久化非机密的凭据描述符与公钥，
收到的是已签名证明而非凭据私钥材料。该公开凭据被烘焙进
agent bootstrap 脚本并以 `--operator-cred` 传入，因此**节点在应用之前会验证签名**。结果：只有无人值守的控制器沦陷时，
它**没有自主能力**去改变集群成员关系。“keystone 开 vs 关”是一种部署姿态；keystone 关意味着节点不强制要求签名成员关系
（仅限开发）。

> **凭据托管局限。** YAOG 不接收 WebAuthn attestation，因此无法证明凭据由硬件支撑或不可导出。软件或同步凭据可能被其提供方复制。
> 新的浏览器登录 passkey 与浏览器 keystone 注册会在服务器挑战的证明中要求用户验证，面板也会提示复制风险；之后的登录与签名检查
> 仍接受有效的用户在场断言，避免通过追加的 UV 要求锁死已有凭据。原始 Ed25519 CLI keystone 没有浏览器仪式，此次保持不变。

### 8.3 零知识密钥托管

**保证：** 任何控制器渲染的配置包都绝不包含可解析的 WireGuard 私钥；注册表只存**公钥**。渲染有两种托管模式
（`render.GenerateKeys`）：

- **AirGap**（本地 / CLI 默认）—— 私钥经拓扑 JSON 往返以实现无状态的密钥稳定性。因此拓扑与浏览器 localStorage
  携带私钥，须视为机密材料。
- **AgentHeld**（控制器）—— `GenerateKeys` 永不返回真实私钥；每个节点的 `[Interface] PrivateKey =` 是一个
  故意无效的占位符，由已注册 agent 或经验证的手动 kit 路径在安装时拼接节点本地持有的私钥。网络配置仍与
  AirGap 等价，但 README/部署指引会按托管模式刻意不同，从而不让任何项目 helper 绕过成员关系验证。

强制是双保险（belt-and-braces）：面板在**每次 `update-topology` POST 之前剥除私钥**，且服务器**拒绝（400）**
任何携带非空 `wireguard_private_key` 的拓扑。常驻测试门禁对两者都做断言。

### 8.4 运营者认证

- **初始化** —— 账户由 `create-operator` 带外创建；密码用 argon2id 哈希（明文绝不存储或记录）。
- **登录** —— `POST /login` 签发一个 256 位会话（仅存其 SHA-256，TTL 12 小时），并设置一个**跨刷新存活的
  httpOnly `yaog_session` cookie**，Web 存储中不放任何 token；面板从 `GET /session` 重新推导状态。
- **CSRF** —— 双重提交（double-submit）：登录设置一个可读的 `yaog_csrf` cookie 并返回 `csrf_token`；每个 cookie
  路径的状态变更请求必须在 `X-CSRF-Token` 中回显它（常量时间比较）。Bearer 路径与 GET 免除。
- **CORS** —— `YAOG_PANEL_ORIGIN` 是允许携带凭据跨源访问的精确源白名单；通配符绝不与凭据一同发送。同源 Docker
  无需设置。
- **TOTP（RFC 6238）** —— 标准库 HMAC-SHA1，仅登录用的第二因子；防重放、±1 步漂移。诚实的局限：该秘密是对称的
  且存于静态——属于便利，弱于 passkey，且**绝不**是 keystone 签名因子。
- **Passkey** —— 一个 WebAuthn 登录凭据（与 keystone 凭据不同）。既可作 2FA 因子（两者都注册时优先于 TOTP），
  也可用于**无密码**登录；挑战是一个单次使用、5 分钟、被原子销毁的 nonce。同步 passkey（Bitwarden/iCloud/…）
  无需硬件密钥，也可能被复制。新注册需要一个独立的已鉴权服务器挑战，并要求候选凭据产生用户验证断言；普通登录仍兼容既有的
  有效用户在场凭据。
- **应急令牌**（`YAOG_CONTROLLER_OPERATOR_TOKEN`）—— 一个可选的恢复凭据，作为 Bearer 令牌直接鉴权运营者路由并
  绕过 `/login`（从按用户名锁定中脱困的逃生通道）。
- **限速** —— 一个共享限流器为每次登录/passkey 尝试在 `user:<name>` 与 `ip:<client>` 两个维度各占一个名额
  （15 分钟内 10 次失败 → 429）；无用户名探测预言机。

> **传输是硬性要求。** `/login` 携带明文密码，且控制器讲明文 HTTP（TLS 委托给反向代理）。生产环境**必须**在
> 控制器前置一个终止 TLS 的代理。明文 HTTP + keystone 关闭的姿态没有信任锚，仅限开发（由启动告警强制，而非
> 代码层拒绝）。

### 8.5 配置包签名 — `YAOG_BUNDLE_SIGNING_KEY`

当设为一个 Ed25519 PKCS#8 PEM 的路径时，每个节点配置包都会得到一个分离的 `bundle.sig`（对规范化
`checksums.sha256` 的原始 Ed25519 签名）+ `signing-pubkey.pem`，且 `install.sh` 把验证公钥内嵌为常量。加载是
**fail-closed** ——一把已设置但不可读的密钥会中止导出，而不会悄悄发出未签名的包。控制器模式下公钥**按租户固定
（pinned）**且无静默降级：先前已固定的密钥消失 → 拒绝（412）；换了一把不同的 → 拒绝（409）。有意轮换用
`YAOG_BUNDLE_SIGNING_KEY_ROTATE`（设置一次部署后取消设置）。请把私钥排除在仓库之外并做静态保护（`chmod 600`、
`systemd-creds` 或编排器密钥库）；KMS/HSM 可通过 `ConfigSigner` 接缝接入。

> **诚实的局限。** Phase-0 签名把公钥装在配置包*内部*，因此真实性只与带外 pin 一样强：来源不可信的配置包可被
> 换上一把密钥重新签名。对于运营者自建的 air-gap 配置包（密钥是你配置的）以及 agent 预置（pinned）的 keystone
> 路径，签名是真正的来源证明；agent 预置的信任锚是更长远的设计。

---

## 9. HTTP API 参考

`yaog-server` 只有一种路由形态（见[第 3 节](#3-两种模式与构建边界)）。

### 9.1 公开路由

- `GET /api/health` —— 未鉴权的公开存活探针（`{status:"ok", timestamp}`）；仅 GET，带 CORS 与 panic 包裹。它是运营者
  mux 上唯一的匿名路由。已退役的 `/api/{validate,compile,export,deploy-script}` 计算路由不存在并返回 404。

### 9.2 控制器运营者路由（`/api/v1/operator/...`，端口 `:8080`）

除未鉴权的登录面外，均位于 `operatorAuth` 之后。要点：`login` / `login/passkey/{begin,finish}`（未鉴权）/
`logout` / `session`；`totp/*`、`passkey/*`；`webauthn/enrollment/begin`（已鉴权，按用途和运营者限定候选凭据证明）；
`update-topology`、`stage`、`compile-preview`、`promote`、
`topology`（含 `?version=N`、`/topology/versions`）；`nodes`、`revoke`、`audit`、`enrollment-token`、
`rekey-all`、`clear-rekey`；`settings`、`release-pins`、`release-assets`；`operator-credential`、`trustlist`、
`trustlist-signature`。

### 9.3 控制器 agent 路由（`/api/v1/agent/...`，端口 `:9090`）

机器对机器 JSON。`enroll`（无鉴权——单次注册令牌）与 `bootstrap`（无鉴权——通用安装器）开放；`config`、`poll`、
`report`、`telemetry`、`rekey` 需要各节点 bearer 令牌。`telemetry` 仅可观测（更新 conditions + last-seen，绝不
触碰部署托管），且不被审计。

> **状态码：** 200 OK；400 请求体损坏/为空；405 方法错误；413 请求体超过 4 MiB 上限；422 结构有效但编译失败；
> 500 keygen/渲染/已恢复的 panic。错误使用嵌套编码信封 `{"error":{"code","message","params"}}`。运营者路由上
> 出现节点令牌 → 403；已吊销节点 → 403；凭据缺失/无效 → 401。

---

## 10. 调试与故障排查

### 10.1 开发环境

```bash
./dev.sh start     # Vite 前端 :5173（设置了控制器环境变量时还会拉起 Go 服务器）
./dev.sh stop
./dev.sh restart
./dev.sh status
./dev.sh logs      # 同时跟踪两份日志
```

日志在项目根目录：`.dev-backend.log`（Go）、`.dev-frontend.log`（Vite）。纯本地设计只需在 `frontend/` 里
`npm run dev`。

### 10.2 本地模式问题

**编译失败。** 编译在浏览器内运行——查看底栏与 DevTools 控制台中的错误。常见原因：未定义网域、节点未指派网域、
非法 CIDR、孤立节点（无边）。

**节点在画布上重叠。** 把它们拖开（位置在会话内持久化）；刷新会回到默认网格。

**WireGuard 接口起不来。**

```bash
wg show                              # 所有接口
wg show wg-beta                      # 单个接口
sudo wg-quick up wg-beta             # 手动启动
cat /etc/wireguard/wg-beta.conf      # 查看配置
systemctl status wg-quick@wg-beta    # 服务状态
```

**Babel 路由不工作。**

```bash
systemctl status babeld
echo "dump" | nc ::1 33123           # 转储 Babel 路由表
journalctl -u babeld -f
ip route show table main | grep -E "^10\."
ip addr show dummy0                  # 验证 overlay 地址
```

**AirGap 安装脚本失败。**（AgentHeld/手动节点应运行 `yaog-agent kit apply` 并排查其验证错误；不要直接调用
下载的脚本。）

```bash
sudo bash -x install.sh                       # 详细模式
cd /path/to/node-dir && sha256sum -c checksums.sha256
sudo wg-quick down wg-beta 2>/dev/null && sudo bash install.sh
```

**网络检查。**

```bash
ping -c 3 10.11.0.2                  # overlay 连通性
ping -c 3 10.10.0.2                  # transit IP（隧道）
sudo wg show all | grep -A5 "latest handshake"
ping -M do -s 1392 10.11.0.2         # MTU
sudo tcpdump -i eth0 udp port 51820  # WireGuard UDP
```

### 10.3 控制器模式问题

**`yaog-server` 立即退出。** 单一服务器构建是仅控制器；请同时设置 `YAOG_CONTROLLER_STATE_DIR` 与 `YAOG_TENANT_ID`
（Docker 已做）。缺少它们时它按设计高声失败——不会回退成一个匿名计算服务器。

**`/api/validate` 或 `/api/compile` 返回 404。** 这是预期行为——这些匿名路由已从服务器移除。请用浏览器内 Go/WASM 生成器或
`cmd/compiler` CLI。（控制器模式下，校验在浏览器内运行，编译在服务端经运营者鉴权的
Deploy/preview 路径运行。）

**Passkey 注册以 “invalid domain” 失败。** 你在 `http://127.0.0.1` 上；请改用主机名 `localhost`（WebAuthn 禁止
IP 地址域名），或为远程访问在控制器前置 TLS。

**登录无法保持 / 跨源面板无法登录。** 会话是一个 httpOnly cookie，在非 localhost 源上需要 `Secure`——TLS 之后
设 `YAOG_SECURE_COOKIE=true`，并为不同源的面板设置 `YAOG_PANEL_ORIGIN`。

**某节点显示 `wireguard: LinkDown` / `SomePeersDown`。** 在节点详情页打开 **WireGuard links** 面板，查看哪个
peer 掉线及其最近握手时间。`SomePeersDown` 表示部分（非全部）链路掉线——Babel 会绕过它们；`LinkDown` 表示尚无
peer 握手成功。在节点上：`sudo wg show all | grep -A5 handshake` 与 `journalctl -u yaog-agent -f`。

**某次自更新卡住。** 一个 `selfupdate: Blocked` 状态会附带可操作的原因（常为“重新装配滚动发布，使其 pin 指向
目标构建”）。控制器拒绝比自己更新的目标；用 `journalctl -u yaog-agent -f` 查看 agent 日志。

**Agent 健康检查。**

```bash
curl http://localhost:8080/api/health        # 控制器存活
systemctl status yaog-agent                   # agent 守护进程
journalctl -u yaog-agent -f                    # agent 日志（poll/verify/apply/自更新）
cat /etc/wireguard/agent-controller.token      # 各节点 bearer 令牌（权限 0600）
```

---

## 11. 术语表

| 术语 | 含义 |
|------|---------|
| **Overlay IP** | 节点在 `dummy0` 上稳定的身份地址，由 Babel 通告。 |
| **Transit IP** | `wg-*` 接口上每条链路的点对点地址；绝不通告。 |
| **Per-peer 接口** | 每个邻居一个专属的 `wg-<peer>` WireGuard 接口（相对于单个 `wg0`）。 |
| **网域（Domain）** | 一个带分配模式与路由模式的 overlay 地址空间（CIDR）。 |
| **Generation** | 控制器单调递增的部署计数器；每次 promote 自增。 |
| **Stage / Promote** | stage 在 `gen+1` 处不可见地渲染配置包；promote 把它们翻转为 current 并自增 generation。 |
| **已注册子图** | 控制器实际渲染的、已批准且有公钥的节点（及它们之间的边）。 |
| **Keystone** | 运营者持有、用于签署信任列表/成员变更的凭据（浏览器 WebAuthn 或原始 Ed25519 CLI）；YAOG 只存储公开材料，不证明 WebAuthn 凭据由硬件支撑或不可导出。 |
| **Node Condition** | 结构化的 `{type,status,reason,message}` 健康项（`configapply`/`selfupdate`/`wireguard`/`mimic`）。 |
| **AirGap 对比 AgentHeld** | 密钥托管模式：私钥在拓扑中（本地）对比只由节点持有（控制器）。 |
| **mimic** | 一个 eBPF UDP→伪 TCP 整形器，为 UDP 不友好的网络包裹链路（transport `tcp`）。 |

---

> **规范交叉引用。** 本 Wiki 叙述系统；规范性细节位于 [`docs/spec/`](spec/)——`overview/`、`data-model/`、
> `roles/`、`compiler/`、`artifacts/`、`api/`、`frontend/`、`operations/`、`security/` 与 `controller/`。
> 从 [spec/README.md](spec/README.md) 开始。
