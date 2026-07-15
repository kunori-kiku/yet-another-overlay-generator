# Install Script

Generated per-node bash script with phases:

- **Integrity preflight**: Before root is required or any host state is touched, require
  `checksums.sha256`; for signed bundles, verify `bundle.sig` first; then verify every listed file.
- **Uninstall mode** (`--uninstall` / `-u`): Complete teardown — stops all WG interfaces, disables
  Babel, removes configs, removes SNAT rules, removes dummy0, removes systemd services
- **Clean mode** (`--clean`): After the integrity and root gates, clear legacy/all-WireGuard layout
  state before a normal install. The operational AirGap `deploy-all` helper delegates its clean
  option here; AgentHeld helpers do not execute the downloaded installer.
- **Phase 0**: Cleanup previous installation (managed + legacy interfaces)
- **Phase 1**: Environment preparation — dependency installation, dummy0 interface creation with
  overlay IP, SNAT source address fix. When
  any link uses `transport: "tcp"`, also installs the `mimic` package, `modprobe mimic` (persisted),
  and checks kernel-eBPF support
- **Phase 2**: Configuration deployment — copies WG configs, Babel config, sysctl config
- **Phase 3**: Activation — applies sysctl; for mimic nodes, detects the egress NIC, writes
  `/etc/mimic/<egress>.conf` (a `local=` filter per mimic listen port + a `remote=` filter per dialed peer) and starts `mimic@<egress>` **before**
  bringing up WireGuard; then starts WG interfaces, configures babeld systemd override, shows status.
  For nodes whose mimic links all resolve to the `udp` fallback policy, a mimic-provisioning failure
  (kernel lacks eBPF / package install / unit start) falls back to plain-UDP WireGuard and writes a
  status breadcrumb; otherwise it fails closed. See [mimic.md](./mimic.md) (UDP fallback).

mimic teardown (`local=`/`remote=` filters on the egress NIC, MTU −12 per mimic interface) is detailed
in [mimic.md](./mimic.md); uninstall stops/disables `mimic@<egress>`, removes its config and
modules-load entry, and detaches.

## Bundle Verification (Preflight)

Before uninstall, clean mode, Phase 0, or any other mutation, the preflight verifies the bundle.
The ordering depends on whether
the bundle is signed (i.e. whether `bundle.sig` was shipped — see
[../controller/signing.md](../controller/signing.md)):

**Signed bundle (rendered with signing enabled):**

1. Write the **embedded** verifying public key to a temp file. The key is baked into `install.sh` at
   generation time and is the trust anchor; `signing-pubkey.pem` ships the same key for out-of-band
   `openssl` verification but is *not* what the script trusts.
2. Base64-decode `bundle.sig` into a raw 64-byte signature file.
3. Verify the Ed25519 signature over the canonical `checksums.sha256`:
   `openssl pkeyutl -verify -pubin -inkey <pub.pem> -rawin -sigfile <sig> -in checksums.sha256`
   (`-rawin` requires **OpenSSL 3.0+**).
4. **Only if the signature verifies**, run the existing `sha256sum --status -c checksums.sha256` to
   confirm every file matches its signed digest.

The signature check **precedes** the `sha256sum -c` check by design: authenticity is established
before integrity. Because the verifying key is embedded, a signed `install.sh` **requires**
`bundle.sig`: a **missing** signature is treated as signature-stripping tamper and the script
**refuses to proceed** (it does not fall back to the bare `sha256sum -c`, which an attacker could
satisfy with rewritten files + rewritten checksums). If `bundle.sig` is present but `openssl` is
missing or its build lacks Ed25519 / `-rawin` support, the script **fails loudly with a nonzero
exit** — it never silently downgrades to hash-only when a signature was expected.

**Unsigned bundle (rendered without signing):**

The script behaves exactly as before Phase 0 — `sha256sum --status -c checksums.sha256` only — so the
hash-only / air-gap path is unchanged. Signing is opt-in; an operator who never sets
`YAOG_BUNDLE_SIGNING_KEY` sees identical install-time behavior.

`checksums.sha256` is mandatory in both modes. A missing checksum manifest fails before root
checking or cleanup; hash-only means there is no provenance signature, not that integrity
verification is optional. The covered member set includes `install.sh` and `README.txt` as well as
the rendered WireGuard/Babel/sysctl files and optional `artifacts.json`. `manifest.json` and its
short `checksum` field are compile metadata, not the integrity authority and not inputs to this
preflight.

> **Trust caveat:** when the verifying public key ships *inside* the bundle, the signature proves
> internal consistency, not provenance, against a bundle from an untrusted source — an attacker who
> rewrites the bundle can swap the bundled pubkey too. The signature is only an authenticity anchor
> when the key is pinned out of band. See the limitation in
> [../controller/signing.md](../controller/signing.md) and [../security/security.md](../security/security.md).

## Custody-specific execution boundary

The direct `sudo bash install.sh` path is only for a locally trusted **AirGap** bundle. The
operational AirGap `deploy-all` helpers copy the complete directory to a fresh remote staging path;
this script then performs the verification sequence above before mutation.

Controller/manual **AgentHeld** bundles contain a private-key placeholder and carry a generated
header telling the operator not to execute the downloaded script directly. Their project-level
`deploy-all` files are fail-closed guidance stubs. Managed nodes deploy through controller
stage/promote and the enrolled agent. A manual node uses `kit apply`, which captures a bounded
immutable snapshot, verifies the exact candidate through the bundle and off-host membership gates,
checks rollback state, stages an owned copy, and invokes only that verified copy.

```bash
# Raw Ed25519 operator credential
sudo yaog-agent kit apply \
  --bundle <node-bundle-dir-or-zip> --node-id <node-id> \
  --operator-cred <trusted-public-key.pem> --operator-cred-alg ed25519

# WebAuthn operator credential
sudo yaog-agent kit apply \
  --bundle <node-bundle-dir-or-zip> --node-id <node-id> \
  --operator-cred <trusted-public-key.pem> \
  --operator-cred-alg <webauthn-es256|webauthn-eddsa> \
  --operator-rpid <rp-id> --operator-origin <origin>
```

The RP ID is required for WebAuthn verification; the origin preserves the browser-enrollment
binding. Append `--uninstall` to the same verified command for removal. A never-keystoned legacy
node can instead make the unsafe absence explicit with `--dangerously-allow-no-keystone`. The agent
rejects that acknowledgement when the bundle carries trust-list files or durable state records a
previously verified keystone, so it cannot silently downgrade an existing trust commitment.

## Source Address Fix (SNAT)

The per-peer WireGuard model uses transit IPs (`10.10.0.0/24`) on tunnel interfaces. Without a
fix, outgoing packets to overlay destinations use the transit IP as the source instead of the
overlay IP, causing `ping <overlay_ip>` to silently fail. The install script adds an SNAT rule
(nftables preferred, iptables fallback) that rewrites transit source IPs to the node's overlay IP
on all `wg-*` interfaces. A persistent `overlay-snat.service` systemd unit ensures the rule
survives reboots.

## Uninstall Support

Both the per-peer install script and the client install script support a `--uninstall` (or `-u`)
flag. This direct example is the AirGap path:

```bash
sudo bash install.sh --uninstall
```

For AgentHeld/manual bundles, use the verified `yaog-agent kit apply ... --uninstall` command above;
do not run the downloaded script directly.

The uninstall operation:
1. Stops and disables all managed WireGuard interfaces
2. Stops and disables ALL remaining WireGuard interfaces (catches any leftovers)
3. Removes all WireGuard config files from `/etc/wireguard/`
4. (Per-peer nodes) Stops and disables Babel, removes Babel configs and systemd overrides
5. Removes sysctl overlay configuration and re-applies system defaults
6. (Per-peer nodes) Removes the `dummy0` overlay interface and its `overlay-dummy.service`
7. Reloads systemd daemon
