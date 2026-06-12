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
	// topoHistory retains the last TopologyHistoryLimit stored records, oldest
	// first (the FileStore analogue is topology-history/<version>.json).
	topoHistory []TopologyRecord
	// staged maps NodeID -> the node's staged (not-yet-current) bundle.
	staged map[string]SignedBundle
	// current maps NodeID -> the node's current (promoted) bundle.
	current map[string]SignedBundle
	// tokens maps an enrollment token's hash -> the EnrollmentToken record. The
	// plaintext token is never stored; only its hash keys this map.
	tokens map[string]EnrollmentToken
	// loginChallenges maps a passkey login challenge's hash -> the LoginChallenge
	// record. The plaintext challenge is never stored; only its hash keys this map
	// (the login-challenge analogue of tokens). Single-use is enforced under s.mu.
	loginChallenges map[string]LoginChallenge
	// apiTokens is the reverse index for per-node bearer auth: a node API token's
	// hash -> the owning NodeID. IssueNodeAPIToken populates it, RevokeNodeAPIToken
	// deletes the entry, and LookupNodeByAPIToken resolves it. The plaintext token
	// is never stored; only its hash keys this map.
	apiTokens map[string]string
	// generation is the tenant's current generation (0 before any promote).
	generation int64
	// operatorCred is the tenant's pinned off-host operator signing credential, or nil
	// when none is pinned (keystone OFF). The keystone trust anchor.
	operatorCred *OperatorCredential
	// signedTrustList is the tenant's current operator-signed membership trust-list, or
	// nil when none has been signed yet.
	signedTrustList *StoredTrustList
	// operators maps an operator Username -> the Operator account (argon2id PHC hash;
	// never a plaintext password). Operator login, plan-5.2.
	operators map[string]Operator
	// sessions maps a session token's hash -> the Session record. The plaintext token
	// is never stored; only its hash keys this map (the operator-side analogue of
	// apiTokens).
	sessions map[string]Session
	// settings is the tenant's saved controller settings (bootstrap), or nil when none
	// has been saved (the caller applies DefaultSettings).
	settings *ControllerSettings
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
		tokens:          make(map[string]EnrollmentToken),
		loginChallenges: make(map[string]LoginChallenge),
		apiTokens:       make(map[string]string),
		operators:       make(map[string]Operator),
		sessions:        make(map[string]Session),
		nextSeq:         1,
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

// SetAppliedGeneration records what an agent reported applying (generation,
// checksum, and health). Returns ErrNotFound if the node does not exist.
func (s *MemStore) SetAppliedGeneration(ctx context.Context, t TenantID, nodeID string, gen int64, checksum, health string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	n, ok := ts.nodes[nodeID]
	if !ok {
		return ErrNotFound
	}
	n.AppliedGeneration = gen
	n.LastChecksum = checksum
	n.LastHealth = health
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
// assigned (incrementing) Version. The version is also retained in the bounded
// history (TopologyHistoryLimit, oldest pruned).
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
	ts.topoHistory = append(ts.topoHistory, cloneTopology(rec))
	if over := len(ts.topoHistory) - TopologyHistoryLimit; over > 0 {
		ts.topoHistory = append([]TopologyRecord(nil), ts.topoHistory[over:]...)
	}
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

// ListTopologyVersions returns the retained versions, newest first.
func (s *MemStore) ListTopologyVersions(ctx context.Context, t TenantID) ([]TopologyVersionInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	out := make([]TopologyVersionInfo, 0, len(ts.topoHistory))
	for i := len(ts.topoHistory) - 1; i >= 0; i-- {
		rec := ts.topoHistory[i]
		out = append(out, TopologyVersionInfo{
			Version:   rec.Version,
			UpdatedAt: rec.UpdatedAt,
			Bytes:     len(rec.JSON),
		})
	}
	return out, nil
}

// GetTopologyVersion returns one retained version, or ErrNotFound (unknown or
// already pruned).
func (s *MemStore) GetTopologyVersion(ctx context.Context, t TenantID, version int64) (TopologyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	for _, rec := range ts.topoHistory {
		if rec.Version == version {
			return cloneTopology(rec), nil
		}
	}
	return TopologyRecord{}, ErrNotFound
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

// BumpGeneration increments the tenant's generation and broadcasts to wake any
// WaitForGeneration waiters, WITHOUT touching any bundle (current/staged are left
// untouched, so GetCurrentBundle keeps returning the last promoted bundle). It uses
// the same ts.generation++ + s.cond.Broadcast() pattern as PromoteStaged, so a parked
// agent's long-poll fires; the agent then Fetches /config and acts on the (unchanged-
// bundle) signal rather than re-applying. Returns the new generation.
func (s *MemStore) BumpGeneration(ctx context.Context, t TenantID) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	ts.generation++
	// Wake all WaitForGeneration waiters across tenants; each rechecks its predicate.
	s.cond.Broadcast()
	return ts.generation, nil
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

// cloneOperator deep-copies an Operator's pointer fields (its *LoginCredential) so the
// store never shares mutable state with a caller — a GetOperator caller that mutates the
// returned credential, or a PutOperator caller that retains its pointer, cannot reach
// into the stored map. LoginCredential has no nested reference types, so a shallow copy
// of the pointee suffices.
func cloneOperator(op Operator) Operator {
	if op.LoginCredential != nil {
		lc := *op.LoginCredential
		op.LoginCredential = &lc
	}
	return op
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

// --- Passkey login challenges (plan-5.2) ---

func (s *MemStore) CreateLoginChallenge(ctx context.Context, t TenantID, lc LoginChallenge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	// LoginChallenge has no reference fields, so the map stores a full value copy.
	ts.loginChallenges[lc.ChallengeHash] = lc
	return nil
}

// ConsumeLoginChallenge atomically validates and burns a login challenge under the lock
// by DELETING it: it returns ErrChallengeInvalid if no challenge matches challengeHash, if
// its Operator != operator, or if now is at/after ExpiresAt; otherwise it deletes the
// record and returns nil. The check-and-delete runs under s.mu so a captured assertion
// cannot be replayed (the record is gone) and two concurrent logins cannot both win. An
// expired record is deleted (lazy GC); a wrong-operator record is left intact.
func (s *MemStore) ConsumeLoginChallenge(ctx context.Context, t TenantID, challengeHash, operator string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	lc, ok := ts.loginChallenges[challengeHash]
	if !ok {
		return ErrChallengeInvalid
	}
	if !now.Before(lc.ExpiresAt) {
		delete(ts.loginChallenges, challengeHash) // expired: lazy GC
		return ErrChallengeInvalid
	}
	if lc.Operator != operator {
		return ErrChallengeInvalid // not the caller's challenge to burn
	}
	delete(ts.loginChallenges, challengeHash) // success: single-use consume
	return nil
}

// --- Node API tokens (per-node bearer auth) ---

// IssueNodeAPIToken stamps tokenHash onto the node's APITokenHash and writes the
// reverse index apiTokens[hash]=nodeID, all under s.mu. It returns ErrNotFound if
// no node record exists for nodeID. Rotation is self-cleaning: if the node already
// carried a different APITokenHash, that prior reverse-index entry is deleted before
// the new one is written, so a rotated (stale) token leaves no orphan in the index.
func (s *MemStore) IssueNodeAPIToken(ctx context.Context, t TenantID, nodeID, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	n, ok := ts.nodes[nodeID]
	if !ok {
		return ErrNotFound
	}
	// Drop any prior reverse-index entry for this node's old token so a rotated
	// token can never linger and resolve to the node.
	if n.APITokenHash != "" && n.APITokenHash != tokenHash {
		delete(ts.apiTokens, n.APITokenHash)
	}
	n.APITokenHash = tokenHash
	ts.nodes[nodeID] = n
	ts.apiTokens[tokenHash] = nodeID
	return nil
}

// LookupNodeByAPIToken resolves a presented token's hash to its Node via the
// reverse index, self-consistently: it returns ErrTokenInvalid unless the index
// resolves to a live node whose own APITokenHash still equals tokenHash AND whose
// Status is NodeApproved. This rejects an unmapped hash, a vanished node, a
// stale/orphaned index entry that no longer matches the node's current token, and
// any non-approved (pending or revoked) node.
func (s *MemStore) LookupNodeByAPIToken(ctx context.Context, t TenantID, tokenHash string) (Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	nodeID, ok := ts.apiTokens[tokenHash]
	if !ok {
		return Node{}, ErrTokenInvalid
	}
	n, ok := ts.nodes[nodeID]
	if !ok || n.APITokenHash != tokenHash || n.Status != NodeApproved {
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

// --- Keystone: operator credential + signed trust-list ---

// SetOperatorCredential pins (or replaces) the tenant's off-host operator signing
// credential. A private copy is stored so a later caller mutation cannot leak in.
func (s *MemStore) SetOperatorCredential(ctx context.Context, t TenantID, c OperatorCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	cp := c
	ts.operatorCred = &cp
	return nil
}

// GetOperatorCredential returns the tenant's pinned operator credential, or
// ErrNotFound when none is pinned.
func (s *MemStore) GetOperatorCredential(ctx context.Context, t TenantID) (OperatorCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	if ts.operatorCred == nil {
		return OperatorCredential{}, ErrNotFound
	}
	return *ts.operatorCred, nil
}

// PutSignedTrustList stores (replacing any prior) the operator-signed membership
// trust-list. The byte slices are deep-copied so the stored record shares no backing
// arrays with the caller.
func (s *MemStore) PutSignedTrustList(ctx context.Context, t TenantID, sl StoredTrustList) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	ts.signedTrustList = cloneStoredTrustList(&sl)
	return nil
}

// GetCurrentSignedTrustList returns the tenant's current signed trust-list, or
// ErrNotFound when none has been signed yet. A deep copy is returned.
func (s *MemStore) GetCurrentSignedTrustList(ctx context.Context, t TenantID) (StoredTrustList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	if ts.signedTrustList == nil {
		return StoredTrustList{}, ErrNotFound
	}
	return *cloneStoredTrustList(ts.signedTrustList), nil
}

// cloneStoredTrustList returns a deep copy of a StoredTrustList, copying its byte
// slices so stored/returned records share no backing arrays with the caller.
func cloneStoredTrustList(in *StoredTrustList) *StoredTrustList {
	out := StoredTrustList{Epoch: in.Epoch}
	if in.TrustListJSON != nil {
		out.TrustListJSON = append([]byte(nil), in.TrustListJSON...)
	}
	if in.SignatureJSON != nil {
		out.SignatureJSON = append([]byte(nil), in.SignatureJSON...)
	}
	return &out
}

// --- Operators + sessions (operator login, plan-5.2) ---

// PutOperator creates or replaces an operator account (matched by Username). Operator
// is a value type with no reference fields, so the stored copy is independent.
func (s *MemStore) PutOperator(ctx context.Context, t TenantID, op Operator) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	ts.operators[op.Username] = cloneOperator(op)
	return nil
}

// GetOperator returns the operator account, or ErrNotFound.
func (s *MemStore) GetOperator(ctx context.Context, t TenantID, username string) (Operator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	op, ok := ts.operators[username]
	if !ok {
		return Operator{}, ErrNotFound
	}
	return cloneOperator(op), nil
}

// ListOperators returns all operator accounts for the tenant, stably ordered by
// Username.
func (s *MemStore) ListOperators(ctx context.Context, t TenantID) ([]Operator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	out := make([]Operator, 0, len(ts.operators))
	for _, op := range ts.operators {
		out = append(out, cloneOperator(op))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out, nil
}

// DeleteOperator removes an operator account. Idempotent (a missing account is a
// no-op success). Sessions are not cascaded (they expire on their own TTL).
func (s *MemStore) DeleteOperator(ctx context.Context, t TenantID, username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	delete(ts.operators, username)
	return nil
}

// AdvanceTOTPStep atomically bumps the operator's TOTP replay watermark to step iff
// step > the stored value (the whole check-and-set under one lock). See the Store doc.
func (s *MemStore) AdvanceTOTPStep(ctx context.Context, t TenantID, username string, step int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	op, ok := ts.operators[username]
	if !ok {
		return false, ErrNotFound
	}
	if step <= op.TOTPLastUsedStep {
		return false, nil
	}
	op.TOTPLastUsedStep = step
	ts.operators[username] = op
	return true, nil
}

// CreateSession stores a minted operator session, keyed by its TokenHash.
func (s *MemStore) CreateSession(ctx context.Context, t TenantID, sess Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	ts.sessions[sess.TokenHash] = sess
	return nil
}

// LookupSession resolves a session token's hash to its Session, returning
// ErrTokenInvalid if absent OR expired (now at/after ExpiresAt). An expired session
// encountered here is lazily deleted so abandoned-but-presented sessions self-clean.
func (s *MemStore) LookupSession(ctx context.Context, t TenantID, tokenHash string, now time.Time) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	sess, ok := ts.sessions[tokenHash]
	if !ok {
		return Session{}, ErrTokenInvalid
	}
	if !now.Before(sess.ExpiresAt) {
		delete(ts.sessions, tokenHash)
		return Session{}, ErrTokenInvalid
	}
	return sess, nil
}

// DeleteSession removes a session (logout / revoke). Idempotent.
func (s *MemStore) DeleteSession(ctx context.Context, t TenantID, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	delete(ts.sessions, tokenHash)
	return nil
}

// --- Controller settings (bootstrap, plan-5.2) ---

// GetSettings returns the tenant's saved settings, or ErrNotFound when none saved.
func (s *MemStore) GetSettings(ctx context.Context, t TenantID) (ControllerSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	if ts.settings == nil {
		return ControllerSettings{}, ErrNotFound
	}
	return *ts.settings, nil
}

// PutSettings stores (replacing) the tenant's settings. ControllerSettings is a value
// type with no reference fields, so the stored copy is independent.
func (s *MemStore) PutSettings(ctx context.Context, t TenantID, cs ControllerSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tenant(t)
	cp := cs
	ts.settings = &cp
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
