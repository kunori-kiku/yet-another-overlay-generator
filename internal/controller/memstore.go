package controller

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Compile-time assertion that *MemStore satisfies the Store interface.
var _ Store = (*MemStore)(nil)

// tenantState holds all of one tenant's in-memory control-plane state. Every field
// is guarded by MemStore.mu (the per-tenant state is never accessed without it).
type tenantState struct {
	// nodes is the registry, keyed by NodeID.
	nodes map[string]Node
	// topology is the current stored topology (nil until the first PutTopology).
	topology *TopologyRecord
	// topoVersion is the last assigned topology Version; PutTopology pre-increments it.
	topoVersion int64
	// staged maps NodeID -> the node's staged (not-yet-current) bundle.
	staged map[string]SignedBundle
	// current maps NodeID -> the node's current (promoted) bundle.
	current map[string]SignedBundle
	// tokens maps an enrollment token's hash -> the EnrollmentToken record. The
	// plaintext token is never stored; only its hash keys this map.
	tokens map[string]EnrollmentToken
	// apiTokens is the reverse index for per-node bearer auth: a node API token's
	// hash -> the owning NodeID. IssueNodeAPIToken populates it, RevokeNodeAPIToken
	// deletes the entry, and LookupNodeByAPIToken resolves it. The plaintext token
	// is never stored; only its hash keys this map.
	apiTokens map[string]string
	// generation is the tenant's current generation (0 before any promote).
	generation int64
	// audit is the append-only, hash-chained audit log in Seq order.
	audit []AuditEntry
	// lastHash is the Hash of the most recent audit entry ("" if none yet).
	lastHash string
	// nextSeq is the Seq to assign to the next appended audit entry (starts at 1).
	nextSeq int64
}

func newTenantState() *tenantState {
	return &tenantState{
		nodes:     make(map[string]Node),
		staged:    make(map[string]SignedBundle),
		current:   make(map[string]SignedBundle),
		tokens:    make(map[string]EnrollmentToken),
		apiTokens: make(map[string]string),
		nextSeq:   1,
	}
}

// MemStore is an in-memory, mutex-guarded Store implementation. It is the
// CI-exercised reference impl and the long-poll primitive backing
// WaitForGeneration. All state is partitioned by TenantID so one tenant's data is
// never visible to another. SignedBundle.Files and all returned slices are
// deep-copied on store and on return so callers cannot mutate internal state.
type MemStore struct {
	mu sync.Mutex
	// cond is broadcast whenever any tenant's generation advances (PromoteStaged)
	// or a WaitForGeneration ctx-watcher fires; it shares mu so all generation
	// reads/writes and waits are serialized.
	cond    *sync.Cond
	tenants map[TenantID]*tenantState
}

// NewMemStore returns an empty, ready-to-use in-memory Store.
func NewMemStore() *MemStore {
	s := &MemStore{
		tenants: make(map[TenantID]*tenantState),
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// tenant returns the tenant's state, creating it on first access. Caller must hold mu.
func (s *MemStore) tenant(t TenantID) *tenantState {
	ts := s.tenants[t]
	if ts == nil {
		ts = newTenantState()
		s.tenants[t] = ts
	}
	return ts
}

// --- deep-copy helpers ---

// cloneFiles returns a deep copy of a bundle's Files map (and its byte slices) so
// stored/returned bundles share no backing arrays with the caller.
func cloneFiles(in map[string][]byte) map[string][]byte {
	if in == nil {
		return nil
	}
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		cp := make([]byte, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

// cloneBundle returns a deep copy of a SignedBundle (copying its Files).
func cloneBundle(b SignedBundle) SignedBundle {
	b.Files = cloneFiles(b.Files)
	return b
}

// cloneTopology returns a deep copy of a TopologyRecord (copying its JSON bytes).
func cloneTopology(r TopologyRecord) TopologyRecord {
	if r.JSON != nil {
		cp := make([]byte, len(r.JSON))
		copy(cp, r.JSON)
		r.JSON = cp
	}
	return r
}

// --- Registry ---

// UpsertNode creates or updates a node registry record (matched by NodeID).
func (s *MemStore) UpsertNode(ctx context.Context, t TenantID, n Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	ts.nodes[n.NodeID] = n
	return nil
}

// GetNode returns the node, or ErrNotFound.
func (s *MemStore) GetNode(ctx context.Context, t TenantID, nodeID string) (Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	n, ok := ts.nodes[nodeID]
	if !ok {
		return Node{}, ErrNotFound
	}
	return n, nil
}

// ListNodes returns all nodes for the tenant in stable order by NodeID.
func (s *MemStore) ListNodes(ctx context.Context, t TenantID) ([]Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	out := make([]Node, 0, len(ts.nodes))
	for _, n := range ts.nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out, nil
}

// SetAppliedGeneration records what an agent reported applying. Returns ErrNotFound
// if the node does not exist.
func (s *MemStore) SetAppliedGeneration(ctx context.Context, t TenantID, nodeID string, gen int64, checksum string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	n, ok := ts.nodes[nodeID]
	if !ok {
		return ErrNotFound
	}
	n.AppliedGeneration = gen
	n.LastChecksum = checksum
	ts.nodes[nodeID] = n
	return nil
}

// TouchLastSeen records that the agent for nodeID checked in at the given time.
// Returns ErrNotFound if the node does not exist.
func (s *MemStore) TouchLastSeen(ctx context.Context, t TenantID, nodeID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	n, ok := ts.nodes[nodeID]
	if !ok {
		return ErrNotFound
	}
	n.LastSeen = at
	ts.nodes[nodeID] = n
	return nil
}

// --- Topology (public-keys-only) ---

// PutTopology stores a new topology version and returns the stored record with its
// assigned (incrementing) Version.
func (s *MemStore) PutTopology(ctx context.Context, t TenantID, json []byte) (TopologyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	ts.topoVersion++
	// Store a private copy so later caller mutations of json don't leak in.
	stored := make([]byte, len(json))
	copy(stored, json)
	rec := TopologyRecord{
		Version:   ts.topoVersion,
		JSON:      stored,
		UpdatedAt: time.Now().UTC(),
	}
	ts.topology = &rec
	return cloneTopology(rec), nil
}

// GetTopology returns the current topology, or ErrNotFound.
func (s *MemStore) GetTopology(ctx context.Context, t TenantID) (TopologyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	if ts.topology == nil {
		return TopologyRecord{}, ErrNotFound
	}
	return cloneTopology(*ts.topology), nil
}

// --- Bundles + generation ---

// StageBundle stores a node's bundle as the staged (not-yet-current) version,
// replacing any prior staged bundle for that node.
func (s *MemStore) StageBundle(ctx context.Context, t TenantID, b SignedBundle) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	b.IsStaged = true
	b.IsCurrent = false
	ts.staged[b.NodeID] = cloneBundle(b)
	return nil
}

// PromoteStaged atomically flips all staged bundles to current, increments the
// tenant's generation, sets each promoted node's DesiredGeneration to the new
// generation, wakes WaitForGeneration waiters, and returns the new generation.
// Returns ErrNoStagedBundle when nothing is staged.
func (s *MemStore) PromoteStaged(ctx context.Context, t TenantID) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	if len(ts.staged) == 0 {
		return 0, ErrNoStagedBundle
	}
	newGen := ts.generation + 1
	for nodeID, b := range ts.staged {
		b.IsStaged = false
		b.IsCurrent = true
		b.Generation = newGen
		// Replacing ts.current[nodeID] clears the prior current for that node.
		ts.current[nodeID] = b
		delete(ts.staged, nodeID)
		// Bump the node's DesiredGeneration to the new generation.
		if n, ok := ts.nodes[nodeID]; ok {
			n.DesiredGeneration = newGen
			ts.nodes[nodeID] = n
		}
	}
	ts.generation = newGen
	// Wake all WaitForGeneration waiters across tenants; each rechecks its predicate.
	s.cond.Broadcast()
	return newGen, nil
}

// GetCurrentBundle returns the node's current (promoted) bundle, or ErrNotFound.
func (s *MemStore) GetCurrentBundle(ctx context.Context, t TenantID, nodeID string) (SignedBundle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	b, ok := ts.current[nodeID]
	if !ok {
		return SignedBundle{}, ErrNotFound
	}
	return cloneBundle(b), nil
}

// CurrentGeneration returns the tenant's current generation (0 if none promoted).
func (s *MemStore) CurrentGeneration(ctx context.Context, t TenantID) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tenant(t).generation, nil
}

// WaitForGeneration blocks until the tenant's generation is strictly greater than
// afterGen (returning it), or until ctx is done (returning 0, ctx.Err()). It uses
// the shared sync.Cond plus a watcher goroutine that broadcasts on ctx.Done() so a
// cancelled wait is woken promptly even if no promote occurs.
func (s *MemStore) WaitForGeneration(ctx context.Context, t TenantID, afterGen int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Fast path: already satisfied or already cancelled.
	if g := s.tenant(t).generation; g > afterGen {
		return g, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	// Watcher goroutine: when ctx is done, broadcast so the Wait below re-evaluates
	// and observes ctx.Err(). It is stopped via the local done channel once we exit
	// the loop, so it never leaks past this call.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			// Broadcast under the lock: this forces the watcher to wait until the
			// waiter has parked inside cond.Wait (which releases mu), so the wakeup
			// can never be lost in the gap between the waiter's predicate check and
			// its cond.Wait call. A lockless Broadcast here can race into that gap
			// and be dropped, hanging the waiter until an unrelated promote.
			s.mu.Lock()
			s.cond.Broadcast()
			s.mu.Unlock()
		case <-watchDone:
		}
	}()

	for {
		if g := s.tenant(t).generation; g > afterGen {
			return g, nil
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		// Cond.Wait atomically unlocks mu, sleeps until Broadcast, and relocks mu.
		s.cond.Wait()
	}
}

// --- Enrollment tokens ---

// cloneToken returns a deep copy of an EnrollmentToken, copying the ConsumedAt
// pointer's pointee so stored/returned tokens share no *time.Time with the caller.
func cloneToken(tok EnrollmentToken) EnrollmentToken {
	if tok.ConsumedAt != nil {
		c := *tok.ConsumedAt
		tok.ConsumedAt = &c
	}
	return tok
}

// CreateEnrollmentToken stores a single-use, node-scoped, TTL token keyed by its
// TokenHash within the tenant. A later CreateEnrollmentToken with the same hash
// overwrites the prior record (parity with the registry's upsert semantics).
func (s *MemStore) CreateEnrollmentToken(ctx context.Context, t TenantID, tok EnrollmentToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	ts.tokens[tok.TokenHash] = cloneToken(tok)
	return nil
}

// ConsumeEnrollmentToken atomically validates and burns a token under the lock:
// it returns ErrTokenInvalid if no token matches tokenHash for the tenant, if its
// NodeID != nodeID, or if now is at/after ExpiresAt; ErrTokenConsumed if it was
// already burned; otherwise it sets ConsumedAt=now and returns nil. The whole
// check-and-burn runs under s.mu so two concurrent enrollments cannot both succeed.
func (s *MemStore) ConsumeEnrollmentToken(ctx context.Context, t TenantID, tokenHash, nodeID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	tok, ok := ts.tokens[tokenHash]
	if !ok || tok.NodeID != nodeID || !now.Before(tok.ExpiresAt) {
		return ErrTokenInvalid
	}
	if tok.ConsumedAt != nil {
		return ErrTokenConsumed
	}
	consumed := now
	tok.ConsumedAt = &consumed
	ts.tokens[tokenHash] = tok
	return nil
}

// --- Node API tokens (per-node bearer auth) ---

// IssueNodeAPIToken stamps tokenHash onto the node's APITokenHash and writes the
// reverse index apiTokens[hash]=nodeID, all under s.mu. It returns ErrNotFound if
// no node record exists for nodeID. A node may be re-issued a token (e.g. a fresh
// enrollment): the prior index entry is left in place only if it pointed at this
// same node, but the canonical authorization is APITokenHash, and LookupNodeByAPIToken
// resolves via the index — so callers that rotate tokens should revoke first.
func (s *MemStore) IssueNodeAPIToken(ctx context.Context, t TenantID, nodeID, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	n, ok := ts.nodes[nodeID]
	if !ok {
		return ErrNotFound
	}
	n.APITokenHash = tokenHash
	ts.nodes[nodeID] = n
	ts.apiTokens[tokenHash] = nodeID
	return nil
}

// LookupNodeByAPIToken resolves a presented token's hash to its Node via the
// reverse index. It returns ErrTokenInvalid if the hash is unmapped, if the mapped
// node has since disappeared, or if the resolved node's Status is NodeRevoked.
func (s *MemStore) LookupNodeByAPIToken(ctx context.Context, t TenantID, tokenHash string) (Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	nodeID, ok := ts.apiTokens[tokenHash]
	if !ok {
		return Node{}, ErrTokenInvalid
	}
	n, ok := ts.nodes[nodeID]
	if !ok || n.Status == NodeRevoked {
		return Node{}, ErrTokenInvalid
	}
	return n, nil
}

// RevokeNodeAPIToken clears the node's APITokenHash and deletes its reverse index
// entry, immediately invalidating the bearer token. It is idempotent: a missing
// node or a node with no issued token is a no-op success (no ErrNotFound).
func (s *MemStore) RevokeNodeAPIToken(ctx context.Context, t TenantID, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	if n, ok := ts.nodes[nodeID]; ok && n.APITokenHash != "" {
		delete(ts.apiTokens, n.APITokenHash)
		n.APITokenHash = ""
		ts.nodes[nodeID] = n
	}
	return nil
}

// --- Audit (append-only, hash-chained) ---

// AppendAudit appends an entry, chaining its PrevHash/Hash to the tenant's prior
// entry and assigning a monotonic Seq. The caller-provided Timestamp is preserved.
// Returns the stored entry with Seq/PrevHash/Hash set.
func (s *MemStore) AppendAudit(ctx context.Context, t TenantID, e AuditEntry) (AuditEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	e.Seq = ts.nextSeq
	entry := chainAudit(e, ts.lastHash)
	ts.audit = append(ts.audit, entry)
	ts.lastHash = entry.Hash
	ts.nextSeq++
	return entry, nil
}

// ListAudit returns the tenant's audit entries in Seq order.
func (s *MemStore) ListAudit(ctx context.Context, t TenantID) ([]AuditEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	out := make([]AuditEntry, len(ts.audit))
	copy(out, ts.audit)
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}
