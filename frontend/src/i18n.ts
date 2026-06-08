export type UILanguage = 'zh' | 'en';

export function detectSystemLanguage(): UILanguage {
  if (typeof navigator === 'undefined') {
    return 'en';
  }
  return navigator.language.toLowerCase().startsWith('zh') ? 'zh' : 'en';
}

export function txt(lang: UILanguage, zh: string, en: string): string {
  return lang === 'zh' ? zh : en;
}

// 跨组件复用的 UI 文案。每条以 [zh, en] 元组存放，配合 txt(language, ...str) 展开，
// 保持与内联文案一致的双语习惯。新增需要在多处共享的文案时放在这里，避免散落重复。
export const STRINGS = {
  // UX-5：节点表单顶层“公网地址”输入（替代“公网可达”勾选框成为主入口）。
  publicAddressLabel: [
    '公网地址（其他节点如何访问到它，IP:端口）',
    'Public address (how peers reach this server, IP:port)',
  ] as const,
  // 占位示例：演示 IP 或域名，可带可选的 :端口 后缀。
  publicAddressPlaceholder: ['例: 203.0.113.10:51820', 'e.g. 203.0.113.10:51820'] as const,
  // 输入留空时的解释：该节点位于 NAT 之后、由对端主动拨入。
  publicAddressHint: [
    '留空表示该节点在 NAT 之后（由对端主动连入）。',
    'Leave empty if this node is behind NAT (peers dial in to it).',
  ] as const,
  // 画布工具栏：一键整理布局（dagre 分层布局 + 平滑动画过渡）。
  autoLayoutLabel: ['自动布局', 'Auto layout'] as const,
  // 画布工具栏：展开/收起节点卡片上的已编译接口详情（纯展示，不影响连线手势）。
  showInterfacesLabel: ['显示接口详情', 'Show interfaces'] as const,
  // 边端口标签占位：边已绘制但尚未编译，端口由后端在下次编译时分配（后端是唯一端口权威）。
  portPendingLabel: ['待分配', 'auto'] as const,
  // RightPanel 边编辑器：为选中的主链路添加一条备份链路（自成独立 WG 接口，Babel 做成本故障切换）。
  addBackupLink: ['添加备份链路', 'Add backup link'] as const,
  // 添加备份链路后的提示：备份链路当前复制了主链路的公网地址，建议改成不同地址以获得路径分集。
  backupEndpointNudge: [
    '为备份链路指定不同的公网地址以获得路径分集（当前复制了主链路的地址）。',
    'Point the backup at a distinct endpoint for path diversity (it copied the primary’s address).',
  ] as const,
  // 链路角色选择器标签（主/备）。
  roleLabel: ['链路角色', 'Link role'] as const,
  rolePrimary: ['主链路', 'Primary'] as const,
  roleBackup: ['备份链路', 'Backup'] as const,
  // 同方向意外重复边的告警徽标（非刻意创建的备份；建议改用 role: backup）。
  duplicateChip: ['重复?', 'duplicate?'] as const,
  // transport=tcp（mimic）提示：诚实定位为「UDP 受限网络」用途，不是反审查工具。
  mimicHint: [
    '用 mimic 把该链路伪装成 TCP，适用于限速/封锁 UDP 的网络；两端均需 Linux（eBPF），MTU 会自动下调。注意：这不是用于绕过审查（DPI）的功能。',
    'Wraps this link as TCP via mimic for networks that throttle or block UDP. Both ends must be Linux (eBPF); MTU is auto-lowered. Not a censorship/DPI-circumvention feature.',
  ] as const,
  // 节点级 mimic XDP 模式选择器标签。
  xdpModeLabel: ['mimic XDP 模式', 'mimic XDP mode'] as const,
  // XDP 模式提示：仅对 transport=tcp 的链路生效；部分 VPS 网卡不支持 native，故默认 skb。
  xdpModeHint: [
    '仅影响 transport=tcp（mimic）的链路。部分 VPS 网卡不支持 native XDP，故默认用 skb；确认网卡支持时再选 native。',
    'Affects only transport=tcp (mimic) links. Some VPS NICs do not support native XDP, so skb is the default; choose native only if you know this NIC supports it.',
  ] as const,

  // ── App-shell chrome (sidebar nav, top bar, theme toggle, user menu) ──
  brandName: ['YAOG 控制台', 'YAOG Console'] as const,
  primaryNavLabel: ['主导航', 'Primary'] as const,
  navOverview: ['总览', 'Overview'] as const,
  navDesign: ['拓扑设计', 'Design'] as const,
  navFleet: ['节点机群', 'Fleet'] as const,
  navDeploy: ['部署', 'Deploy'] as const,
  navSecurity: ['安全', 'Security'] as const,
  navSettings: ['设置', 'Settings'] as const,
  sidebarCollapse: ['收起侧边栏', 'Collapse sidebar'] as const,
  sidebarExpand: ['展开侧边栏', 'Expand sidebar'] as const,
  themeToggleLabel: ['切换主题', 'Toggle theme'] as const,
  themeSystem: ['跟随系统', 'System'] as const,
  themeLight: ['浅色', 'Light'] as const,
  themeDark: ['深色', 'Dark'] as const,
  userMenuLabel: ['账户', 'Account'] as const,

  // ── Route pages (P2) ──
  overviewTopologyHeading: ['拓扑', 'Topology'] as const,
  overviewControllerHeading: ['控制器机群', 'Controller Fleet'] as const,
  overviewDomains: ['域', 'Domains'] as const,
  overviewNodes: ['节点', 'Nodes'] as const,
  overviewEdges: ['边', 'Edges'] as const,
  overviewFleetNodes: ['已注册节点', 'Registered nodes'] as const,
  overviewLastDeploy: ['上次部署', 'Last deploy'] as const,
  overviewLastSynced: ['上次同步', 'Last synced'] as const,
  overviewNotSynced: [
    '尚未同步——请在「设置」中连接控制器。',
    'Not synced yet — connect a controller in Settings.',
  ] as const,
  settingsModeHeading: ['模式', 'Mode'] as const,
  settingsModeHint: [
    '选择本地/手动工作流，或控制器机群工作流。',
    'Choose the local/manual workflow or the controller fleet workflow.',
  ] as const,
  modeLocal: ['本地 / 手动', 'Local / Manual'] as const,
  modeController: ['控制器', 'Controller'] as const,
  settingsConnectionHeading: ['连接', 'Connection'] as const,
  settingsBootstrapHeading: ['Bootstrap', 'Bootstrap'] as const,
  settingsAppearanceHeading: ['外观', 'Appearance'] as const,
  settingsAppearanceComingSoon: [
    '外观设置（主题与半透明）将在后续阶段接入。',
    'Appearance settings (theme & translucency) arrive in a later phase.',
  ] as const,
  compileHistoryTitle: ['编译历史', 'Compile History'] as const,
  deployControllerHint: [
    '控制器部署：在下方的部署条进行编排、签名与推送。',
    'Controller deploy: stage, sign, and promote in the deploy bar below.',
  ] as const,
} as const;
