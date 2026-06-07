# Compilation Pipeline

The compiler (`internal/compiler/compiler.go`) operates as a multi-pass pipeline:

| Pass | Name | Spec |
|---|---|---|
| 1 | Schema Validation (`validator.ValidateSchema`) | [validation.md](validation.md) |
| 2 | Semantic Validation (`validator.ValidateSemantic`) | [validation.md](validation.md) |
| 3 | IP Allocation (`allocator.AllocateIPs`) | [ip-allocation.md](ip-allocation.md) |
| 3b | Capability Inference (`InferCapabilitiesFromRole`) | below |
| 3c | Peer Derivation (`DerivePeers`) | [peer-derivation.md](peer-derivation.md) |
| 3d | CompiledPort Write-back | below |

## Enum normalization during validation

Validation MUST normalize the `Domain.routing_mode` enum before the value reaches any renderer: an
empty `routing_mode` is written back as `babel`, and `static`/`none` are rejected as
not-yet-implemented. The normalization is a write-back into the topology object so the value
round-trips. See [routing-modes.md](routing-modes.md) for the full enum contract, the rejection
rule, and why `Table = off` and `redistribute local` depend on it.

## Pass 3b: Capability Inference (`InferCapabilitiesFromRole`)

- Applies role-based capability overrides to each node
  (see [../roles/roles.md](../roles/roles.md))

## Pass 3d: CompiledPort Write-back

The compiler writes the allocated port back into `Edge.CompiledPort` so the frontend can
display/auto-fill it.

## Output: CompileResult

```go
type CompileResult struct {
    Topology         *model.Topology
    PeerMap          map[string][]PeerInfo       // nodeID → per-peer interfaces
    WireGuardConfigs map[string]string            // "nodeID:ifaceName" → config content
    BabelConfigs     map[string]string            // nodeID → babeld.conf content
    SysctlConfigs    map[string]string            // nodeID → sysctl content
    InstallScripts   map[string]string            // nodeID → install.sh content
    DeployScripts    map[string]string            // "deploy-all.sh" / "deploy-all.ps1"
    ClientConfigs    map[string]*ClientPeerInfo   // nodeID → client wg0 info
    Manifest         CompileManifest
}
```
