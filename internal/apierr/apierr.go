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
	CodeCompileFailed        Code = "compile_failed"
	CodeTransitPoolExhausted Code = "compile_transit_pool_exhausted"
	CodeTransitCIDRInvalid   Code = "compile_transit_cidr_invalid"
	CodeTransitCIDRNotIPv4   Code = "compile_transit_cidr_not_ipv4"
	CodeListenPortExhausted  Code = "compile_listen_port_exhausted"
	CodeOverlayCIDRInvalid   Code = "compile_overlay_cidr_invalid"
	CodeOverlayPoolExhausted Code = "compile_overlay_pool_exhausted"
	CodeNodeUnknownDomain    Code = "compile_node_unknown_domain"

	// Render + export (plan-3.5b) — the template/IO layer. CodeRenderFailed is a 500 bucket the
	// handler assigns when render.All fails (internal template/plumbing, not operator-fixable).
	// CodeExportUnsafeName is a 400 coded at the source (Export holds the node name). CodeExportIOFailed
	// is a 500 bucket returned at the export source for disk/marshal/sign write failures.
	CodeRenderFailed     Code = "render_failed"
	CodeExportUnsafeName Code = "export_unsafe_name"
	CodeExportIOFailed   Code = "export_io_failed"

	// Request envelope (plan-3.5b) — method + body framing shared by every endpoint.
	CodeMethodNotAllowed Code = "method_not_allowed"
	CodeReqBodyTooLarge  Code = "req_body_too_large"
	CodeReqBodyEmpty     Code = "req_body_empty"
	CodeReqInvalidBody   Code = "req_invalid_body"

	// Controller HTTP surface (plan-3.5b) — operator/agent endpoints in handler_controller.go.
	CodeInternalIdentityMissing  Code = "internal_identity_missing"
	CodeInternalStorage          Code = "internal_storage"
	CodeNodeNotFound             Code = "node_not_found"
	CodeConfigNotFound           Code = "config_not_found"
	CodeNodeIDReserved           Code = "node_id_reserved"
	CodeEnrollmentTokenInvalid   Code = "enrollment_token_invalid"
	CodeEnrollNodeRevoked        Code = "enroll_node_revoked"
	CodeDuplicateWGKey           Code = "duplicate_wg_key"
	CodeNoStagedBundle           Code = "no_staged_bundle"
	CodeReqFieldRequired         Code = "req_field_required"
	CodeReqFieldInvalid          Code = "req_field_invalid"
	CodeReqUnsupportedAlg        Code = "req_unsupported_alg"
	CodeTopologyVersionNotFound  Code = "topology_version_not_found"
	CodeNoTopologyStored         Code = "no_topology_stored"
	CodeKeystoneNoSignedManifest Code = "keystone_no_signed_manifest"
	CodeNoStagedManifest         Code = "no_staged_manifest"
	CodeNoPinnedCredential       Code = "no_pinned_credential"
	CodeStagedManifestMismatch   Code = "staged_manifest_mismatch"
	CodeManifestSignatureInvalid Code = "manifest_signature_invalid"
	CodeStageFailed              Code = "stage_failed"

	// Assisted release-pin fetch (controller-panel-rollout-ui plan-1) — the operator-only
	// release-pins endpoint that fetches per-asset .sha256 sidecars through the gh-proxy to
	// PRE-FILL agent/mimic artifact pins for operator review. The sidecar is convenience-only
	// transport; trust stays the keystone-signed artifacts.json the agent verifies against.
	CodeAgentReleaseRequestInvalid Code = "agent_release_request_invalid" // 400: bad request shape
	CodeAgentReleaseFetchFailed    Code = "agent_release_fetch_failed"    // 502: upstream fetch failed
	CodeAgentReleaseSidecarInvalid Code = "agent_release_sidecar_invalid" // 502: sidecar not a SHA-256

	// Bundle-signing anchor (persist-signing-anchor) — the controller pins the signing PUBLIC key
	// a fleet's bundles are signed with, so a redeploy that drops or swaps the signing key is
	// DETECTED at stage time instead of silently downgrading. CodeSigningKeyMissing: the fleet is
	// pinned to a signing key but YAOG_BUNDLE_SIGNING_KEY is now unset/unreadable.
	// CodeSigningKeyMismatch: the configured key differs from the pinned anchor (set
	// YAOG_BUNDLE_SIGNING_KEY_ROTATE to re-pin intentionally).
	CodeSigningKeyMissing  Code = "signing_key_missing"  // 412: pinned-but-absent
	CodeSigningKeyMismatch Code = "signing_key_mismatch" // 409: configured key != pinned anchor

	// Keystone rotation (keystone-rotation-safety) — re-pinning the OFF-HOST operator
	// credential to a DIFFERENT key strands every enrolled node (each verifies the served
	// trust-list against the credential it was provisioned with out of band) until it is
	// re-provisioned AND a fresh deploy is signed under the new key. So a CHANGED credential
	// is refused unless the operator explicitly acknowledges the rotation (rotate:true) —
	// the anti-footgun analogue of YAOG_BUNDLE_SIGNING_KEY_ROTATE for the keystone.
	CodeKeystoneRotationRequiresAck Code = "keystone_rotation_requires_ack" // 409: changed cred, no rotate ack

	// Auth + session surface (plan-3.5b) — login / passkey / TOTP / bootstrap / node + operator auth.
	CodeReqBearerRequired       Code = "req_bearer_required"
	CodeAuthCredentialsInvalid  Code = "auth_credentials_invalid"
	CodeReqCSRFInvalid          Code = "req_csrf_invalid"
	CodeReqOperatorRequired     Code = "req_operator_required"
	CodeAuthRateLimited         Code = "auth_rate_limited"
	CodeAuthPasskeyFailed       Code = "auth_passkey_failed"
	CodeAuthPasskeyVerifyFailed Code = "auth_passkey_verify_failed"
	CodeTotpInvalidCode         Code = "totp_invalid_code"
	CodeTotpRequiresLogin       Code = "totp_requires_login"
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
	CodeListenPortExhausted:  {"Node {node}'s effective listen port cannot be allocated within [{base}, 65535]; reduce its connections.", http.StatusUnprocessableEntity},
	CodeOverlayCIDRInvalid:   {"The overlay CIDR {cidr} is invalid.", http.StatusUnprocessableEntity},
	CodeOverlayPoolExhausted: {"The overlay address pool for CIDR {cidr} is exhausted; widen the domain CIDR or reduce the number of nodes.", http.StatusUnprocessableEntity},
	CodeNodeUnknownDomain:    {"Node {node} references unknown domain {domain}.", http.StatusUnprocessableEntity},

	CodeRenderFailed:     {"Rendering the deployment artifacts failed.", http.StatusInternalServerError},
	CodeExportUnsafeName: {"Node name {name} is unsafe for export: it must be non-empty and must not be an absolute path or contain a path separator or \"..\".", http.StatusBadRequest},
	CodeExportIOFailed:   {"Writing the export artifacts failed.", http.StatusInternalServerError},

	CodeMethodNotAllowed: {"Only {method} is supported for this endpoint.", http.StatusMethodNotAllowed},
	CodeReqBodyTooLarge:  {"The request body exceeds the maximum size of {limit} bytes.", http.StatusRequestEntityTooLarge},
	CodeReqBodyEmpty:     {"The request body is empty.", http.StatusBadRequest},
	CodeReqInvalidBody:   {"The request body could not be parsed.", http.StatusBadRequest},

	CodeInternalIdentityMissing:  {"The request is missing an authenticated identity.", http.StatusInternalServerError},
	CodeInternalStorage:          {"A storage operation failed; please retry.", http.StatusInternalServerError},
	CodeNodeNotFound:             {"The requested node was not found.", http.StatusNotFound},
	CodeConfigNotFound:           {"No configuration is available for this node yet.", http.StatusNotFound},
	CodeNodeIDReserved:           {"That node id is reserved and cannot be used.", http.StatusForbidden},
	CodeEnrollmentTokenInvalid:   {"The enrollment token is invalid or has expired; request a new one.", http.StatusUnauthorized},
	CodeEnrollNodeRevoked:        {"That node id has been revoked; delete it before re-enrolling.", http.StatusConflict},
	CodeDuplicateWGKey:           {"That WireGuard public key is already enrolled under a different node.", http.StatusConflict},
	CodeNoStagedBundle:           {"Nothing is staged for the next generation; stage a deploy before promoting.", http.StatusConflict},
	CodeReqFieldRequired:         {"The field {field} is required.", http.StatusBadRequest},
	CodeReqFieldInvalid:          {"The field {field} is invalid.", http.StatusBadRequest},
	CodeReqUnsupportedAlg:        {"Unsupported algorithm {alg}.", http.StatusBadRequest},
	CodeTopologyVersionNotFound:  {"No such retained topology version (it may have been pruned).", http.StatusNotFound},
	CodeNoTopologyStored:         {"No topology has been stored yet.", http.StatusNotFound},
	CodeKeystoneNoSignedManifest: {"The keystone is enabled but no signed membership manifest has been promoted to serve yet; sign and promote a deploy under the pinned credential. Nodes keep their current config and retry.", http.StatusConflict},
	CodeNoStagedManifest:         {"No staged membership manifest; stage a deploy before signing.", http.StatusNotFound},
	CodeNoPinnedCredential:       {"No operator credential is pinned; pin one before signing.", http.StatusPreconditionFailed},
	CodeStagedManifestMismatch:   {"The submitted manifest does not match the current staged manifest; re-fetch and re-sign.", http.StatusConflict},
	CodeManifestSignatureInvalid: {"The manifest signature could not be verified against the pinned credential.", http.StatusBadRequest},
	CodeStageFailed:              {"Staging or promoting the deployment failed.", http.StatusUnprocessableEntity},

	CodeAgentReleaseRequestInvalid: {"The release-pin request field {field} is invalid.", http.StatusBadRequest},
	CodeAgentReleaseFetchFailed:    {"Could not fetch the release checksum from {url}: {detail}", http.StatusBadGateway},
	CodeAgentReleaseSidecarInvalid: {"The release checksum fetched from {url} is not a valid SHA-256.", http.StatusBadGateway},

	CodeSigningKeyMissing:  {"This fleet's bundles are signed, but no signing key is configured (YAOG_BUNDLE_SIGNING_KEY is unset or unreadable). Refusing to stage unsigned bundles — restore the signing key.", http.StatusPreconditionFailed},
	CodeSigningKeyMismatch: {"The configured bundle signing key does not match the one this fleet was pinned to. Restore the original key, or set YAOG_BUNDLE_SIGNING_KEY_ROTATE=1 for one deploy to intentionally rotate it.", http.StatusConflict},

	CodeKeystoneRotationRequiresAck: {"A different operator signing credential is already pinned. Rotating it strands every enrolled node until each is re-provisioned (yaog-agent reprovision-keystone) AND a fresh deploy is signed under the new key. Re-send with rotate:true to acknowledge and proceed.", http.StatusConflict},

	CodeReqBearerRequired:       {"A valid bearer token is required.", http.StatusUnauthorized},
	CodeAuthCredentialsInvalid:  {"Invalid username or password.", http.StatusUnauthorized},
	CodeReqCSRFInvalid:          {"Missing or invalid CSRF token.", http.StatusForbidden},
	CodeReqOperatorRequired:     {"Operator privileges are required.", http.StatusForbidden},
	CodeAuthRateLimited:         {"Too many login attempts; try again later.", http.StatusTooManyRequests},
	CodeAuthPasskeyFailed:       {"Passkey login failed.", http.StatusUnauthorized},
	CodeAuthPasskeyVerifyFailed: {"Passkey verification failed.", http.StatusBadRequest},
	CodeTotpInvalidCode:         {"Invalid two-factor code; check your authenticator's time and try again.", http.StatusBadRequest},
	CodeTotpRequiresLogin:       {"Two-factor management requires a logged-in operator account, not the break-glass token.", http.StatusForbidden},
}

// Error is a coded API error. It implements error and supports errors.Is/As via Unwrap,
// so a handler can wrap an existing sentinel (e.g. controller.ErrTokenInvalid) and the
// existing errors.Is branches keep working.
type Error struct {
	code   Code
	params map[string]string
	cause  error // wrapped origin: logs + errors.Is/As; NEVER serialized
	status int
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

func (e *Error) Error() string { return e.Message() }
func (e *Error) Unwrap() error { return e.cause }
func (e *Error) Code() Code    { return e.code }
func (e *Error) Status() int   { return e.status }

// Params returns the template parameters (string→string) for client-side interpolation.
func (e *Error) Params() map[string]string { return e.params }

// Message renders the human message: the registry template interpolated with params — the
// English default for CLI/curl and the i18n English fallback.
func (e *Error) Message() string {
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
