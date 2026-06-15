# plan-3.5 — Remaining backend user-facing strings → coded + localized

<!-- authored 2026-06-14. Owns the design-error-envelope.md plan-3.5 scope. -->

The remaining backend Chinese/user-facing strings split into TWO channels (design-error-envelope.md):
the validator's **200 ValidateResponse** channel, and the **HTTP error envelope** channel.

## plan-3.5a — Validator channel (SHIPPED, this PR)

**The biggest remaining wrong-language gap:** the validator's ~91 messages were Chinese, shown
verbatim by the panel → an English operator saw Chinese on any validation failure.

Design locked via a 2-Opus debate (`wf_f060ce0d`): a **validator-local `Code` system**
(`internal/validator/code.go`), deliberately separate from `internal/apierr` (a finding rides a
200, has no HTTP status; the distinct Go type can't leak through the HTTP envelope), localized on
the frontend through the **same `error.<code>` catalog** as the envelope.

- `ValidationError` gains `Code`+`Params`; `AddError/AddWarning(field, Code, ...P)`; `newFinding`
  renders the English `Message` from a registry template at the source (panics on an unregistered
  code). 89 codes; all 91 call sites in schema/semantic/nat converted; `linkDescription` Englished.
- Completeness gates: compiler (unported call = build error), `newFinding` panic, `code_test.go`
  (registry English-only + CJK grep-gate over the validator source + end-to-end coded/English smoke).
- Frontend: `tValidationError` (one localization path with `tError`); BottomBar + CompilePreview
  wired; 89 `error.validation_*` keys in en.ts + zh.ts.

## plan-3.5b — HTTP error envelope channel (REMAINING)

The remaining backend Chinese on the HTTP path, coded into `apierr` exactly like plan-3, then the
**`writeError` shim deletion** as the final grep-gated step:
- `internal/compiler` (peers.go etc., surfaced via `HandleCompile`'s 422), `cmd/compiler`,
  `render.go` 152–217 remaining Chinese, `internal/artifacts`.
- `auth_controller.go`, `handler_login/passkey/totp/bootstrap.go`, and the `handler_controller.go`
  English `writeError` sites (split by domain per the design doc).
- FINAL: delete the `writeError` legacy delegate + `CodeLegacyUncoded` (grep proving zero
  `writeError(` references remain is the gate).

This is mechanical follow-through of the plan-3 pattern; scoped as its own PR(s) after 3.5a.

## Verification gate

`go test ./... && go vet ./...` green; `cd frontend && npm run lint && npm run build` green; an
English-locale operator sees English on a validation failure; a Chinese operator gets the zh catalog.
