package apierr

import (
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
)

var cjk = regexp.MustCompile(`\p{Han}`)

// TestRegistryEnglishOnly: every registry template is the single source of the CLI/curl message
// and the i18n English fallback, so it must be non-empty and carry no CJK — an English-locale
// operator never sees another language, even for a code not yet keyed in the panel catalog.
func TestRegistryEnglishOnly(t *testing.T) {
	if len(registry) == 0 {
		t.Fatal("registry is empty")
	}
	for code, d := range registry {
		if strings.TrimSpace(d.tmpl) == "" {
			t.Errorf("%s has an empty template", code)
		}
		if cjk.MatchString(d.tmpl) {
			t.Errorf("%s template contains CJK (must be English): %q", code, d.tmpl)
		}
	}
}

// TestNoChineseInApierrSource: apierr.go carries no Chinese in code/string position (it is a
// fully-English leaf package). Mirrors the validator's source gate: scan only the code portion
// (strip the // comment) so a Chinese string literal can never regress in as a wire message.
func TestNoChineseInApierrSource(t *testing.T) {
	b, err := os.ReadFile("apierr.go")
	if err != nil {
		t.Fatalf("read apierr.go: %v", err)
	}
	for i, line := range strings.Split(string(b), "\n") {
		code := line
		if c := strings.Index(code, "//"); c >= 0 {
			code = code[:c]
		}
		if cjk.MatchString(code) {
			t.Errorf("apierr.go:%d has Chinese in code/string position (must be English): %s", i+1, strings.TrimSpace(line))
		}
	}
}

// allCodes is the authoritative list of declared Code consts. TestRegistryBijection
// asserts it matches the registry exactly in BOTH directions, so a code can never be
// declared without a template nor a template left without a const. Add new codes here
// too when extending the registry.
var allCodes = []Code{
	CodeInternalPanic,
	CodeInternal,
	CodeCustodyPrivateKey,
	CodeKeygenMissingPubkey,
	CodeKeygenPrivkeyParse,
	CodeKeygenPinnedNoPrivkey,
	CodeKeygenGenerateFailed,
	CodeCompileFailed,
	CodeTransitPoolExhausted,
	CodeTransitCIDRInvalid,
	CodeTransitCIDRNotIPv4,
	CodeListenPortExhausted,
	CodeOverlayCIDRInvalid,
	CodeOverlayPoolExhausted,
	CodeNodeUnknownDomain,
	CodeRenderFailed,
	CodeExportUnsafeName,
	CodeExportIOFailed,
	CodeMethodNotAllowed,
	CodeReqBodyTooLarge,
	CodeReqBodyEmpty,
	CodeReqInvalidBody,
	CodeInternalIdentityMissing,
	CodeInternalStorage,
	CodeNodeNotFound,
	CodeConfigNotFound,
	CodeNodeIDReserved,
	CodeEnrollmentTokenInvalid,
	CodeDuplicateWGKey,
	CodeNoStagedBundle,
	CodeReqFieldRequired,
	CodeReqFieldInvalid,
	CodeReqUnsupportedAlg,
	CodeTopologyVersionNotFound,
	CodeNoTopologyStored,
	CodeKeystoneNoSignedManifest,
	CodeNoStagedManifest,
	CodeNoPinnedCredential,
	CodeStagedManifestMismatch,
	CodeManifestSignatureInvalid,
	CodeStageFailed,
	CodeAgentReleaseRequestInvalid,
	CodeAgentReleaseFetchFailed,
	CodeAgentReleaseSidecarInvalid,
	CodeSigningKeyMissing,
	CodeSigningKeyMismatch,
	CodeKeystoneRotationRequiresAck,
	CodeReqBearerRequired,
	CodeAuthCredentialsInvalid,
	CodeReqCSRFInvalid,
	CodeReqOperatorRequired,
	CodeAuthRateLimited,
	CodeAuthPasskeyFailed,
	CodeAuthPasskeyVerifyFailed,
	CodeTotpInvalidCode,
	CodeTotpRequiresLogin,
}

func TestRegistryBijection(t *testing.T) {
	declared := make(map[Code]bool, len(allCodes))
	for _, c := range allCodes {
		declared[c] = true
		if _, ok := registry[c]; !ok {
			t.Errorf("declared code %q has no registry entry", c)
		}
		if registry[c].tmpl == "" {
			t.Errorf("code %q has an empty template", c)
		}
	}
	for c := range registry {
		if !declared[c] {
			t.Errorf("registry has entry %q with no declared Code const (add it to allCodes)", c)
		}
	}
}

func TestNewUsesRegistryStatusAndMessage(t *testing.T) {
	e := New(CodeCustodyPrivateKey)
	if e.Status() != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", e.Status())
	}
	if e.Code() != CodeCustodyPrivateKey {
		t.Errorf("code = %q", e.Code())
	}
	// No params + a placeholder-free template → message is the template verbatim.
	if got := e.Message(); got == "" || got != registry[CodeCustodyPrivateKey].tmpl {
		t.Errorf("message = %q, want the registry template", got)
	}
}

// TestKeystoneNoSignedManifestIs409 pins the wire status of the keystone-on-but-nothing-promoted
// state to 409 Conflict. It was reclassified from 500: the state is operator-actionable (sign +
// promote a deploy under the pinned credential), not a server fault, and nodes keep their current
// config and retry. A regression that flips it back to a 5xx would trip ops 5xx alerting and
// mislabel an expected mid-rotation state as a server bug.
func TestKeystoneNoSignedManifestIs409(t *testing.T) {
	if got := New(CodeKeystoneNoSignedManifest).Status(); got != http.StatusConflict {
		t.Fatalf("CodeKeystoneNoSignedManifest status = %d, want 409 (must not be a 5xx server-fault)", got)
	}
}

func TestInterpolation(t *testing.T) {
	cases := []struct {
		tmpl   string
		params map[string]string
		want   string
	}{
		{"no placeholders", nil, "no placeholders"},
		{"node {node} failed", map[string]string{"node": "edge-03"}, "node edge-03 failed"},
		{"{a} and {b}", map[string]string{"a": "X", "b": "Y"}, "X and Y"},
		// Unknown placeholder left intact (param missing) — visible, never a panic.
		{"missing {gone} here", map[string]string{"other": "z"}, "missing {gone} here"},
		// Empty braces are not a placeholder.
		{"a {} b", map[string]string{}, "a {} b"},
		// Unclosed brace is literal.
		{"open { brace", map[string]string{"x": "1"}, "open { brace"},
	}
	for _, c := range cases {
		if got := interpolate(c.tmpl, c.params); got != c.want {
			t.Errorf("interpolate(%q,%v) = %q, want %q", c.tmpl, c.params, got, c.want)
		}
	}
}

func TestWithParamsInterpolate(t *testing.T) {
	e := New(CodeCustodyPrivateKey).With("ignored", "x")
	if e.Params()["ignored"] != "x" {
		t.Errorf("params not retained: %v", e.Params())
	}
}

func TestUnwrapAndHasCode(t *testing.T) {
	sentinel := errors.New("token invalid")
	e := New(CodeCustodyPrivateKey).Wrap(sentinel)
	if !errors.Is(e, sentinel) {
		t.Error("errors.Is should chain into the wrapped cause via Unwrap")
	}
	if !HasCode(e, CodeCustodyPrivateKey) {
		t.Error("HasCode should match the error's own code")
	}
	if HasCode(e, CodeInternalPanic) {
		t.Error("HasCode should not match a different code")
	}
	// HasCode also matches when the *Error is itself wrapped by a plain error.
	wrapped := errWrap{e}
	if !HasCode(wrapped, CodeCustodyPrivateKey) {
		t.Error("HasCode should find a wrapped *Error via errors.As")
	}
}

func TestNewPanicsOnUnknownCode(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New on an unregistered code should panic")
		}
	}()
	_ = New(Code("definitely_not_registered"))
}

// errWrap is a minimal error wrapper for the errors.As path in TestUnwrapAndHasCode.
type errWrap struct{ inner error }

func (w errWrap) Error() string { return w.inner.Error() }
func (w errWrap) Unwrap() error { return w.inner }
