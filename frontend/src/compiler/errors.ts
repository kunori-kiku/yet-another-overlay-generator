// CompileError — the TypeScript mirror of internal/apierr.Error: a coded compile failure carrying a
// stable string `code` (matching the Go apierr Code constants, e.g. "compile_transit_pool_exhausted")
// plus `params` for message interpolation. The conformance harness pins TS failures to the Go side by
// error-code SET, so the code strings here MUST equal the Go apierr literals byte-for-byte.
//
// This is the surviving FE-side code carrier; per plan-4 (codes.ts SUPERSEDED), there is no generated
// code enum — the canonical code strings live in the Go apierr/validator source and the FE i18n
// catalog. These constants are the subset the leaf primitives raise; later phases add the rest.

// Code strings raised by the allocation primitives + the compile orchestration, mirroring the Go
// apierr Code constants (internal/apierr/apierr.go:46-64). The allocation/keygen codes ride the apierr
// channel of the conformance harness and MUST equal the Go literals byte-for-byte.
export const CompileCode = {
  // Transit / overlay / port allocation (apierr.go:57-64).
  TransitPoolExhausted: 'compile_transit_pool_exhausted',
  TransitCIDRInvalid: 'compile_transit_cidr_invalid',
  TransitCIDRNotIPv4: 'compile_transit_cidr_not_ipv4',
  ListenPortExhausted: 'compile_listen_port_exhausted',
  OverlayCIDRInvalid: 'compile_overlay_cidr_invalid',
  OverlayPoolExhausted: 'compile_overlay_pool_exhausted',
  OverlayScanBudgetExceeded: 'compile_overlay_scan_budget_exceeded',
  NodeUnknownDomain: 'compile_node_unknown_domain',
  // Key derivation (apierr.go:46-49) — raised by the AirGap key pre-pass in index.ts.
  KeygenMissingPubkey: 'keygen_missing_pubkey',
  KeygenPrivkeyParse: 'keygen_privkey_parse_failed',
  KeygenPinnedNoPrivkey: 'keygen_pinned_pubkey_no_privkey',
  KeygenGenerateFailed: 'keygen_generate_failed',
  // Validation-failure SENTINEL (NOT a Go apierr code): the Go compiler rejects an invalid topology
  // with a plain fmt.Errorf wrap ("topology failed {schema,semantic} validation"), which is a DIFFERENT
  // channel from the apierr envelope (validator/code.go's design lock). The conformance harness routes
  // such a failure to the validator channel (it runs validate() directly), so this sentinel never
  // appears in the apierr channel — it only lets a compile() caller distinguish a validation rejection
  // from a coded allocation/key failure. Prefixed "ts_" so it can never collide with a real Go code.
  TopologyValidationFailed: 'ts_topology_validation_failed',
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
