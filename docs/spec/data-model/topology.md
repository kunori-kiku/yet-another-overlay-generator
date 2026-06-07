# Topology (Root Object)

```go
type Topology struct {
    Project            Project       `json:"project"`
    Domains            []Domain      `json:"domains"`
    Nodes              []Node        `json:"nodes"`
    Edges              []Edge        `json:"edges"`
    RoutePolicies      []RoutePolicy `json:"route_policies,omitempty"`
    AllocSchemaVersion int           `json:"alloc_schema_version,omitempty"`
}
```

See: [domain.md](domain.md), [node.md](node.md), [edge.md](edge.md),
[route-policy.md](route-policy.md).

## Allocation schema version

`alloc_schema_version` records which version of the sticky-pin allocation scheme produced this
topology's pinned values (Edge `pinned_*` fields). It exists so a future change to the pinning
format can detect and migrate prior allocations rather than mis-reading them; this is invariant I10
in [../compiler/allocation-stability.md](../compiler/allocation-stability.md).

- The compiler MUST stamp the current scheme version onto the topology when it writes back pinned
  values.
- An absent or zero value denotes a topology from the pre-pinning era (or a frontend that does
  not yet echo the field). Any `pinned_*` values present are interpreted as the current format
  (version 1) and honored when valid — the version exists to branch on or migrate FUTURE format
  changes, not to gate whether pins are honored today. The compiler restamps the current version
  on every compile.
- The version MUST be bumped only when the meaning or format of the `pinned_*` fields changes, so
  that older topologies can be branched on or migrated.

> **Compliance:** the Topology struct currently has no `alloc_schema_version` field
> (`internal/model/topology.go`); allocation is positional and unversioned. Added with the
> sticky-pin allocation work (see [../compiler/allocation-stability.md](../compiler/allocation-stability.md)).

## Project

```go
type Project struct {
    ID          string `json:"id"`
    Name        string `json:"name"`
    Description string `json:"description,omitempty"`
    Version     string `json:"version,omitempty"`
}
```
