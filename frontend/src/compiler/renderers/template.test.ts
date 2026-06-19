import { describe, expect, it } from 'vitest';

import { renderTemplate } from './template';

// Unit tests for the Go text/template subset interpreter (template.ts). Every expected value here was
// produced by the real Go text/template engine (a probe program in tmp run with the same template
// string + data), so these tests pin the {{- -}} chomp semantics and the pipeline evaluator against
// the authoritative Go behavior independently of the renderer goldens. A divergence here is a template
// engine bug, isolated from the renderer port.

const r = (tmpl: string, data: unknown): string => renderTemplate('t', tmpl, data);

describe('template chomp ({{- -}}) semantics', () => {
  it('left-trim strips preceding whitespace including one+ newlines', () => {
    // a\n{{- if true }}...  -> the \n before {{- is consumed.
    expect(r('a\n{{- if true }}\nb\n{{- end }}\nc', null)).toBe('a\nb\nc');
  });

  it('right-trim on a range head strips the following newline; empty slice yields only the head', () => {
    expect(
      r('head\n{{ range .Items -}}\nx {{ . }}\n{{ end }}', { Items: [] }),
    ).toBe('head\n');
  });

  it('range over multiple items repeats the body, right-trim eating the head newline each time', () => {
    expect(
      r('head\n{{ range .Items -}}\nx {{ . }}\n{{ end }}', { Items: ['a', 'b'] }),
    ).toBe('head\nx a\nx b\n');
  });

  it('if-else with left-trim selects the else branch and trims correctly', () => {
    expect(r('x\n{{- if .F }}\nyes\n{{- else }}\nno\n{{- end }}\n', { F: false })).toBe(
      'x\nno\n',
    );
  });

  it('leading {{- if }} after text trims the blank line, matching the sysctl shape', () => {
    const s = '# Sysctl\n\n{{- if .E }}\n\nfoo\n{{- else }}\n\nbar\n{{- end }}\n';
    expect(r(s, { E: true })).toBe('# Sysctl\n\nfoo\n');
    expect(r(s, { E: false })).toBe('# Sysctl\n\nbar\n');
  });

  // Go's lexer trim set is spaceChars = " \t\r\n" — it does NOT include vertical tab ('\v') or form
  // feed ('\f'). So a {{- next to a '\v' (or '\f') leaves that byte intact; only space/tab/CR/LF are
  // chomped. Both expected values were produced by the real Go text/template engine.
  it('left-trim does NOT consume a preceding vertical tab', () => {
    expect(r('A\v{{- 1 }}', null)).toBe('A\v1');
  });
  it('right-trim does NOT consume a following vertical tab', () => {
    expect(r('{{ 1 -}}\vB', null)).toBe('1\vB');
  });
});

describe('comparison operators (gt) on int fields', () => {
  it('gt true emits the block', () => {
    expect(r('{{ if gt .N 0 }}P={{ .N }}{{ end }}', { N: 5 })).toBe('P=5');
  });
  it('gt false (zero) omits the block', () => {
    expect(r('{{ if gt .N 0 }}P={{ .N }}{{ end }}', { N: 0 })).toBe('');
  });
});

describe('the wireguard optional-line shape (ListenPort / MTU)', () => {
  const w =
    'Table = off\n{{- if gt .LP 0 }}\nListenPort = {{ .LP }}\n{{- end }}\n{{- if gt .MTU 0 }}\nMTU = {{ .MTU }}\n{{- end }}\n\nnext';
  it('LP set, MTU 0 omits the MTU line', () => {
    expect(r(w, { LP: 51820, MTU: 0 })).toBe('Table = off\nListenPort = 51820\n\nnext');
  });
  it('LP and MTU both set emits both lines', () => {
    expect(r(w, { LP: 51820, MTU: 1408 })).toBe(
      'Table = off\nListenPort = 51820\nMTU = 1408\n\nnext',
    );
  });
});

describe('root ($) access and nested if inside a range (the babel interface line)', () => {
  it('$.Field resolves the root data even when the dot is a range element', () => {
    expect(
      r(
        '{{ range .Items -}}\nname={{ .Name }} hi={{ if gt $.Hello 0 }} hello {{ $.Hello }}{{ end }}\n{{ end }}',
        { Hello: 4, Items: [{ Name: 'a' }] },
      ),
    ).toBe('name=a hi= hello 4\n');
  });
});

describe('the babel redistribute block shape (range with {{ end -}})', () => {
  const tmpl =
    '# Route redistribution\n{{ range .L -}}\nredistribute local ip {{ . }} allow\n{{ end -}}\n# Deny all other local routes\nredistribute local deny\n';
  it('one prefix', () => {
    expect(r(tmpl, { L: ['10.33.0.2/32'] })).toBe(
      '# Route redistribution\nredistribute local ip 10.33.0.2/32 allow\n# Deny all other local routes\nredistribute local deny\n',
    );
  });
  it('two prefixes', () => {
    expect(r(tmpl, { L: ['a', 'b'] })).toBe(
      '# Route redistribution\nredistribute local ip a allow\nredistribute local ip b allow\n# Deny all other local routes\nredistribute local deny\n',
    );
  });
  it('empty list collapses to just the surrounding lines', () => {
    expect(r(tmpl, { L: [] })).toBe(
      '# Route redistribution\n# Deny all other local routes\nredistribute local deny\n',
    );
  });
});

describe('consecutive empty ranges with {{ end -}} collapse to the tail', () => {
  it('both empty', () => {
    expect(
      r(
        '{{ range .A -}}\nA {{ . }}\n{{ end -}}\n{{ range .B -}}\nB {{ . }}\n{{ end -}}\ntail\n',
        { A: [], B: [] },
      ),
    ).toBe('tail\n');
  });
});

describe('the shq custom func (bashSingleQuote)', () => {
  it('wraps a value in single quotes', () => {
    expect(r('x={{ shq .V }}', { V: 'hello' })).toBe("x='hello'");
  });
  it('escapes an embedded single quote with the close/escape/reopen idiom', () => {
    expect(r('x={{ shq .V }}', { V: "don't" })).toBe("x='don'\\''t'");
  });
});

describe('$var := assignment and reference', () => {
  it('declares and uses a variable', () => {
    expect(r('{{ $n := .N }}value={{ $n }}', { N: 42 })).toBe('value=42');
  });
});
