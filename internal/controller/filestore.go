package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FileStore is a JSON-on-disk Store implementation, durable for the single-tenant
// v1 deployment. It mirrors MemStore's semantics exactly (the shared compat test
// runs against both) while persisting every mutation to disk via temp-file +
// rename so a crash can never leave a half-written record. All in-process access
// is serialized by a sync.Mutex; cross-process durability is provided by the
// atomic renames, but FileStore does not arbitrate between separate processes
// sharing a root (a single controller process owns its root).
//
// On-disk layout under <root>/<tenant>/:
//
//	nodes/<nodeID>.json                 one Node record
//	topology.json                       the current TopologyRecord (with Version)
//	topology-history/<version>.json     one retained TopologyRecord per version
//	                                    (last TopologyHistoryLimit kept, oldest pruned)
//	bundles/<nodeID>.staged.json        the node's staged SignedBundle (if any)
//	bundles/<nodeID>.current.json       the node's current SignedBundle (if any)
//	tokens/<tokenHash>.json             one EnrollmentToken record (keyed by hash)
//	apitokens/<hash>.json               node API token reverse index ({NodeID}), keyed by hash
//	generation.json                     the tenant's current generation counter
//	audit.json                          the full []AuditEntry, in Seq order
//	operator_credential.json            the pinned off-host operator credential (keystone)
//	signed_trustlist.json               the operator-signed membership trust-list (keystone)
//	operators/<username>.json           one operator account (argon2id PHC hash)
//	sessions/<tokenHash>.json           one operator login session, keyed by token hash
//	settings.json                       operator-editable controller settings (bootstrap)
//
// Directories are created 0700 and files written 0600. SignedBundle.Files
// (map[string][]byte) serializes as base64 under encoding/json, which round-trips
// the raw bytes faithfully.
type FileStore struct {
	root string
	mu   sync.Mutex
}

// Compile-time assertion that *FileStore satisfies the Store interface.
var _ Store = (*FileStore)(nil)

// NewFileStore returns a FileStore rooted at the given base directory, creating
// it (0700) if it does not exist.
func NewFileStore(root string) (*FileStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("controller: filestore root must not be empty")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("controller: create filestore root: %w", err)
	}
	return &FileStore{root: root}, nil
}

// --- path helpers -----------------------------------------------------------

// sanitizeComponent rejects values that are unsafe to use as a single path
// component: empty, ".", "..", or anything containing a path separator (or a
// platform-specific separator / NUL). This prevents a malicious or malformed
// TenantID/NodeID from escaping the store root via path traversal.
func sanitizeComponent(kind, v string) (string, error) {
	if v == "" {
		return "", fmt.Errorf("controller: %s must not be empty", kind)
	}
	if v == "." || v == ".." {
		return "", fmt.Errorf("controller: %s %q is not a valid path component", kind, v)
	}
	if strings.ContainsRune(v, '/') || strings.ContainsRune(v, os.PathSeparator) ||
		strings.ContainsRune(v, '\x00') {
		return "", fmt.Errorf("controller: %s %q must not contain a path separator", kind, v)
	}
	return v, nil
}

// tenantDir returns (and does not create) the directory for a tenant, after
// validating the TenantID is a safe path component.
func (fs *FileStore) tenantDir(t TenantID) (string, error) {
	tc, err := sanitizeComponent("tenant id", string(t))
	if err != nil {
		return "", err
	}
	return filepath.Join(fs.root, tc), nil
}

// ensureTenantDir creates the tenant directory and its sub-directories (0700).
func (fs *FileStore) ensureTenantDir(t TenantID) (string, error) {
	dir, err := fs.tenantDir(t)
	if err != nil {
		return "", err
	}
	for _, sub := range []string{"", "nodes", "bundles", "tokens", "login-challenges", "apitokens", "operators", "sessions", "topology-history"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0700); err != nil {
			return "", fmt.Errorf("controller: create tenant dir: %w", err)
		}
	}
	return dir, nil
}

// nodePath returns the on-disk path for a node record after validating nodeID.
func (fs *FileStore) nodePath(dir, nodeID string) (string, error) {
	nc, err := sanitizeComponent("node id", nodeID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "nodes", nc+".json"), nil
}

// bundlePath returns the on-disk path for a node's staged/current bundle.
func (fs *FileStore) bundlePath(dir, nodeID, kind string) (string, error) {
	nc, err := sanitizeComponent("node id", nodeID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bundles", nc+"."+kind+".json"), nil
}

// tokenPath returns the on-disk path for an enrollment token after validating the
// tokenHash is a safe single path component (it is a hex SHA-256 in practice, but
// it is sanitized like any other untrusted key to prevent path traversal).
func (fs *FileStore) tokenPath(dir, tokenHash string) (string, error) {
	tc, err := sanitizeComponent("token hash", tokenHash)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tokens", tc+".json"), nil
}

// loginChallengePath returns the on-disk path for a passkey login challenge after
// validating the challengeHash is a safe single path component (a hex SHA-256 in
// practice, sanitized like any untrusted key to prevent path traversal).
func (fs *FileStore) loginChallengePath(dir, challengeHash string) (string, error) {
	cc, err := sanitizeComponent("login challenge hash", challengeHash)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "login-challenges", cc+".json"), nil
}

// apiTokenPath returns the on-disk path for a node API token's reverse-index entry
// after validating the hash is a safe single path component (a hex SHA-256 in
// practice, sanitized like any untrusted key to prevent path traversal).
func (fs *FileStore) apiTokenPath(dir, tokenHash string) (string, error) {
	tc, err := sanitizeComponent("api token hash", tokenHash)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "apitokens", tc+".json"), nil
}

// operatorPath returns the on-disk path for an operator account after validating the
// username is a safe single path component.
func (fs *FileStore) operatorPath(dir, username string) (string, error) {
	uc, err := sanitizeComponent("operator username", username)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "operators", uc+".json"), nil
}

// sessionPath returns the on-disk path for an operator session after validating the
// token hash is a safe single path component (a hex SHA-256 in practice, sanitized
// like any untrusted key to prevent path traversal).
func (fs *FileStore) sessionPath(dir, tokenHash string) (string, error) {
	tc, err := sanitizeComponent("session token hash", tokenHash)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sessions", tc+".json"), nil
}

// --- atomic JSON IO ---------------------------------------------------------

// writeJSONAtomic marshals v and writes it to path via a temp-file + rename so a
// crash cannot leave a truncated file. The parent directory must already exist.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("controller: marshal %s: %w", filepath.Base(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("controller: write %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("controller: install %s: %w", filepath.Base(path), err)
	}
	return nil
}

// readJSON reads and unmarshals path into v. A missing file is reported via
// os.IsNotExist on the returned error so callers can map it to ErrNotFound.
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("controller: parse %s: %w", filepath.Base(path), err)
	}
	return nil
}

// --- generation -------------------------------------------------------------

// generationFile is the on-disk shape of generation.json.
type generationFile struct {
	Generation int64 `json:"generation"`
}

// apiTokenIndex is the on-disk shape of apitokens/<hash>.json: the reverse index
// from a node API token's hash to the owning NodeID.
type apiTokenIndex struct {
	NodeID string `json:"node_id"`
}

// readGeneration returns the tenant's current generation, defaulting to 0 when
// generation.json is absent (nothing promoted yet).
func (fs *FileStore) readGeneration(dir string) (int64, error) {
	var g generationFile
	err := readJSON(filepath.Join(dir, "generation.json"), &g)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return g.Generation, nil
}

// --- audit ------------------------------------------------------------------

// readAudit returns the tenant's audit entries (empty slice when audit.json is
// absent), in their stored Seq order.
func (fs *FileStore) readAudit(dir string) ([]AuditEntry, error) {
	var entries []AuditEntry
	err := readJSON(filepath.Join(dir, "audit.json"), &entries)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return entries, nil
}

// =============================== Registry ==================================

// UpsertNode creates or updates a node registry record, matched by NodeID.
func (fs *FileStore) UpsertNode(ctx context.Context, t TenantID, n Node) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.nodePath(dir, n.NodeID)
	if err != nil {
		return err
	}
	return writeJSONAtomic(p, n)
}

// GetNode returns the node, or ErrNotFound.
func (fs *FileStore) GetNode(ctx context.Context, t TenantID, nodeID string) (Node, error) {
	if err := ctx.Err(); err != nil {
		return Node{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return Node{}, err
	}
	p, err := fs.nodePath(dir, nodeID)
	if err != nil {
		return Node{}, err
	}
	var n Node
	if err := readJSON(p, &n); err != nil {
		if os.IsNotExist(err) {
			return Node{}, ErrNotFound
		}
		return Node{}, err
	}
	return n, nil
}

// ListNodes returns all nodes for the tenant, stably ordered by NodeID.
func (fs *FileStore) ListNodes(ctx context.Context, t TenantID) ([]Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return nil, err
	}
	return fs.listNodesLocked(dir)
}

// listNodesLocked reads every node record under <dir>/nodes, sorted by NodeID.
// The caller must hold fs.mu.
func (fs *FileStore) listNodesLocked(dir string) ([]Node, error) {
	nodesDir := filepath.Join(dir, "nodes")
	ents, err := os.ReadDir(nodesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Node{}, nil
		}
		return nil, fmt.Errorf("controller: list nodes: %w", err)
	}
	out := make([]Node, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var n Node
		if err := readJSON(filepath.Join(nodesDir, e.Name()), &n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out, nil
}

// SetAppliedGeneration records what an agent reported applying (generation,
// checksum, and health).
func (fs *FileStore) SetAppliedGeneration(ctx context.Context, t TenantID, nodeID string, gen int64, checksum, health string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.nodePath(dir, nodeID)
	if err != nil {
		return err
	}
	var n Node
	if err := readJSON(p, &n); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	n.AppliedGeneration = gen
	n.LastChecksum = checksum
	n.LastHealth = health
	return writeJSONAtomic(p, n)
}

// TouchLastSeen records that the agent for nodeID checked in at the given time.
func (fs *FileStore) TouchLastSeen(ctx context.Context, t TenantID, nodeID string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.nodePath(dir, nodeID)
	if err != nil {
		return err
	}
	var n Node
	if err := readJSON(p, &n); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	n.LastSeen = at
	return writeJSONAtomic(p, n)
}

// =============================== Topology ==================================

// PutTopology stores a new topology version and returns the stored record with
// its assigned Version (1, 2, 3, …). The version is also retained under
// topology-history/ (bounded by TopologyHistoryLimit, oldest pruned). The history
// file is written BEFORE topology.json flips, so a crash between the two leaves a
// harmless extra history entry, never a current record missing from history.
func (fs *FileStore) PutTopology(ctx context.Context, t TenantID, jsonBytes []byte) (TopologyRecord, error) {
	if err := ctx.Err(); err != nil {
		return TopologyRecord{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return TopologyRecord{}, err
	}
	p := filepath.Join(dir, "topology.json")

	var prevVersion int64
	var prev TopologyRecord
	if err := readJSON(p, &prev); err != nil {
		if !os.IsNotExist(err) {
			return TopologyRecord{}, err
		}
	} else {
		prevVersion = prev.Version
	}

	// Defensive copy so the returned record does not alias the caller's slice
	// (parity with MemStore: a caller mutating its input must not affect the store).
	rec := TopologyRecord{
		Version:   prevVersion + 1,
		JSON:      append([]byte(nil), jsonBytes...),
		UpdatedAt: time.Now().UTC(),
	}
	histDir := filepath.Join(dir, "topology-history")
	if err := writeJSONAtomic(filepath.Join(histDir, fmt.Sprintf("%d.json", rec.Version)), rec); err != nil {
		return TopologyRecord{}, err
	}
	if err := writeJSONAtomic(p, rec); err != nil {
		return TopologyRecord{}, err
	}
	// Prune beyond the retention bound. Best-effort: a leftover file is re-pruned on
	// the next put, and pruning must not fail a successful store.
	if cutoff := rec.Version - TopologyHistoryLimit; cutoff > 0 {
		entries, err := os.ReadDir(histDir)
		if err == nil {
			for _, e := range entries {
				v, ok := historyVersionFromName(e.Name())
				if ok && v <= cutoff {
					_ = os.Remove(filepath.Join(histDir, e.Name()))
				}
			}
		}
	}
	return rec, nil
}

// historyVersionFromName parses a topology-history file name ("<version>.json")
// into its version, reporting ok=false for anything else (temp files, foreign
// names) so directory scans skip them instead of failing.
func historyVersionFromName(name string) (int64, bool) {
	base, found := strings.CutSuffix(name, ".json")
	if !found {
		return 0, false
	}
	v, err := strconv.ParseInt(base, 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// ListTopologyVersions returns the retained versions, newest first. An absent
// history directory (tenant never stored a topology) is an empty list.
func (fs *FileStore) ListTopologyVersions(ctx context.Context, t TenantID) ([]TopologyVersionInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return nil, err
	}
	histDir := filepath.Join(dir, "topology-history")
	entries, err := os.ReadDir(histDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []TopologyVersionInfo{}, nil
		}
		return nil, err
	}
	out := make([]TopologyVersionInfo, 0, len(entries))
	for _, e := range entries {
		if _, ok := historyVersionFromName(e.Name()); !ok {
			continue
		}
		var rec TopologyRecord
		if err := readJSON(filepath.Join(histDir, e.Name()), &rec); err != nil {
			// A file deleted between ReadDir and read (concurrent prune in another
			// process is out of contract, but be tolerant); anything else is real.
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		out = append(out, TopologyVersionInfo{
			Version:   rec.Version,
			UpdatedAt: rec.UpdatedAt,
			Bytes:     len(rec.JSON),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

// GetTopologyVersion returns one retained version, or ErrNotFound (unknown or
// already pruned).
func (fs *FileStore) GetTopologyVersion(ctx context.Context, t TenantID, version int64) (TopologyRecord, error) {
	if err := ctx.Err(); err != nil {
		return TopologyRecord{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return TopologyRecord{}, err
	}
	if version <= 0 {
		return TopologyRecord{}, ErrNotFound
	}
	var rec TopologyRecord
	if err := readJSON(filepath.Join(dir, "topology-history", fmt.Sprintf("%d.json", version)), &rec); err != nil {
		if os.IsNotExist(err) {
			return TopologyRecord{}, ErrNotFound
		}
		return TopologyRecord{}, err
	}
	return rec, nil
}

// GetTopology returns the current topology, or ErrNotFound.
func (fs *FileStore) GetTopology(ctx context.Context, t TenantID) (TopologyRecord, error) {
	if err := ctx.Err(); err != nil {
		return TopologyRecord{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return TopologyRecord{}, err
	}
	var rec TopologyRecord
	if err := readJSON(filepath.Join(dir, "topology.json"), &rec); err != nil {
		if os.IsNotExist(err) {
			return TopologyRecord{}, ErrNotFound
		}
		return TopologyRecord{}, err
	}
	return rec, nil
}

// ========================= Bundles + generation ============================

// StageBundle stores a node's bundle as the staged (not-yet-current) version,
// replacing any prior staged bundle for that node.
func (fs *FileStore) StageBundle(ctx context.Context, t TenantID, b SignedBundle) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.bundlePath(dir, b.NodeID, "staged")
	if err != nil {
		return err
	}
	b.IsStaged = true
	b.IsCurrent = false
	return writeJSONAtomic(p, b)
}

// PromoteStaged atomically flips all staged bundles to current, clears each
// promoted node's prior current bundle, increments the tenant generation by one,
// sets each promoted node's DesiredGeneration to the new generation, wakes
// WaitForGeneration waiters, and returns the new generation. With nothing staged
// it returns ErrNoStagedBundle and changes nothing.
func (fs *FileStore) PromoteStaged(ctx context.Context, t TenantID) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return 0, err
	}
	bundlesDir := filepath.Join(dir, "bundles")

	ents, err := os.ReadDir(bundlesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ErrNoStagedBundle
		}
		return 0, fmt.Errorf("controller: list bundles: %w", err)
	}

	// Collect staged bundles in stable NodeID order for deterministic behavior.
	var staged []SignedBundle
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".staged.json") {
			continue
		}
		var b SignedBundle
		if err := readJSON(filepath.Join(bundlesDir, e.Name()), &b); err != nil {
			return 0, err
		}
		staged = append(staged, b)
	}
	if len(staged) == 0 {
		return 0, ErrNoStagedBundle
	}
	sort.Slice(staged, func(i, j int) bool { return staged[i].NodeID < staged[j].NodeID })

	cur, err := fs.readGeneration(dir)
	if err != nil {
		return 0, err
	}
	newGen := cur + 1

	// Flip each staged bundle to current: write the new current, remove the
	// staged marker, and bump the node's DesiredGeneration. Each per-file write
	// is atomic; the in-process mutex makes the batch logically atomic for any
	// other caller of this Store.
	for _, b := range staged {
		curPath, err := fs.bundlePath(dir, b.NodeID, "current")
		if err != nil {
			return 0, err
		}
		stagedPath, err := fs.bundlePath(dir, b.NodeID, "staged")
		if err != nil {
			return 0, err
		}

		b.IsStaged = false
		b.IsCurrent = true
		b.Generation = newGen
		if err := writeJSONAtomic(curPath, b); err != nil {
			return 0, err
		}
		if err := os.Remove(stagedPath); err != nil && !os.IsNotExist(err) {
			return 0, fmt.Errorf("controller: clear staged bundle: %w", err)
		}

		// Bump the promoted node's DesiredGeneration if a node record exists.
		np, err := fs.nodePath(dir, b.NodeID)
		if err != nil {
			return 0, err
		}
		var n Node
		if err := readJSON(np, &n); err != nil {
			if !os.IsNotExist(err) {
				return 0, err
			}
			// No registry record for this node; nothing to update.
		} else {
			n.DesiredGeneration = newGen
			if err := writeJSONAtomic(np, n); err != nil {
				return 0, err
			}
		}
	}

	// Commit the new generation last: WaitForGeneration polls generation.json,
	// so the bundles/nodes are already in place before the counter advances.
	if err := writeJSONAtomic(filepath.Join(dir, "generation.json"), generationFile{Generation: newGen}); err != nil {
		return 0, err
	}
	return newGen, nil
}

// GetCurrentBundle returns the node's current (promoted) bundle, or ErrNotFound.
func (fs *FileStore) GetCurrentBundle(ctx context.Context, t TenantID, nodeID string) (SignedBundle, error) {
	if err := ctx.Err(); err != nil {
		return SignedBundle{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return SignedBundle{}, err
	}
	p, err := fs.bundlePath(dir, nodeID, "current")
	if err != nil {
		return SignedBundle{}, err
	}
	var b SignedBundle
	if err := readJSON(p, &b); err != nil {
		if os.IsNotExist(err) {
			return SignedBundle{}, ErrNotFound
		}
		return SignedBundle{}, err
	}
	return b, nil
}

// CurrentGeneration returns the tenant's current generation (0 if none promoted).
func (fs *FileStore) CurrentGeneration(ctx context.Context, t TenantID) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return 0, err
	}
	return fs.readGeneration(dir)
}

// BumpGeneration increments the persisted generation counter (generation.json)
// atomically, WITHOUT touching any bundle: the staged/current bundle files are left
// in place, so GetCurrentBundle keeps returning the last promoted bundle. It mirrors
// how PromoteStaged advances generation.json (read current, write current+1 via the
// atomic temp-file + rename), so the ~200ms WaitForGeneration poller picks it up and
// a parked agent's long-poll fires. Returns the new generation. Use PromoteStaged to
// actually flip a staged bundle set live; BumpGeneration is a WAKE-only advance.
func (fs *FileStore) BumpGeneration(ctx context.Context, t TenantID) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return 0, err
	}
	cur, err := fs.readGeneration(dir)
	if err != nil {
		return 0, err
	}
	newGen := cur + 1
	if err := writeJSONAtomic(filepath.Join(dir, "generation.json"), generationFile{Generation: newGen}); err != nil {
		return 0, err
	}
	return newGen, nil
}

// WaitForGeneration blocks until the tenant's current generation is strictly
// greater than afterGen, then returns it; or returns ctx.Err() if ctx is done
// first. It polls generation.json on a short interval (the disk Store has no
// in-process condition variable to wake, so promotion is observed by polling).
// If the condition is already satisfied it returns immediately, even when ctx is
// already done — available data is delivered ahead of cancellation.
func (fs *FileStore) WaitForGeneration(ctx context.Context, t TenantID, afterGen int64) (int64, error) {
	if _, err := fs.tenantDir(t); err != nil {
		return 0, err
	}

	// Fast path: if already past afterGen, return without waiting.
	if gen, err := fs.lockedGeneration(t); err != nil {
		return 0, err
	} else if gen > afterGen {
		return gen, nil
	}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-ticker.C:
			gen, err := fs.lockedGeneration(t)
			if err != nil {
				return 0, err
			}
			if gen > afterGen {
				return gen, nil
			}
		}
	}
}

// lockedGeneration reads the tenant's generation under the mutex without an
// up-front ctx check; WaitForGeneration uses it so an already-satisfied
// condition is reported even if ctx is already done.
func (fs *FileStore) lockedGeneration(t TenantID) (int64, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return 0, err
	}
	return fs.readGeneration(dir)
}

// =========================== Enrollment tokens =============================

// CreateEnrollmentToken stores a single-use, node-scoped, TTL token as JSON under
// <root>/<tenant>/tokens/<tokenHash>.json (0700 dir / 0600 file, atomic write). A
// later CreateEnrollmentToken with the same hash overwrites the prior record.
func (fs *FileStore) CreateEnrollmentToken(ctx context.Context, t TenantID, tok EnrollmentToken) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.tokenPath(dir, tok.TokenHash)
	if err != nil {
		return err
	}
	return writeJSONAtomic(p, tok)
}

// ConsumeEnrollmentToken atomically validates and burns a token under the mutex:
// it reads the token at tokens/<tokenHash>.json and returns ErrTokenInvalid if it
// is absent, if its NodeID != nodeID, or if now is at/after ExpiresAt;
// ErrTokenConsumed if it was already burned; otherwise it sets ConsumedAt=now and
// writes the record back atomically. Holding fs.mu across the read-modify-write
// makes the check-and-burn race-safe within this process.
func (fs *FileStore) ConsumeEnrollmentToken(ctx context.Context, t TenantID, tokenHash, nodeID string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.tokenPath(dir, tokenHash)
	if err != nil {
		return err
	}
	var tok EnrollmentToken
	if err := readJSON(p, &tok); err != nil {
		if os.IsNotExist(err) {
			return ErrTokenInvalid
		}
		return err
	}
	if tok.NodeID != nodeID || !now.Before(tok.ExpiresAt) {
		return ErrTokenInvalid
	}
	if tok.ConsumedAt != nil {
		return ErrTokenConsumed
	}
	consumed := now
	tok.ConsumedAt = &consumed
	return writeJSONAtomic(p, tok)
}

// ====================== Passkey login challenges ===========================

func (fs *FileStore) CreateLoginChallenge(ctx context.Context, t TenantID, lc LoginChallenge) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.loginChallengePath(dir, lc.ChallengeHash)
	if err != nil {
		return err
	}
	return writeJSONAtomic(p, lc)
}

// ConsumeLoginChallenge atomically validates and burns a login challenge under the mutex
// by DELETING its file: it reads login-challenges/<challengeHash>.json and returns
// ErrChallengeInvalid if it is absent, if its Operator != operator, or if now is at/after
// ExpiresAt; otherwise it removes the file and returns nil. Holding fs.mu across the
// read-and-delete makes the check-and-burn race-safe within this process, so a captured
// assertion cannot be replayed (the file is gone) and two concurrent logins cannot both
// win. An expired file is removed (lazy GC); a wrong-operator file is left intact.
func (fs *FileStore) ConsumeLoginChallenge(ctx context.Context, t TenantID, challengeHash, operator string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.loginChallengePath(dir, challengeHash)
	if err != nil {
		return err
	}
	var lc LoginChallenge
	if err := readJSON(p, &lc); err != nil {
		if os.IsNotExist(err) {
			return ErrChallengeInvalid
		}
		return err
	}
	if !now.Before(lc.ExpiresAt) {
		_ = os.Remove(p) // expired: lazy GC
		return ErrChallengeInvalid
	}
	if lc.Operator != operator {
		return ErrChallengeInvalid // not the caller's challenge to burn
	}
	return os.Remove(p) // success: single-use consume
}

// ========================== Node API tokens ================================

// IssueNodeAPIToken stamps tokenHash onto the node record's APITokenHash AND writes
// the reverse index apitokens/<hash>.json ({node_id}) under the mutex. It returns
// ErrNotFound if no node record exists for nodeID. Rotation is self-cleaning: if the
// node already carried a different APITokenHash, that prior apitokens/<oldhash>.json
// entry is removed before the new one is written (a not-exist removal is tolerated),
// so a rotated (stale) token leaves no orphan index file. Each write is individually
// atomic (temp-file + rename); the in-process mutex makes the sequence logically
// atomic for any other caller of this Store.
func (fs *FileStore) IssueNodeAPIToken(ctx context.Context, t TenantID, nodeID, tokenHash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	np, err := fs.nodePath(dir, nodeID)
	if err != nil {
		return err
	}
	var n Node
	if err := readJSON(np, &n); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	ip, err := fs.apiTokenPath(dir, tokenHash)
	if err != nil {
		return err
	}
	// Drop any prior reverse-index file for this node's old token so a rotated
	// token can never linger and resolve to the node.
	if n.APITokenHash != "" && n.APITokenHash != tokenHash {
		oldIP, err := fs.apiTokenPath(dir, n.APITokenHash)
		if err != nil {
			return err
		}
		if err := os.Remove(oldIP); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("controller: delete stale api token index: %w", err)
		}
	}
	n.APITokenHash = tokenHash
	if err := writeJSONAtomic(np, n); err != nil {
		return err
	}
	return writeJSONAtomic(ip, apiTokenIndex{NodeID: nodeID})
}

// LookupNodeByAPIToken resolves a presented token's hash to its Node by reading the
// reverse index apitokens/<hash>.json, then the referenced node record, self-
// consistently: it returns ErrTokenInvalid unless the index resolves to a live node
// whose own APITokenHash still equals tokenHash AND whose Status is NodeApproved.
// This rejects an absent index entry, a missing node record, a stale/orphaned index
// file that no longer matches the node's current token, and any non-approved
// (pending or revoked) node.
func (fs *FileStore) LookupNodeByAPIToken(ctx context.Context, t TenantID, tokenHash string) (Node, error) {
	if err := ctx.Err(); err != nil {
		return Node{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return Node{}, err
	}
	ip, err := fs.apiTokenPath(dir, tokenHash)
	if err != nil {
		return Node{}, err
	}
	var idx apiTokenIndex
	if err := readJSON(ip, &idx); err != nil {
		if os.IsNotExist(err) {
			return Node{}, ErrTokenInvalid
		}
		return Node{}, err
	}
	np, err := fs.nodePath(dir, idx.NodeID)
	if err != nil {
		return Node{}, err
	}
	var n Node
	if err := readJSON(np, &n); err != nil {
		if os.IsNotExist(err) {
			return Node{}, ErrTokenInvalid
		}
		return Node{}, err
	}
	if n.APITokenHash != tokenHash || n.Status != NodeApproved {
		return Node{}, ErrTokenInvalid
	}
	return n, nil
}

// RevokeNodeAPIToken clears the node record's APITokenHash and deletes the reverse
// index entry, immediately invalidating the bearer token. It is idempotent: a
// missing node, or a node with no issued token, is a no-op success (no ErrNotFound).
func (fs *FileStore) RevokeNodeAPIToken(ctx context.Context, t TenantID, nodeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	np, err := fs.nodePath(dir, nodeID)
	if err != nil {
		return err
	}
	var n Node
	if err := readJSON(np, &n); err != nil {
		if os.IsNotExist(err) {
			return nil // idempotent: no node, nothing to revoke
		}
		return err
	}
	if n.APITokenHash == "" {
		return nil // idempotent: no issued token
	}
	ip, err := fs.apiTokenPath(dir, n.APITokenHash)
	if err != nil {
		return err
	}
	n.APITokenHash = ""
	if err := writeJSONAtomic(np, n); err != nil {
		return err
	}
	if err := os.Remove(ip); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("controller: delete api token index: %w", err)
	}
	return nil
}

// ===================== Keystone: operator credential + trust-list ==========

// SetOperatorCredential pins (or replaces) the tenant's off-host operator signing
// credential as <root>/<tenant>/operator_credential.json (0700 dir / 0600 file,
// atomic write). Pinning one turns KEYSTONE ON for the tenant.
func (fs *FileStore) SetOperatorCredential(ctx context.Context, t TenantID, c OperatorCredential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(dir, "operator_credential.json"), c)
}

// GetOperatorCredential returns the tenant's pinned operator credential, or
// ErrNotFound when operator_credential.json is absent (keystone OFF).
func (fs *FileStore) GetOperatorCredential(ctx context.Context, t TenantID) (OperatorCredential, error) {
	if err := ctx.Err(); err != nil {
		return OperatorCredential{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return OperatorCredential{}, err
	}
	var c OperatorCredential
	if err := readJSON(filepath.Join(dir, "operator_credential.json"), &c); err != nil {
		if os.IsNotExist(err) {
			return OperatorCredential{}, ErrNotFound
		}
		return OperatorCredential{}, err
	}
	return c, nil
}

// PutSignedTrustList stores (replacing any prior) the operator-signed membership
// trust-list as <root>/<tenant>/signed_trustlist.json (0700 dir / 0600 file, atomic
// write). The byte fields serialize as base64 under encoding/json, round-tripping the
// raw bytes faithfully.
func (fs *FileStore) PutSignedTrustList(ctx context.Context, t TenantID, sl StoredTrustList) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(dir, "signed_trustlist.json"), sl)
}

// GetCurrentSignedTrustList returns the tenant's current signed trust-list, or
// ErrNotFound when signed_trustlist.json is absent (none signed yet).
func (fs *FileStore) GetCurrentSignedTrustList(ctx context.Context, t TenantID) (StoredTrustList, error) {
	if err := ctx.Err(); err != nil {
		return StoredTrustList{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return StoredTrustList{}, err
	}
	var sl StoredTrustList
	if err := readJSON(filepath.Join(dir, "signed_trustlist.json"), &sl); err != nil {
		if os.IsNotExist(err) {
			return StoredTrustList{}, ErrNotFound
		}
		return StoredTrustList{}, err
	}
	return sl, nil
}

// ===================== Operators + sessions (login) ========================

// PutOperator creates or replaces an operator account as operators/<username>.json
// (0700 dir / 0600 file, atomic write). The record carries only the argon2id PHC
// hash — never a plaintext password.
func (fs *FileStore) PutOperator(ctx context.Context, t TenantID, op Operator) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.operatorPath(dir, op.Username)
	if err != nil {
		return err
	}
	return writeJSONAtomic(p, op)
}

// GetOperator returns the operator account, or ErrNotFound.
func (fs *FileStore) GetOperator(ctx context.Context, t TenantID, username string) (Operator, error) {
	if err := ctx.Err(); err != nil {
		return Operator{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return Operator{}, err
	}
	p, err := fs.operatorPath(dir, username)
	if err != nil {
		return Operator{}, err
	}
	var op Operator
	if err := readJSON(p, &op); err != nil {
		if os.IsNotExist(err) {
			return Operator{}, ErrNotFound
		}
		return Operator{}, err
	}
	return op, nil
}

// ListOperators reads every operator record under <dir>/operators, sorted by
// Username.
func (fs *FileStore) ListOperators(ctx context.Context, t TenantID) ([]Operator, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return nil, err
	}
	opsDir := filepath.Join(dir, "operators")
	ents, err := os.ReadDir(opsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Operator{}, nil
		}
		return nil, fmt.Errorf("controller: list operators: %w", err)
	}
	out := make([]Operator, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var op Operator
		if err := readJSON(filepath.Join(opsDir, e.Name()), &op); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out, nil
}

// DeleteOperator removes an operator account. Idempotent: a missing account is a
// no-op success.
func (fs *FileStore) DeleteOperator(ctx context.Context, t TenantID, username string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.operatorPath(dir, username)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("controller: delete operator: %w", err)
	}
	return nil
}

// AdvanceTOTPStep atomically bumps the operator's TOTP replay watermark to step iff
// step > the stored value. The read-modify-write is held under fs.mu for the whole
// operation (in-process atomic), closing the login TOCTOU. See the Store doc.
func (fs *FileStore) AdvanceTOTPStep(ctx context.Context, t TenantID, username string, step int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return false, err
	}
	p, err := fs.operatorPath(dir, username)
	if err != nil {
		return false, err
	}
	var op Operator
	if err := readJSON(p, &op); err != nil {
		if os.IsNotExist(err) {
			return false, ErrNotFound
		}
		return false, err
	}
	if step <= op.TOTPLastUsedStep {
		return false, nil
	}
	op.TOTPLastUsedStep = step
	if err := writeJSONAtomic(p, op); err != nil {
		return false, err
	}
	return true, nil
}

// CreateSession stores a minted operator session as sessions/<tokenHash>.json (0700
// dir / 0600 file, atomic write), keyed by the token's hash.
func (fs *FileStore) CreateSession(ctx context.Context, t TenantID, sess Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.sessionPath(dir, sess.TokenHash)
	if err != nil {
		return err
	}
	return writeJSONAtomic(p, sess)
}

// LookupSession resolves a session token's hash to its Session, returning
// ErrTokenInvalid if the file is absent OR the session has expired (now at/after
// ExpiresAt). An expired session encountered here is lazily removed so abandoned-
// but-presented sessions self-clean.
func (fs *FileStore) LookupSession(ctx context.Context, t TenantID, tokenHash string, now time.Time) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return Session{}, err
	}
	p, err := fs.sessionPath(dir, tokenHash)
	if err != nil {
		return Session{}, err
	}
	var sess Session
	if err := readJSON(p, &sess); err != nil {
		if os.IsNotExist(err) {
			return Session{}, ErrTokenInvalid
		}
		return Session{}, err
	}
	if !now.Before(sess.ExpiresAt) {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return Session{}, fmt.Errorf("controller: delete expired session: %w", err)
		}
		return Session{}, ErrTokenInvalid
	}
	return sess, nil
}

// DeleteSession removes a session (logout / revoke). Idempotent: a missing session
// is a no-op success.
func (fs *FileStore) DeleteSession(ctx context.Context, t TenantID, tokenHash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return err
	}
	p, err := fs.sessionPath(dir, tokenHash)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("controller: delete session: %w", err)
	}
	return nil
}

// ===================== Controller settings (bootstrap) =====================

// GetSettings returns the tenant's saved settings, or ErrNotFound when settings.json
// is absent.
func (fs *FileStore) GetSettings(ctx context.Context, t TenantID) (ControllerSettings, error) {
	if err := ctx.Err(); err != nil {
		return ControllerSettings{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return ControllerSettings{}, err
	}
	var cs ControllerSettings
	if err := readJSON(filepath.Join(dir, "settings.json"), &cs); err != nil {
		if os.IsNotExist(err) {
			return ControllerSettings{}, ErrNotFound
		}
		return ControllerSettings{}, err
	}
	return cs, nil
}

// PutSettings stores (replacing) the tenant's settings as settings.json (0700 dir /
// 0600 file, atomic write).
func (fs *FileStore) PutSettings(ctx context.Context, t TenantID, cs ControllerSettings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(dir, "settings.json"), cs)
}

// ================================ Audit ====================================

// AppendAudit appends an entry, chaining its PrevHash/Hash to the tenant's prior
// entry and assigning a monotonic Seq. The caller-provided Timestamp is
// preserved. Returns the stored entry with Seq/PrevHash/Hash set.
func (fs *FileStore) AppendAudit(ctx context.Context, t TenantID, e AuditEntry) (AuditEntry, error) {
	if err := ctx.Err(); err != nil {
		return AuditEntry{}, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.ensureTenantDir(t)
	if err != nil {
		return AuditEntry{}, err
	}
	entries, err := fs.readAudit(dir)
	if err != nil {
		return AuditEntry{}, err
	}

	var seq int64 = 1
	prevHash := ""
	if n := len(entries); n > 0 {
		seq = entries[n-1].Seq + 1
		prevHash = entries[n-1].Hash
	}
	e.Seq = seq
	e = chainAudit(e, prevHash)

	entries = append(entries, e)
	if err := writeJSONAtomic(filepath.Join(dir, "audit.json"), entries); err != nil {
		return AuditEntry{}, err
	}
	return e, nil
}

// ListAudit returns the tenant's audit entries in Seq order.
func (fs *FileStore) ListAudit(ctx context.Context, t TenantID) ([]AuditEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, err := fs.tenantDir(t)
	if err != nil {
		return nil, err
	}
	entries, err := fs.readAudit(dir)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		return []AuditEntry{}, nil
	}
	return entries, nil
}
