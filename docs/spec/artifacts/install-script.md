# Install Script

Generated per-node bash script with phases:
- **Uninstall mode** (`--uninstall` / `-u`): Complete teardown — stops all WG interfaces, disables
  Babel, removes configs, removes SNAT rules, removes dummy0, removes systemd services
- **Phase 0**: Cleanup previous installation (managed + legacy interfaces)
- **Phase 1**: Environment preparation — bundle verification (signature, then checksums; see below),
  dependency installation, dummy0 interface creation with overlay IP, SNAT source address fix. When
  any link uses `transport: "tcp"`, also installs the `mimic` package, `modprobe mimic` (persisted),
  and checks kernel-eBPF support
- **Phase 2**: Configuration deployment — copies WG configs, Babel config, sysctl config
- **Phase 3**: Activation — applies sysctl; for mimic nodes, detects the egress NIC, writes
  `/etc/mimic/<egress>.conf` (one filter per mimic listen port) and starts `mimic@<egress>` **before**
  bringing up WireGuard; then starts WG interfaces, configures babeld systemd override, shows status

mimic teardown (one filter per mimic link on the egress NIC, MTU −12 per mimic interface) is detailed
in [mimic.md](./mimic.md); uninstall stops/disables `mimic@<egress>`, removes its config and
modules-load entry, and detaches.

## Bundle Verification (Phase 1)

Before any configuration is touched, Phase 1 verifies the bundle. The ordering depends on whether
the bundle is signed (i.e. whether `bundle.sig` was shipped — see
[../controller/signing.md](../controller/signing.md)):

**Signed bundle (`bundle.sig` present):**

1. Write the embedded/shipped public key to a temp file (it is also available as
   `signing-pubkey.pem`; the same key is compiled into `install.sh` as a Go-emitted constant).
2. Base64-decode `bundle.sig` into a raw 64-byte signature file.
3. Verify the Ed25519 signature over the canonical `checksums.sha256`:
   `openssl pkeyutl -verify -pubin -inkey <pub.pem> -rawin -sigfile <sig> -in checksums.sha256`.
4. **Only if the signature verifies**, run the existing `sha256sum --status -c checksums.sha256` to
   confirm every file matches its signed digest.

The signature check **precedes** the `sha256sum -c` check by design: authenticity is established
before integrity. If `bundle.sig` is present but `openssl` is missing or its build lacks Ed25519
support, the script **fails loudly with a nonzero exit** — it never silently downgrades to hash-only
when a signature was expected (mirroring the `EXPECTED_PAYLOAD_SHA256` fail-clear style of the
self-extracting installer).

**Unsigned bundle (`bundle.sig` absent):**

The script behaves exactly as before Phase 0 — `sha256sum --status -c checksums.sha256` only — so the
hash-only / air-gap path is unchanged. Signing is opt-in; an operator who never sets
`YAOG_BUNDLE_SIGNING_KEY` sees identical install-time behavior.

> **Trust caveat:** when the verifying public key ships *inside* the bundle, the signature proves
> internal consistency, not provenance, against a bundle from an untrusted source — an attacker who
> rewrites the bundle can swap the bundled pubkey too. The signature is only an authenticity anchor
> when the key is pinned out of band. See the limitation in
> [../controller/signing.md](../controller/signing.md) and [../security/security.md](../security/security.md).

## Source Address Fix (SNAT)

The per-peer WireGuard model uses transit IPs (`10.10.0.0/24`) on tunnel interfaces. Without a
fix, outgoing packets to overlay destinations use the transit IP as the source instead of the
overlay IP, causing `ping <overlay_ip>` to silently fail. The install script adds an SNAT rule
(nftables preferred, iptables fallback) that rewrites transit source IPs to the node's overlay IP
on all `wg-*` interfaces. A persistent `overlay-snat.service` systemd unit ensures the rule
survives reboots.

## Uninstall Support

Both the per-peer install script and the client install script support a `--uninstall` (or `-u`)
flag:

```bash
sudo bash install.sh --uninstall
```

The uninstall operation:
1. Stops and disables all managed WireGuard interfaces
2. Stops and disables ALL remaining WireGuard interfaces (catches any leftovers)
3. Removes all WireGuard config files from `/etc/wireguard/`
4. (Per-peer nodes) Stops and disables Babel, removes Babel configs and systemd overrides
5. Removes sysctl overlay configuration and re-applies system defaults
6. (Per-peer nodes) Removes the `dummy0` overlay interface and its `overlay-dummy.service`
7. Reloads systemd daemon
