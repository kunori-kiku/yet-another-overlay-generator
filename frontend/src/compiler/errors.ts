// CompileError — the TypeScript mirror of internal/apierr.Error: a coded compile failure carrying a
// stable string `code` (matching the Go apierr Code constants, e.g. "compile_transit_pool_exhausted")
// plus `params` for message interpolation. The conformance harness pins TS failures to the Go side by
// error-code SET, so the code strings here MUST equal the Go apierr literals byte-for-byte.
//
// This is the surviving FE-side code carrier; per plan-4 (codes.ts SUPERSEDED), there is no generated
// code enum — the canonical code strings live in the Go apierr/validator source and the FE i18n
// catalog. These constants are the subset the leaf primitives raise; later phases add the rest.

// Code strings raised by the leaf allocation primitives, mirroring internal/apierr/apierr.go:57-64.
export const CompileCode = {
  TransitPoolExhausted: 'compile_transit_pool_exhausted',
  TransitCIDRInvalid: 'compile_transit_cidr_invalid',
  TransitCIDRNotIPv4: 'compile_transit_cidr_not_ipv4',
  ListenPortExhausted: 'compile_listen_port_exhausted',
  OverlayCIDRInvalid: 'compile_overlay_cidr_invalid',
  OverlayPoolExhausted: 'compile_overlay_pool_exhausted',
  OverlayScanBudgetExceeded: 'compile_overlay_scan_budget_exceeded',
  NodeUnknownDomain: 'compile_node_unknown_domain',
} as const;

export type CompileCodeValue = (typeof CompileCode)[keyof typeof CompileCode];

// CompileError carries a coded compile failure. `code` is the stable apierr string the harness pins
// against; `params` mirrors apierr's With(key, value) interpolation map (string values, as Go renders
// them). The English `message` is best-effort context for logs/throw inspection — the code is the
// load-bearing field.
export class CompileError extends Error {
  readonly code: string;
  readonly params: Record<string, string>;

  constructor(code: string, params: Record<string, string> = {}, message?: string) {
    super(message ?? code);
    this.name = 'CompileError';
    this.code = code;
    this.params = params;
  }
}
