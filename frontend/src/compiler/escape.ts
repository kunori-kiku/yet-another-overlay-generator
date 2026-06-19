// Shell-escaping helpers — the TypeScript mirror of internal/renderer/escape.go.
//
// The generated install scripts run as root on every node and the deploy script runs on the operator's
// own machine; both interpolate user-supplied text (node names, ssh_host / ssh_user / ssh_alias) into
// shell command lines. These helpers render that text INERT so the payload survives only as a literal
// string. bashSingleQuote is also the `shq` template func (wireguard.go:216). The output MUST be
// byte-identical to escape.go so the rendered scripts match the Go oracle.

// bashSingleQuote wraps s in single quotes, rendering it inert in a POSIX shell. Mirrors
// renderer.bashSingleQuote (escape.go:48-50): inside single quotes the shell interprets nothing; the
// one exception is the single-quote character itself, replaced by the four-char close/escape/reopen
// idiom '\'' . Example: don't → 'don'\''t'.
export function bashSingleQuote(s: string): string {
  return "'" + s.replaceAll("'", "'\\''") + "'";
}

// powerShellArgQuote double-quotes s for a PowerShell argument context, escaping the characters
// PowerShell expands inside a double-quoted string. Mirrors renderer.powerShellArgQuote (escape.go:77-82)
// in EXACT order:
//   1. escape each backtick by doubling it (`` ) FIRST, so the backticks introduced for the other
//      escapes are not re-escaped and a literal user backtick cannot start an escape;
//   2. escape each double quote as `" (backtick-quote);
//   3. escape each `$` as `$ so neither $var expansion nor $(...) evaluation fires;
// then wrap the whole thing in double quotes.
export function powerShellArgQuote(s: string): string {
  let escaped = s.replaceAll('`', '``');
  escaped = escaped.replaceAll('"', '`"');
  escaped = escaped.replaceAll('$', '`$');
  return '"' + escaped + '"';
}
