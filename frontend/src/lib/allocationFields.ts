// Persisted allocation state written by the compiler. These six fields are the executable sticky
// allocation; compiled_port is a read-only echo of the actual dial target and belongs only in the
// broader server-derived field set below.
export const PERSISTED_ALLOCATION_PIN_FIELDS = [
  'pinned_from_port',
  'pinned_to_port',
  'pinned_from_transit_ip',
  'pinned_to_transit_ip',
  'pinned_from_link_local',
  'pinned_to_link_local',
] as const;

// Every server-derived per-edge allocation field reconciled into the browser canvas or cleared at
// a custody/mode boundary. Keep this leaf definition shared by normalization, topology state, and
// controller canonicalization so a similarly named but differently sized list cannot be imported.
export const SERVER_ALLOCATION_FIELDS = [
  'compiled_port',
  ...PERSISTED_ALLOCATION_PIN_FIELDS,
] as const;
