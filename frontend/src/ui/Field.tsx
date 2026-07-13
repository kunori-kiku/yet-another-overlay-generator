import type { InputHTMLAttributes, ReactNode } from 'react';

// Field — the single form-field primitive for the design / settings side-panels: a caption label over
// a full-width control, optionally with a small hint line under it. It replaces the label+control
// markup + control class strings that were hand-repeated ~50× across ~10 components (DomainEditor,
// NodeEditor, EdgeEditor, the deploy settings panels, …), plus the two local `field()` closures that
// had grown in AgentUpdateSettings and BootstrapSettings. Rendered markup + behaviour are identical to
// those sites — this is a pure refactor (framework-refactor plan-10).
//
// Two shapes, mirroring the migrated sites:
//   - Default: pass input props (value / onChange / type / placeholder / …) and Field renders the
//     canonical <input>. `mono` selects the font-mono variant; `hint` adds the trailing <p>.
//   - Custom control: pass `children` (a <select>, or a grouped control) and Field renders the label
//     + your control verbatim. Use the exported FIELD_SELECT_CLASS / FIELD_INPUT_CLASS so the control
//     keeps the same class string.
//
// The label is a caption (no htmlFor, not wrapping the control), exactly as every migrated site had
// it — clicking it does not focus the control; behaviour is unchanged.

// The control class strings, single-sourced (previously inlined at every site). FIELD_SELECT_CLASS
// deliberately omits focus:/outline-none — no <select> site carried them.
export const FIELD_INPUT_CLASS =
  'w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)] focus:border-[var(--accent)] outline-none';
export const FIELD_INPUT_MONO_CLASS =
  'w-full px-2 py-1 bg-[var(--control)] rounded text-sm font-mono border border-[var(--hairline)] focus:border-[var(--accent)] outline-none';
export const FIELD_SELECT_CLASS =
  'w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)]';

const LABEL_CLASS = 'text-xs text-[var(--content-muted)]';
const HINT_CLASS = 'text-[10px] text-[var(--content-muted)] mt-0.5';

export type FieldProps = Omit<InputHTMLAttributes<HTMLInputElement>, 'children'> & {
  /** The caption shown above the control. */
  label: ReactNode;
  /** Render the font-mono input variant (keys / IPs / hashes). Ignored when `children` is given. */
  mono?: boolean;
  /** Optional small hint line rendered under the control. */
  hint?: ReactNode;
  /** A custom control (e.g. a <select> or grouped inputs); when present it replaces the <input>. */
  children?: ReactNode;
};

export function Field({ label, mono, hint, children, className, ...inputProps }: FieldProps) {
  return (
    <div>
      <label className={LABEL_CLASS}>{label}</label>
      {children ?? (
        <input
          type="text"
          className={className ?? (mono ? FIELD_INPUT_MONO_CLASS : FIELD_INPUT_CLASS)}
          {...inputProps}
        />
      )}
      {hint !== undefined && <p className={HINT_CLASS}>{hint}</p>}
    </div>
  );
}
