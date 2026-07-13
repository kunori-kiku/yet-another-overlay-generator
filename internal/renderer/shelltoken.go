package renderer

// ShellToken is a string that has been explicitly classified as safe to splice into a generated
// root-executed shell script at a text/template position. A value becomes a ShellToken ONLY through
// ShellQuoted or ShellRaw; the install / client-install / sysctl templates consume ShellToken fields
// directly (never a bare string), and field_safety_test enforces that structurally — so no unescaped
// user-controlled text can reach a root shell (PRINCIPLES: "generated scripts run as root on fleets").
//
// text/template is context-BLIND, so the honest guarantee is exactly "this value was passed through
// ShellQuoted or ShellRaw at construction," NOT "the template proved the value landed in a safe spot."
// The two constructors preserve the two real escaping contexts (a quoted shell argument vs. a raw
// comment/heredoc/constant); the type cannot by itself prove a raw value was not misplaced into an
// argument position. It converts the seam from per-site convention into a machine-checked boundary:
// a new bare-string render field is a red build (field_safety_test), and every value that reaches the
// root shell is forced through one of the two audited constructors.
type ShellToken struct {
	// s is the exact byte sequence spliced into the template. It is unexported so the ONLY way to
	// obtain a ShellToken is via the two constructors below — a bare string cannot be cast to one.
	s string
}

// String returns the token bytes for text/template. ShellToken implements fmt.Stringer, so a
// {{ .Field }} whose field is a ShellToken renders exactly these bytes.
func (t ShellToken) String() string { return t.s }

// ShellQuoted wraps v as one inert single-quoted POSIX shell word (via the audited bashSingleQuote):
// for a shell-ARGUMENT position (an echo argument, a command argument, a redirect target) where v may
// be user-derived (a node name, a GitHub proxy URL, an operator-set egress interface). Inside the
// single quotes the shell expands nothing — command substitution `$(...)`, variable expansion, and
// globbing in v cannot fire — so the value survives only as a literal string.
func ShellQuoted(v string) ShellToken { return ShellToken{s: bashSingleQuote(v)} }

// ShellRaw returns v verbatim as a token, for template positions the shell does NOT evaluate — a
// comment line, or the body of a single-quoted-delimiter heredoc — or where v is a compiler constant
// or deterministic allocator output (an overlay IP, a derived interface name, a fixed conf filename, a
// validated transit CIDR) that is spliced raw by design and byte-frozen by the golden corpus. The
// caller asserts v carries no shell-active bytes in its rendered context; ShellRaw performs no escaping.
func ShellRaw(v string) ShellToken { return ShellToken{s: v} }

// shellRawSlice wraps each element of vs as a ShellRaw token, for a repeated template field the engine
// ranges over and splices raw (e.g. the transit CIDR pools in the SNAT rules).
func shellRawSlice(vs []string) []ShellToken {
	out := make([]ShellToken, len(vs))
	for i, v := range vs {
		out[i] = ShellRaw(v)
	}
	return out
}
