package controller

// tenantlock.go — per-tenant serialization of the MULTI-CALL control-plane
// operations (plan-3 review hardening). Individual Store methods are atomic under
// each impl's own lock, but stage/promote/sign, enrollment/rekey, and keystone
// credential transitions each span multiple calls or must share one ordering boundary.
// CompileAndStage, for example, is a long sequence
// (CurrentGeneration → N×StageBundle → PruneStagedBundles → manifest → audits).
// With nothing serializing the sequences,
// a promote landing mid-stage flips a PARTIAL fresh stage set and permanently
// strands the rest (their provisional generation becomes equal to the now-current
// generation, which the scoped promote filter excludes forever), and two
// interleaved stages purge each other's freshly staged bundles.
//
// The lock is in-process, matching the deployment contract: FileStore is
// single-process by design (filestore.go header), and every participating mutation
// routes through this package's lock boundary.

import (
	"context"
	"sync"
)

// tenantOpMu maps TenantID → *sync.Mutex serializing multi-call trust and fleet
// transitions per tenant.
var tenantOpMu sync.Map

// lockTenantOps acquires the tenant's operation lock and returns the unlock
// function, for use as `defer lockTenantOps(t)()`.
func lockTenantOps(t TenantID) func() {
	m, _ := tenantOpMu.LoadOrStore(t, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// SaveTopology is the production operator-write boundary for topology JSON. A deploy holds the same
// tenant operation lock from its initial read through staged-set publication, so a save either commits
// before that read and is included, or commits after staging and remains the next unapplied draft. It
// can never land inside the compile/sign/export transaction. The Store CAS remains the atomic
// allocation-writeback guard for alternate adapters and direct Store users.
func SaveTopology(ctx context.Context, store Store, t TenantID, raw []byte) (TopologyRecord, error) {
	defer lockTenantOps(t)()
	return store.PutTopology(ctx, t, raw)
}
