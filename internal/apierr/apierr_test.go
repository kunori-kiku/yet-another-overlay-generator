package apierr

import (
	"errors"
	"net/http"
	"testing"
)

// allCodes is the authoritative list of declared Code consts. TestRegistryBijection
// asserts it matches the registry exactly in BOTH directions, so a code can never be
// declared without a template nor a template left without a const. Add new codes here
// too when extending the registry.
var allCodes = []Code{
	CodeLegacyUncoded,
	CodeInternalPanic,
	CodeCustodyPrivateKey,
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

func TestWithMessageOverridesTemplate(t *testing.T) {
	// The legacy bridge: a verbatim message overrides the template, with no params on
	// the wire.
	e := New(CodeLegacyUncoded).WithStatus(http.StatusNotFound).WithMessage("node not found")
	if e.Status() != http.StatusNotFound {
		t.Errorf("status override = %d, want 404", e.Status())
	}
	if got := e.Message(); got != "node not found" {
		t.Errorf("WithMessage override = %q, want %q", got, "node not found")
	}
	if len(e.Params()) != 0 {
		t.Errorf("WithMessage must not populate params; got %v", e.Params())
	}
	// Enforced contract: WithMessage clears params set earlier, so "no params on the
	// wire" holds even under With()-then-WithMessage() misuse.
	cleared := New(CodeLegacyUncoded).With("k", "v").WithMessage("m")
	if len(cleared.Params()) != 0 {
		t.Errorf("WithMessage must clear params set via With(); got %v", cleared.Params())
	}
	if cleared.Message() != "m" {
		t.Errorf("message after With+WithMessage = %q, want %q", cleared.Message(), "m")
	}
	// Without WithMessage, CodeLegacyUncoded falls back to its template.
	if got := New(CodeLegacyUncoded).Message(); got != registry[CodeLegacyUncoded].tmpl {
		t.Errorf("fallback template = %q, want %q", got, registry[CodeLegacyUncoded].tmpl)
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
