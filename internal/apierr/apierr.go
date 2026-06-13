// Package apierr is the typed error-code layer shared by every API surface.
//
// It exists so backend failures can be LOCALIZED by the panel instead of shipped as
// a raw (and historically Chinese-only) sentence. A failure carries a stable Code, a
// string→string Params map, and a default English Message rendered from a registry
// template; the HTTP layer serializes these as the nested envelope
//
//	{"error": {"code": "...", "message": "...", "params": {...}}}
//
// The panel localizes from code+params; CLI/curl read the rendered English message.
//
// DEPENDENCY DIRECTION: this package is a stdlib-only LEAF (imports only errors,
// net/http, strings). It must NOT import internal/api, controller, compiler, etc. — it
// sits BELOW them so any package, including the deep validator/compiler/render layers,
// can return *apierr.Error directly ("coded at the source") with no import cycle and no
// HTTP coupling (the Status int is just a hint those packages need not care about).
package apierr

import (
	"errors"
	"net/http"
	"strings"
)

// Code is a stable, machine-readable error identifier (snake_case, domain-prefixed:
// validation_*, enrollment_*, auth_*, compile_*, internal_*). It is the join key
// between the backend and the frontend's 'error.<code>' localization catalog.
type Code string

const (
	// CodeLegacyUncoded is the TRANSITIONAL escape hatch used by the writeError shim: it
	// carries a bare, not-yet-coded message verbatim (via WithMessage) so every legacy
	// call site emits the nested envelope without being individually rewritten. It is
	// DELETED in the final plan-3 commit, once every emit site is coded (grep-gated).
	// New code must never use it.
	CodeLegacyUncoded Code = "legacy_uncoded"
	// CodeInternalPanic is the recovered-panic 500 (see Server.recoverPanics).
	CodeInternalPanic Code = "internal_panic"
	// CodeCustodyPrivateKey rejects an update-topology payload that carried a WireGuard
	// private key — a key-custody violation. The message names "private key" + "custody"
	// so both the operator and the perpetual custody test see the WHY.
	CodeCustodyPrivateKey Code = "custody_private_key"
)

// def is the immutable per-code metadata: the default English message TEMPLATE (with
// {name} placeholders that map 1:1 to Params) and the default HTTP status. The template
// is the SINGLE source of both the CLI/curl message and the i18n English fallback.
type def struct {
	tmpl   string
	status int
}

// registry declares every Code's template + default status. Adding a Code means adding
// it here AND to the const block above; New panics on an unregistered code (fail-fast),
// and TestRegistryBijection asserts the two sets match exactly.
var registry = map[Code]def{
	// CodeLegacyUncoded's template is only a defensive fallback; the bridge always
	// supplies the verbatim message via WithMessage (which overrides the template).
	CodeLegacyUncoded:     {"An unexpected error occurred.", http.StatusInternalServerError},
	CodeInternalPanic:     {"An unexpected server error occurred.", http.StatusInternalServerError},
	CodeCustodyPrivateKey: {"Topology payload carried a WireGuard private key; this is a key-custody violation — the panel must strip private keys client-side before upload.", http.StatusBadRequest},
}

// Error is a coded API error. It implements error and supports errors.Is/As via Unwrap,
// so a handler can wrap an existing sentinel (e.g. controller.ErrTokenInvalid) and the
// existing errors.Is branches keep working.
type Error struct {
	code    Code
	params  map[string]string
	cause   error // wrapped origin: logs + errors.Is/As; NEVER serialized
	status  int
	literal string // verbatim message set by WithMessage (legacy bridge); overrides the template
}

// New starts a coded error from its registry default status. It panics on an
// unregistered code — a programming error caught at first use, not a silent blank.
func New(c Code) *Error {
	d, ok := registry[c]
	if !ok {
		panic("apierr: unknown code " + string(c) + " (declare it in the const block and registry)")
	}
	return &Error{code: c, status: d.status, params: map[string]string{}}
}

// With sets a template parameter (chainable): apierr.New(C).With("node", id).
func (e *Error) With(k, v string) *Error { e.params[k] = v; return e }

// Wrap attaches the underlying cause (for logs + errors.Is/As); it is never serialized.
func (e *Error) Wrap(cause error) *Error { e.cause = cause; return e }

// WithStatus overrides the registry default HTTP status (rare).
func (e *Error) WithStatus(s int) *Error { e.status = s; return e }

// WithMessage sets a VERBATIM message that overrides the registry template (no
// interpolation, no params on the wire). It exists ONLY for the transitional
// writeError->CodeLegacyUncoded bridge so a bare, not-yet-coded string can ride the
// nested envelope; coded errors render from their template and must not use it. Removed
// with CodeLegacyUncoded in the final plan-3 commit.
func (e *Error) WithMessage(m string) *Error { e.literal = m; return e }

func (e *Error) Error() string { return e.Message() }
func (e *Error) Unwrap() error { return e.cause }
func (e *Error) Code() Code    { return e.code }
func (e *Error) Status() int   { return e.status }

// Params returns the template parameters (string→string) for client-side interpolation.
func (e *Error) Params() map[string]string { return e.params }

// Message renders the human message: a verbatim WithMessage override if set (legacy
// bridge), else the registry template interpolated with params — the English default
// for CLI/curl and the i18n English fallback.
func (e *Error) Message() string {
	if e.literal != "" {
		return e.literal
	}
	return interpolate(registry[e.code].tmpl, e.params)
}

// HasCode reports whether err is, or wraps, an *Error with code c.
func HasCode(err error, c Code) bool {
	var e *Error
	return errors.As(err, &e) && e.code == c
}

// interpolate replaces each {name} in tmpl with params[name]. An unknown placeholder is
// left intact (mirrors the frontend t()), so a missing param is visible, never a panic.
func interpolate(tmpl string, params map[string]string) string {
	if len(params) == 0 || !strings.ContainsRune(tmpl, '{') {
		return tmpl
	}
	var b strings.Builder
	for i := 0; i < len(tmpl); {
		if tmpl[i] == '{' {
			if rel := strings.IndexByte(tmpl[i:], '}'); rel > 1 {
				name := tmpl[i+1 : i+rel]
				if v, ok := params[name]; ok {
					b.WriteString(v)
					i += rel + 1
					continue
				}
			}
		}
		b.WriteByte(tmpl[i])
		i++
	}
	return b.String()
}
