import { t, type MessageKey, type UILanguage } from '../../src/i18n'

// i18n.ts — the EN/ZH assertion helper for the adversarial error-state specs (plan-16 / 3.4, Phase
// 5, step 9). Error-state assertions read their EXPECTED text from the SAME catalog the panel
// renders from (src/i18n), so a spec can never drift from a copy-edit and never hard-codes a
// localized string. This is the only src import the adversarial suite makes (the i18n catalog is a
// pure presentation module — NOT controllerStore/controllerClient, which the custody boundary
// forbids).

// expectedText resolves a message key in a language exactly as t() does in the app. Use it to build
// a Playwright text matcher: e.g. page.getByText(expectedText('en', 'errorBoundary.title')).
export function expectedText(lang: UILanguage, key: MessageKey, params?: Record<string, string | number>): string {
  return t(lang, key, params)
}

export type { MessageKey, UILanguage }
