package renderer

import (
	"reflect"
	"testing"
)

// shellTokenType is the one approved leaf type for a string that a renderer template splices into a
// generated root-executed shell script.
var shellTokenType = reflect.TypeOf(ShellToken{})

// TestTemplateConfigFieldsAreShellTokens is the STRUCTURAL half of the root-shell escape seam: it
// walks every field reachable by the install / client-install / sysctl templates and fails the build
// if ANY string is a bare `string` rather than a ShellToken. text/template is context-blind, so this
// reflection gate is what keeps an unescaped user-controlled value from reaching a root shell — a new
// render field added as a plain string turns this test red, forcing it through ShellQuoted / ShellRaw
// (shelltoken.go). Retirement: never (perpetual seam guard, framework-refactor plan-6; PRINCIPLES:
// "generated scripts run as root on fleets").
func TestTemplateConfigFieldsAreShellTokens(t *testing.T) {
	for _, c := range []struct {
		name string
		typ  reflect.Type
	}{
		{"InstallScriptConfig", reflect.TypeOf(InstallScriptConfig{})},
		{"ClientInstallScriptConfig", reflect.TypeOf(ClientInstallScriptConfig{})},
		{"SysctlConfig", reflect.TypeOf(SysctlConfig{})},
	} {
		for _, path := range bareShellStrings(c.name, c.typ, map[reflect.Type]bool{}) {
			t.Errorf("%s is a bare string reachable by a root-shell template; type it as a ShellToken "+
				"(ShellQuoted for a shell argument, ShellRaw for a constant / comment / heredoc body) so a "+
				"value cannot reach the shell unescaped", path)
		}
	}
}

// TestShellTokenGateCatchesBareString is the negative control that proves the gate is not vacuous: a
// struct carrying a bare string field, a []string, a nested-struct string, a bare-string MAP value, and
// an INTERFACE field is flagged, while a ShellToken field, a ShellToken-valued map, and an int are not.
// The map and interface cases pin the recursion hardening (previously `default: return nil` silently
// skipped both kinds). If bareShellStrings ever stopped catching these, the seam above would silently
// pass anything.
func TestShellTokenGateCatchesBareString(t *testing.T) {
	type nested struct{ Raw string }
	type sample struct {
		OK      ShellToken            // approved leaf — must NOT be flagged
		Danger  string                // bare string — MUST be flagged
		List    []string              // slice of bare strings — MUST be flagged
		Sub     nested                // nested bare string — MUST be flagged
		Count   int                   // non-string — must NOT be flagged
		MapBare map[string]string     // bare-string map VALUES — MUST be flagged (recurse into value)
		MapOK   map[string]ShellToken // ShellToken map values — must NOT be flagged (recursion sees the leaf)
		Iface   interface{}           // opaque interface — MUST be flagged (fail-closed hole)
	}
	got := bareShellStrings("sample", reflect.TypeOf(sample{}), map[reflect.Type]bool{})
	want := map[string]bool{
		"sample.Danger":    true,
		"sample.List[]":    true,
		"sample.Sub.Raw":   true,
		"sample.MapBare[]": true,
		"sample.Iface":     true,
	}
	if len(got) != len(want) {
		t.Fatalf("gate flagged %v, want exactly the bare-string/interface paths %v", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("gate flagged unexpected path %q", p)
		}
	}
}

// bareShellStrings returns the dotted paths of every bare `string` reachable from typ. ShellToken is
// the approved leaf (returned before its unexported bytes are walked); a string kind is a violation;
// slices/arrays/pointers recurse into their element; structs recurse into their EXPORTED fields only
// (an unexported field cannot be reached by a {{ .Field }} template access). Non-string leaves are
// fine. seen breaks any cyclic type graph.
func bareShellStrings(path string, typ reflect.Type, seen map[reflect.Type]bool) []string {
	if typ == shellTokenType {
		return nil
	}
	switch typ.Kind() {
	case reflect.String:
		return []string{path}
	case reflect.Slice, reflect.Array, reflect.Pointer:
		return bareShellStrings(path+"[]", typ.Elem(), seen)
	case reflect.Map:
		// A map's values are reachable from a template ({{ index .M k }} / {{ range .M }}), so recurse
		// into the value type — a bare string hidden as a map value must be caught, and a ShellToken
		// map value must pass. (Keys are conventionally identifiers, not spliced into the shell, so the
		// value type is what the seam guards.) Without this the map was silently skipped by default.
		return bareShellStrings(path+"[]", typ.Elem(), seen)
	case reflect.Interface:
		// An interface field is opaque to this STATIC type walk: its dynamic value could be any type,
		// including a bare string that reaches the root shell, and reflect.Type has no element type to
		// recurse into. Fail closed — surface it as a violation rather than silently skip it (the seam
		// demands a concrete ShellToken, never an interface). No current config uses one; this is a
		// forward guard so a future interface-typed template field cannot smuggle an unescaped value in.
		return []string{path}
	case reflect.Struct:
		if seen[typ] {
			return nil
		}
		seen[typ] = true
		var out []string
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.PkgPath != "" { // unexported: unreachable via {{ .Field }}
				continue
			}
			out = append(out, bareShellStrings(path+"."+f.Name, f.Type, seen)...)
		}
		return out
	default:
		return nil
	}
}
