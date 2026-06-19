// A tiny faithful subset of Go's text/template — the byte-exact rendering engine for the WireGuard,
// Babel, sysctl, and (Phase-4) install-script renderers.
//
// Why a subset interpreter rather than hand-translated string building? The Go renderers
// (internal/renderer/*.go) carry their output shape inside text/template strings with the {{- -}}
// whitespace-chomp markers. The single dominant byte-exact hazard is reproducing those chomp
// semantics. The cleanest faithful approach is to COPY the Go template strings VERBATIM into TS string
// constants (so the templates stay byte-identical to the Go source — a diff is a copy error, visible)
// and make only THIS interpreter responsible for correctness. The conformance harness (every rendered
// file === the Go golden, byte-for-byte) arbitrates.
//
// Supported Go text/template grammar (exactly what the YAOG templates use — nothing more; the
// byte-critical surface is kept as small as the actual renderers require, verified against the full
// renderer/export conformance corpus):
//   - text runs (verbatim, subject to {{- / -}} trimming of adjacent whitespace);
//   - {{ .Field }}, {{ .A.B }} (chained field access), {{ . }} (the current dot);
//   - {{ $ }} / {{ $.Field }} (the root data, even inside a range);
//   - {{ if PIPELINE }} ... {{ else }} ... {{ end }} (truthiness == Go's template "true" rule);
//   - {{ range PIPELINE }} ... {{ else }} ... {{ end }} (over a slice/array; dot becomes each element;
//     the else branch runs when the slice is empty — matching Go), including the loop-variable forms
//     {{ range $i, $v := PIPELINE }} (index + value) and {{ range $v := PIPELINE }} (value-only);
//   - {{ $var }} (reference to a range-bound loop variable);
//   - the single comparison operator the templates use, gt (Go's builtin), and the single custom func
//     "shq" (= escape.bashSingleQuote);
//   - the {{- (trim preceding whitespace incl. newlines) and -}} (trim following whitespace) markers.
//
// Deliberately NOT implemented (no YAOG template uses them — leaving them out shrinks the surface the
// conformance harness must trust): the {{ with }} action, a standalone {{ $var := PIPELINE }} assignment
// (range-var binding is the only `:=` form used), and the comparison/logic ops other than gt
// (eq/ne/lt/le/ge/and/or/not). A future template that needs one must re-add it WITH a Go-pinned test.
//
// Trimming rule (Go's exact behavior): a {{- consumes ALL trailing whitespace of the text immediately
// before the action; a -}} consumes ALL leading whitespace of the text immediately after the action.
// The relevant whitespace is exactly Go's LEXER set spaceChars = " \t\r\n" (text/template/parse/lex.go) —
// space, tab, carriage return, newline — NOT unicode.IsSpace; '\v' and '\f' are NOT trimmed.

import { bashSingleQuote } from '../escape';

// FuncMap is the set of template funcs available to a template. shq is the only custom func the YAOG
// renderers register (wireguard.go:217-219); the comparison operators below are Go text/template
// builtins reimplemented here.
export type TemplateFunc = (...args: unknown[]) => unknown;
export type FuncMap = Record<string, TemplateFunc>;

// templateFuncs mirrors renderer.templateFuncs (wireguard.go:217): the single custom "shq" func, the
// audited bashSingleQuote escaper. Go's builtin comparison funcs (gt/lt/eq/...) are handled by the
// pipeline evaluator below, not registered here (they are language builtins, not FuncMap entries).
export const templateFuncs: FuncMap = {
  shq: (...args: unknown[]) => bashSingleQuote(String(args[0])),
};

// ---- Lexer ----------------------------------------------------------------------------------------

// A lexical token: either a verbatim text run or an action ({{ ... }}). For an action we record whether
// it carried a left-trim ({{-) and/or right-trim (-}}) marker, and the trimmed inner source.
interface TextToken {
  kind: 'text';
  value: string;
}
interface ActionToken {
  kind: 'action';
  trimLeft: boolean;
  trimRight: boolean;
  inner: string; // the action body, with the {{-/-}} markers and surrounding spaces stripped
}
type Token = TextToken | ActionToken;

// isGoSpace mirrors the whitespace set Go's text/template LEXER actually uses for the {{- / -}} trim
// markers and inter-token splitting: spaceChars = " \t\r\n" (text/template/parse/lex.go), i.e. space,
// tab, carriage return, newline — NOT unicode.IsSpace. In particular '\v' and '\f' are NOT trimmed by
// Go's chomp markers, so this set must EXCLUDE them: a '{{- ' next to a '\v' leaves the '\v' intact.
function isGoSpace(ch: string): boolean {
  return ch === ' ' || ch === '\t' || ch === '\n' || ch === '\r';
}

function trimGoSpaceRight(s: string): string {
  let end = s.length;
  while (end > 0 && isGoSpace(s[end - 1])) {
    end--;
  }
  return s.slice(0, end);
}

function trimGoSpaceLeft(s: string): string {
  let start = 0;
  while (start < s.length && isGoSpace(s[start])) {
    start++;
  }
  return s.slice(start);
}

// lex splits a template string into text + action tokens, applying the {{- / -}} whitespace trims to
// the adjacent text tokens EXACTLY as Go's lexer does at parse time. A {{- trims the trailing
// whitespace of the preceding text token; a -}} trims the leading whitespace of the following text
// token.
function lex(src: string): Token[] {
  const tokens: Token[] = [];
  let i = 0;
  // pendingRightTrim: the previous action ended with -}}, so the next text token's leading whitespace
  // must be trimmed.
  let pendingRightTrim = false;

  const pushText = (value: string): void => {
    let v = value;
    if (pendingRightTrim) {
      v = trimGoSpaceLeft(v);
      pendingRightTrim = false;
    }
    tokens.push({ kind: 'text', value: v });
  };

  while (i < src.length) {
    const open = src.indexOf('{{', i);
    if (open === -1) {
      pushText(src.slice(i));
      break;
    }

    // Text before the action.
    let text = src.slice(i, open);

    // Detect a left-trim marker: {{- (followed by whitespace, per Go's lexer, but the YAOG templates
    // always write "{{- " so a simple check suffices and matches Go).
    let bodyStart = open + 2;
    let trimLeft = false;
    if (src[bodyStart] === '-' && isGoSpace(src[bodyStart + 1])) {
      trimLeft = true;
      bodyStart += 1;
    }

    const close = src.indexOf('}}', bodyStart);
    if (close === -1) {
      throw new Error(`template: unclosed action at offset ${open}`);
    }

    // Detect a right-trim marker: -}} (preceded by whitespace).
    let bodyEnd = close;
    let trimRight = false;
    if (src[close - 1] === '-' && isGoSpace(src[close - 2])) {
      trimRight = true;
      bodyEnd = close - 1;
    }

    // Emit the preceding text, trimming its tail if this action left-trims.
    if (trimLeft) {
      text = trimGoSpaceRight(text);
    }
    pushText(text);

    const inner = src.slice(bodyStart, bodyEnd).trim();
    tokens.push({ kind: 'action', trimLeft, trimRight, inner });
    pendingRightTrim = trimRight;

    i = close + 2;
  }

  return tokens;
}

// ---- Parser ---------------------------------------------------------------------------------------

// The node tree the lexer's tokens parse into. Control nodes (if/range) own their branch node lists;
// text and pipeline nodes are leaves.
type Node =
  | { kind: 'text'; value: string }
  | { kind: 'pipeline'; pipeline: PipelineExpr }
  | {
      kind: 'if';
      pipeline: PipelineExpr;
      body: Node[];
      elseBody: Node[] | null;
    }
  | {
      kind: 'range';
      pipeline: PipelineExpr;
      body: Node[];
      elseBody: Node[] | null;
      // Optional loop variables for the `{{ range $i, $v := PIPELINE }}` (index + value) and
      // `{{ range $v := PIPELINE }}` (value-only) forms. null when the range binds no variables
      // (`{{ range PIPELINE }}` — the dot is the only iteration binding). Mirrors Go's range var
      // assignment: with one var $v binds the element (same as dot), with two ($i, $v) $i binds the
      // 0-based index and $v the element.
      indexVar: string | null;
      valueVar: string | null;
    };

// A pipeline is a single command for the YAOG templates (no chained "| func" forms are used). It is a
// function/operator name (or empty for a bare term) plus its argument terms.
interface PipelineExpr {
  // The leading command word when it is a known func/operator (gt/lt/eq/.../shq); empty when the
  // pipeline is a bare term (a field access, variable, dot, or literal).
  func: string;
  args: Term[];
}

// A term is one argument of a pipeline command, or the bare value of a term-only pipeline.
type Term =
  | { kind: 'dot' } // .
  | { kind: 'field'; path: string[] } // .A.B  -> ["A","B"]
  | { kind: 'root' } // $
  | { kind: 'rootField'; path: string[] } // $.A.B
  | { kind: 'var'; name: string } // $x
  | { kind: 'varField'; name: string; path: string[] } // $x.A.B
  | { kind: 'number'; value: number } // 0, 25, ...
  | { kind: 'string'; value: string } // "..."
  | { kind: 'bool'; value: boolean }; // true / false

// COMPARISON_OPS are the builtin operator words a pipeline command can lead with. The YAOG templates use
// ONLY gt (the optional-line `{{ if gt .Field 0 }}` guard), so the set is just that — the other Go
// comparison/logic ops (eq/ne/lt/le/ge/and/or/not) are intentionally absent (no template uses them; see
// the grammar note at the top). A leading word not in this set (and not a registered func) is treated as
// a bare term (e.g. a field path) rather than a call.
const COMPARISON_OPS = new Set(['gt']);

function isComparisonOp(word: string): boolean {
  return COMPARISON_OPS.has(word);
}

// parseTerm parses a single argument term from a whitespace-split token.
function parseTerm(tok: string): Term {
  if (tok === '.') {
    return { kind: 'dot' };
  }
  if (tok === '$') {
    return { kind: 'root' };
  }
  if (tok.startsWith('$.')) {
    return { kind: 'rootField', path: tok.slice(2).split('.') };
  }
  if (tok.startsWith('$')) {
    // $x or $x.A.B — a variable reference, optionally with a trailing field path. Split on the FIRST
    // '.' so a bare $x has an empty path and $iface.Name yields name="iface", path=["Name"].
    const dot = tok.indexOf('.');
    if (dot !== -1) {
      return {
        kind: 'varField',
        name: tok.slice(1, dot),
        path: tok.slice(dot + 1).split('.'),
      };
    }
    return { kind: 'var', name: tok.slice(1) };
  }
  if (tok.startsWith('.')) {
    return { kind: 'field', path: tok.slice(1).split('.') };
  }
  if (tok === 'true') {
    return { kind: 'bool', value: true };
  }
  if (tok === 'false') {
    return { kind: 'bool', value: false };
  }
  if (tok.startsWith('"')) {
    // A double-quoted string literal. The YAOG templates use only simple literals (no escapes), so a
    // plain strip of the surrounding quotes is faithful.
    return { kind: 'string', value: tok.slice(1, -1) };
  }
  const n = Number(tok);
  if (!Number.isNaN(n) && tok.length > 0) {
    return { kind: 'number', value: n };
  }
  throw new Error(`template: unrecognized term ${JSON.stringify(tok)}`);
}

// parsePipeline parses the inner source of an action (after any control keyword has been stripped) into
// a PipelineExpr. The YAOG templates use only single-command pipelines (a func with args, or a bare
// term), so the parser splits on whitespace and treats a known op/func as the leading command.
function parsePipeline(src: string): PipelineExpr {
  const tokens = splitArgs(src);
  if (tokens.length === 0) {
    throw new Error('template: empty pipeline');
  }
  const head = tokens[0];
  if (isComparisonOp(head) || head in templateFuncs) {
    return { func: head, args: tokens.slice(1).map(parseTerm) };
  }
  // A bare term (field/var/dot/literal); represent it as a func-less pipeline with one arg.
  if (tokens.length !== 1) {
    throw new Error(`template: unsupported multi-term pipeline ${JSON.stringify(src)}`);
  }
  return { func: '', args: [parseTerm(head)] };
}

// splitArgs splits a pipeline source into whitespace-delimited tokens, keeping double-quoted string
// literals intact (so a quoted literal containing a space stays one token). The YAOG templates contain
// no such spaces today, but keeping quotes intact is the correct, future-proof split.
function splitArgs(src: string): string[] {
  const out: string[] = [];
  let i = 0;
  const n = src.length;
  while (i < n) {
    while (i < n && isGoSpace(src[i])) {
      i++;
    }
    if (i >= n) {
      break;
    }
    if (src[i] === '"') {
      let j = i + 1;
      while (j < n && src[j] !== '"') {
        if (src[j] === '\\') {
          j++;
        }
        j++;
      }
      out.push(src.slice(i, j + 1));
      i = j + 1;
    } else {
      let j = i;
      while (j < n && !isGoSpace(src[j])) {
        j++;
      }
      out.push(src.slice(i, j));
      i = j;
    }
  }
  return out;
}

// A cursor over the token stream for the recursive-descent parser. A plain closure (no class) keeps the
// module fully erasable (the project's tsconfig sets erasableSyntaxOnly, which forbids the
// parameter-property class shorthand).
interface TokenCursor {
  next(): Token | undefined;
}

function newTokenCursor(tokens: Token[]): TokenCursor {
  let pos = 0;
  return {
    next(): Token | undefined {
      return tokens[pos++];
    },
  };
}

// parseNodes parses a node list up to (but not consuming the body of) a terminator keyword. The
// returned `terminator` is the control keyword ("else"/"end") that stopped the parse, or null at EOF.
function parseNodes(
  cur: TokenCursor,
  stops: Set<string>,
): { nodes: Node[]; terminator: string | null } {
  const nodes: Node[] = [];
  for (;;) {
    const tok = cur.next();
    if (tok === undefined) {
      return { nodes, terminator: null };
    }
    if (tok.kind === 'text') {
      // Skip empty text tokens (a trim can produce ""), matching Go which emits no output for them.
      if (tok.value !== '') {
        nodes.push({ kind: 'text', value: tok.value });
      }
      continue;
    }

    const inner = tok.inner;
    const word = firstWord(inner);

    if (stops.has(word)) {
      return { nodes, terminator: word };
    }

    switch (word) {
      case 'if':
      case 'range': {
        const rest = inner.slice(word.length).trim();
        // range supports loop-variable binding: `range $i, $v := PIPELINE` (index + value) and
        // `range $v := PIPELINE` (value-only). Strip the binding before parsing the pipeline; if never
        // binds variables in the YAOG templates, so it parses the rest directly.
        let indexVar: string | null = null;
        let valueVar: string | null = null;
        let pipelineSrc = rest;
        if (word === 'range') {
          const binding = parseRangeBinding(rest);
          if (binding !== null) {
            indexVar = binding.indexVar;
            valueVar = binding.valueVar;
            pipelineSrc = binding.pipelineSrc;
          }
        }
        const pipeline = parsePipeline(pipelineSrc);
        const body = parseNodes(cur, new Set(['else', 'end']));
        let elseBody: Node[] | null = null;
        let terminator = body.terminator;
        if (terminator === 'else') {
          const elseParsed = parseNodes(cur, new Set(['end']));
          elseBody = elseParsed.nodes;
          terminator = elseParsed.terminator;
        }
        if (terminator !== 'end') {
          throw new Error(`template: unterminated {{ ${word} }}`);
        }
        if (word === 'range') {
          nodes.push({
            kind: 'range',
            pipeline,
            body: body.nodes,
            elseBody,
            indexVar,
            valueVar,
          });
        } else {
          nodes.push({ kind: 'if', pipeline, body: body.nodes, elseBody });
        }
        break;
      }
      default: {
        // A bare {{ pipeline }} output (a field/var/dot access or a gt guard). A standalone
        // {{ $var := ... }} assignment is not supported (no YAOG template uses one); range-var binding
        // is the only `:=` form and is handled in the range arm above.
        nodes.push({ kind: 'pipeline', pipeline: parsePipeline(inner) });
        break;
      }
    }
  }
}

// parseRangeBinding recognizes a range loop-variable assignment: `$i, $v := PIPELINE` (index + value)
// or `$v := PIPELINE` (value-only), returning the bound variable names plus the remaining pipeline
// source. Returns null when the range has no `:=` binding (the `range PIPELINE` form). Mirrors Go's
// template range syntax. Variable names are the bare identifiers (no leading '$').
function parseRangeBinding(
  src: string,
): { indexVar: string | null; valueVar: string; pipelineSrc: string } | null {
  // Two-variable form: $i, $v := PIPELINE
  const two = src.match(
    /^\$([A-Za-z_][A-Za-z0-9_]*)\s*,\s*\$([A-Za-z_][A-Za-z0-9_]*)\s*:=\s*(.+)$/s,
  );
  if (two !== null) {
    return { indexVar: two[1], valueVar: two[2], pipelineSrc: two[3].trim() };
  }
  // One-variable form: $v := PIPELINE
  const one = src.match(/^\$([A-Za-z_][A-Za-z0-9_]*)\s*:=\s*(.+)$/s);
  if (one !== null) {
    return { indexVar: null, valueVar: one[1], pipelineSrc: one[2].trim() };
  }
  return null;
}

function firstWord(s: string): string {
  const m = s.match(/^\S+/);
  return m ? m[0] : '';
}

// parse compiles a template string into a node tree.
function parse(src: string): Node[] {
  const tokens = lex(src);
  const cur = newTokenCursor(tokens);
  const result = parseNodes(cur, new Set());
  if (result.terminator !== null) {
    throw new Error(`template: unexpected {{ ${result.terminator} }}`);
  }
  return result.nodes;
}

// ---- Evaluator ------------------------------------------------------------------------------------

// EvalContext carries the current dot, the root data, and the in-scope variables (for $x lookups).
interface EvalContext {
  dot: unknown;
  root: unknown;
  vars: Map<string, unknown>;
}

// resolveField walks a dot-separated field path on a value, mirroring Go's struct-field access. The Go
// renderers feed plain objects whose keys are the exact Go field names (PascalCase), so the lookup is a
// direct property access. A missing field is an error (Go would too, for a non-existent field), but the
// YAOG templates never reference one.
function resolveField(base: unknown, path: string[]): unknown {
  let cur = base;
  for (const part of path) {
    if (cur === null || cur === undefined) {
      throw new Error(`template: nil dereference accessing .${part}`);
    }
    if (typeof cur !== 'object') {
      throw new Error(`template: cannot access .${part} on non-object`);
    }
    cur = (cur as Record<string, unknown>)[part];
  }
  return cur;
}

// evalTerm evaluates a single term to its runtime value.
function evalTerm(term: Term, ctx: EvalContext): unknown {
  switch (term.kind) {
    case 'dot':
      return ctx.dot;
    case 'field':
      return resolveField(ctx.dot, term.path);
    case 'root':
      return ctx.root;
    case 'rootField':
      return resolveField(ctx.root, term.path);
    case 'var': {
      if (!ctx.vars.has(term.name)) {
        throw new Error(`template: undefined variable $${term.name}`);
      }
      return ctx.vars.get(term.name);
    }
    case 'varField': {
      if (!ctx.vars.has(term.name)) {
        throw new Error(`template: undefined variable $${term.name}`);
      }
      return resolveField(ctx.vars.get(term.name), term.path);
    }
    case 'number':
      return term.value;
    case 'string':
      return term.value;
    case 'bool':
      return term.value;
  }
}

// asNumber coerces a value to a number for the comparison operators (Go compares int fields).
function asNumber(v: unknown): number {
  if (typeof v === 'number') {
    return v;
  }
  if (typeof v === 'boolean') {
    return v ? 1 : 0;
  }
  throw new Error(`template: cannot compare non-number ${JSON.stringify(v)}`);
}

// evalPipeline evaluates a pipeline to its value. A func-less pipeline returns its single term; the gt
// operator returns a boolean; the shq func returns the escaped string. Only gt and shq are reachable —
// the parser admits no other operator (see COMPARISON_OPS).
function evalPipeline(p: PipelineExpr, ctx: EvalContext): unknown {
  if (p.func === '') {
    return evalTerm(p.args[0], ctx);
  }

  if (p.func in templateFuncs) {
    const fnArgs = p.args.map((a) => evalTerm(a, ctx));
    return templateFuncs[p.func](...fnArgs);
  }

  const vals = p.args.map((a) => evalTerm(a, ctx));
  switch (p.func) {
    case 'gt':
      return asNumber(vals[0]) > asNumber(vals[1]);
    default:
      throw new Error(`template: unknown func ${p.func}`);
  }
}

// isTruthy mirrors Go's template "is true" rule for if: false, 0, "", nil, and an empty array/slice/map
// are false; everything else is true.
function isTruthy(v: unknown): boolean {
  if (v === null || v === undefined) {
    return false;
  }
  if (typeof v === 'boolean') {
    return v;
  }
  if (typeof v === 'number') {
    return v !== 0;
  }
  if (typeof v === 'string') {
    return v.length > 0;
  }
  if (Array.isArray(v)) {
    return v.length > 0;
  }
  if (typeof v === 'object') {
    return Object.keys(v as object).length > 0;
  }
  return true;
}

// formatValue stringifies a pipeline value for {{ ... }} output, mirroring Go's fmt-based default
// rendering of the scalar types the YAOG templates emit (string verbatim, int as base-10, bool as
// true/false).
function formatValue(v: unknown): string {
  if (v === null || v === undefined) {
    // Go renders a nil interface as "<no value>"; the YAOG templates never output one.
    return '<no value>';
  }
  if (typeof v === 'string') {
    return v;
  }
  if (typeof v === 'number') {
    return String(v);
  }
  if (typeof v === 'boolean') {
    return v ? 'true' : 'false';
  }
  return String(v);
}

// render walks the node tree, appending output to `out`.
function renderNodes(nodes: Node[], ctx: EvalContext, out: string[]): void {
  for (const node of nodes) {
    switch (node.kind) {
      case 'text':
        out.push(node.value);
        break;
      case 'pipeline':
        out.push(formatValue(evalPipeline(node.pipeline, ctx)));
        break;
      case 'if': {
        const v = evalPipeline(node.pipeline, ctx);
        if (isTruthy(v)) {
          renderNodes(node.body, ctx, out);
        } else if (node.elseBody !== null) {
          renderNodes(node.elseBody, ctx, out);
        }
        break;
      }
      case 'range': {
        const v = evalPipeline(node.pipeline, ctx);
        const items = Array.isArray(v) ? v : [];
        if (items.length === 0) {
          if (node.elseBody !== null) {
            renderNodes(node.elseBody, ctx, out);
          }
          break;
        }
        for (let idx = 0; idx < items.length; idx++) {
          const item = items[idx];
          // Go's range gives each iteration a fresh variable scope but a SHARED outer scope; reusing
          // the same vars map is faithful for the YAOG templates (no per-iteration $x:= shadowing that
          // must reset). The dot becomes the element; $ stays the root. When the range binds loop
          // variables (`range $i, $v := ...` / `range $v := ...`), set them per iteration: $v = element,
          // $i = 0-based index — matching Go's two-variable range.
          if (node.valueVar !== null) {
            ctx.vars.set(node.valueVar, item);
          }
          if (node.indexVar !== null) {
            ctx.vars.set(node.indexVar, idx);
          }
          renderNodes(node.body, { ...ctx, dot: item }, out);
        }
        break;
      }
    }
  }
}

// CompiledTemplate is a parsed, reusable template.
export class CompiledTemplate {
  private readonly nodes: Node[];
  constructor(src: string) {
    this.nodes = parse(src);
  }
  // execute renders the template against the given data (the root dot).
  execute(data: unknown): string {
    const out: string[] = [];
    renderNodes(
      this.nodes,
      { dot: data, root: data, vars: new Map() },
      out,
    );
    return out.join('');
  }
}

// renderTemplate is the byte-exact mirror of renderer.renderTemplate (wireguard.go:221-233): compile
// the template (with the shq func registered) and execute it against `data`, returning the rendered
// string. The `name` argument is accepted for parity with the Go signature (Go uses it only in error
// messages); it does not affect output.
export function renderTemplate(_name: string, tmpl: string, data: unknown): string {
  return new CompiledTemplate(tmpl).execute(data);
}
