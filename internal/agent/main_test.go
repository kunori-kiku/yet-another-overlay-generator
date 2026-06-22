package agent

import (
	"errors"
	"os"
	"testing"
)

// TestMain keeps the agent package's unit tests HERMETIC. The production wgShowFn (plan-3) shells out
// to `wg show all dump`; a unit test must not invoke the real `wg` (non-hermetic + machine-dependent,
// and the existing Run/recordSuccess tests would otherwise trigger it via collectConditions). Default
// it to a stub reporting "unavailable" — exactly the best-effort path (no wireguard condition) a host
// without a readable WireGuard interface takes. The WG-specific tests override wgShowFn as needed.
func TestMain(m *testing.M) {
	wgShowFn = func() ([]byte, error) { return nil, errors.New("wg unavailable (hermetic test default)") }
	os.Exit(m.Run())
}
