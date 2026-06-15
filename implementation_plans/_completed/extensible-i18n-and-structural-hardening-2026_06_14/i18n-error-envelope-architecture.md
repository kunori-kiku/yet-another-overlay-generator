# i18n + error-envelope architecture & migration note

<!-- plan-9 closeout, 2026-06-14. The durable description of the localization architecture this
     subject delivered, and what a deployer/integrator must know. -->

This subject made every operator-facing string in YAOG **localizable or English** — no Chinese
reaches an English-locale operator. There are **three localization channels**, each with its own
mechanism, plus perpetual gates that keep them from regressing.

## 1. Keyed UI i18n (the panel)

- `frontend/src/i18n/index.ts`: `t(lang, key, params?)`. `MessageKey = keyof typeof en` — a typo or
  dropped key is a **build error**, never a blank at runtime. `{param}` interpolation; fallback ladder
  current-language → English → the key string.
- Catalogs: `messages/en.ts` (canonical, complete by construction) + `messages/zh.ts` typed
  `Record<keyof typeof en, string>` so **zh parity is build-enforced** (`npm run build` fails on a gap).
- **Adding a language is additive**: create `messages/<lang>.ts` (a `Partial`), widen the `UILanguage`
  union, register it in `catalogs`. No call-site or signature change — language is data, not control flow.
- The legacy positional `txt(lang, zh, en)` / `STRINGS` helpers were fully migrated out and deleted.

## 2. HTTP error envelope (`internal/apierr`)

The wire contract is the **nested** shape — there is no top-level `error` string:

```json
{ "error": { "code": "compile_transit_pool_exhausted", "message": "…English…", "params": { "cidr": "10.20.0.0/30" } } }
```

- `internal/apierr` is a **stdlib-only leaf** (imports only `errors`/`net/http`/`strings`). Every package
  — including the deep validator/compiler/render/allocator/artifacts layers — imports it with no cycle, so
  errors are **coded at the source**: deep code returns `*apierr.Error` directly (`apierr.New(Code).With(k,v).Wrap(cause)`).
- A `Code` is a stable snake_case identifier; `registry` maps it to `{template, status}`. The `template`
  is the single source of the CLI/curl English message AND the i18n English fallback. `TestRegistryBijection`
  asserts const-set == registry-set == `allCodes`.
- **Sentinels stay internal**; the *handler* maps a `controller.Err*` (or stdlib typed error) to a `Code`
  at the route seam via the existing `errors.Is/As` branch + `.Wrap(err)`. The store never imports `apierr`.
- **`cause` is never serialized** — it rides logs + `errors.Is/As` only; the wire carries only `params`
  (string→string). So an internal English detail (an OS error, an op verb) never leaks into a localized
  zh sentence.
- The HTTP seam: `writeAPIError(w, *apierr.Error)` emits the envelope at the error's own status;
  `writeCodedOr(w, fallbackCode, err)` relays a deep error (source code wins via `errors.As`, else the
  fallback bucket). The **transitional `writeError`/`CodeLegacyUncoded`/`WithMessage` shim was deleted** —
  every wire error is coded; a `grep 'writeError('` gate proves zero call sites, and the deleted symbols
  are compiler-enforced.
- Frontend: `tError(body, lang)` localizes via `error.<code>` (falling back to the server `.message`,
  then `error.generic`). CLI/curl read the English `.message`.

## 3. Validator findings (the 200 channel)

Validation results ride a **200 `ValidateResponse`** (`errors[]`/`warnings[]`), not the HTTP error
envelope. `internal/validator` has its own local `Code` system (`code.go`): a `ValidationError` carries
`Code`+`Params`+a rendered English `Message`; the frontend localizes via `tValidationError(v, lang)` —
the same `error.<code>` catalog. (Separate from `apierr` because a finding rides a 200 and has no HTTP
status; the distinct Go type can't leak through the envelope.)

## Deploy artifacts (English-only, no channel)

The CLI (`cmd/compiler`), the server startup banner, and the generated **install scripts** /
self-extracting installer print to a process stdout/stderr or a target host with no `Accept-Language`
and no JSON consumer — there is no localization channel. These were **Englishized in place**
(English-default), not coded.

## Perpetual gates (regression prevention)

- **`MessageKey` union** — a missing/typo'd panel key is a TS build error.
- **zh `Record<keyof typeof en>`** — a missing zh key is a build error.
- **`TestRegistryBijection`** (apierr) + **`newFinding` panic** (validator) — an emitted code with no
  registered template fails fast.
- **`TestRegistryEnglishOnly`** — apierr/validator registry templates are CJK-free (the English default
  can never be another language).
- **`TestNoChineseInWireAndDeploySurfaces`** (api) + **`TestNoChineseInValidatorSource`** (validator) —
  no Chinese in Go string-literal position across the wire-envelope + deploy-artifact + validator
  packages (`//` developer comments allowed). New Chinese in a string literal trips the suite.

## Migration / integration notes

- **Wire shape**: API error bodies are `{error:{code,message,params}}` (nested). Any integrator that
  read a top-level `error` string must read `error.message` (human) and/or `error.code` (machine). No
  backcompat shim remains.
- **No env or topology-JSON changes** in this subject (the controller env rename + login gate belong to
  the separate controller-server-authority subject). Topology JSON is unchanged.
- **Install scripts / CLI output are English.** A non-English operator localizes in the panel; deploy-time
  shell output is English by design.
- **Adding a language** (panel): one new `messages/<lang>.ts` + union + registry entry. Backend error
  messages remain English on the wire; the panel supplies the translation per `error.<code>`.

## Out of scope / deferred to formal close-phase

Per-component `specs/` refresh, the closure README, the memory note, the archive move, `STATUS.md`
regeneration, and the long-owed two-node controller smoke test are the owner-owned `/close-phase`
ritual (status classification is the owner's call). plan-8 (frontend god-store/coupling) was triaged
to **no refactor** (verifier-refuted; see Decisions log D16).
