# Artifact Naming & Deploy Identity

YAOG renders one bundle per node and one combined deploy script for the whole fleet. Both halves
must agree, byte for byte, on the file name each node's installer carries: the deploy script SCPs a
file by name and then runs it remotely, so a name written by the ZIP writer that the deploy renderer
cannot reproduce results in a silent skip, and two nodes that resolve to the same name result in one
node receiving the other node's keys, IPs, and config while the run reports success. This document
defines the single naming function that both halves MUST use, the uniqueness invariants that make
name collisions impossible, the remote upload path keyed on node identity, and the WireGuard
interface-name algorithm that the frontend MUST consume rather than reimplement.

Related: [export-bundle.md](./export-bundle.md) (directory structure), [deploy-scripts.md](./deploy-scripts.md)
(SCP + remote execution), [wireguard.md](./wireguard.md) (per-peer interface configs),
[../compiler/peer-derivation.md](../compiler/peer-derivation.md) (where interface names are stamped),
[../frontend/architecture.md](../frontend/architecture.md) (frontend must consume compiled names).

## Canonical installer name

There MUST be exactly one function that maps a node name to an installer file name, and it MUST be
the single source of truth for **both** the ZIP entry name written by the export endpoint **and** the
file name the deploy script looks up and uploads. The function is `safeInstallerFileName` and behaves
as follows:

1. Lowercase the node name.
2. Map every rune outside `[a-z0-9-_]` to a hyphen (`-`).
3. Collapse every run of two or more consecutive hyphens to a single hyphen.
4. Trim leading and trailing hyphens.
5. If the result is empty, substitute the literal `node`.
6. Append the suffix `.install.sh`.

For example, `"Web 1"` and `"web-1"` both produce `web-1.install.sh`; `"  ***  "` produces
`node.install.sh`.

The ZIP entry name, the deploy-script `INSTALLER="$WORKDIR/<name>"` lookup, and the SCP source path
MUST be produced by calling this one function on the same node name. Neither side may apply its own
sanitization, truncation, or suffixing. The export ZIP writer and the deploy renderer MUST NOT carry
divergent name derivations.

> **Compliance:** the ZIP writer names the entry `nodeName + ".install.sh"` from the raw export
> directory name — which is the raw `node.Name` — at `internal/api/handler.go:407` (directory created
> at `internal/artifacts/export.go:42`), while the deploy renderer looks up and uploads
> `safeInstallerFileName(node.Name)` at `internal/renderer/deploy.go:47,216,324`. Any node whose raw
> name differs from its sanitized name (uppercase, space, or special character) is written under one
> name and sought under another, so it is silently skipped. Closed by Plan 4 (PR #6).

## Export directory naming

Inside the export bundle each node owns a directory and the deploy script extracts the ZIP into a
work directory before looking up installers by name. The per-node directory name and the installer
name it yields MUST be derived such that the deploy renderer can reproduce the exact installer file
name from the same node without consulting the filesystem.

Because the canonical installer name is `safeInstallerFileName(node.Name)`, the export layout MUST
guarantee that the installer file the deploy script seeks (`safeInstallerFileName(node.Name)`) is the
file the ZIP writer actually emitted for that node. The directory name MAY remain a human-readable
form of the node name, but the ZIP-level installer entry that the deploy script references MUST be the
canonical installer name, not the raw directory name. The export directory writer MUST reject names
that are unsafe as path components (empty, `.`, `..`, names containing path separators, absolute
paths, or names containing `..`).

> **Compliance:** path-component safety is enforced by `validateSafeName` at
> `internal/artifacts/export.go:39,187-204`, but the installer ZIP entry is still keyed on the raw
> directory name (`internal/api/handler.go:407`) rather than the canonical name. Closed by Plan 4
> (PR #6).

## Remote upload path keyed by node ID

The deploy script uploads each node's installer to a path on the remote host, runs it under `sudo`,
and removes it. That path MUST be keyed by the node's unique ID, not by any name-derived string:

```
/tmp/<node.ID>-install.sh
```

Node IDs are unique by construction (the semantic validator already rejects duplicate node IDs), so a
node-ID-keyed upload path cannot collide even when two nodes share a name. Keying the upload path on
the sanitized installer name instead lets two distinct nodes that sanitize to the same name overwrite
each other's payload on the same remote path; on a self-deploy or co-hosted target this clobbers one
node's installer with the other's.

> **Compliance:** the upload destination is currently `target:/tmp/<installerFile>` where
> `installerFile` is the sanitized installer name, and the subsequent `sudo bash /tmp/<installerFile>`
> reads the same name — `internal/renderer/deploy.go:324,325,358-359`. Two nodes whose names sanitize
> identically share `/tmp/<name>.install.sh`. Closed by Plan 4 (PR #6).

## Uniqueness invariants

A topology MUST fail semantic validation if any of the following name collisions exist across two
distinct nodes. These checks make the de-collision rule below a defence-in-depth fallback rather than
the primary guard:

| # | Invariant | Rationale |
|---|---|---|
| N1 | No two nodes share a **raw name** | Two raw-identical names are indistinguishable to operators and to any name-derived artifact |
| N2 | No two nodes produce the same **sanitized installer name** (`safeInstallerFileName`) | Identical installer names cause silent skips and wrong-identity deploys |
| N3 | No two nodes produce the same **`wgInterfaceName`** | Colliding interface names cause one WireGuard config and one Babel interface line to silently overwrite the other |

Each violation MUST be reported as a semantic validation error naming both offending nodes, in the
locale style used by the rest of the semantic validator.

> **Compliance:** the semantic validator rejects duplicate node IDs and duplicate overlay IPs
> (`internal/validator/semantic.go`) but performs none of N1–N3. Colliding `wg-<name>` interface
> names overwrite each other at the renderer (`internal/compiler/peers.go:492-522`). Closed by Plan 4
> (PR #6).

## Deterministic de-collision

Where a generated name could still collide after validation — for example two nodes whose names are
distinct but truncate or sanitize to the same string — the generator MUST de-collide deterministically
by appending a short hash suffix derived from the node's unique ID. Specifically, generated names
(installer names and interface names) that would otherwise collide MUST incorporate a short hex slice
of `sha256(node.ID)` so that the suffix is stable across recompiles and unique per node. De-collision
MUST be deterministic: compiling the same topology twice MUST produce the same names.

The uniqueness invariants (N1–N3) remain the primary contract; de-collision is the deterministic
fallback that guarantees distinct artifacts even if a future name source is added that the validator
does not yet cover.

## WireGuard interface-name algorithm

Each per-peer WireGuard interface is named from the **remote** peer's node name via `wgInterfaceName`.
The Linux kernel limits interface names to 15 characters, so the algorithm has a short path and a
hashed long path. It is the single authority for interface names; the backend stamps the result onto
`PeerInfo.InterfaceName` during peer derivation, and every consumer (ZIP config file names, Babel
interface lines, deploy teardown, frontend lookups) MUST use the stamped value.

The algorithm, given a remote node name `remoteName`:

1. `clean := lowercase(remoteName)`, then map every rune outside `[a-z0-9-]` to a hyphen.
   (Note: unlike `safeInstallerFileName`, the interface cleaner does **not** preserve `_`; underscore
   maps to a hyphen.)
2. `name := "wg-" + clean`.
3. **Short path:** if `len(name) <= 15`, return `name`.
4. **Long path (>15 chars):** return `"wg-" + clean[:8] + sha256(remoteName)[:4]`, i.e. the `wg-`
   prefix, the first 8 cleaned characters, and the first 4 hex characters of `sha256(remoteName)`,
   for a total of `3 + 8 + 4 = 15` characters. The 8-character clean slice is bounded by the actual
   cleaned length when it is shorter than 8 (a defensive guard that does not arise on the long path).

The hash suffix exists precisely so that two distinct names sharing a long common prefix do not
truncate to the same interface name. Plain truncation (`name[:15]`) is therefore **wrong** for names
longer than 12 characters and MUST NOT be used.

> **Compliance:** the algorithm is implemented at `internal/compiler/peers.go:492-522` and called on
> the remote node name at `internal/compiler/peers.go:267,332,376`. The contract holds in the backend;
> the deviation is in the frontend (below). Closed by Plan 4 (PR #6).

### The frontend MUST NOT reimplement this

The frontend MUST NOT recompute interface names. It MUST read the compiled interface names out of the
compile response (the per-peer config keys are `"<nodeID>:<interfaceName>"`) and use those verbatim.
Any independent reconstruction of `wgInterfaceName` in the frontend is a contract violation, because
the frontend cannot faithfully reproduce the hashed long path and will diverge for names longer than
12 characters.

> **Compliance:** the frontend reconstructs the interface name as
> `` `wg-${toNode.name.toLowerCase().replace(/[^a-z0-9-]/g, '-')}`.slice(0, 15) `` at
> `frontend/src/components/layout/RightPanel.tsx:622`. This is plain truncation with no hash branch,
> so the "Compiled values" lookup silently misses for any node name longer than 12 characters. The
> normative consumption rule is stated in [../frontend/architecture.md](../frontend/architecture.md).
> Closed by Plan 4 (PR #6).
