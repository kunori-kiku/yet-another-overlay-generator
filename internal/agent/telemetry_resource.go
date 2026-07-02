package agent

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// resourceMetricKey is the telemetry metrics-map key carrying host resource utilization (load + memory).
const resourceMetricKey = "resource"

// hostResource is the point-in-time host load + memory reading, carried on the telemetry metrics map
// (metrics["resource"]) and rendered on the node detail page. It carries NO endpoint/IP/key material —
// only load averages and memory totals — so it is observability-only and stripped live-only client-side.
type hostResource struct {
	Load1      float64 `json:"load1"`
	Load5      float64 `json:"load5"`
	Load15     float64 `json:"load15"`
	MemTotalKB uint64  `json:"mem_total_kb"`
	MemAvailKB uint64  `json:"mem_available_kb"`
}

// loadavgFn / meminfoFn read /proc/loadavg and /proc/meminfo, indirected so a test injects fixtures
// without a Linux /proc. A read error is BEST-EFFORT: the sampler emits nothing and never fails a cycle.
var (
	loadavgFn = func() ([]byte, error) { return os.ReadFile("/proc/loadavg") }
	meminfoFn = func() ([]byte, error) { return os.ReadFile("/proc/meminfo") }
)

// resourceSampler emits host load + memory as metrics["resource"] via PURE /proc file reads (no shell),
// mirroring wireguardPeersSampler: best-effort, self-contained, nil on any read/parse failure.
type resourceSampler struct{}

func (resourceSampler) Name() string { return "resource" }

func (resourceSampler) Sample(_ time.Time) ([]model.Condition, map[string]any) {
	loadRaw, err := loadavgFn()
	if err != nil {
		return nil, nil
	}
	l1, l5, l15, ok := parseLoadavg(loadRaw)
	if !ok {
		return nil, nil
	}
	res := hostResource{Load1: l1, Load5: l5, Load15: l15}
	// Memory is a bonus: a /proc/meminfo read/parse failure still reports the load averages.
	if memRaw, merr := meminfoFn(); merr == nil {
		if total, avail, mok := parseMeminfo(memRaw); mok {
			res.MemTotalKB, res.MemAvailKB = total, avail
		}
	}
	return nil, map[string]any{resourceMetricKey: res}
}

// parseLoadavg extracts the 1/5/15-minute load averages from /proc/loadavg
// ("0.52 0.58 0.59 1/834 12345\n"). ok=false unless the first three fields are all present + numeric.
func parseLoadavg(data []byte) (l1, l5, l15 float64, ok bool) {
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0, false
	}
	var err error
	if l1, err = strconv.ParseFloat(fields[0], 64); err != nil {
		return 0, 0, 0, false
	}
	if l5, err = strconv.ParseFloat(fields[1], 64); err != nil {
		return 0, 0, 0, false
	}
	if l15, err = strconv.ParseFloat(fields[2], 64); err != nil {
		return 0, 0, 0, false
	}
	return l1, l5, l15, true
}

// parseMeminfo extracts MemTotal and MemAvailable (both kB) from /proc/meminfo. ok=false unless BOTH
// are present + numeric (an old kernel without MemAvailable simply reports no memory, not garbage).
func parseMeminfo(data []byte) (total, avail uint64, ok bool) {
	var haveTotal, haveAvail bool
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			if v, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
				total, haveTotal = v, true
			}
		case "MemAvailable:":
			if v, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
				avail, haveAvail = v, true
			}
		}
		if haveTotal && haveAvail {
			break
		}
	}
	return total, avail, haveTotal && haveAvail
}
