package agent

import (
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
)

// resourceMetricKey is the telemetry metrics-map key carrying host resource utilization (load + memory).
const resourceMetricKey = "resource"

// hostResource is the point-in-time host load + memory reading, carried on the telemetry metrics map
// (metrics["resource"]) and rendered on the node detail page. It carries NO endpoint/IP/key material —
// only load averages and memory totals — so it is observability-only and stripped live-only client-side.
type hostResource struct {
	// CpuPct is host CPU utilization percent (0..100, 0.1 resolution), a delta of /proc/stat jiffies
	// between heartbeats. A POINTER so "unknown" is ABSENT on the wire (omitempty), never a fake 0: the
	// first beat after agent start (no prior snapshot) and a non-advancing/wrapped counter omit it, and
	// the panel renders a gap rather than 0%.
	CpuPct     *float64 `json:"cpu_pct,omitempty"`
	Load1      float64  `json:"load1"`
	Load5      float64  `json:"load5"`
	Load15     float64  `json:"load15"`
	MemTotalKB uint64   `json:"mem_total_kb"`
	MemAvailKB uint64   `json:"mem_available_kb"`
}

// loadavgFn / meminfoFn / statFn read /proc/loadavg, /proc/meminfo and /proc/stat, indirected so a test
// injects fixtures without a Linux /proc. A read error is BEST-EFFORT: the sampler emits nothing (load)
// or omits the affected field (cpu/mem) and never fails a cycle.
var (
	loadavgFn = func() ([]byte, error) { return os.ReadFile("/proc/loadavg") }
	meminfoFn = func() ([]byte, error) { return os.ReadFile("/proc/meminfo") }
	statFn    = func() ([]byte, error) { return os.ReadFile("/proc/stat") }
)

// resourceSampler emits host CPU% + load + memory as metrics["resource"] via PURE /proc file reads (no
// shell), mirroring wireguardPeersSampler: best-effort, self-contained, nil on any read/parse failure.
// It is STATEFUL for CPU: cpu_pct is a delta of /proc/stat jiffies between heartbeats, so it keeps the
// previous aggregate snapshot. The telemetry framework builds ONE instance per daemon and Collects from
// a single goroutine (telemetry.go), so the snapshot needs no locking — it is registered as a POINTER
// (&resourceSampler{}) in BuildTelemetry precisely so its state survives across beats.
type resourceSampler struct {
	prevCPUTotal uint64 // aggregate /proc/stat jiffies at the previous sample (0 until the first read)
	prevCPUIdle  uint64 // idle+iowait jiffies at the previous sample
	hasPrevCPU   bool   // false until the first successful /proc/stat read (no delta on the first beat)
}

func (*resourceSampler) Name() string { return "resource" }

func (s *resourceSampler) Sample(_ time.Time) ([]runtimecontract.Condition, map[string]any) {
	loadRaw, err := loadavgFn()
	if err != nil {
		return nil, nil
	}
	l1, l5, l15, ok := parseLoadavg(loadRaw)
	if !ok {
		return nil, nil
	}
	res := hostResource{Load1: l1, Load5: l5, Load15: l15}
	// CPU% is a delta of /proc/stat between heartbeats: the FIRST beat (no prior snapshot) and any
	// non-advancing/wrapped counter (Δtotal == 0, or idle went backwards) omit cpu_pct entirely (a nil
	// pointer → the field is absent on the wire → a gap, never a fake 0%). Best-effort: a /proc/stat
	// failure still reports load + memory below.
	if statRaw, serr := statFn(); serr == nil {
		if total, idle, sok := parseProcStat(statRaw); sok {
			if s.hasPrevCPU && total > s.prevCPUTotal && idle >= s.prevCPUIdle {
				dTotal := total - s.prevCPUTotal
				dIdle := idle - s.prevCPUIdle
				if dIdle > dTotal { // impossible for a monotonic counter; guard the subtraction anyway
					dIdle = dTotal
				}
				pct := math.Round(100*float64(dTotal-dIdle)/float64(dTotal)*10) / 10
				if pct > 100 {
					pct = 100
				}
				res.CpuPct = &pct
			}
			s.prevCPUTotal, s.prevCPUIdle, s.hasPrevCPU = total, idle, true
		}
	}
	// Memory is a bonus: a /proc/meminfo read/parse failure still reports the load averages (+ CPU).
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

// parseProcStat extracts the aggregate CPU jiffies from /proc/stat's leading "cpu " line
// ("cpu  user nice system idle iowait irq softirq steal guest guest_nice"). total sums the first EIGHT
// columns (user..steal); idle = idle + iowait (columns 4 + 5). guest/guest_nice are EXCLUDED because the
// kernel already counts them inside user/nice — summing them would double-count. Only the aggregate line
// (fields[0] == "cpu", not the per-core "cpuN") is used. ok=false unless it is present and total > 0.
func parseProcStat(data []byte) (total, idle uint64, ok bool) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		for i, f := range fields[1:] {
			if i >= 8 { // stop before guest/guest_nice (already inside user/nice)
				break
			}
			v, perr := strconv.ParseUint(f, 10, 64)
			if perr != nil {
				return 0, 0, false
			}
			total += v
			if i == 3 || i == 4 { // idle (col 4) + iowait (col 5), 0-indexed within fields[1:]
				idle += v
			}
		}
		return total, idle, total > 0
	}
	return 0, 0, false
}
