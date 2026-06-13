# Design — backend error-code envelope (implementation spec for plan-2 / plan-3)
<!-- locked 2026-06-14 via 2-Opus debate + synthesis (task wmhzmpy2x); see outline D9 -->

The decision that frees the panel to localize backend errors (the bug the owner hit: an
English-locale operator sees Chinese). Implement plan-2 and plan-3 to this spec verbatim.

## Wire envelope — NESTED

```json
{
  "error": {
    "code": "enrollment_token_invalid",
    "message": "Enrollment token is invalid or has expired; request a new one.",
    "params": { "node_id": "edge-03" }
  }
}
```

- `code` — stable machine identifier (snake_case, domain-prefixed: `validation_*`, `enrollment_*`,
  `auth_*`, `compile_*`, `internal_*`).
- `message` — server-rendered **English** default (always populated, for CLI/curl + as the i18n
  English fallback). Rendered from the registry template + params.
- `params` — string→string map for client-side interpolation of the localized template.

No top-level `error` string (rejected the additive shape; the owner waived backcompat and wants the
clean nested contract).

## Go API — `internal/apierr` (NEW, stdlib-only leaf)

Zero imports beyond `fmt`/`errors`/`net/http`. MUST NOT import `internal/api`, `controller`,
`compiler`, etc. — it sits BELOW them so any package (incl. deep ones in plan-3) imports it with no
cycle and no HTTP coupling. Name it `apierr` (not `errors`) to avoid shadowing the stdlib.

```go
type Code string

// def: immutable per-code metadata. The template is the SINGLE source of both the CLI/curl
// message and the i18n English fallback (keep identical). {placeholders} map 1:1 to params.
type def struct { tmpl string; status int }

var registry = map[Code]def{
  CodeEnrollmentTokenInvalid: {"Enrollment token is invalid or has expired; request a new one.", http.StatusUnauthorized},
  CodeDuplicateWGKey:         {"WireGuard public key {wg_pubkey} is already enrolled under node {existing_node}.", http.StatusConflict},
  CodeInternalPanic:          {"An unexpected server error occurred.", http.StatusInternalServerError},
  CodeMethodNotAllowed:       {"Only {method} is supported for this endpoint.", http.StatusMethodNotAllowed},
  CodeCustodyPrivateKey:      {"Topology payload carried a WireGuard private key; this is a key-custody violation — the panel must strip private keys client-side before upload.", http.StatusBadRequest},
  // grows in plan-2 (infra codes) + plan-3 (deep-package codes)
}

type Error struct {
  code   Code
  params map[string]string   // string→string ONLY (deep packages stay HTTP-free)
  cause  error               // wrapped origin: logs + errors.Is/As; NEVER serialized
  status int                 // from registry, overridable
}
func (e *Error) Error() string  { return e.Message() }                 // rendered English (logs, CLI)
func (e *Error) Unwrap() error  { return e.cause }                     // errors.Is/As into the cause
func (e *Error) Code() Code     { return e.code }
func (e *Error) Status() int    { return e.status }
func (e *Error) Params() map[string]string { return e.params }
func (e *Error) Message() string { return interpolate(registry[e.code].tmpl, e.params) }

func New(c Code) *Error              { d := registry[c]; return &Error{code: c, status: d.status, params: map[string]string{}} }
func (e *Error) With(k, v string) *Error  { e.params[k] = v; return e }   // chainable
func (e *Error) Wrap(cause error) *Error  { e.cause = cause; return e }
func (e *Error) WithStatus(s int) *Error  { e.status = s; return e }       // rare override
func HasCode(err error, c Code) bool { var e *Error; return errors.As(err, &e) && e.code == c }
```

- Codes are `Code` consts in one alphabetized-by-domain block. An `init()` guard panics at startup
  if a declared const has no registry entry; a test asserts every registry key has a const
  (bijection) — the auditability win, enforced mechanically.
- Constructor reads: `apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST")`. Status comes
  from the registry, so the typical site is a one-liner with no status arg.
- `interpolate(tmpl, params)`: replace `{name}` with `params[name]` (mirror the frontend `t()` rule).

## Deep-package strategy — CODED AT THE SOURCE

The outline principle ("errors are coded at the source, localized at the edge") is HIGH/STATED, so
classify-at-the-boundary (string-sniffing) is rejected as the end state.

1. Because `apierr` is a stdlib-only leaf, deep packages (validator/compiler/render/renderer/
   allocator/artifacts) import it with no cycle — coding-at-source costs nothing architecturally.
2. Deep code returns `*apierr.Error` directly where it produces a user-facing failure, e.g.
   `render.go:86/90/127` → `apierr.New(CodeKeygenMissingPubkey).With("node", n).Wrap(err)`. The
   Chinese/English prose in `.Error()` strings is DELETED — the English template lives once in the
   registry. Internal-only diagnostics (logs) stay plain `fmt.Errorf`.
3. Sentinels in `controller/store.go:34-58` are KEPT (the `errors.Is` branch contract that
   `handler_controller.go:528-538` + the custody test depend on). The store keeps returning the
   sentinel; the HANDLER maps sentinel→Code at the route seam via the existing `errors.Is` →
   `apierr.New(CodeX).Wrap(err)`. This is the PERMANENT shape (sentinels = internal branch contract;
   codes = wire contract), not a transitional hack. Do NOT make the store import apierr.
4. `decodeJSON` / `readTopology` (the Chinese `读取请求体失败`/`JSON 解析失败`/`errBodyTooLarge`)
   return `*apierr.Error` (CodeJSONParseError / CodeRequestBodyTooLarge), fixing the leak finding.
5. The corrupted `allocator/ip.go:68` format string (`" %s  Domain %s "`, Chinese chars stripped) is
   fixed here as part of coding allocator errors.

## `writeError` disposition — REMOVE ENTIRELY (transitional delegate only in plan-2)

End state: one path, `writeAPIError(w, *apierr.Error)` emitting the nested envelope. No permanent
polymorphic shim (that is the half-measure the audit condemns).

## Plan split (honor the order: plan-1 → plan-2 → plan-3; the frontend reads the shape before the backend emits it)

**plan-1 (shipped): i18n core + shape-tolerant `tError`** — done. The panel already handles BOTH the
legacy string and the coded object shapes, so plan-2 can flip the wire with zero frontend coordination.

**plan-2 (backend infra + contract — small, revertable as a unit):**
- Commit 1: `internal/apierr` package (Code, Error, registry, New/With/Wrap/WithStatus, HasCode, init
  guard) + tests. Dead code until used.
- Commit 2: `writeAPIError(w, *apierr.Error)` emitting nested `{error:{code,message,params}}`; adopt
  it in the panic→500 path (`server.go:116`) and the login/passkey 401 bodies. Migrate
  `topology_custody_test.go:90` from `raw["error"]` (string) to `raw["error"]["message"]`; keep the
  `CodeCustodyPrivateKey` default message naming "private key" + "custody".
- Commit 3: convert the existing `writeError(w,status,string)` into a thin DELEGATE wrapping the bare
  string in `apierr.New(CodeLegacyUncoded).WithStatus(status)` routed through `writeAPIError` — so all
  ~204 existing call sites instantly emit the nested shape WITHOUT being individually touched. The
  atomic-flip bridge. TRANSITIONAL.
- After plan-2: every error is nested-shape; the (plan-1-ready) panel localizes coded ones and shows
  `.message` for still-uncoded ones; curl/CLI get `.message`.

**plan-3 (bulk string→code migration — package-by-package, each commit revertable):**
- 3a: deep packages own codes (validator detail-rich → one code per failure class + params;
  render.go:86/90/127; allocator incl. the corrupted format string; artifacts/compiler/cmd). Add the
  matching `error.<code>` keys to the frontend catalog (plan-1's `t`).
- 3b: api handlers — replace each `writeError(w,status,err.Error())` with `writeAPIError(...)`.
  `handler_controller.go` (~111 sites) split by domain (enrollment/config → auth/login →
  operator/topology/deploy → bootstrap/sessions); each domain = one PR.
- FINAL commit: delete the `writeError` legacy delegate + `CodeLegacyUncoded`; a grep proving zero
  `writeError(` references remain is the gate. End state: every wire error coded; no shim survives.

Each PR gate: `go test ./...` + `npm run build` green; one representative error round-trips
coded→localized in the panel; curl shows English `.message`.

## Frontend seam (already shipped in plan-1; remaining wiring in plan-3)

The panel reads errors ONLY through `tError(responseBody, lang)` (shipped, shape-tolerant). plan-1
wired the four `topologyStore` sites. plan-3 also normalizes `controllerClient.ts` (`request()`/
`login()`/passkey/session helpers currently throw `new Error(`${status} ${rawText}`)`): parse JSON
and throw `new Error(tError(parsed, lang))` (or carry the code on a typed error so the ~9
`controllerStore` `err.message` reads localize). After the flip every `body.error` is an object;
branch (b) of `tError` handles coded-vs-uncoded via the `?? message` fallback. The panel never sees
`[object Object]` because nothing reads `body.error` directly — only through `tError`.
