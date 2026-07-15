# Artifact Naming and Deploy Identity

YAOG renders one complete directory per node and two project-level deploy helpers. Every producer
and consumer must agree on one identity namespace; otherwise one node can be silently skipped or,
worse, consume another node's keys and configuration. The canonical bundle identity is the stable
node ID. The human node name is display text and remains an input only to name-derived WireGuard
interfaces.

The shared leaf package `internal/naming` owns the portable node-ID and WireGuard-interface naming
rules. Validators, exporters, renderers, and compilers import that package rather than maintaining
parallel sanitizers.

Related: [export-bundle.md](./export-bundle.md), [deploy-scripts.md](./deploy-scripts.md),
[wireguard.md](./wireguard.md), [../compiler/peer-derivation.md](../compiler/peer-derivation.md), and
[../frontend/architecture.md](../frontend/architecture.md).

## Canonical per-node directory

Every export surface writes and reads the complete per-node bundle under:

```text
<artifact-root>/<node.ID>/
```

This applies to:

- `internal/artifacts.Export` (CLI files and the controller's temporary stage export);
- `internal/localcompile.CompileArtifacts.Files` (`nodeID -> relpath -> content`);
- the browser/WASM ZIP (`<nodeID>/<relpath>`);
- the controller stage reader; and
- AirGap `deploy-all.sh` / `deploy-all.ps1` lookups.

Consumers must not fall back to `node.Name`, try an ID-then-name search, or derive a flat
`<node>.install.sh` wrapper. Those retired presentations create multiple names for one authority and
make collision behavior filesystem-dependent.

Project helpers share the artifact root with node directories, so `deploy-all.sh` and
`deploy-all.ps1` are reserved node IDs.

## Portable node-ID contract

`naming.ValidPortableNodeID` is the shared path-component predicate. A valid node ID:

1. is non-empty and is neither `.` nor `..`;
2. contains only ASCII letters, digits, `.`, `_`, and `-` (`[A-Za-z0-9._-]+`);
3. is at most `naming.MaxPortableNodeIDLength` bytes (currently 240; IDs are ASCII);
4. does not end with `.`;
5. is not `deploy-all.sh` or `deploy-all.ps1`, ignoring ASCII letter case; and
6. does not have a Windows device basename, ignoring case: `CON`, `PRN`, `AUX`, `NUL`,
   `COM1`–`COM9`, or `LPT1`–`LPT9`. The basename rule also rejects extensions such as `con.txt`.

The 240-byte ceiling leaves room for the AirGap remote staging component
`yaog-<node-id>-XXXXXXXX` under the common 255-byte filesystem-component limit.

Node IDs must also remain unique after ASCII case-folding. `Alpha` and `alpha` are distinct Go map
keys and Linux paths but collide after extraction on case-insensitive Windows filesystems.
`naming.PortableNodeIDKey` supplies the semantic validator's collision key.

Schema validation reports non-portable IDs before compilation; semantic validation reports the
second case-folding collision and names both IDs. Export and deploy rendering repeat the portable
predicate as defence in depth because they are callable independently in tests and internal code.

## Remote staging identity

An operational AirGap deploy helper creates a fresh remote directory with:

```text
/tmp/yaog-<node.ID>-XXXXXXXX
```

It validates `mktemp`'s returned path against that exact prefix and eight-character suffix, uploads
the whole local `<node.ID>/` directory to `<remote>/bundle`, executes the copied
`bundle/install.sh`, and removes the fresh directory. The node ID—not a display name—therefore
binds the local directory, remote path, and target node. A fresh directory avoids predictable-file
replacement and concurrent-deploy clobbering.

AgentHeld deploy helpers are fail-closed guidance stubs and never construct or execute a remote
installer path; AgentHeld application goes through the enrolled agent or `yaog-agent kit apply`.

## Name and interface uniqueness invariants

Node names remain operator-facing identity and feed the remote-peer portion of WireGuard interface
names. Validation enforces:

| Invariant | Rule | Rationale |
|---|---|---|
| N1 | Two nodes cannot share the exact raw `node.Name` | Duplicate display identities are ambiguous to operators and every name-derived artifact. |
| N2 | Node IDs remain unique after portable case-folding | ID-keyed directories must survive Windows extraction without aliasing. |
| N3 | Two distinct remote node names cannot produce the same primary `WgInterfaceName` | A collision would overwrite a WireGuard config and Babel interface line. |
| N4 | Every compiled interface name on one node is unique across primary and backup links | Parallel-link/hash collisions must fail before rendering. |

There is no sanitized-installer-name invariant: sanitized flat installers no longer exist. Bundle
directory uniqueness comes entirely from the portable node-ID rules.

## WireGuard interface-name algorithm

Each per-peer WireGuard interface is named from the **remote** peer's node name through
`naming.WgInterfaceName`. Linux limits interface names to 15 bytes, so the function has a short and
a hashed long path. The compiler stamps the result onto `PeerInfo.InterfaceName`; every downstream
consumer must use that compiled value.

Given `remoteName`:

1. `clean := lowercase(remoteName)`, mapping every rune outside `[a-z0-9-]` to `-`.
   Underscore is not preserved.
2. `name := "wg-" + clean`.
3. If `len(name) <= 15`, return `name`.
4. Otherwise return `"wg-" + clean[:8] + sha256(remoteName)[:4]`, with the clean slice bounded by
   its actual length.

The long form is exactly 15 bytes (`3 + 8 + 4`). The hash suffix prevents distinct long names with
the same prefix from collapsing through plain truncation; a consumer must not substitute
`name[:15]`.

### Edge-aware backup-link names

Parallel links can put several interfaces toward the same remote peer on one node. The edge-aware
authority is `naming.WgInterfaceNameForEdge(remoteName, edgeID, backup)`:

- `backup == false`: return `WgInterfaceName(remoteName)` byte-for-byte, preserving primary-link
  names for existing fleets.
- `backup == true`: always return
  `"wg-" + clean[:8] + sha256(remoteName + "|" + edgeID)[:4]` (with the bounded clean slice).

Folding the stable edge ID into the backup suffix makes parallel backup interfaces distinct and
reproducible. The 16-bit suffix is not assumed collision-proof; invariant N4 turns a collision into
a compile-blocking validation error with an actionable rename rather than an overwrite.

## Frontend consumption rule

The frontend must not reconstruct interface names from node names. It reads the compiled names from
the Go result (for example, WireGuard config keys of `"<nodeID>:<interfaceName>"`) and uses them
verbatim. Local design runs the same Go naming and validation code through WASM, so there is no
separate TypeScript artifact-name authority.
