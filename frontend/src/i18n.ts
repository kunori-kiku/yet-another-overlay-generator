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
} as const;
