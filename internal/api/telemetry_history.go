package api

import (
	"context"
	"errors"
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
	// minHistoryStep floors the granularity (samples arrive ~every 30s, so sub-second steps are noise).
	minHistoryStep = time.Second
	// maxHistoryRange bounds the query window (defense-in-depth; the retained history is capped anyway).
	maxHistoryRange = 366 * 24 * time.Hour
)

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
	sum, min, max float64
	n             int
}

func (a *aggAcc) add(v float64) {
	if a.n == 0 || v < a.min {
		a.min = v
	}
	if a.n == 0 || v > a.max {
		a.max = v
	}
	a.sum += v
	a.n++
}

func (a aggAcc) result() metricAgg {
	return metricAgg{Avg: a.sum / float64(a.n), Min: a.min, Max: a.max}
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

// aggregateHistory buckets samples by `step` from `from`, computing avg/min/max per metric. Empty
// buckets are OMITTED (gaps stay gaps); cpu_pct / mem_used_pct are absent when no sample in the bucket
// carried them. PURE — table-tested.
func aggregateHistory(samples []controller.ResourceSample, from time.Time, step time.Duration) []historyBucket {
	if step <= 0 || len(samples) == 0 {
		return nil
	}
	type bucketAcc struct {
		load1, load5, load15 aggAcc
		cpu, mem             aggAcc
	}
	byIdx := map[int64]*bucketAcc{}
	var order []int64
	for _, s := range samples {
		idx := int64(s.TS.Sub(from) / step)
		a := byIdx[idx]
		if a == nil {
			a = &bucketAcc{}
			byIdx[idx] = a
			order = append(order, idx)
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
	for _, idx := range order {
		a := byIdx[idx]
		b := historyBucket{
			T:      from.Add(time.Duration(idx) * step),
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
	// step is optional; default to a sensible granularity that fits the window under the bucket cap.
	step := to.Sub(from) / maxHistoryBuckets
	if raw := q.Get("step"); raw != "" {
		d, perr := time.ParseDuration(raw)
		if perr != nil || d <= 0 {
			return nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "step")
		}
		step = d
	}
	if step < minHistoryStep {
		step = minHistoryStep
	}
	// Cap the bucket count: widen the step so the response never exceeds maxHistoryBuckets (echoed back).
	if int64(to.Sub(from)/step) > maxHistoryBuckets {
		step = to.Sub(from) / maxHistoryBuckets
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
		return historyResponse{Step: step.String(), Disabled: true, Buckets: []historyBucket{}}, nil
	}

	samples, err := h.store.QueryTelemetryHistory(ctx, tenant, nodeID, from, to)
	if err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	buckets := aggregateHistory(samples, from, step)
	if buckets == nil {
		buckets = []historyBucket{}
	}
	return historyResponse{Step: step.String(), Buckets: buckets}, nil
}
