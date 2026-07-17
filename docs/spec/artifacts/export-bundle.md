# Export Bundle

## Filesystem export structure

The CLI/controller export adapter uses the node ID—not the human node name—as the canonical
per-node directory key:

```text
<output>/
├── deploy-all.sh
├── deploy-all.ps1
├── <node-id>/
│   ├── wireguard/
│   │   ├── wg-peer1.conf
│   │   ├── wg-peer2.conf
│   │   └── ...
│   ├── babel/
│   │   └── babeld.conf             # non-client nodes only
│   ├── sysctl/
│   │   └── 99-overlay.conf
│   ├── install.sh
│   ├── README.txt
│   ├── artifacts.json              # only when a catalog produced content
│   ├── telemetry.json              # optional AgentHeld ICMP/TCP-only v1 policy
│   ├── telemetry-policy.json       # optional AgentHeld URL/device v2 policy; mutually exclusive
│   ├── checksums.sha256
│   ├── bundle.sig                  # only when bundle signing is enabled
│   ├── signing-pubkey.pem          # only when bundle signing is enabled
│   └── manifest.json
└── ...
```

There is no ID/name fallback and no flat self-extracting installer presentation. The same
`<node-id>/` namespace is consumed by the CLI, controller stage reader, browser/WASM ZIP, and
project deploy helpers. See [naming.md](./naming.md).

## Canonical bundle members

`internal/artifacts.BundleFiles` is the single source for the per-node member set. It contains:

- `wireguard/<iface>.conf` (including the client's `wireguard/wg0.conf`);
- `babel/babeld.conf` for non-client nodes;
- `sysctl/99-overlay.conf`;
- `install.sh`;
- `README.txt` (its custody-critical application guidance is integrity-bound); and
- `artifacts.json` only when a configured catalog emits non-empty content; and
- at most one AgentHeld telemetry member: frozen `telemetry.json` v1 for ICMP/TCP-only policy, or
  strict `telemetry-policy.json` v2 when URL probes or automatic devices are enabled.

Those members are written to disk, listed as members in `manifest.json`, and covered by
`checksums.sha256`. `bundle.sig` and `signing-pubkey.pem` are authenticity sidecars over that set;
`checksums.sha256` is the digest list itself; `manifest.json` is compile metadata. Those four files
are not members and cannot self-reference.

The telemetry members are mutually exclusive and absent from AirGap bundles. When present they are
ordinary integrity members: `checksums.sha256` covers their exact bytes, optional tier-1
`bundle.sig` authenticates that checksum set, and the controller's required off-host keystone
membership binds the resulting per-node bundle digest before an agent can activate the policy.

WireGuard configuration files are written at mode `0600`; `install.sh` is `0755`; the remaining
bundle files are `0644`. Export renders the complete result into a fresh sibling tree, rejects a
symlink or special-file destination, and publishes the finished tree as a replacement. A failed
validation, signature, or write therefore leaves the prior export untouched; a successful export is
exact and cannot retain removed nodes, obsolete members, stale signing sidecars, or an older file's
permissive mode.

## The two checksum fields are different

`manifest.json.checksum` is a short compiler summary derived from the compiled topology. It is
display/provenance metadata only: install scripts and agents do **not** verify it, it is not a
member hash, and it is never signed.

The security-bearing integrity authority is the per-node `checksums.sha256`. Its bytes are produced
by `internal/bundlesig.Canonicalize(bundleFiles)`:

1. compute SHA-256 over each member's exact bytes;
2. emit `<64-lowercase-hex><two spaces><slash-relative-path>\n`;
3. sort entries by path in raw byte order; and
4. retain one trailing LF.

The output is deterministic and directly consumable by `sha256sum -c`. `install.sh` is mandatory
and covered, so a modified root-executed script cannot pass with the original checksum list.
`README.txt` is covered for the same reason: an untrusted delivery cannot rewrite AgentHeld safety
instructions while retaining a valid bundle. `manifest.json`, including volatile `compiled_at`,
remains deliberately outside this set.

## Signed bundles (opt-in)

Bundle signing is opt-in. `YAOG_BUNDLE_SIGNING_KEY` names an Ed25519 PKCS#8 PEM private key. When it
is unset, AirGap exports remain hash-only. When it is set, each node directory additionally gets:

- `bundle.sig`: standard-base64 of the raw 64-byte Ed25519 signature over the **exact canonical
  `checksums.sha256` bytes**; and
- `signing-pubkey.pem`: the PKIX/SubjectPublicKeyInfo public key used for independent verification
  (the same public key is embedded in the generated signed `install.sh`).

The signed object is never `manifest.json.checksum`. Production controller stage resolves the
signer once and passes the same in-memory signer through rendering, anchor enforcement, and export,
preventing a mid-stage key-file change from splitting the embedded key and detached signature. See
[../controller/signing.md](../controller/signing.md).

## Browser/WASM ZIP

The local browser engine compiles through the same Go `internal/localcompile` façade. Its preview ZIP
contains ID-keyed node directories with the canonical member set plus each node's matching
`checksums.sha256`, and places the matching `deploy-all.sh` and `deploy-all.ps1` from that same compile
at the ZIP root. The archive container bytes are presentation; the member contents, checksum bytes,
and helper contents are the conformance surfaces.

Local browser compilation uses AirGap custody. Controller/manual AgentHeld bundles must be applied
through enrolled agents or `yaog-agent kit apply`, not by directly running their downloaded
`install.sh`; see [deploy-scripts.md](./deploy-scripts.md).
