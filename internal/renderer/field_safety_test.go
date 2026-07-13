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
// struct carrying a bare string field, a []string, and a nested-struct string is flagged, while a
// ShellToken field and an int are not. If bareShellStrings ever stopped catching these, the seam above
// would silently pass anything.
func TestShellTokenGateCatchesBareString(t *testing.T) {
	type nested struct{ Raw string }
	type sample struct {
		OK     ShellToken // approved leaf — must NOT be flagged
		Danger string     // bare string — MUST be flagged
		List   []string   // slice of bare strings — MUST be flagged
		Sub    nested     // nested bare string — MUST be flagged
		Count  int        // non-string — must NOT be flagged
	}
	got := bareShellStrings("sample", reflect.TypeOf(sample{}), map[reflect.Type]bool{})
	want := map[string]bool{"sample.Danger": true, "sample.List[]": true, "sample.Sub.Raw": true}
	if len(got) != len(want) {
		t.Fatalf("gate flagged %v, want exactly the three bare-string paths %v", got, want)
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
