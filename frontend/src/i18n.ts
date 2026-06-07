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
} as const;
