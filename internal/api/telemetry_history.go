package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// telemetry_history.go serves the operator-gated node resource-history query (plan-3): it reads the raw
// samples the controller retains (plan-2 QueryTelemetryHistory) and AGGREGATES them into a bounded
// bucketed series (avg/min/max per metric) the node-detail charts render. Operator mux ONLY — never the
// agent/anonymous muxes (observability, but still operator-authenticated like every other node view).

const (
	// maxHistoryBuckets caps the response so a huge range with a tiny step can't produce an unbounded
	// series; the server widens the step to fit and echoes the effective step.
	maxHistoryBuckets = 1000
	// minHistoryStep preserves the existing explicit-step contract for faster configured agents.
	minHistoryStep = time.Second
	// autoHistoryStepFloor is both Auto's fallback and its minimum. Faster observed or advertised
	// sampling must not manufacture a denser history series than the panel can usefully render.
	autoHistoryStepFloor = 30 * time.Second
	// maxHistoryRange bounds the query window (defense-in-depth; the retained history is capped anyway).
	maxHistoryRange = 366 * 24 * time.Hour
)

// ceilDurationDiv divides d by n, rounding up. The rounded-up value is the smallest step that can
// cover a window with at most n buckets; floor division can accidentally admit a 1001st bucket.
func ceilDurationDiv(d time.Duration, n int64) time.Duration {
	q := d / time.Duration(n)
	if d%time.Duration(n) != 0 {
		q++
	}
	return q
}

// autoHistoryStep prefers the newest valid cadence advertised by an agent. Legacy samples do not
// carry that field, so it falls back to a robust observed cadence: the lower median of positive
// timestamp deltas, rounded to the nearest second. Ignoring duplicate/non-positive deltas and using
// the lower median keeps brief kick uploads and isolated outage gaps from stretching Auto.
func autoHistoryStep(samples []controller.ResourceSample) time.Duration {
	ordered := append([]controller.ResourceSample(nil), samples...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].TS.Before(ordered[j].TS)
	})

	const maxDurationMilliseconds = int64((1<<63 - 1) / time.Millisecond)
	for i := len(ordered) - 1; i >= 0; i-- {
		intervalMS := ordered[i].IntervalMS
		if intervalMS > 0 && intervalMS <= maxDurationMilliseconds {
			return time.Duration(intervalMS) * time.Millisecond
		}
	}

	deltas := make([]time.Duration, 0, len(ordered))
	for i := 1; i < len(ordered); i++ {
		if delta := ordered[i].TS.Sub(ordered[i-1].TS); delta > 0 {
			deltas = append(deltas, delta)
		}
	}
	if len(deltas) < 2 {
		return autoHistoryStepFloor
	}
	sort.Slice(deltas, func(i, j int) bool {
		return deltas[i] < deltas[j]
	})
	return deltas[(len(deltas)-1)/2].Round(time.Second)
}

// effectiveHistoryStep resolves Auto (requested == 0), the legacy explicit-step floor, and the hard
// response-size cap in one pure seam. Explicit steps deliberately bypass cadence discovery so their
// existing 1s-floor/cap-only contract stays unchanged.
func effectiveHistoryStep(window, requested time.Duration, samples []controller.ResourceSample) time.Duration {
	// Stable epoch anchoring can expose a partial bucket at both window edges, so reserve one
	// response slot beyond the divisions that fit wholly inside the requested duration.
	capStep := ceilDurationDiv(window, maxHistoryBuckets-1)
	var step time.Duration
	if requested == 0 {
		step = autoHistoryStep(samples)
		if step < autoHistoryStepFloor {
			step = autoHistoryStepFloor
		}
	} else {
		step = requested
		if step < minHistoryStep {
			step = minHistoryStep
		}
	}
	if step < capStep {
		step = capStep
	}
	return step
}

// historyBucketStart anchors a bucket to the Unix epoch instead of the request's moving from.
// Re-fetching a sliding window therefore leaves existing bucket timestamps unchanged. Operational
// telemetry timestamps are in time.Time's UnixNano range; the negative-time branch keeps the helper
// mathematically correct for focused tests and defensive use.
func historyBucketStart(t time.Time, step time.Duration) time.Time {
	ns := t.UnixNano()
	stepNS := step.Nanoseconds()
	start := ns - ns%stepNS
	if ns < 0 && ns%stepNS != 0 {
		start -= stepNS
	}
	return time.Unix(0, start).UTC()
}

// metricAgg is the avg/min/max of one metric over a bucket's samples.
type metricAgg struct {
	Avg float64 `json:"avg"`
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// historyBucket is one time bucket. Load is always present (every sample has it); cpu_pct and
// mem_used_pct are pointers, ABSENT when no sample in the bucket carried that metric (a gap, never a
// fabricated 0). t is the bucket START.
type historyBucket struct {
	T          time.Time  `json:"t"`
	CpuPct     *metricAgg `json:"cpu_pct,omitempty"`
	Load1      metricAgg  `json:"load1"`
	Load5      metricAgg  `json:"load5"`
	Load15     metricAgg  `json:"load15"`
	MemUsedPct *metricAgg `json:"mem_used_pct,omitempty"`
}

type historyResponse struct {
	Step     string          `json:"step"`     // the EFFECTIVE step (may be widened from the request)
	Disabled bool            `json:"disabled"` // history retention is off for this fleet (cap 0)
	Buckets  []historyBucket `json:"buckets"`
}

// aggAcc accumulates one metric across a bucket.
type aggAcc struct {
	mean, min, max float64
	n              int
}

func (a *aggAcc) add(v float64) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	if a.n == 0 {
		a.mean = v
		a.min = v
		a.max = v
		a.n = 1
		return
	}
	if v < a.min {
		a.min = v
	}
	if v > a.max {
		a.max = v
	}
	previous := float64(a.n)
	a.n++
	count := float64(a.n)
	if math.Signbit(a.mean) == math.Signbit(v) {
		a.mean += (v - a.mean) / count
	} else {
		// Opposite signs can overflow v-mean. Weight each term before adding instead.
		a.mean = a.mean*(previous/count) + v/count
	}
}

func (a aggAcc) result() metricAgg {
	return metricAgg{Avg: a.mean, Min: a.min, Max: a.max}
}

// memUsedPct is used-memory percent for a sample, or ok=false when total is unknown/zero.
func memUsedPct(s controller.ResourceSample) (float64, bool) {
	if s.MemTotalKB == 0 {
		return 0, false
	}
	used := float64(s.MemTotalKB) - float64(s.MemAvailKB)
	pct := used / float64(s.MemTotalKB) * 100
	if pct < 0 {
		pct = 0
	} else if pct > 100 {
		pct = 100
	}
	return pct, true
}

// aggregateHistory buckets samples by step on a stable Unix-epoch grid, computing avg/min/max per
// metric. Empty buckets are OMITTED (gaps stay gaps); cpu_pct / mem_used_pct are absent when no sample
// in the bucket carried them. PURE — table-tested. The request's from is intentionally not an
// anchor: moving history windows must not re-phase already-rendered buckets.
func aggregateHistory(samples []controller.ResourceSample, step time.Duration) []historyBucket {
	if step <= 0 || len(samples) == 0 {
		return nil
	}
	type bucketAcc struct {
		load1, load5, load15 aggAcc
		cpu, mem             aggAcc
	}
	byStart := map[int64]*bucketAcc{}
	var order []int64
	for _, s := range samples {
		if math.IsNaN(s.Load1) || math.IsInf(s.Load1, 0) || math.IsNaN(s.Load5) || math.IsInf(s.Load5, 0) || math.IsNaN(s.Load15) || math.IsInf(s.Load15, 0) {
			continue
		}
		start := historyBucketStart(s.TS, step).UnixNano()
		a := byStart[start]
		if a == nil {
			a = &bucketAcc{}
			byStart[start] = a
			order = append(order, start)
		}
		a.load1.add(s.Load1)
		a.load5.add(s.Load5)
		a.load15.add(s.Load15)
		if s.CpuPct != nil {
			a.cpu.add(*s.CpuPct)
		}
		if pct, ok := memUsedPct(s); ok {
			a.mem.add(pct)
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]historyBucket, 0, len(order))
	for _, start := range order {
		a := byStart[start]
		b := historyBucket{
			T:      time.Unix(0, start).UTC(),
			Load1:  a.load1.result(),
			Load5:  a.load5.result(),
			Load15: a.load15.result(),
		}
		if a.cpu.n > 0 {
			r := a.cpu.result()
			b.CpuPct = &r
		}
		if a.mem.n > 0 {
			r := a.mem.result()
			b.MemUsedPct = &r
		}
		out = append(out, b)
	}
	return out
}

// HandleNodeHistory serves GET ?node=<id>&from=<RFC3339>&to=<RFC3339>&step=<duration>. Operator-gated
// (routed through the op() adapter, which applies the method guard + structural identity() check).
func (h *ControllerHandler) HandleNodeHistory(ctx context.Context, tenant controller.TenantID, _ string, _ http.ResponseWriter, r *http.Request) (any, *apierr.Error) {
	q := r.URL.Query()
	nodeID := q.Get("node")
	if nodeID == "" {
		return nil, apierr.New(apierr.CodeReqFieldRequired).With("field", "node")
	}
	from, err := time.Parse(time.RFC3339, q.Get("from"))
	if err != nil {
		return nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "from")
	}
	to, err := time.Parse(time.RFC3339, q.Get("to"))
	if err != nil {
		return nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "to")
	}
	if !to.After(from) || to.Sub(from) > maxHistoryRange {
		return nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "to")
	}
	// step is optional; Auto chooses the smallest granularity that honors both the normal heartbeat
	// cadence and response cap. Explicit steps retain the legacy 1s floor and widen only for the cap.
	var requestedStep time.Duration
	if raw := q.Get("step"); raw != "" {
		d, perr := time.ParseDuration(raw)
		if perr != nil || d <= 0 {
			return nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "step")
		}
		requestedStep = d
	}
	// Unknown node → 404 (nothing to chart).
	if _, err := h.store.GetNode(ctx, tenant, nodeID); err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			return nil, apierr.New(apierr.CodeNodeNotFound).Wrap(err)
		}
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}

	// History disabled (cap 0) → 200 with a flag + empty buckets (the panel shows a "history off" hint).
	cs, err := h.loadSettings(r)
	if err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	if cs.EffectiveHistoryCap() == 0 {
		step := effectiveHistoryStep(to.Sub(from), requestedStep, nil)
		return historyResponse{Step: step.String(), Disabled: true, Buckets: []historyBucket{}}, nil
	}

	samples, err := h.store.QueryTelemetryHistory(ctx, tenant, nodeID, from, to)
	if err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	step := effectiveHistoryStep(to.Sub(from), requestedStep, samples)
	buckets := aggregateHistory(samples, step)
	if buckets == nil {
		buckets = []historyBucket{}
	}
	return historyResponse{Step: step.String(), Buckets: buckets}, nil
}
