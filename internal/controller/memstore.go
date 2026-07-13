package controller

// memstore.go — memkv, the in-memory kvBackend, plus the thin MemStore wrapper. memkv holds ONLY
// storage (a per-tenant, per-collection byte map, the generation counter + its sync.Cond wake, and the
// slice-backed audit log); every business rule lives in the shared storeCore (storecore.go). Records
// round-trip as JSON bytes exactly like filekv, so the JSON marshal/unmarshal is the isolation boundary
// (no hand-written clone helpers) and MemStore exercises the identical shipping path, telemetry overlay
// included. MemStore is the CI-exercised reference impl and the long-poll primitive.

import (
	"context"
	"sort"
	"sync"
)

// memkv is the in-memory backend. All state is partitioned by TenantID. data holds tenant → collection
// → key → raw JSON bytes; gen holds the per-tenant generation counter; audit holds the slice-backed
// hash-chained log. mu serializes every operation and backs cond (the WaitForGeneration wake).
type memkv struct {
	mu    sync.Mutex
	cond  *sync.Cond
	data  map[TenantID]map[string]map[string][]byte
	gen   map[TenantID]int64
	audit map[TenantID]*memAuditLog
}

// memAuditLog is one tenant's in-memory audit log: the entries plus the tail (next Seq + last Hash) to
// chain and bound-check without rescanning.
type memAuditLog struct {
	entries  []AuditEntry
	nextSeq  int64
	lastHash string
}

var _ kvBackend = (*memkv)(nil)

func newMemkv() *memkv {
	m := &memkv{
		data:  make(map[TenantID]map[string]map[string][]byte),
		gen:   make(map[TenantID]int64),
		audit: make(map[TenantID]*memAuditLog),
	}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// collMap returns the (tenant, collection) key→bytes map, creating the path when create is set.
func (m *memkv) collMap(t TenantID, coll string, create bool) map[string][]byte {
	byColl := m.data[t]
	if byColl == nil {
		if !create {
			return nil
		}
		byColl = make(map[string]map[string][]byte)
		m.data[t] = byColl
	}
	byKey := byColl[coll]
	if byKey == nil {
		if !create {
			return nil
		}
		byKey = make(map[string][]byte)
		byColl[coll] = byKey
	}
	return byKey
}

// withLock runs fn while holding mu, so the core can compose several primitives atomically.
func (m *memkv) withLock(fn func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fn()
}

// get/put/del/list — caller holds withLock. Bytes are copied in and out so a stored record shares no
// backing array with the caller (parity with filekv's file round-trip).
func (m *memkv) get(t TenantID, coll, key string) ([]byte, error) {
	byKey := m.collMap(t, coll, false)
	if byKey == nil {
		return nil, errKVNotFound
	}
	v, ok := byKey[key]
	if !ok {
		return nil, errKVNotFound
	}
	return append([]byte(nil), v...), nil
}

func (m *memkv) put(t TenantID, coll, key string, val []byte) error {
	byKey := m.collMap(t, coll, true)
	byKey[key] = append([]byte(nil), val...)
	return nil
}

func (m *memkv) del(t TenantID, coll, key string) error {
	if byKey := m.collMap(t, coll, false); byKey != nil {
		delete(byKey, key)
	}
	return nil
}

func (m *memkv) list(t TenantID, coll string) ([]kvRecord, error) {
	byKey := m.collMap(t, coll, false)
	if byKey == nil {
		return nil, nil
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]kvRecord, 0, len(keys))
	for _, k := range keys {
		out = append(out, kvRecord{key: k, val: append([]byte(nil), byKey[k]...)})
	}
	return out, nil
}

// exists is the self-synchronizing heartbeat existence probe (called OUTSIDE withLock).
func (m *memkv) exists(t TenantID, coll, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	byKey := m.collMap(t, coll, false)
	if byKey == nil {
		return false, nil
	}
	_, ok := byKey[key]
	return ok, nil
}

// generation reads / setGeneration writes the counter — caller holds withLock. setGeneration wakes any
// WaitForGeneration waiters via the shared cond (Broadcast under the held mu, so a wakeup is never lost
// in the gap between a waiter's predicate check and its cond.Wait).
func (m *memkv) generation(t TenantID) (int64, error) {
	return m.gen[t], nil
}

func (m *memkv) setGeneration(t TenantID, gen int64) error {
	m.gen[t] = gen
	m.cond.Broadcast()
	return nil
}

// awaitGenerationChange blocks until the tenant generation is strictly greater than afterGen, or ctx is
// done. A watcher goroutine broadcasts on ctx.Done() (under mu, so the wakeup can never race into the
// gap before the waiter parks in cond.Wait) so a cancelled wait is woken promptly.
func (m *memkv) awaitGenerationChange(ctx context.Context, t TenantID, afterGen int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if g := m.gen[t]; g > afterGen {
		return g, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			m.cond.Broadcast()
			m.mu.Unlock()
		case <-watchDone:
		}
	}()

	for {
		if g := m.gen[t]; g > afterGen {
			return g, nil
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		m.cond.Wait()
	}
}

// auditLogLocked returns the tenant's audit log, creating it (nextSeq starts at 1). Caller holds mu.
func (m *memkv) auditLogLocked(t TenantID) *memAuditLog {
	log := m.audit[t]
	if log == nil {
		log = &memAuditLog{nextSeq: 1}
		m.audit[t] = log
	}
	return log
}

// appendAudit chains and appends an entry, bounding the log (trim to auditRetain past auditRotateAt).
// Self-synchronizing (standalone). Seq/lastHash keep advancing so the retained window stays a valid
// chain under VerifyAuditChain's first-entry anchoring.
func (m *memkv) appendAudit(t TenantID, e AuditEntry) (AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	log := m.auditLogLocked(t)
	e.Seq = log.nextSeq
	entry := chainAudit(e, log.lastHash)
	log.entries = append(log.entries, entry)
	log.lastHash = entry.Hash
	log.nextSeq++
	if len(log.entries) > auditRotateAt {
		log.entries = append([]AuditEntry(nil), log.entries[len(log.entries)-auditRetain:]...)
	}
	return entry, nil
}

// listAudit returns the tenant's audit entries in Seq order. Self-synchronizing.
func (m *memkv) listAudit(t TenantID) ([]AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	log := m.audit[t]
	if log == nil {
		return []AuditEntry{}, nil
	}
	out := make([]AuditEntry, len(log.entries))
	copy(out, log.entries)
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// MemStore is the in-memory Store: a storeCore over a memkv, with an in-memory (dir-less)
// resource-history buffer. All state is partitioned by TenantID.
type MemStore struct {
	*storeCore
}

// Compile-time assertion that *MemStore satisfies the Store interface.
var _ Store = (*MemStore)(nil)

// NewMemStore returns an empty, ready-to-use in-memory Store.
func NewMemStore() *MemStore {
	// in-memory history (dir==""); no persisted-settings cap seed needed.
	hist := newTelemetryHistory("", DefaultTelemetryHistoryCap, nil)
	return &MemStore{storeCore: newStoreCore(newMemkv(), hist)}
}
