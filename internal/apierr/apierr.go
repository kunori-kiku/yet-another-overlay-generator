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
	// CodeInternal is the generic internal-server-error fallback used by writeCodedOr when a
	// relayed error is NOT coded at the source. It is the safety net so a handler relay never
	// has to reach for the legacy shim; a specific relay seam should pass a more precise bucket
	// (e.g. CodeRenderFailed) instead where one fits.
	CodeInternal Code = "internal"
	// CodeCustodyPrivateKey rejects an update-topology payload that carried a WireGuard
	// private key — a key-custody violation. The message names "private key" + "custody"
	// so both the operator and the perpetual custody test see the WHY.
	CodeCustodyPrivateKey Code = "custody_private_key"

	// WireGuard key preparation (render.GenerateKeys) — surfaced to the operator on the
	// air-gap compile/export path and the controller deploy path. params: {node} and,
	// where a parse/generate failed, {detail}.
	CodeKeygenMissingPubkey   Code = "keygen_missing_pubkey"
	CodeKeygenPrivkeyParse    Code = "keygen_privkey_parse_failed"
	CodeKeygenPinnedNoPrivkey Code = "keygen_pinned_pubkey_no_privkey"
	CodeKeygenGenerateFailed  Code = "keygen_generate_failed"

	// Compile-time constraints (plan-3.5b) — surfaced to the operator via HandleCompile/
	// HandleExport/HandleDeployScript as 422 (the topology is the thing to change). These are
	// coded at the SOURCE in internal/compiler + internal/allocator and flow through the
	// writeCodedOr relay; CodeCompileFailed is the relay's 422 fallback for any compile error
	// not coded at the source (e.g. a schema/semantic validation failure reaching compile).
	CodeCompileFailed         Code = "compile_failed"
	CodeTransitPoolExhausted  Code = "compile_transit_pool_exhausted"
	CodeTransitCIDRInvalid    Code = "compile_transit_cidr_invalid"
	CodeTransitCIDRNotIPv4    Code = "compile_transit_cidr_not_ipv4"
	CodeListenPortExhausted   Code = "compile_listen_port_exhausted"
	CodeOverlayCIDRInvalid    Code = "compile_overlay_cidr_invalid"
	CodeOverlayPoolExhausted  Code = "compile_overlay_pool_exhausted"
	CodeNodeUnknownDomain     Code = "compile_node_unknown_domain"

	// Render + export (plan-3.5b) — the template/IO layer. CodeRenderFailed is a 500 bucket the
	// handler assigns when render.All fails (internal template/plumbing, not operator-fixable).
	// CodeExportUnsafeName is a 400 coded at the source (Export holds the node name). CodeExportIOFailed
	// is a 500 bucket returned at the export source for disk/marshal/sign write failures.
	CodeRenderFailed     Code = "render_failed"
	CodeExportUnsafeName Code = "export_unsafe_name"
	CodeExportIOFailed   Code = "export_io_failed"
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
	CodeInternal:          {"An internal server error occurred. Please try again.", http.StatusInternalServerError},
	CodeCustodyPrivateKey: {"Topology payload carried a WireGuard private key; this is a key-custody violation — the panel must strip private keys client-side before upload.", http.StatusBadRequest},

	CodeKeygenMissingPubkey:   {"Node {node} is in agent-held custody but has not registered a WireGuard public key yet — the agent must register one before the controller can render it.", http.StatusBadRequest},
	CodeKeygenPrivkeyParse:    {"Node {node}'s WireGuard private key could not be parsed: {detail}", http.StatusBadRequest},
	CodeKeygenPinnedNoPrivkey: {"Node {node} has a pinned WireGuard public key but no matching private key: the stateless compiler cannot reconstruct it. Paste the in-use private key from that host's /etc/wireguard/<interface>.conf, or clear BOTH key fields to rotate.", http.StatusBadRequest},
	CodeKeygenGenerateFailed:  {"Failed to generate a WireGuard key for node {node}: {detail}", http.StatusInternalServerError},

	CodeCompileFailed:        {"Compilation failed. Check the topology and try again.", http.StatusUnprocessableEntity},
	CodeTransitPoolExhausted: {"The transit address pool for CIDR {cidr} is exhausted; widen the transit CIDR or reduce the number of links between these nodes.", http.StatusUnprocessableEntity},
	CodeTransitCIDRInvalid:   {"The transit CIDR {cidr} is invalid: {detail}", http.StatusUnprocessableEntity},
	CodeTransitCIDRNotIPv4:   {"The transit CIDR {cidr} must be IPv4.", http.StatusUnprocessableEntity},
	CodeListenPortExhausted:  {"Node {node}'s effective listen port cannot be allocated within [{base}, 65535]; lower its listen_port or reduce its connections.", http.StatusUnprocessableEntity},
	CodeOverlayCIDRInvalid:   {"The overlay CIDR {cidr} is invalid.", http.StatusUnprocessableEntity},
	CodeOverlayPoolExhausted: {"The overlay address pool for CIDR {cidr} is exhausted; widen the domain CIDR or reduce the number of nodes.", http.StatusUnprocessableEntity},
	CodeNodeUnknownDomain:    {"Node {node} references unknown domain {domain}.", http.StatusUnprocessableEntity},

	CodeRenderFailed:     {"Rendering the deployment artifacts failed.", http.StatusInternalServerError},
	CodeExportUnsafeName: {"Node name {name} is unsafe for export: it must be non-empty and must not be an absolute path or contain a path separator or \"..\".", http.StatusBadRequest},
	CodeExportIOFailed:   {"Writing the export artifacts failed.", http.StatusInternalServerError},
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
// interpolation). It ENFORCES "no params on the wire" by clearing params, so the
// documented contract holds even if a caller did With() first. It exists ONLY for the
// transitional writeError->CodeLegacyUncoded bridge so a bare, not-yet-coded string can
// ride the nested envelope; coded errors render from their template and must not use it.
// Removed with CodeLegacyUncoded in the final plan-3 commit.
func (e *Error) WithMessage(m string) *Error {
	e.literal = m
	e.params = nil
	return e
}

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
