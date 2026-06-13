import { en } from './messages/en';
import { zh } from './messages/zh';

// Extensible i18n core. Replaces the former positional txt(lang, zh, en) helper with
// a KEYED, parameterized, fallback-aware, language-extensible catalog.
//
// Adding a language is purely additive: create messages/<lang>.ts (a Partial of the
// canonical English keys), widen the UILanguage union, and register it in `catalogs`.
// No call site and no t()/tError() signature change — language is data, not control flow.

// The set of registered UI languages.
export type UILanguage = 'zh' | 'en';

// MessageKey is the compile-time set of valid keys: exactly the keys of the canonical
// English catalog. t(), tError(), and the transitional STRINGS are all keyed by it, so
// a typo or a dropped key is a BUILD error, never a silent blank at runtime.
export type MessageKey = keyof typeof en;

// Values substituted into {placeholder} tokens by t().
export type TParams = Record<string, string | number>;

// Registered catalogs. `en` is canonical (complete by construction); others are Partial
// and fall back to English per-key. `satisfies` forces every UILanguage to be present.
const catalogs = { en, zh } satisfies Record<UILanguage, Partial<Record<MessageKey, string>>>;

export function detectSystemLanguage(): UILanguage {
  if (typeof navigator === 'undefined') return 'en';
  return navigator.language.toLowerCase().startsWith('zh') ? 'zh' : 'en';
}

const PLACEHOLDER = /\{(\w+)\}/g;
function interpolate(template: string, params: TParams): string {
  return template.replace(PLACEHOLDER, (whole, name: string) =>
    Object.prototype.hasOwnProperty.call(params, name) ? String(params[name]) : whole,
  );
}

// t resolves `key` in `lang`, falling back current-language → English → the key string
// itself (a visible dev signal, never blank/undefined), then interpolates any {params}.
// `lang` is threaded explicitly from the store's `language`, so components that already
// subscribe to it re-render on a language switch — the same reactivity txt(language, …)
// had, now keyed.
export function t(lang: UILanguage, key: MessageKey, params?: TParams): string {
  const template = catalogs[lang]?.[key] ?? en[key] ?? key;
  return params ? interpolate(template, params) : template;
}

// The coded backend error envelope introduced in plan-2: { error: { code, message, params } }.
type ErrorEnvelopeObject = { code?: string; message?: string; params?: TParams };

// tError localizes a backend error response body, tolerating BOTH shapes:
//   - legacy/uncoded { error: "<string>" } (still on the wire until plan-2) → return it;
//   - coded { error: { code, message, params } } → localize via the 'error.<code>' catalog
//     key when present, else show the backend's English `message`.
// Shipping this dual tolerance in plan-1 (before the backend ever emits the coded shape)
// is the non-breaking seam: plan-2 can flip the wire shape with no synchronized frontend
// change, and the panel never renders [object Object] because nothing reads body.error
// directly — only through tError.
export function tError(body: unknown, lang: UILanguage): string {
  const err = (body as { error?: unknown } | null | undefined)?.error;
  if (typeof err === 'string' && err.trim()) return err;
  if (err && typeof err === 'object') {
    const { code, message, params } = err as ErrorEnvelopeObject;
    if (code) {
      const key = ('error.' + code) as MessageKey;
      if (key in en || catalogs[lang]?.[key] !== undefined) return t(lang, key, params);
    }
    if (message && message.trim()) return message;
  }
  return t(lang, 'error.generic');
}

// ─── Transitional shims (removed in the final plan-1 commit) ──────────────────────
// Kept ONLY so call sites not yet migrated to t() keep compiling during the txt→t
// sweep. STRINGS is DERIVED from the catalogs (no text duplicated). Both are deleted
// once every site uses t(); a grep for `txt(` / `STRINGS` is the removal gate.
/** @deprecated migrate to t(lang, key, params?) — define the key in messages/en.ts + zh.ts. */
export function txt(lang: UILanguage, zhText: string, enText: string): string {
  return lang === 'zh' ? zhText : enText;
}
/** @deprecated migrate `txt(lang, ...STRINGS.key)` to `t(lang, 'key')`. */
export const STRINGS = Object.fromEntries(
  (Object.keys(en) as MessageKey[]).map((k) => [k, [zh[k] ?? en[k], en[k]] as const]),
) as { [K in MessageKey]: readonly [string, string] };
