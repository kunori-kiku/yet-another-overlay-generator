// Bundle export — the TypeScript mirror of internal/artifacts/export.go (the disk-write tail of the
// local compile pipeline) + internal/bundlesig/bundlesig.go's Canonicalize. It is the PURE library's
// presentation layer: it takes a compile() CompileResult (or a Topology, via compile()) and lays the
// rendered bytes out as a per-node bundle ZIP, byte-for-byte identical to what the Go exporter writes.
//
// Go is the authoritative oracle. The two byte-exact surfaces the conformance harness pins are:
//   - the per-node bundle FILE SET (bundleFiles, mirroring artifacts.BundleFiles): every per-peer
//     wireguard/<iface>.conf (a client's single wg0 is "nodeID:wg0"), babel/babeld.conf (non-client
//     nodes only), sysctl/99-overlay.conf, install.sh, and artifacts.json ONLY when a catalog produced
//     non-empty content (the D4 guard — local mode never has a catalog, so artifacts.json is omitted and
//     the air-gap bundle stays byte-identical); and
//   - the per-node checksums.sha256 content (canonicalize, mirroring bundlesig.Canonicalize): the
//     SHA-256 of each file's bytes in sha256sum format, sorted by path in byte order, single-LF.
//
// manifest.json carries compile-time timestamps (compiled_at) and is OUT of the conformance byte set;
// README.txt is the human usage note. The self-extracting tar.gz installer wrapper and Ed25519 bundle
// signing are OUT of scope for local mode (plan-4): the signer is always off here, so a signing fixture's
// install.sh + checksums differ from the Go golden's signed bytes and are excluded by the harness the
// same way (the signing block is a documented NO-OP).

import JSZip from 'jszip';

import { sha256 } from '@noble/hashes/sha2.js';
import { bytesToHex } from '@noble/hashes/utils.js';

import { compile } from './index';
import type { KeyCustody } from './index';
import type { CompileResult, Topology } from './model';

// utf8 encodes a string to its UTF-8 bytes, mirroring Go's []byte(content) on a string. canonicalize
// hashes over these exact bytes (matching bundlesig.go's sha256.Sum256([]byte(files[path]))), and the
// ZIP writer stores these exact bytes for each file.
const utf8 = new TextEncoder();

// byteCompare orders two strings by raw byte (UTF-8) order, mirroring Go's sort.Strings (Go string
// comparison is byte-wise). For the ASCII relpaths the bundle uses this is identical to the default JS
// UTF-16 sort, but comparing the encoded bytes keeps the canonical ordering faithful for any path.
function byteCompare(a: string, b: string): number {
  const ba = utf8.encode(a);
  const bb = utf8.encode(b);
  const n = Math.min(ba.length, bb.length);
  for (let i = 0; i < n; i++) {
    if (ba[i] !== bb[i]) {
      return ba[i] - bb[i];
    }
  }
  return ba.length - bb.length;
}

// canonicalize produces the canonical checksums.sha256 byte string for a bundle, mirroring
// bundlesig.Canonicalize (bundlesig.go:172-189). For every (path, content) pair it emits one
// sha256sum-format line:
//
//   <64-hex-lowercase-sha256><two spaces><path>\n
//
// Lines are sorted by path in byte order and terminated by a single LF (no CR). The result is the exact
// content of the bundle's checksums.sha256 file and the message the Go signer would sign. The output is
// deterministic and independent of object key-insertion order.
export function canonicalize(files: Record<string, string>): string {
  const paths = Object.keys(files).sort(byteCompare);
  let out = '';
  for (const path of paths) {
    // bytesToHex emits lowercase hex (matching Go's "%x"); two spaces are the sha256sum binary-mode
    // separator that `sha256sum -c` expects.
    const sum = bytesToHex(sha256(utf8.encode(files[path])));
    out += sum + '  ' + path + '\n';
  }
  return out;
}

// bundleFiles builds a node's canonical, checksummed bundle file set as a relpath->content map,
// mirroring artifacts.BundleFiles (export.go:45-67) EXACTLY:
//   - every per-peer wireguard/<iface>.conf (WireGuardConfigs is keyed "nodeID:interfaceName"; a
//     client's single wg0 is "nodeID:wg0") whose key prefix is this node;
//   - babel/babeld.conf (present for non-client nodes — keyed by node ID in babelConfigs);
//   - sysctl/99-overlay.conf;
//   - install.sh;
//   - artifacts.json ONLY when a catalog produced non-empty content (the D4 guard — an empty catalog
//     omits the file so the air-gap bundle stays byte-identical; local mode is always empty here).
//
// bundle.sig / signing-pubkey.pem / manifest.json are NOT members: they are the authenticity/metadata
// layer over this set, not part of the checksummed bytes. This is the single source for the set —
// canonicalize(bundleFiles(...)) is the node's checksums.sha256, exactly as the Go exporter writes it.
export function bundleFiles(
  result: CompileResult,
  nodeID: string,
): Record<string, string> {
  const files: Record<string, string> = {};

  for (const configKey of Object.keys(result.wireGuardConfigs)) {
    const idx = configKey.indexOf(':');
    if (idx < 0) {
      continue;
    }
    const keyNode = configKey.slice(0, idx);
    if (keyNode !== nodeID) {
      continue;
    }
    const iface = configKey.slice(idx + 1);
    files['wireguard/' + iface + '.conf'] = result.wireGuardConfigs[configKey];
  }

  if (result.babelConfigs[nodeID] !== undefined) {
    files['babel/babeld.conf'] = result.babelConfigs[nodeID];
  }
  if (result.sysctlConfigs[nodeID] !== undefined) {
    files['sysctl/99-overlay.conf'] = result.sysctlConfigs[nodeID];
  }
  if (result.installScripts[nodeID] !== undefined) {
    files['install.sh'] = result.installScripts[nodeID];
  }
  // D4: artifacts.json joins the set only when a catalog produced non-empty content. Local mode never
  // configures a catalog, so artifactsJSON is empty and the file is omitted — keeping the bundle
  // byte-identical to the Go air-gap export.
  const artifactsJSON = result.artifactsJSON[nodeID];
  if (artifactsJSON !== undefined && artifactsJSON !== '') {
    files['artifacts.json'] = artifactsJSON;
  }

  return files;
}

// buildFiles projects a CompileResult into the per-node bundle byte set the conformance harness compares:
// nodeID -> relpath -> content, built from bundleFiles for every node in the compiled topology (the same
// node iteration order Go's Export uses). This is the in-memory files builder the harness asserts against
// golden.files; it is also what the ZIP writer lays out on disk.
export function buildFiles(
  result: CompileResult,
): Record<string, Record<string, string>> {
  const out: Record<string, Record<string, string>> = {};
  for (const node of result.topology.nodes) {
    out[node.id] = bundleFiles(result, node.id);
  }
  return out;
}

// buildChecksums projects a CompileResult into the per-node checksums.sha256 content the conformance
// harness compares: nodeID -> canonicalize(bundleFiles(node)). This is the in-memory checksums builder
// the harness asserts against golden.checksums (the bundlesig.Canonicalize output), and it is the exact
// content the ZIP writer stores at <node>/checksums.sha256.
export function buildChecksums(result: CompileResult): Record<string, string> {
  const out: Record<string, string> = {};
  for (const node of result.topology.nodes) {
    out[node.id] = canonicalize(bundleFiles(result, node.id));
  }
  return out;
}

// exportArtifacts compiles a topology and returns the per-node bundle ZIP as a Blob, mirroring the
// directory shape internal/artifacts/export.go writes to disk:
//
//   <node.name>/
//     wireguard/<iface>.conf        (per-peer, 0600 on disk; client = wireguard/wg0.conf)
//     babel/babeld.conf             (non-client nodes only)
//     sysctl/99-overlay.conf
//     install.sh
//     checksums.sha256              (canonicalize over the four file-set members above)
//     manifest.json                 (carries compiled_at — OUT of the conformance byte set)
//     README.txt
//   deploy-all.sh                   (project-level)
//   deploy-all.ps1
//
// Ed25519 signing (bundle.sig / signing-pubkey.pem) and the self-extracting tar.gz wrapper are OUT of
// scope in local mode, so no signature files are emitted. JSZip stores file content verbatim (UTF-8),
// so the in-archive bytes equal the Go exporter's on-disk bytes for every checksummed member.
//
// The compiled_at the manifest.json carries is the one impurity in this layer: it is read from the
// caller-supplied clock (default: the wall clock at export time). It is excluded from the conformance
// byte set, so a varying timestamp never reds the harness.
export async function exportArtifacts(
  topo: Topology,
  custody: KeyCustody = 'airgap',
  compiledAt: Date = new Date(),
): Promise<Blob> {
  const result = compile(topo, custody);
  const zip = new JSZip();

  for (const node of result.topology.nodes) {
    const isClient = node.role === 'client';
    const dir = node.name;
    const files = bundleFiles(result, node.id);

    // The checksummed bundle members (byte-for-byte the Go on-disk bytes).
    for (const relpath of Object.keys(files)) {
      zip.file(dir + '/' + relpath, files[relpath]);
    }

    // checksums.sha256 — the canonical checksums over exactly the file set above.
    zip.file(dir + '/checksums.sha256', canonicalize(files));

    // manifest.json — metadata (compiled_at is OUT of the conformance byte set). Mirrors the field set
    // and ordering of export.go's manifest map (json.MarshalIndent sorts keys, so the Go output is
    // alphabetical; we build an ordered object and serialize with two-space indent to match shape).
    const architecture = isClient ? 'single-interface' : 'per-peer-interface';
    const allFiles = manifestFileList(files, isClient);
    const manifest = {
      architecture,
      checksum: result.manifest.checksum,
      compiled_at: formatCompiledAt(compiledAt),
      domain_id: node.domain_id,
      files: allFiles,
      node_id: node.id,
      node_name: node.name,
      overlay_ip: node.overlay_ip ?? '',
      project_id: result.manifest.project_id,
      project_name: result.manifest.project_name,
      role: node.role,
      version: result.manifest.version,
    };
    zip.file(dir + '/manifest.json', JSON.stringify(manifest, null, 2));

    // README.txt — the human usage note (D76: Architecture line reuses the manifest's value so the two
    // stay consistent for a client bundle).
    const readme =
      'Node: ' +
      node.name +
      '\nOverlay IP: ' +
      (node.overlay_ip ?? '') +
      '\nRole: ' +
      node.role +
      '\nArchitecture: ' +
      architecture +
      '\n\nUsage:\n  1. Copy this directory to the target host\n  2. Run: sudo bash install.sh\n';
    zip.file(dir + '/README.txt', readme);
  }

  // Project-level deploy scripts at the archive root.
  for (const name of Object.keys(result.deployScripts)) {
    zip.file(name, result.deployScripts[name]);
  }

  return zip.generateAsync({ type: 'blob' });
}

// manifestFileList builds the manifest.json "files" array in the same order export.go assembles allFiles:
// the per-peer wireguard configs (sorted by relpath for determinism), then babel/babeld.conf (non-client
// only), then sysctl/99-overlay.conf and install.sh, then artifacts.json when present. This list is part
// of manifest.json only (OUT of the conformance byte set), so its ordering is cosmetic.
function manifestFileList(
  files: Record<string, string>,
  isClient: boolean,
): string[] {
  const wgFiles = Object.keys(files)
    .filter((p) => p.startsWith('wireguard/'))
    .sort(byteCompare);
  const out: string[] = [...wgFiles];
  if (!isClient) {
    out.push('babel/babeld.conf');
  }
  out.push('sysctl/99-overlay.conf', 'install.sh');
  if (files['artifacts.json'] !== undefined) {
    out.push('artifacts.json');
  }
  return out;
}

// formatCompiledAt renders a Date as the "2006-01-02T15:04:05Z" UTC layout export.go uses for
// manifest.json's compiled_at. compiled_at is excluded from the conformance byte set, so this is
// presentation-only.
function formatCompiledAt(d: Date): string {
  const p2 = (n: number): string => String(n).padStart(2, '0');
  return (
    d.getUTCFullYear() +
    '-' +
    p2(d.getUTCMonth() + 1) +
    '-' +
    p2(d.getUTCDate()) +
    'T' +
    p2(d.getUTCHours()) +
    ':' +
    p2(d.getUTCMinutes()) +
    ':' +
    p2(d.getUTCSeconds()) +
    'Z'
  );
}
