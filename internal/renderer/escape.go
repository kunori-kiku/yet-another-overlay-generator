package renderer

import "strings"

// Shell-escaping helpers for the deploy and install renderers.
//
// Generated install scripts run as root on every node in a fleet, and the
// generated deploy script runs on the OPERATOR's own machine. Both interpolate
// user-supplied text (node names, ssh_host / ssh_user / ssh_alias) into shell
// command lines. Per docs/spec/security/security.md and the validation coverage
// contract in docs/spec/compiler/validation.md ("strict charset (interpolated
// into root/operator shell)"), any such interpolation that is not quoted is a
// command-injection path: a node named `x$(touch /tmp/pwned)` or an ssh_host of
// `x; rm -rf $HOME #` would otherwise execute as written. These helpers render
// the user text INERT by wrapping it in a quoting context the shell will not
// interpret, so the payload survives only as a literal string.
//
// Validation (Plan 5 charset rules) is the primary defence; these helpers are
// the defence-in-depth escaping that the renderers MUST apply at every
// interpolation site regardless. They are unexported because only this package
// (the renderer) produces shell text.

// bashSingleQuote wraps s in single quotes, rendering it inert in a POSIX shell.
//
// Inside single quotes the shell interprets nothing — no variable expansion, no
// command substitution `$(...)`, no backticks, no globbing. The single
// exception is the single-quote character itself, which cannot appear inside a
// single-quoted string. The standard POSIX-safe idiom for an embedded single
// quote is to close the quote, emit an escaped single quote, and reopen the
// quote: the literal sequence '\'' . For example the input
//
//	don't
//
// becomes
//
//	'don'\''t'
//
// which the shell reads as the three concatenated tokens 'don', \' and 't',
// i.e. the literal string don't. The result is always a single shell word, so
// it is safe to splice directly where a bare token (an ssh target, a remote
// path component, an echo argument) is expected. Spec home:
// docs/spec/security/security.md; closes the unquoted-SSHTarget and node-name
// injection paths (audit theme T4, findings D7/D15/D16).
func bashSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// powerShellArgQuote double-quotes s for a PowerShell argument context, escaping
// the characters PowerShell expands inside a double-quoted string.
//
// The deploy PowerShell script invokes ssh / scp through the call operator
// (`& ssh ...`). An argument that is not quoted splits on spaces, so an ssh
// target containing a space becomes several arguments; an argument that is
// double-quoted but contains an embedded double quote terminates the quoted
// region and lets the remainder be reinterpreted. PowerShell's double-quoted
// string also expands `$var` / `$(...)` and treats the backtick as its escape
// character. This helper:
//
//   - escapes each backtick as `` (PowerShell's literal-backtick escape) FIRST,
//     so the backticks it introduces for the other escapes are not themselves
//     re-escaped, and so a literal user backtick cannot start an escape;
//   - escapes each double quote as `" (backtick-quote, the PowerShell escape
//     for a literal double quote inside a double-quoted string);
//   - escapes each `$` as `$ so neither `$var` expansion nor `$(...)`
//     subexpression evaluation fires on user text;
//
// then wraps the whole thing in double quotes. The result is a single
// PowerShell argument carrying the literal value, safe to splice into an
// `& ssh <arg>` / `& scp <arg>` call site. Spec home:
// docs/spec/security/security.md and docs/spec/artifacts/deploy-scripts.md;
// closes the unquoted-target / quote-injection path in the PowerShell deploy
// variant (audit theme T4, finding D43).
func powerShellArgQuote(s string) string {
	escaped := strings.ReplaceAll(s, "`", "``")
	escaped = strings.ReplaceAll(escaped, `"`, "`\"")
	escaped = strings.ReplaceAll(escaped, "$", "`$")
	return `"` + escaped + `"`
}
