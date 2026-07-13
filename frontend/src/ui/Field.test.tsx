import { describe, it, expect } from 'vitest';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { Field, FIELD_INPUT_CLASS, FIELD_INPUT_MONO_CLASS, FIELD_SELECT_CLASS } from './Field';

// Field unit test (framework-refactor plan-10) — guards the shared form-field primitive. Rendered via
// react-dom/server so it needs no jsdom (the conformance vitest env is `node`). Elements are built
// with createElement (no JSX) so the test is independent of the runner's JSX-transform config.
describe('Field', () => {
  it('renders the caption label with the shared label class', () => {
    const html = renderToStaticMarkup(createElement(Field, { label: 'Name', value: 'x', readOnly: true }));
    expect(html).toContain('Name');
    expect(html).toContain('text-xs text-[var(--content-muted)]');
    // The label is a caption sibling of the control, not a wrapping/associated label.
    expect(html).not.toContain('for=');
  });

  it('renders the canonical text input by default', () => {
    const html = renderToStaticMarkup(createElement(Field, { label: 'Name', value: 'x', readOnly: true }));
    expect(html).toContain('<input');
    expect(html).toContain(`class="${FIELD_INPUT_CLASS}"`);
    expect(html).toContain('type="text"');
  });

  it('uses the font-mono control class when mono is set', () => {
    const html = renderToStaticMarkup(createElement(Field, { label: 'Key', value: 'x', readOnly: true, mono: true }));
    expect(html).toContain(`class="${FIELD_INPUT_MONO_CLASS}"`);
    expect(html).toContain('font-mono');
  });

  it('renders children as the control instead of an <input> (the select shape)', () => {
    const html = renderToStaticMarkup(
      createElement(
        Field,
        { label: 'Mode' },
        createElement('select', { className: FIELD_SELECT_CLASS }, createElement('option', { value: 'a' }, 'A')),
      ),
    );
    expect(html).toContain('<select');
    expect(html).toContain(FIELD_SELECT_CLASS);
    expect(html).not.toContain('<input');
    expect(html).toContain('Mode');
  });

  it('renders a hint line only when a hint is provided', () => {
    const withHint = renderToStaticMarkup(
      createElement(Field, { label: 'URL', value: 'x', readOnly: true, hint: 'the reachable URL' }),
    );
    expect(withHint).toContain('the reachable URL');
    expect(withHint).toContain('text-[10px] text-[var(--content-muted)] mt-0.5');

    const noHint = renderToStaticMarkup(createElement(Field, { label: 'URL', value: 'x', readOnly: true }));
    expect(noHint).not.toContain('text-[10px]');
  });

  it('forwards input props (type / placeholder / value)', () => {
    const html = renderToStaticMarkup(
      createElement(Field, { label: 'Port', type: 'number', value: 22, placeholder: '22', readOnly: true }),
    );
    expect(html).toContain('type="number"');
    expect(html).toContain('placeholder="22"');
    expect(html).toContain('value="22"');
  });

  it('lets an explicit className override the default control class', () => {
    const html = renderToStaticMarkup(
      createElement(Field, { label: 'X', value: 'x', readOnly: true, className: 'custom-control-class' }),
    );
    expect(html).toContain('class="custom-control-class"');
    expect(html).not.toContain(FIELD_INPUT_CLASS);
  });
});
