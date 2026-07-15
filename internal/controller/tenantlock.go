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

import "sync"

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
