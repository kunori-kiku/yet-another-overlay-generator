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
