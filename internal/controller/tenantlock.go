package controller

// tenantlock.go — per-tenant serialization of the MULTI-CALL control-plane
// operations (plan-3 review hardening). Individual Store methods are atomic under
// each impl's own lock, but CompileAndStage is a long sequence of them
// (CurrentGeneration → N×StageBundle → PruneStagedBundles → manifest → audits)
// and PromoteStaged a shorter one. With nothing serializing the sequences,
// a promote landing mid-stage flips a PARTIAL fresh stage set and permanently
// strands the rest (their provisional generation becomes equal to the now-current
// generation, which the scoped promote filter excludes forever), and two
// interleaved stages purge each other's freshly staged bundles.
//
// The lock is in-process, matching the deployment contract: FileStore is
// single-process by design (filestore.go header), and both operator entry points
// route through this package's CompileAndStage/PromoteStaged.

import "sync"

// tenantOpMu maps TenantID → *sync.Mutex serializing stage/promote per tenant.
var tenantOpMu sync.Map

// lockTenantOps acquires the tenant's operation lock and returns the unlock
// function, for use as `defer lockTenantOps(t)()`.
func lockTenantOps(t TenantID) func() {
	m, _ := tenantOpMu.LoadOrStore(t, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
