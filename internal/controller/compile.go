package controller

// compile.go is the controller's compile-and-stage step (plan-4.3a, reworked for the
// keystone CORRECTION of plan-5.1, 2026-06-08): it turns the stored, public-keys-only
// topology plus the enrolled registry into signed per-node bundles staged at the next
// generation, and — when the keystone is ON — builds the OFF-HOST-signable membership
// MANIFEST that binds each node's bundle digest.
//
// Two design commitments shape this file:
//
//   - REUSE the frozen pipeline, do not reimplement it. The compiler, renderer,
//     and exporter stay frozen and dependency-minimal (see
//     docs/spec/controller/persistence.md §The quarantine boundary). This step
//     drives them exactly as the air-gap CLI/API does — render.GenerateKeys (in
//     AgentHeld custody) → compiler.Compile → render.All → artifacts.Export — and
//     reads the export back through a temp directory.
//
//   - RENDER WHAT'S READY. Only the enrolled subgraph is compiled: a topology node
//     is included iff its registry record is NodeApproved with a non-empty
//     WGPublicKey, and any edge whose far end is not enrolled is dropped.
//
// KEYSTONE (CORRECTION). The off-host signature must cover what RUNS, not merely the
// membership list. So the staged bundles are exported WITHOUT any trust-list files
// (the trust-list binds the checksums digest and therefore cannot live inside it);
// instead CompileAndStage computes, for every staged node, bundleSHA256 =
// hex(sha256(checksums.sha256)) — and checksums.sha256 covers install.sh + every
// config — and assembles a TrustList whose Members each carry {NodeID, WGPublicKey,
// BundleSHA256}. That manifest is STORED as the staged, to-be-signed manifest (its
// canonical bytes in StoredTrustList.TrustListJSON, an EMPTY SignatureJSON until the
// operator signs off-host). Staging does NOT require a signature; PROMOTING does (see
// PromoteStaged below). The signed manifest is appended to the SERVED file map at
// /config time — never embedded in the bundle's checksum set.
//
// Zero-knowledge custody is preserved end-to-end: GenerateKeys runs in AgentHeld
// mode, the registry holds public keys only, and any stray private key on the
// topology node is cleared before rendering.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/artifacts"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

// bundleSHA256 is the lowercase-hex SHA-256 of a bundle's checksums.sha256 bytes — the
// digest bound into each member of the off-host-signed manifest. checksums.sha256 covers
// install.sh AND every config, so this single digest pins the entire deployed bundle: a
// tampered install.sh changes checksums.sha256, which changes this digest, which the
// breached controller cannot re-sign without the off-host key. It is computed from the
// SAME bytes the agent re-derives (files["checksums.sha256"]).
func bundleSHA256(checksums []byte) string {
	sum := sha256.Sum256(checksums)
	return hex.EncodeToString(sum[:])
}

// memberKey is the comparable identity of a manifest member used by the monotonic-epoch
// rule: the tuple (wg_public_key, bundle_sha256) keyed by node_id. Two manifests carry
// the same membership iff they map the same node_id set to the same tuples.
type memberKey struct {
	wgPublicKey  string
	bundleSHA256 string
}

// manifestMembers decodes a stored manifest's canonical JSON into a node_id -> memberKey
// map for membership comparison.
func manifestMembers(trustListJSON []byte) (map[string]memberKey, error) {
	var tl trustlist.TrustList
	if err := json.Unmarshal(trustListJSON, &tl); err != nil {
		return nil, fmt.Errorf("controller: parsing stored manifest: %w", err)
	}
	out := make(map[string]memberKey, len(tl.Members))
	for _, m := range tl.Members {
		out[m.NodeID] = memberKey{wgPublicKey: m.WGPublicKey, bundleSHA256: m.BundleSHA256}
	}
	return out, nil
}

// sameMembership reports whether two node_id -> memberKey maps are equal (same node set,
// same tuple per node). It is the freshness test the monotonic-epoch rule uses to decide
// whether to REUSE the stored epoch (identical signed content) or BUMP it.
func sameMembership(a, b map[string]memberKey) bool {
	if len(a) != len(b) {
		return false
	}
	for id, ka := range a {
		kb, ok := b[id]
		if !ok || ka != kb {
			return false
		}
	}
	return true
}

// StageResult reports the outcome of CompileAndStage. Staged and SkippedUnenrolled
// are NODE IDs (the registry/agent identity), not node names. Generation is the
// staged generation (CurrentGeneration+1); it becomes current only when the
// operator calls PromoteStaged.
type StageResult struct {
	// Staged holds the node IDs that were compiled and staged this generation.
	Staged []string
	// SkippedUnenrolled holds the node IDs present in the topology but excluded
	// from the render because they are not yet enrolled (not NodeApproved, or no
	// WGPublicKey). Each fills in on a later deploy once it enrolls.
	SkippedUnenrolled []string
	// Generation is the staged generation. Zero when nothing was staged.
	Generation int64
}

// CompileAndStage renders the enrolled subgraph of the stored topology into signed
// per-node bundles and stages them at the next generation. When the keystone is ON it
// also builds the off-host-signable membership manifest (binding each node's bundle
// digest) and stores it as the staged, UNSIGNED manifest — staging never requires a
// signature.
//
// The flow:
//
//  1. Load the stored topology (ErrNotFound → empty result, no error).
//  2. Build the enrolled subgraph; drop edges to unenrolled peers. Zero enrolled →
//     empty result, no error.
//  3. GenerateKeys(AgentHeld) → Compile → render.All on the subgraph.
//  4. Export to a temp dir (removed on return) — WITHOUT any trust-list files.
//  5. Read each enrolled node's exported dir back into a file map and StageBundle it.
//  6. KEYSTONE ON: compute each staged node's bundle digest, assemble the manifest with
//     the monotonic epoch, and store it as the staged (unsigned) manifest.
//  7. Append one "stage" audit entry.
//
// Bundles are signed iff YAOG_BUNDLE_SIGNING_KEY is set — that tier-1 signing happens
// inside artifacts.Export (the Phase-0 env path), not here.
func CompileAndStage(ctx context.Context, store Store, t TenantID, now time.Time) (StageResult, error) {
	// Serialize the whole stage against any concurrent stage/promote for this
	// tenant (review finding): the sequence below is many individual Store calls,
	// and a promote landing mid-loop would flip a PARTIAL fresh stage set and
	// permanently strand the remainder (their provisional generation would equal
	// the now-current one, so the scoped promote filter excludes them forever);
	// two interleaved stages would purge each other's freshly staged bundles.
	defer lockTenantOps(t)()

	// (1) Load the stored, public-keys-only topology. No stored topology is a
	// benign no-op: there is nothing to stage yet (and nothing can be staged —
	// staging requires a stored topology — so there is nothing to purge either).
	rec, err := store.GetTopology(ctx, t)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return StageResult{}, nil
		}
		return StageResult{}, fmt.Errorf("controller: loading topology to stage: %w", err)
	}
	var topo model.Topology
	if err := json.Unmarshal(rec.JSON, &topo); err != nil {
		return StageResult{}, fmt.Errorf("controller: parsing stored topology: %w", err)
	}

	// (2) Build the enrolled subgraph from the registry.
	nodes, err := store.ListNodes(ctx, t)
	if err != nil {
		return StageResult{}, fmt.Errorf("controller: listing nodes to stage: %w", err)
	}
	subgraph, skipped := enrolledSubgraph(&topo, nodes)

	// Nothing enrolled → nothing to render or stage. Report the skips so the caller
	// can surface "no node has enrolled yet" — and leave an audit trace (plan-3):
	// a stage that staged ZERO nodes is exactly the shape of a design-destroying
	// deploy (every node silently skipped), so its occurrence must be visible in
	// the audit log, not just in a transient HTTP response. Best-effort: the audit
	// must not turn the benign no-op into an error.
	//
	// The purge MUST still run on this path (review finding): an empty stage is a
	// stage — the previous stage's bundles keep their promotable provisional
	// generation, so without the purge an operator who retracted the whole design
	// and "cleared" it with an empty stage would have the retracted bundles flip
	// LIVE on the next promote (running install.sh as root with a dead design).
	if len(subgraph.Nodes) == 0 {
		purged, err := store.PruneStagedBundles(ctx, t, nil)
		if err != nil {
			return StageResult{}, fmt.Errorf("controller: purging staged bundles on empty stage: %w", err)
		}
		for _, nodeID := range purged {
			appendStageAudit(ctx, store, t, now, "purge-staged", nodeID)
		}
		appendStageAudit(ctx, store, t, now, "stage-empty", "")
		return StageResult{SkippedUnenrolled: skipped}, nil
	}

	// Is the keystone ON for this tenant? A pinned operator credential turns it on. We
	// read it up front so a store failure (other than ErrNotFound) fails fast, but note
	// the keystone gate to STAGE is intentionally weak: we build + store the manifest,
	// but DO NOT require a signature here (the signature gate is in PromoteStaged).
	keystoneOn := false
	if _, err := store.GetOperatorCredential(ctx, t); err == nil {
		keystoneOn = true
	} else if !errors.Is(err, ErrNotFound) {
		return StageResult{}, fmt.Errorf("controller: loading operator credential to stage: %w", err)
	}

	// (3) Drive the frozen pipeline: AgentHeld keys (zero-knowledge), compile, render.
	keys, err := render.GenerateKeys(&subgraph, render.AgentHeld)
	if err != nil {
		return StageResult{}, fmt.Errorf("controller: preparing keys for stage: %w", err)
	}
	result, err := compiler.NewCompiler().Compile(&subgraph, keys)
	if err != nil {
		return StageResult{}, fmt.Errorf("controller: compiling enrolled subgraph: %w", err)
	}
	if err := render.All(result, keys); err != nil {
		return StageResult{}, fmt.Errorf("controller: rendering enrolled subgraph: %w", err)
	}

	// Persist the compiled allocation pins back into the FULL stored topology so a later
	// re-compile sticky-pins them (invariant I10). rec.JSON is passed so a write-back
	// that changes NOTHING (sticky pins re-derived byte-identically) is skipped instead
	// of burning one of the bounded history slots.
	if err := persistAllocations(ctx, store, t, &topo, result.Topology, rec.JSON); err != nil {
		return StageResult{}, err
	}

	// (4) Export to a temp dir we own and remove on return. The export carries NO
	// trust-list files: the off-host manifest binds each node's checksums.sha256 digest,
	// so the trust-list cannot live inside that very checksum set. The served file map
	// appends trustlist.json/.sig at /config time instead.
	tmp, err := os.MkdirTemp("", "yaog-stage-")
	if err != nil {
		return StageResult{}, fmt.Errorf("controller: creating stage temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)
	if _, err := artifacts.Export(result, tmp); err != nil {
		return StageResult{}, fmt.Errorf("controller: exporting bundles to stage: %w", err)
	}

	// (5) Read each enrolled node's exported dir back into a file map and stage it at
	// the next generation. While doing so, capture each node's checksums.sha256 so the
	// keystone manifest (step 6) can bind its digest.
	cur, err := store.CurrentGeneration(ctx, t)
	if err != nil {
		return StageResult{}, fmt.Errorf("controller: reading current generation: %w", err)
	}
	nextGen := cur + 1

	var staged []string
	digests := make(map[string]string, len(subgraph.Nodes)) // nodeID -> bundleSHA256
	pubKeys := make(map[string]string, len(subgraph.Nodes)) // nodeID -> wg public key (from the registry)
	for _, node := range subgraph.Nodes {
		nodeDir := filepath.Join(tmp, node.Name)
		files, err := readBundleDir(nodeDir)
		if err != nil {
			return StageResult{}, fmt.Errorf("controller: reading bundle for node %s: %w", node.ID, err)
		}
		if keystoneOn {
			checks, ok := files["checksums.sha256"]
			if !ok {
				return StageResult{}, fmt.Errorf("controller: staged bundle for node %s has no checksums.sha256 to bind", node.ID)
			}
			digests[node.ID] = bundleSHA256(checks)
			pubKeys[node.ID] = node.WireGuardPublicKey
		}
		if err := store.StageBundle(ctx, t, SignedBundle{
			NodeID:     node.ID,
			Generation: nextGen,
			Files:      files,
			IsStaged:   true,
		}); err != nil {
			return StageResult{}, fmt.Errorf("controller: staging bundle for node %s: %w", node.ID, err)
		}
		staged = append(staged, node.ID)
	}

	// (5b) Purge staged bundles that are NOT part of this stage set (plan-3): a
	// node removed from the design since the previous stage would otherwise leave
	// its stale staged bundle behind, and the next promote would flip it live.
	// One audit entry per purged node keeps the disappearance attributable —
	// written BEFORE the error check, so a prune that failed partway still leaves
	// an audit trace for everything it actually removed (review finding).
	purged, pruneErr := store.PruneStagedBundles(ctx, t, staged)
	for _, nodeID := range purged {
		appendStageAudit(ctx, store, t, now, "purge-staged", nodeID)
	}
	if pruneErr != nil {
		return StageResult{}, fmt.Errorf("controller: purging stale staged bundles: %w", pruneErr)
	}

	// (6) KEYSTONE ON: build the off-host-signable manifest binding each staged node's
	// bundle digest, then STORE it as the staged (unsigned) manifest. Staging does not
	// require a signature; PromoteStaged refuses to promote until a valid off-host
	// signature over THESE exact bytes exists.
	if keystoneOn {
		if err := stageManifest(ctx, store, t, digests, pubKeys); err != nil {
			return StageResult{}, err
		}
	}

	// (7) One audit entry for the whole stage operation. Post-commit (the bundles
	// are staged), so best-effort like the other stage-path audits.
	appendStageAudit(ctx, store, t, now, "stage", "")

	return StageResult{
		Staged:            staged,
		SkippedUnenrolled: skipped,
		Generation:        nextGen,
	}, nil
}

// appendStageAudit appends one best-effort audit entry for a stage-path action
// (stage / stage-empty / purge-staged). Best-effort by design: these audits record
// state changes that have ALREADY committed, and converting an audit-write hiccup
// into a failed stage would tell the operator the action failed when it happened
// (the same post-commit convention as the update-topology/promote audits).
func appendStageAudit(ctx context.Context, store Store, t TenantID, now time.Time, action, nodeID string) {
	_, _ = store.AppendAudit(ctx, t, AuditEntry{
		Timestamp: now,
		Actor:     "operator",
		Action:    action,
		NodeID:    nodeID,
	})
}

// stageManifest assembles the off-host-signable membership manifest from the staged
// nodes — each Member is {NodeID, WGPublicKey, BundleSHA256} — and stores it as the
// staged, UNSIGNED manifest (StoredTrustList.TrustListJSON = Canonical(manifest),
// SignatureJSON empty, Epoch set by the monotonic rule). The members are exactly the
// nodes that were rendered this stage (only they carry a bundle digest); their WG public
// keys come from the registry value stamped on the subgraph.
//
// Monotonic epoch (anti-rollback): reuse the prior stored manifest's epoch iff its
// membership (node_id -> {wg key, bundle digest}) is byte-for-byte the same; otherwise
// prior-epoch+1, or 0 when no manifest has ever been stored. Because BundleSHA256 is now
// part of the membership tuple, ANY change to a node's install.sh/config (which changes
// its bundle digest) advances the epoch, so a node's anti-rollback floor admits the fresh
// deploy and rejects a stale one.
func stageManifest(ctx context.Context, store Store, t TenantID, digests, pubKeys map[string]string) error {
	members := make([]trustlist.Member, 0, len(digests))
	for nodeID, dig := range digests {
		members = append(members, trustlist.Member{
			NodeID:       nodeID,
			WGPublicKey:  pubKeys[nodeID],
			BundleSHA256: dig,
		})
	}

	newMembers := make(map[string]memberKey, len(members))
	for _, m := range members {
		newMembers[m.NodeID] = memberKey{wgPublicKey: m.WGPublicKey, bundleSHA256: m.BundleSHA256}
	}

	// Monotonic epoch relative to the prior stored manifest.
	var epoch int64
	if stored, err := store.GetCurrentSignedTrustList(ctx, t); err == nil {
		priorMembers, perr := manifestMembers(stored.TrustListJSON)
		if perr != nil {
			return perr
		}
		if sameMembership(newMembers, priorMembers) {
			epoch = stored.Epoch
		} else {
			epoch = stored.Epoch + 1
		}
	} else if !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("controller: loading prior manifest for epoch: %w", err)
	}

	manifest := trustlist.TrustList{
		SchemaVersion: 1,
		Tenant:        string(t),
		Epoch:         epoch,
		Members:       members,
	}
	canonical, err := trustlist.Canonical(manifest)
	if err != nil {
		return fmt.Errorf("controller: canonicalizing staged manifest: %w", err)
	}

	// Store the staged manifest with an EMPTY signature: staging never requires a
	// signature. The operator signs it off-host (GET /trustlist → POST
	// /trustlist-signature, which sets SignatureJSON), and PromoteStaged refuses until
	// that signature exists, matches these bytes, and verifies.
	if err := store.PutSignedTrustList(ctx, t, StoredTrustList{
		TrustListJSON: canonical,
		SignatureJSON: nil,
		Epoch:         epoch,
	}); err != nil {
		return fmt.Errorf("controller: storing staged manifest: %w", err)
	}
	return nil
}

// PromoteStaged flips the tenant's staged bundles to current via Store.PromoteStaged,
// after enforcing the KEYSTONE gate: when an operator credential is pinned (keystone
// ON), a promote is refused unless a NON-EMPTY off-host signature exists over EXACTLY
// the staged manifest bytes AND that signature verifies against the pinned credential.
// This is the deploy-time chokepoint that makes the off-host signature mandatory: a
// breached controller can stage anything, but cannot make a node trust it without a
// signature only the off-host key can produce.
//
// Keystone OFF (no credential pinned): promote exactly as before — Store.PromoteStaged
// with no extra gate.
//
// It returns the new generation, ErrNoStagedBundle when nothing is staged, or a
// descriptive error when the keystone gate refuses.
//
// NOTE: with the keystone on this verifies the off-host SIGNATURE over the stored staged
// manifest as an early, operator-visible defense-in-depth check — it does NOT re-derive
// the staged bundles' checksums digests and compare them to the manifest's BundleSHA256
// values. The authoritative chokepoint is the AGENT, which re-derives
// hex(sha256(checksums.sha256)) offline and binds it to its signed member entry before
// applying. Do not mistake this controller gate for the trust root.
func PromoteStaged(ctx context.Context, store Store, t TenantID) (int64, error) {
	// Serialized against any concurrent stage/promote for this tenant — a promote
	// landing mid-stage would flip a partial stage set (see lockTenantOps).
	defer lockTenantOps(t)()

	cred, err := store.GetOperatorCredential(ctx, t)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Keystone OFF: promote as today.
			return store.PromoteStaged(ctx, t)
		}
		return 0, fmt.Errorf("controller: loading operator credential to promote: %w", err)
	}

	// Keystone ON: a valid off-host signature over the staged manifest is mandatory.
	stored, err := store.GetCurrentSignedTrustList(ctx, t)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return 0, errors.New("controller: keystone is enabled but no membership manifest is staged; stage a deploy before promote")
		}
		return 0, fmt.Errorf("controller: loading staged manifest to promote: %w", err)
	}
	if len(stored.SignatureJSON) == 0 {
		return 0, errors.New("controller: the staged membership manifest is not signed off-host yet; sign it (GET /trustlist, POST /trustlist-signature) before promote")
	}

	// Verify the stored off-host signature over the staged manifest against the pinned
	// credential — exactly what a node does offline. The stored TrustListJSON IS the
	// staged manifest canonical bytes; trustlist.Verify re-canonicalizes internally, and
	// the SignedTrustList carries the detached signature.
	var manifest trustlist.TrustList
	if err := json.Unmarshal(stored.TrustListJSON, &manifest); err != nil {
		return 0, fmt.Errorf("controller: parsing staged manifest to promote: %w", err)
	}
	var signed trustlist.SignedTrustList
	if err := json.Unmarshal(stored.SignatureJSON, &signed); err != nil {
		return 0, fmt.Errorf("controller: parsing staged manifest signature to promote: %w", err)
	}
	pin, err := pinFromOperatorCredential(cred)
	if err != nil {
		return 0, fmt.Errorf("controller: building pinned credential to promote: %w", err)
	}
	if err := trustlist.Verify(manifest, signed, pin); err != nil {
		return 0, fmt.Errorf("controller: staged membership manifest signature is invalid; re-sign before promote: %w", err)
	}

	return store.PromoteStaged(ctx, t)
}

// pinFromOperatorCredential builds the trustlist.PinnedCredential the verifier checks
// against from a stored OperatorCredential, parsing the PEM by the credential's
// algorithm. It mirrors the HTTP layer's pinFromCredential so the promote-gate verifies
// with exactly the anchor a node would use.
func pinFromOperatorCredential(c OperatorCredential) (trustlist.PinnedCredential, error) {
	pin := trustlist.PinnedCredential{
		Alg:          trustlist.Alg(c.Alg),
		CredentialID: c.CredentialID,
		RPID:         c.RPID,
		Origin:       c.Origin,
	}
	switch trustlist.Alg(c.Alg) {
	case trustlist.AlgEd25519, trustlist.AlgWebAuthnEdDSA:
		pub, err := trustlist.ParseEd25519PinPEM([]byte(c.PublicKeyPEM))
		if err != nil {
			return trustlist.PinnedCredential{}, err
		}
		pin.Ed25519Pub = pub
	case trustlist.AlgWebAuthnES256:
		pub, err := trustlist.ParseES256Pin([]byte(c.PublicKeyPEM))
		if err != nil {
			return trustlist.PinnedCredential{}, err
		}
		pin.ES256Pub = pub
	default:
		return trustlist.PinnedCredential{}, fmt.Errorf("controller: unsupported operator credential algorithm %q", c.Alg)
	}
	return pin, nil
}

// enrolledSubgraph projects a stored topology down to its enrolled subgraph under
// the render-what's-ready policy.
//
// A topology node is included iff the registry holds a record for it that is
// NodeApproved with a non-empty WGPublicKey. On every included node it stamps
// WireGuardPublicKey from the registry value (authoritative: the agent holds the
// matching private key) and clears WireGuardPrivateKey — zero-knowledge custody
// means a stray private key from an imported topology must never reach a rendered
// bundle. Any edge whose FromNodeID or ToNodeID is outside the enrolled set is
// dropped; that edge activates on a later deploy once its far end enrolls.
//
// It returns the subgraph plus the list of excluded topology node IDs (skipped).
// The input topology is never mutated (nodes are projected by value copy).
func enrolledSubgraph(topo *model.Topology, nodes []Node) (model.Topology, []string) {
	// registry indexes the enrolled public key by node ID. A node is enrolled iff it
	// is NodeApproved with a non-empty WGPublicKey — the admission test.
	registry := make(map[string]string, len(nodes))
	for _, n := range nodes {
		if n.Status == NodeApproved && n.WGPublicKey != "" {
			registry[n.NodeID] = n.WGPublicKey
		}
	}

	sub := model.Topology{
		Project:            topo.Project,
		Domains:            topo.Domains,
		RoutePolicies:      topo.RoutePolicies,
		AllocSchemaVersion: topo.AllocSchemaVersion,
	}

	// First pass: the set of nodes whose key material is enrolled.
	enrolled := make(map[string]bool, len(topo.Nodes))
	for _, node := range topo.Nodes {
		if _, ok := registry[node.ID]; ok {
			enrolled[node.ID] = true
		}
	}

	// Render-what's-ready for the client role. A client requires EXACTLY ONE enabled
	// outbound edge (compiler validateClientEdges is a HARD error otherwise), so an
	// enrolled client whose dial target is not yet enrolled would be left edgeless and
	// fail the whole stage. Treat such a client as itself not-ready: exclude it now and
	// let it activate on a later deploy once its router/relay/gateway enrolls.
	for _, node := range topo.Nodes {
		if enrolled[node.ID] && node.Role == "client" && !clientTargetEnrolled(topo, node.ID, enrolled) {
			delete(enrolled, node.ID)
		}
	}

	var skipped []string
	for _, node := range topo.Nodes { // value copy: never mutate the caller's slice
		if !enrolled[node.ID] {
			skipped = append(skipped, node.ID)
			continue
		}
		node.WireGuardPublicKey = registry[node.ID]
		node.WireGuardPrivateKey = ""
		sub.Nodes = append(sub.Nodes, node)
	}

	// Drop any edge whose far end is not enrolled: it activates on a later deploy.
	for _, edge := range topo.Edges {
		if enrolled[edge.FromNodeID] && enrolled[edge.ToNodeID] {
			sub.Edges = append(sub.Edges, edge)
		}
	}

	return sub, skipped
}

// clientTargetEnrolled reports whether a client node has an enabled outbound edge
// whose dial target is enrolled — the readiness condition for compiling the client
// (a client must have exactly one enabled outbound edge).
func clientTargetEnrolled(topo *model.Topology, clientID string, enrolled map[string]bool) bool {
	for _, e := range topo.Edges {
		if e.FromNodeID == clientID && e.IsEnabled && enrolled[e.ToNodeID] {
			return true
		}
	}
	return false
}

// persistAllocations merges the allocation pins the compiler stamped onto the
// compiled subgraph back into the FULL stored topology, then re-stores it. It copies
// per-node OverlayIP and the per-edge pin set (transit IPs, link-locals, ports,
// CompiledPort) by ID — never any key material, so the stored topology stays
// public-keys-only — and stamps AllocSchemaVersion. The next CompileAndStage then
// finds these pins in the stored topology and the compiler reuses them (sticky-pin),
// which is what keeps allocations stable across incremental enrollment (I10).
//
// Note (plan-2): a PutTopology write-back that CHANGES the stored topology counts
// as a retained version like any other — the pinned post-stage shape is itself a
// state an operator may want to recover. A write-back whose bytes equal the stored
// record (sticky pins re-derived identically, the common re-stage case) is SKIPPED:
// burning one of the bounded history slots per no-op stage would let routine
// incremental-enrollment staging flush every operator-authored version out of the
// recovery window (review finding, D7).
func persistAllocations(ctx context.Context, store Store, t TenantID, full, compiled *model.Topology, originalJSON []byte) error {
	ipByID := make(map[string]string, len(compiled.Nodes))
	for _, n := range compiled.Nodes {
		ipByID[n.ID] = n.OverlayIP
	}
	edgeByID := make(map[string]model.Edge, len(compiled.Edges))
	for _, e := range compiled.Edges {
		edgeByID[e.ID] = e
	}

	for i := range full.Nodes {
		if ip, ok := ipByID[full.Nodes[i].ID]; ok && ip != "" {
			full.Nodes[i].OverlayIP = ip
		}
	}
	for i := range full.Edges {
		c, ok := edgeByID[full.Edges[i].ID]
		if !ok {
			continue // edge not in the compiled subgraph (far end unenrolled) — leave unpinned
		}
		full.Edges[i].CompiledPort = c.CompiledPort
		full.Edges[i].PinnedFromPort = c.PinnedFromPort
		full.Edges[i].PinnedToPort = c.PinnedToPort
		full.Edges[i].PinnedFromTransitIP = c.PinnedFromTransitIP
		full.Edges[i].PinnedToTransitIP = c.PinnedToTransitIP
		full.Edges[i].PinnedFromLinkLocal = c.PinnedFromLinkLocal
		full.Edges[i].PinnedToLinkLocal = c.PinnedToLinkLocal
	}
	full.AllocSchemaVersion = compiled.AllocSchemaVersion

	raw, err := json.Marshal(full)
	if err != nil {
		return fmt.Errorf("controller: marshaling topology with persisted allocations: %w", err)
	}
	// No-op write-back: the stored record is canonical json.Marshal output (the
	// update-topology custody gate canonicalizes), so byte equality here means the
	// pins changed nothing. Skip the put — do not burn a history slot.
	if bytes.Equal(raw, originalJSON) {
		return nil
	}
	if _, err := store.PutTopology(ctx, t, raw); err != nil {
		return fmt.Errorf("controller: persisting allocations: %w", err)
	}
	return nil
}

// readBundleDir walks an exported node directory and returns its files keyed by
// bundle-relative slash path (e.g. "install.sh", "wireguard/wg-alpha.conf"). It
// skips directories and normalizes separators with filepath.ToSlash so the bundle
// keys are platform-independent — the same keys the agent expects regardless of the
// controller's OS.
func readBundleDir(nodeDir string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := filepath.Walk(nodeDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(nodeDir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
