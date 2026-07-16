package controller

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/probemetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

// telemetry_history.go is the controller's bounded, per-(tenant,node) resource and active-probe
// history — the durable backing for the node-detail charts. It is layered strictly ON TOP of the
// telemetry heartbeat: RecordTelemetry appends one bounded projection IN-MEMORY (NO disk IO — preserving
// the RecordTelemetry "a 30s heartbeat must never fsync" DoS invariant), and a SEPARATE background
// flusher (FileStore only) drains the buffer to append-only per-node JSONL off the heartbeat path. It
// owns its OWN mutex — never the store-wide mu nor telemetryMu — so history cannot stall a heartbeat.

// DefaultTelemetryHistoryCap is the per-node logical target for retained telemetry-history records
// when the operator has not configured one: 20160 ≈ 7 days at a 30-second heartbeat. The independent
// physical byte ceiling remains authoritative; 0 disables history.
const DefaultTelemetryHistoryCap = 20160

// historyCompactSlack: a durable JSONL is compacted to its last `cap` lines only once it grows past
// cap×slack lines, so the common flush is a pure append and the O(cap) rewrite is amortized.
const historyCompactSlack = 2

const (
	// MaxTelemetryHistoryFileBytes is the hard per-node physical JSONL ceiling. The historical
	// one-million-record settings guard was sized for small resource-only lines; active-probe attempts
	// make a record variable-width, so a record count alone no longer bounds disk use. The byte ceiling
	// deliberately wins over the configured record target. Rewrites aim below the ceiling, leaving
	// headroom for ordinary five-minute append batches instead of rewriting a nearly-full file on every
	// flush.
	MaxTelemetryHistoryFileBytes int64 = 128 << 20
	historyCompactTargetBytes    int64 = 96 << 20

	// FileStore's unflushed/retry tail is intentionally much smaller than its durable file: a failed
	// disk must not let large probe records consume the process heap up to the record-count cap. MemStore
	// is the complete backing store, so it receives the full per-node byte ceiling instead.
	maxFileTelemetryHistoryVolatileBytes int64 = 8 << 20
	maxMemTelemetryHistoryVolatileBytes  int64 = MaxTelemetryHistoryFileBytes

	// History records are produced from an admitted 64 KiB telemetry metrics map. One megabyte leaves
	// ample expansion room for the retained series identity while bounding corrupt local-custody lines.
	maxTelemetryHistoryLineBytes       = 1 << 20
	historyIOChunkBytes                = 64 << 10
	int64Max                     int64 = 1<<63 - 1
)

const (
	// maxProbeHistoryAttemptsPerRecord admits one full high-fidelity window plus the bounded rc.9
	// latest-result fallback. They normally overlap, but retaining both bounds the safe race where
	// latest state advances while the completed-attempt snapshot is being assembled.
	maxProbeHistoryAttemptsPerRecord = probemetric.MaxRecentSamples + probepolicy.MaxProbes
	// maxProbeHistoryRecentKeys bounds the volatile exact-attempt deduper. Retaining two high-fidelity
	// windows handles either ordering of overlapping snapshots without making the heartbeat path grow
	// per fleet lifetime. Query deduplication remains the restart-safe backstop after a restart.
	maxProbeHistoryRecentKeys = probemetric.MaxRecentSamples * 2
)

// historyFlushInterval is how often the background flusher drains the in-memory buffer to disk. Well
// above the 30s heartbeat, so a burst of beats collapses into one batched append.
const historyFlushInterval = 5 * time.Minute

// ResourceSample is one retained host-resource reading (a projection of the agent's metrics["resource"])
// stamped with its server-observed time. Exported so the query API (plan-3) and tests consume it. It
// carries NO endpoint/IP/key material — observability only.
type ResourceSample struct {
	TS         time.Time `json:"ts"`
	IntervalMS int64     `json:"interval_ms,omitempty"`
	CpuPct     *float64  `json:"cpu_pct,omitempty"`
	Load1      float64   `json:"load1"`
	Load5      float64   `json:"load5"`
	Load15     float64   `json:"load15"`
	MemTotalKB uint64    `json:"mem_total_kb,omitempty"`
	MemAvailKB uint64    `json:"mem_available_kb,omitempty"`
}

// ProbeHistorySample is one completed, typed active-probe attempt admitted to bounded history.
// SeriesID is derived from the exact executable identity (id/type/host/port or
// id/type/url/expected-status), so reusing a human id for a changed destination or URL success contract
// never splices two targets into one chart. CheckedAt has already been parsed and bounded against the
// enclosing telemetry sample before this value reaches the store. Actual URL status is intentionally
// omitted: it is categorical latest-result metadata, not retained chart data.
type ProbeHistorySample struct {
	SeriesID       string    `json:"series_id"`
	ID             string    `json:"id"`
	Type           string    `json:"type"`
	Host           string    `json:"host,omitempty"`
	Port           int       `json:"port,omitempty"`
	URL            string    `json:"url,omitempty"`
	ExpectedStatus int       `json:"expected_status,omitempty"`
	Status         string    `json:"status"`
	LatencyMS      *float64  `json:"latency_ms,omitempty"`
	CheckedAt      time.Time `json:"checked_at"`
	FailureReason  string    `json:"failure_reason,omitempty"`
	IntervalMS     int64     `json:"interval_ms,omitempty"`
}

// TelemetryHistorySnapshot is one coherent query over the shared resource/probe history. Both
// projections come from the same disk/inflight/buffer merge, avoiding duplicate JSONL scans and
// preventing one response from observing two different flush states.
type TelemetryHistorySnapshot struct {
	Resources []ResourceSample
	Probes    []ProbeHistorySample
}

// TelemetryHistoryQueryOptions controls additive store-level probe filtering. The zero value returns
// resource history with no probe attempts; a non-empty ProbeSeriesID admits only that exact executable
// series. The legacy unfiltered snapshot method remains available for omitted-filter compatibility.
type TelemetryHistoryQueryOptions struct {
	ProbeSeriesID string
}

type telemetryHistoryProbeFilter struct {
	all      bool
	seriesID string
}

// telemetryHistoryRecord is one accepted heartbeat's retained projection. Resource remains a pointer
// so a probe-only heartbeat cannot manufacture a zero load sample. The JSON encoder below preserves
// the historical flat ResourceSample line shape when Resource is present; probe-only lines omit `ts`,
// so an older controller safely ignores them as year-one data after a downgrade.
type telemetryHistoryRecord struct {
	Resource      *ResourceSample
	RecordedAt    time.Time
	ProbeAttempts []ProbeHistorySample
	// encodedBytes is the exact in-memory JSONL size (including '\n') used only for volatile retention
	// accounting. MarshalJSON deliberately omits it, and disk-decoded records need not populate it.
	encodedBytes int64
}

type telemetryHistoryRecordJSON struct {
	TS            *time.Time           `json:"ts,omitempty"`
	IntervalMS    int64                `json:"interval_ms,omitempty"`
	CpuPct        *float64             `json:"cpu_pct,omitempty"`
	Load1         float64              `json:"load1"`
	Load5         float64              `json:"load5"`
	Load15        float64              `json:"load15"`
	MemTotalKB    uint64               `json:"mem_total_kb,omitempty"`
	MemAvailKB    uint64               `json:"mem_available_kb,omitempty"`
	RecordedAt    *time.Time           `json:"recorded_at,omitempty"`
	ProbeAttempts []ProbeHistorySample `json:"probe_attempts,omitempty"`
}

func (r telemetryHistoryRecord) MarshalJSON() ([]byte, error) {
	w := telemetryHistoryRecordJSON{ProbeAttempts: r.ProbeAttempts}
	if r.Resource != nil {
		ts := r.Resource.TS
		w.TS = &ts
		w.IntervalMS = r.Resource.IntervalMS
		w.CpuPct = r.Resource.CpuPct
		w.Load1 = r.Resource.Load1
		w.Load5 = r.Resource.Load5
		w.Load15 = r.Resource.Load15
		w.MemTotalKB = r.Resource.MemTotalKB
		w.MemAvailKB = r.Resource.MemAvailKB
	} else if !r.RecordedAt.IsZero() {
		recordedAt := r.RecordedAt
		w.RecordedAt = &recordedAt
	}
	return json.Marshal(w)
}

func (r *telemetryHistoryRecord) UnmarshalJSON(data []byte) error {
	var w telemetryHistoryRecordJSON
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*r = telemetryHistoryRecord{}
	if w.TS != nil && !w.TS.IsZero() {
		r.Resource = &ResourceSample{
			TS: *w.TS, IntervalMS: w.IntervalMS, CpuPct: w.CpuPct,
			Load1: w.Load1, Load5: w.Load5, Load15: w.Load15,
			MemTotalKB: w.MemTotalKB, MemAvailKB: w.MemAvailKB,
		}
		r.RecordedAt = *w.TS
	} else if w.RecordedAt != nil {
		r.RecordedAt = *w.RecordedAt
	}
	for _, sample := range w.ProbeAttempts {
		if validStoredProbeHistorySample(sample) {
			r.ProbeAttempts = append(r.ProbeAttempts, sample)
		}
	}
	if len(r.ProbeAttempts) > 1 {
		r.ProbeAttempts = dedupeProbeHistorySamples(r.ProbeAttempts)
	}
	if over := len(r.ProbeAttempts) - maxProbeHistoryAttemptsPerRecord; over > 0 {
		r.ProbeAttempts = r.ProbeAttempts[over:]
	}
	return nil
}

func validStoredProbeHistorySample(sample ProbeHistorySample) bool {
	if sample.CheckedAt.IsZero() {
		return false
	}
	result := probemetric.Result{
		ID: sample.ID, Type: sample.Type, Host: sample.Host, Port: sample.Port,
		URL: sample.URL, ExpectedStatus: sample.ExpectedStatus,
		Status: sample.Status, LatencyMS: sample.LatencyMS,
		CheckedAt:     sample.CheckedAt.UTC().Format(time.RFC3339Nano),
		FailureReason: sample.FailureReason, IntervalMS: sample.IntervalMS,
	}
	return probemetric.ValidHistoryProjection(result) && sample.SeriesID == probemetric.SeriesID(result)
}

// resourceSampleFromMetrics projects metrics["resource"] into a ResourceSample. ok=false when the key is
// absent or malformed — a heartbeat without a usable resource metric simply adds no history sample
// (tolerant, never an error on the heartbeat path).
func resourceSampleFromMetrics(metrics map[string]json.RawMessage, at time.Time, interval time.Duration) (ResourceSample, bool) {
	raw, present := metrics[telemetrymetric.Resource.Key]
	if !present || len(raw) == 0 {
		return ResourceSample{}, false
	}
	return resourceSampleFromRaw(raw, at, interval)
}

func resourceSampleFromRaw(raw json.RawMessage, at time.Time, interval time.Duration) (ResourceSample, bool) {
	var w struct {
		CpuPct     *float64 `json:"cpu_pct"`
		Load1      float64  `json:"load1"`
		Load5      float64  `json:"load5"`
		Load15     float64  `json:"load15"`
		MemTotalKB uint64   `json:"mem_total_kb"`
		MemAvailKB uint64   `json:"mem_available_kb"`
	}
	if json.Unmarshal(raw, &w) != nil || !finiteTelemetryNumber(w.Load1) || !finiteTelemetryNumber(w.Load5) || !finiteTelemetryNumber(w.Load15) {
		return ResourceSample{}, false
	}
	if w.CpuPct != nil && !finiteTelemetryNumber(*w.CpuPct) {
		w.CpuPct = nil
	}
	intervalMS := interval.Milliseconds()
	if intervalMS < 0 {
		intervalMS = 0
	}
	return ResourceSample{
		TS: at, IntervalMS: intervalMS, CpuPct: w.CpuPct,
		Load1: w.Load1, Load5: w.Load5, Load15: w.Load15,
		MemTotalKB: w.MemTotalKB, MemAvailKB: w.MemAvailKB,
	}, true
}

type telemetryHistoryProjection struct {
	resource *ResourceSample
	probes   []ProbeHistorySample
}

type telemetryHistoryProjector func(json.RawMessage, time.Time, time.Duration) telemetryHistoryProjection

type telemetryHistoryProjectorRegistration struct {
	family  telemetrymetric.ChartFamily
	project telemetryHistoryProjector
}

type telemetryHistoryFamilyAccumulator func(*telemetryHistoryRecord, telemetryHistoryProjection)

// telemetryHistoryProjectors is the executable counterpart to telemetrymetric's disposition catalog.
// Each entry declares the family it produces; exact catalog/registry and valid-fixture tests prevent
// either an unregistered source or a source routed into the wrong retained/API family. Runtime order
// comes only from telemetrymetric.Charted, where probe_samples precedes the rc.9 fallback.
var telemetryHistoryProjectors = map[string]telemetryHistoryProjectorRegistration{
	telemetrymetric.Resource.Key: {
		family: telemetrymetric.ChartFamilyResource,
		project: func(raw json.RawMessage, at time.Time, interval time.Duration) telemetryHistoryProjection {
			resource, ok := resourceSampleFromRaw(raw, at, interval)
			if !ok {
				return telemetryHistoryProjection{}
			}
			return telemetryHistoryProjection{resource: &resource}
		},
	},
	telemetrymetric.ProbeSamples.Key: {
		family: telemetrymetric.ChartFamilyProbe,
		project: func(raw json.RawMessage, at time.Time, _ time.Duration) telemetryHistoryProjection {
			return telemetryHistoryProjection{probes: probeHistorySamplesFromRaw(raw, at, probemetric.MaxRecentSamples, false)}
		},
	},
	telemetrymetric.ProbeResults.Key: {
		family: telemetrymetric.ChartFamilyProbe,
		project: func(raw json.RawMessage, at time.Time, _ time.Duration) telemetryHistoryProjection {
			return telemetryHistoryProjection{probes: probeHistorySamplesFromRaw(raw, at, probepolicy.MaxProbes, true)}
		},
	},
}

var telemetryHistoryFamilyAccumulators = map[telemetrymetric.ChartFamily]telemetryHistoryFamilyAccumulator{
	telemetrymetric.ChartFamilyResource: func(record *telemetryHistoryRecord, projection telemetryHistoryProjection) {
		if projection.resource != nil {
			record.Resource = projection.resource
		}
	},
	telemetrymetric.ChartFamilyProbe: func(record *telemetryHistoryRecord, projection telemetryHistoryProjection) {
		record.ProbeAttempts = append(record.ProbeAttempts, projection.probes...)
	},
}

func validateTelemetryHistoryProjectorRegistry() error {
	if err := telemetrymetric.ValidateCatalog(telemetrymetric.All()); err != nil {
		return fmt.Errorf("telemetry catalog: %w", err)
	}
	charted := make(map[string]telemetrymetric.ChartFamily)
	for _, definition := range telemetrymetric.Charted() {
		charted[definition.Key] = definition.ChartFamily
		registration, ok := telemetryHistoryProjectors[definition.Key]
		if !ok || registration.project == nil {
			return fmt.Errorf("charted metric %q has no projector", definition.Key)
		}
		if registration.family != definition.ChartFamily {
			return fmt.Errorf("projector %q family %q does not match catalog family %q", definition.Key, registration.family, definition.ChartFamily)
		}
		if telemetryHistoryFamilyAccumulators[definition.ChartFamily] == nil {
			return fmt.Errorf("charted family %q has no history accumulator", definition.ChartFamily)
		}
	}
	for key := range telemetryHistoryProjectors {
		if _, ok := charted[key]; !ok {
			return fmt.Errorf("projector %q is not a charted catalog metric", key)
		}
	}
	if len(telemetryHistoryProjectors) != len(charted) {
		return fmt.Errorf("projector/catalog cardinality %d/%d", len(telemetryHistoryProjectors), len(charted))
	}
	families := make(map[telemetrymetric.ChartFamily]struct{})
	for _, family := range telemetrymetric.ChartFamilies() {
		families[family] = struct{}{}
	}
	for family, accumulator := range telemetryHistoryFamilyAccumulators {
		if accumulator == nil {
			return fmt.Errorf("history accumulator %q is nil", family)
		}
		if _, ok := families[family]; !ok {
			return fmt.Errorf("history accumulator %q is not a charted catalog family", family)
		}
	}
	if len(telemetryHistoryFamilyAccumulators) != len(families) {
		return fmt.Errorf("history accumulator/catalog family cardinality %d/%d", len(telemetryHistoryFamilyAccumulators), len(families))
	}
	return nil
}

func init() {
	if err := validateTelemetryHistoryProjectorRegistry(); err != nil {
		panic("controller: invalid telemetry history projector registry: " + err.Error())
	}
}

func telemetryHistoryRecordFromMetrics(metrics map[string]json.RawMessage, at time.Time, interval time.Duration) telemetryHistoryRecord {
	record := telemetryHistoryRecord{RecordedAt: at.UTC()}
	for _, definition := range telemetrymetric.Charted() {
		raw, ok := metrics[definition.Key]
		if !ok || len(raw) == 0 {
			continue
		}
		registration := telemetryHistoryProjectors[definition.Key] // validated at package initialization
		projection := registration.project(raw, at, interval)
		telemetryHistoryFamilyAccumulators[registration.family](&record, projection)
	}
	if len(record.ProbeAttempts) > 1 {
		record.ProbeAttempts = dedupeProbeHistorySamples(record.ProbeAttempts)
	}
	if over := len(record.ProbeAttempts) - maxProbeHistoryAttemptsPerRecord; over > 0 {
		record.ProbeAttempts = record.ProbeAttempts[over:]
	}
	return record
}

func probeHistorySamplesFromRaw(raw json.RawMessage, outerAt time.Time, max int, allowPending bool) []ProbeHistorySample {
	decoded := probemetric.DecodeArray(raw, max, allowPending)
	out := make([]ProbeHistorySample, 0, len(decoded))
	for _, result := range decoded {
		if !probemetric.Completed(result) {
			continue
		}
		checkedAt, err := time.Parse(time.RFC3339Nano, result.CheckedAt)
		if err != nil || checkedAt.IsZero() {
			continue
		}
		checkedAt = checkedAt.UTC()
		// The inner attempt clock has less authority than the already-normalized outer telemetry sample.
		// Drop an impossible timestamp instead of letting a compromised/skewed node escape the bounded
		// replay window or manufacture future chart buckets.
		if checkedAt.Before(outerAt.Add(-maxTelemetryReplayAge)) || checkedAt.After(outerAt.Add(maxTelemetryFutureSkew)) {
			continue
		}
		out = append(out, ProbeHistorySample{
			SeriesID: probemetric.SeriesID(result),
			ID:       result.ID, Type: result.Type, Host: result.Host, Port: result.Port,
			URL: result.URL, ExpectedStatus: result.ExpectedStatus,
			Status: result.Status, LatencyMS: result.LatencyMS, CheckedAt: checkedAt,
			FailureReason: result.FailureReason, IntervalMS: result.IntervalMS,
		})
	}
	return out
}

func finiteTelemetryNumber(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// nodeHist is one node's in-memory history state: the not-yet-flushed samples plus (durable mode) the
// known JSONL line count for amortized compaction (-1 until counted once from disk).
type nodeHist struct {
	buf      []telemetryHistoryRecord
	bufBytes int64
	// inflight is the batch drained from buf and currently being written. Query snapshots it
	// alongside buf so a read racing the drain/write window cannot temporarily lose samples.
	inflight      []telemetryHistoryRecord
	inflightBytes int64
	// inflightOnDisk closes the query race between an append completing and flushOnce clearing the
	// drained batch. Before the append it is false and queries include inflight; after the exact slice is
	// appended it is true and queries read that batch from disk instead, so logical-cap accounting never
	// spends two slots on the same observation.
	inflightOnDisk bool
	fileLines      int
	// recentProbeKeys is an in-memory exact-attempt deduper for overlapping probe_samples windows and
	// repeated rc.9 probe_results snapshots. It is bounded and deliberately not loaded from disk on the
	// heartbeat path; query-time dedupe covers the first repeated snapshot after controller restart.
	recentProbeKeys  map[probeHistoryIdentity]struct{}
	recentProbeOrder []probeHistoryIdentity
}

// telemetryHistory holds the per-(tenant,node) buffers. dir != "" (FileStore) flushes to JSONL under
// dir; dir == "" (MemStore) keeps the whole capped history in the buffer (dev parity; nothing durable).
//
// The per-node CAP is read from an IN-MEMORY cache (capByTenant) — never from disk — so append() on the
// heartbeat path never does IO to learn the cap. The store refreshes the cache from settings via
// setCap() on PutSettings (and seeds it on read); an unseen tenant uses defaultCap.
type telemetryHistory struct {
	mu    sync.Mutex
	nodes map[TenantID]map[string]*nodeHist
	dir   string

	// flushMu admits one drain/write pass at a time. Production has one flusher goroutine, but
	// serializing the primitive also keeps direct shutdown/test flushes from creating overlapping
	// in-flight batches whose failure requeue order would otherwise be ambiguous.
	flushMu sync.Mutex
	// writeBatch is an injected seam for deterministic drain/write visibility tests.
	writeBatch func(TenantID, string, []telemetryHistoryRecord, int) error

	capMu       sync.RWMutex
	capByTenant map[TenantID]int
	defaultCap  int
	// maxFileBytes is a testable copy of the production per-node physical ceiling. compactTargetBytes
	// is deliberately lower to provide append headroom after a byte-triggered rewrite.
	maxFileBytes       int64
	compactTargetBytes int64
	// capLoader reads a tenant's persisted cap from settings without mutating the history cache
	// (FileStore: side-effect-free settings read → EffectiveHistoryCap; nil for MemStore). It is called
	// ONLY from the flusher (off the heartbeat path) to SEED an unseen
	// tenant's cap on its first flush — so a tenant that persisted cap=0 (history disabled) is honored
	// across a controller restart (the in-memory cache starts empty; without this seed the flush would
	// use defaultCap>0 and write to disk data the operator disabled). append never calls it.
	capLoader func(TenantID) int

	stop    chan struct{}
	stopped chan struct{}

	// Cap changes are coalesced through a tenant set plus a one-slot wake channel. A fixed tenant
	// queue could silently drop the 65th concurrent change and leave an offline node's physical file
	// above its new cap until restart. The set is non-blocking for settings requests, deduplicates
	// repeated changes, and retains every distinct tenant until the maintenance goroutine drains it.
	maintenanceMu      sync.Mutex
	maintenancePending map[TenantID]struct{}
	maintenanceWake    chan struct{}
	maintenanceCancel  context.CancelFunc
}

func newTelemetryHistory(dir string, defaultCap int, capLoader func(TenantID) int) *telemetryHistory {
	h := &telemetryHistory{
		nodes:              map[TenantID]map[string]*nodeHist{},
		dir:                dir,
		capByTenant:        map[TenantID]int{},
		defaultCap:         defaultCap,
		capLoader:          capLoader,
		maxFileBytes:       MaxTelemetryHistoryFileBytes,
		compactTargetBytes: historyCompactTargetBytes,
	}
	h.writeBatch = h.writeHistoryJSONL
	return h
}

// ensureSeeded seeds a tenant's cap from persisted settings once, on its first flush (off the heartbeat
// path). No-op once seeded or when there is no loader (MemStore). This is what makes an operator's
// persisted cap=0 (disable) survive a controller restart.
func (h *telemetryHistory) ensureSeeded(t TenantID) {
	if h.capLoader == nil {
		return
	}
	h.capMu.RLock()
	_, ok := h.capByTenant[t]
	h.capMu.RUnlock()
	if ok {
		return
	}
	loaded := h.capLoader(t) // settings disk read — flusher only, never append
	// A settings write may have installed a newer cap while the disk read above was in flight. Seed
	// only if the tenant is still absent so that explicit GetSettings/PutSettings observations always
	// win over a stale startup read.
	h.capMu.Lock()
	if _, exists := h.capByTenant[t]; exists {
		h.capMu.Unlock()
		return
	}
	h.capByTenant[t] = loaded
	h.capMu.Unlock()
	h.applyCapChange(t, loaded)
}

// capFor returns the cached per-node record target for a tenant (defaultCap until the store seeds one).
// No disk IO — safe on the heartbeat append path.
func (h *telemetryHistory) capFor(t TenantID) int {
	h.capMu.RLock()
	defer h.capMu.RUnlock()
	if c, ok := h.capByTenant[t]; ok {
		return c
	}
	return h.defaultCap
}

// setCap updates a tenant's cached cap; the store calls it whenever settings are saved/read so the
// history tracks the operator's configured cap without reading settings on the append path.
func (h *telemetryHistory) setCap(t TenantID, cap int) {
	h.capMu.Lock()
	previous, existed := h.capByTenant[t]
	h.capByTenant[t] = cap
	h.capMu.Unlock()
	if existed && previous == cap {
		return
	}
	h.applyCapChange(t, cap)
}

func (h *telemetryHistory) applyCapChange(t TenantID, cap int) {
	h.mu.Lock()
	if byNode := h.nodes[t]; byNode != nil {
		for _, entry := range byNode {
			trimTelemetryHistoryBuffer(entry, cap, h.volatileByteLimit()-entry.inflightBytes)
		}
	}
	h.mu.Unlock()
	// Cap changes schedule off-heartbeat physical convergence without blocking the settings path.
	h.scheduleMaintenance(t)
}

func (h *telemetryHistory) scheduleMaintenance(t TenantID) {
	h.maintenanceMu.Lock()
	if h.maintenancePending == nil || h.maintenanceWake == nil {
		h.maintenanceMu.Unlock()
		return
	}
	h.maintenancePending[t] = struct{}{}
	wake := h.maintenanceWake
	h.maintenanceMu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (h *telemetryHistory) takePendingMaintenance() []TenantID {
	h.maintenanceMu.Lock()
	if len(h.maintenancePending) == 0 {
		h.maintenanceMu.Unlock()
		return nil
	}
	tenants := make([]TenantID, 0, len(h.maintenancePending))
	for tenant := range h.maintenancePending {
		tenants = append(tenants, tenant)
		delete(h.maintenancePending, tenant)
	}
	h.maintenanceMu.Unlock()
	sort.Slice(tenants, func(i, j int) bool { return tenants[i] < tenants[j] })
	return tenants
}

func (h *telemetryHistory) fileByteLimit() int64 {
	if h.maxFileBytes <= 0 {
		return MaxTelemetryHistoryFileBytes
	}
	return h.maxFileBytes
}

func (h *telemetryHistory) compactByteLimit() int64 {
	limit := h.fileByteLimit()
	target := h.compactTargetBytes
	if target <= 0 || target > limit {
		target = limit
	}
	return target
}

func (h *telemetryHistory) volatileByteLimit() int64 {
	if h.dir == "" {
		return maxMemTelemetryHistoryVolatileBytes
	}
	return maxFileTelemetryHistoryVolatileBytes
}

func (h *telemetryHistory) entryLocked(t TenantID, nodeID string) *nodeHist {
	byNode := h.nodes[t]
	if byNode == nil {
		byNode = map[string]*nodeHist{}
		h.nodes[t] = byNode
	}
	e := byNode[nodeID]
	if e == nil {
		e = &nodeHist{fileLines: -1}
		byNode[nodeID] = e
	}
	return e
}

// append records one sample IN-MEMORY (no disk IO — the DoS invariant). The buffer is capped at `cap` by
// front-eviction: in MemStore the buffer IS the history; in FileStore this is a safety bound if the
// flusher stalls (the durable file is the real history, capped at flush).
func (h *telemetryHistory) append(t TenantID, nodeID string, s ResourceSample) {
	h.appendRecord(t, nodeID, telemetryHistoryRecord{Resource: &s, RecordedAt: s.TS})
}

// appendMetrics projects all history-charted telemetry keys and appends one bounded record. Projection
// and JSON parsing are CPU/memory-only; appendRecord performs no disk or settings IO.
func (h *telemetryHistory) appendMetrics(t TenantID, nodeID string, metrics map[string]json.RawMessage, at time.Time, interval time.Duration) {
	h.appendRecord(t, nodeID, telemetryHistoryRecordFromMetrics(metrics, at, interval))
}

func (h *telemetryHistory) appendRecord(t TenantID, nodeID string, record telemetryHistoryRecord) {
	if record.Resource == nil && len(record.ProbeAttempts) == 0 {
		return
	}
	cap := h.capFor(t)
	if cap <= 0 {
		return // history disabled
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entryLocked(t, nodeID)
	record.ProbeAttempts = e.filterNewProbeAttempts(record.ProbeAttempts)
	if record.Resource == nil && len(record.ProbeAttempts) == 0 {
		return
	}
	encodedBytes, err := telemetryHistoryRecordEncodedBytes(record)
	if err != nil || encodedBytes > maxTelemetryHistoryLineBytes {
		// History is observability-only: a record that cannot fit the bounded JSONL contract becomes a
		// visible chart gap, never a heartbeat failure or unbounded volatile allocation.
		return
	}
	record.encodedBytes = encodedBytes
	e.buf = append(e.buf, record)
	e.bufBytes += encodedBytes
	byteLimit := h.volatileByteLimit() - e.inflightBytes
	trimTelemetryHistoryBuffer(e, cap, byteLimit)
}

func telemetryHistoryRecordEncodedBytes(record telemetryHistoryRecord) (int64, error) {
	data, err := json.Marshal(&record)
	if err != nil {
		return 0, err
	}
	return int64(len(data) + 1), nil // JSONL newline
}

func trimTelemetryHistoryBuffer(e *nodeHist, cap int, byteLimit int64) {
	if byteLimit < 0 {
		byteLimit = 0
	}
	drop := 0
	for drop < len(e.buf) && (len(e.buf)-drop > cap || e.bufBytes > byteLimit) {
		e.bufBytes -= e.buf[drop].encodedBytes
		drop++
	}
	if drop == 0 {
		return
	}
	clear(e.buf[:drop])
	e.buf = e.buf[drop:]
	if len(e.buf) == 0 {
		e.buf = nil
		e.bufBytes = 0
	}
}

func (e *nodeHist) filterNewProbeAttempts(samples []ProbeHistorySample) []ProbeHistorySample {
	if len(samples) == 0 {
		return nil
	}
	if e.recentProbeKeys == nil {
		e.recentProbeKeys = make(map[probeHistoryIdentity]struct{}, maxProbeHistoryRecentKeys)
	}
	out := make([]ProbeHistorySample, 0, len(samples))
	for _, sample := range samples {
		key := probeHistorySampleIdentity(sample)
		if _, duplicate := e.recentProbeKeys[key]; duplicate {
			continue
		}
		e.recentProbeKeys[key] = struct{}{}
		e.recentProbeOrder = append(e.recentProbeOrder, key)
		out = append(out, sample)
		if len(e.recentProbeOrder) > maxProbeHistoryRecentKeys {
			oldest := e.recentProbeOrder[0]
			delete(e.recentProbeKeys, oldest)
			copy(e.recentProbeOrder, e.recentProbeOrder[1:])
			e.recentProbeOrder = e.recentProbeOrder[:len(e.recentProbeOrder)-1]
		}
	}
	return out
}

// start launches the background flusher (FileStore only). MemStore (dir=="") keeps everything in memory,
// so there is nothing to flush.
func (h *telemetryHistory) start() {
	if h.dir == "" {
		return
	}
	h.stop = make(chan struct{})
	h.stopped = make(chan struct{})
	h.maintenanceMu.Lock()
	h.maintenancePending = make(map[TenantID]struct{})
	h.maintenanceWake = make(chan struct{}, 1)
	h.maintenanceMu.Unlock()
	maintenanceCtx, cancel := context.WithCancel(context.Background())
	h.maintenanceCancel = cancel
	go h.run(maintenanceCtx, historyFlushInterval)
}

func (h *telemetryHistory) run(maintenanceCtx context.Context, interval time.Duration) {
	defer close(h.stopped)
	if err := h.maintainExistingHistory(maintenanceCtx); err != nil && maintenanceCtx.Err() == nil {
		log.Printf("controller: telemetry history: startup maintenance: %v", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			h.flushOnce() // best-effort final drain on shutdown
			return
		case <-h.maintenanceWake:
			for _, tenant := range h.takePendingMaintenance() {
				if maintenanceCtx.Err() != nil {
					break
				}
				if err := h.maintainHistoryTenant(maintenanceCtx, tenant); err != nil && maintenanceCtx.Err() == nil {
					log.Printf("controller: telemetry history: maintain tenant %q: %v", tenant, err)
				}
			}
		case <-t.C:
			h.flushOnce()
		}
	}
}

// close stops the flusher and does a final drain. Safe to call once; no-op for MemStore.
func (h *telemetryHistory) close() {
	if h.dir == "" || h.stop == nil {
		return
	}
	if h.maintenanceCancel != nil {
		h.maintenanceCancel()
	}
	close(h.stop)
	<-h.stopped
	h.maintenanceMu.Lock()
	h.maintenancePending = nil
	h.maintenanceWake = nil
	h.maintenanceMu.Unlock()
}

// maintainExistingHistory performs a bounded, off-heartbeat startup pass so files written by an older
// release converge to the current physical byte ceiling even when their nodes remain offline.
func (h *telemetryHistory) maintainExistingHistory(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := revalidateSecureStoreDir(h.dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(h.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if _, err := sanitizeComponent("telemetry history tenant", entry.Name()); err != nil {
			continue
		}
		if err := h.maintainHistoryTenant(ctx, TenantID(entry.Name())); err != nil {
			log.Printf("controller: telemetry history: maintain tenant %q during startup: %v", entry.Name(), err)
		}
	}
	return nil
}

// maintainHistoryTenant converges every existing node file to the tenant's current logical cap and the
// independent physical byte target. It never runs on the heartbeat path: startup and cap changes only
// enqueue this work on the history goroutine.
func (h *telemetryHistory) maintainHistoryTenant(ctx context.Context, t TenantID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tc, err := sanitizeComponent("telemetry history tenant", string(t))
	if err != nil {
		return err
	}
	tenantDir := filepath.Join(h.dir, tc)
	if err := validateSecureStoreDirIfExists(tenantDir); err != nil {
		return err
	}
	entries, err := os.ReadDir(tenantDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	h.ensureSeeded(t)
	cap := h.capFor(t)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		p := filepath.Join(tenantDir, entry.Name())
		kept, changed, err := h.maintainHistoryFile(ctx, p, cap)
		if err != nil {
			log.Printf("controller: telemetry history: maintain %s: %v", p, err)
			continue
		}
		if changed {
			nodeID := strings.TrimSuffix(entry.Name(), ".jsonl")
			h.mu.Lock()
			h.entryLocked(t, nodeID).fileLines = kept
			h.mu.Unlock()
		}
	}
	return nil
}

func (h *telemetryHistory) maintainHistoryFile(ctx context.Context, p string, cap int) (kept int, changed bool, err error) {
	f, err := openTelemetryHistoryFile(p)
	if err != nil {
		return 0, false, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return 0, false, err
	}
	lines := 0
	if cap > 0 {
		_, _, lines, err = historyTailBounds(ctx, f, cap+1, int64Max)
	}
	if closeErr := f.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return 0, false, err
	}
	if info.Size() <= h.fileByteLimit() && cap > 0 && lines <= cap {
		return lines, false, nil
	}
	kept, err = compactJSONLContext(ctx, p, cap, h.compactByteLimit())
	if err != nil {
		return 0, false, err
	}
	return kept, true, nil
}

type flushJob struct {
	t       TenantID
	nodeID  string
	samples []telemetryHistoryRecord
}

// flushOnce drains each node's buffer UNDER the lock, then writes OUTSIDE the lock so an append never
// blocks on disk. A write failure re-queues the drained samples (retry next tick), never surfacing to
// the heartbeat.
func (h *telemetryHistory) flushOnce() {
	h.flushMu.Lock()
	defer h.flushMu.Unlock()

	h.mu.Lock()
	var jobs []flushJob
	for t, byNode := range h.nodes {
		for nodeID, e := range byNode {
			if len(e.buf) == 0 {
				continue
			}
			e.inflight = e.buf
			e.inflightBytes = e.bufBytes
			e.inflightOnDisk = false
			e.buf = nil
			e.bufBytes = 0
			jobs = append(jobs, flushJob{t: t, nodeID: nodeID, samples: e.inflight})
		}
	}
	h.mu.Unlock()

	for _, j := range jobs {
		h.ensureSeeded(j.t) // seed a restarted-controller's cap from settings before deciding to write
		cap := h.capFor(j.t)
		if cap <= 0 {
			h.clearInflight(j.t, j.nodeID)
			continue // history disabled (incl. persisted cap=0 across restart): drop, no disk write
		}
		if err := h.writeBatch(j.t, j.nodeID, j.samples, cap); err != nil {
			h.requeueInflight(j.t, j.nodeID, cap)
			continue
		}
		h.clearInflight(j.t, j.nodeID)
	}
}

func (h *telemetryHistory) clearInflight(t TenantID, nodeID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entryLocked(t, nodeID)
	e.inflight = nil
	e.inflightBytes = 0
	e.inflightOnDisk = false
}

// requeueInflight atomically moves the failed in-flight batch back to the FRONT of the buffer. The
// batch is older than samples appended while the write ran, so this preserves chronological order.
// flushMu ensures two failed writes cannot invert their batches while requeueing.
func (h *telemetryHistory) requeueInflight(t TenantID, nodeID string, cap int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entryLocked(t, nodeID)
	requeued := make([]telemetryHistoryRecord, 0, len(e.inflight)+len(e.buf))
	requeued = append(requeued, e.inflight...)
	requeued = append(requeued, e.buf...)
	requeuedBytes := e.inflightBytes + e.bufBytes
	e.inflight = nil
	e.inflightBytes = 0
	e.inflightOnDisk = false
	e.buf = requeued
	e.bufBytes = requeuedBytes
	trimTelemetryHistoryBuffer(e, cap, h.volatileByteLimit())
}

func (h *telemetryHistory) nodeFile(t TenantID, nodeID string) (string, error) {
	tc, err := sanitizeComponent("tenant", string(t))
	if err != nil {
		return "", err
	}
	nc, err := sanitizeComponent("node", nodeID)
	if err != nil {
		return "", err
	}
	if err := revalidateSecureStoreDir(h.dir); err != nil {
		return "", fmt.Errorf("controller: telemetry history custody: %w", err)
	}
	tenantDir := filepath.Join(h.dir, tc)
	if err := validateSecureStoreDirIfExists(tenantDir); err != nil {
		return "", err
	}
	return filepath.Join(tenantDir, nc+".jsonl"), nil
}

// writeJSONL retains the pre-probe focused-test seam and writes ordinary resource samples using the
// historical flat line shape. Production flushes call writeHistoryJSONL with the additive record type.
func (h *telemetryHistory) writeJSONL(t TenantID, nodeID string, samples []ResourceSample, cap int) error {
	records := make([]telemetryHistoryRecord, len(samples))
	for i := range samples {
		sample := samples[i]
		records[i] = telemetryHistoryRecord{Resource: &sample, RecordedAt: sample.TS}
	}
	return h.writeHistoryJSONL(t, nodeID, records, cap)
}

// writeHistoryJSONL appends the records to the node's JSONL using only one bounded encoded line at a
// time. A torn crash tail is truncated to the last complete newline before append. If the append would
// cross the physical byte ceiling, the old suffix and new batch are streamed into one same-directory
// atomic replacement instead, so the hard ceiling is never crossed. Once the new batch has been
// fsync'd or atomically published, later maintenance failures are logged rather than returned: a retry
// would duplicate a batch that is already committed.
func (h *telemetryHistory) writeHistoryJSONL(t TenantID, nodeID string, samples []telemetryHistoryRecord, cap int) error {
	tc, err := sanitizeComponent("tenant", string(t))
	if err != nil {
		return err
	}
	if _, err := ensureSecureStoreChild(h.dir, tc); err != nil {
		return fmt.Errorf("controller: telemetry history custody: %w", err)
	}
	p, err := h.nodeFile(t, nodeID)
	if err != nil {
		return err
	}
	selected, batchBytes, err := prepareTelemetryHistoryBatch(samples, cap, h.fileByteLimit())
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		return nil
	}
	if dropped := len(samples) - len(selected); dropped > 0 {
		log.Printf("controller: telemetry history: dropped %d oldest buffered records for %s to honor the physical byte ceiling", dropped, p)
	}

	f, err := openTelemetryHistoryAppendFile(p)
	if err != nil {
		return err
	}
	completeEnd, torn, err := historyCompleteEnd(context.Background(), f)
	if err != nil {
		_ = f.Close()
		return err
	}
	if torn {
		if err := f.Truncate(completeEnd); err != nil {
			_ = f.Close()
			return fmt.Errorf("controller: truncate torn telemetry history tail in %s: %w", p, err)
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return fmt.Errorf("controller: sync repaired telemetry history %s: %w", p, err)
		}
	}

	// Overflow uses one atomic streamed replacement containing the retained old suffix plus the new
	// batch. This avoids both a transient over-ceiling file and the data-loss window of compacting the
	// old file before an append that could subsequently fail.
	if batchBytes > h.fileByteLimit()-completeEnd {
		if err := f.Close(); err != nil {
			return fmt.Errorf("controller: close telemetry history %s before bounded rewrite: %w", p, err)
		}
		kept, rewriteErr := rewriteHistoryWithBatchContext(context.Background(), p, selected, cap, h.compactByteLimit(), batchBytes)
		if rewriteErr != nil {
			return rewriteErr
		}
		h.markInflightOnDisk(t, nodeID, samples)
		h.mu.Lock()
		h.entryLocked(t, nodeID).fileLines = kept
		h.mu.Unlock()
		return nil
	}

	appendStart := completeEnd
	for i := range selected {
		if err := writeTelemetryHistoryRecord(f, selected[i]); err != nil {
			rollbackTelemetryHistoryAppend(f, appendStart, p)
			_ = f.Close()
			return err
		}
	}
	if err := f.Sync(); err != nil {
		rollbackTelemetryHistoryAppend(f, appendStart, p)
		_ = f.Close()
		return fmt.Errorf("controller: sync telemetry history append %s: %w", p, err)
	}
	// Sync is the commit point. A later close error cannot safely become a flush retry because the exact
	// batch is already durable; report it diagnostically and continue bookkeeping.
	if cerr := f.Close(); cerr != nil {
		log.Printf("controller: telemetry history: close committed append to %s: %v", p, cerr)
	}
	h.markInflightOnDisk(t, nodeID, samples)

	threshold := cap * historyCompactSlack
	h.mu.Lock()
	e := h.entryLocked(t, nodeID)
	knownLines := e.fileLines
	if knownLines >= 0 {
		e.fileLines += len(selected)
		knownLines = e.fileLines
	}
	h.mu.Unlock()

	// On the first flush after a restart, count only far enough backwards to decide whether the
	// threshold was crossed. This work is outside history.mu, so a large pre-existing file never stalls
	// the heartbeat append path while the flusher seeds its bookkeeping.
	lineCountFailed := false
	if knownLines < 0 {
		count, countErr := countHistoryLinesBounded(context.Background(), p, threshold+1)
		if countErr != nil {
			lineCountFailed = true
			log.Printf("controller: telemetry history: count lines in %s after append: %v (forcing maintenance)", p, countErr)
		} else {
			knownLines = count
			h.mu.Lock()
			h.entryLocked(t, nodeID).fileLines = count
			h.mu.Unlock()
		}
	}

	fileSize, sizeErr := historyFileSize(p)
	if sizeErr != nil {
		// The append is already present. Force maintenance and report the diagnostic without converting a
		// successful append into a retry that would duplicate the batch.
		log.Printf("controller: telemetry history: inspect %s after append: %v (forcing maintenance)", p, sizeErr)
	}
	overLines := lineCountFailed || knownLines > threshold
	overBytes := sizeErr != nil || fileSize > h.fileByteLimit()
	if overLines || overBytes {
		kept, compactErr := compactJSONLContext(context.Background(), p, cap, h.compactByteLimit())
		if compactErr != nil {
			log.Printf("controller: telemetry history: compact %s: %v (append retained; will retry on a later flush)", p, compactErr)
		} else {
			h.mu.Lock()
			h.entryLocked(t, nodeID).fileLines = kept
			h.mu.Unlock()
		}
	}
	return nil
}

func prepareTelemetryHistoryBatch(samples []telemetryHistoryRecord, cap int, maxBytes int64) ([]telemetryHistoryRecord, int64, error) {
	if cap <= 0 || len(samples) == 0 {
		return nil, 0, nil
	}
	start := 0
	if len(samples) > cap {
		start = len(samples) - cap
	}
	sizes := make([]int64, len(samples)-start)
	var total int64
	for i := start; i < len(samples); i++ {
		size, err := telemetryHistoryRecordEncodedBytes(samples[i])
		if err != nil {
			return nil, 0, err
		}
		if size > maxTelemetryHistoryLineBytes {
			return nil, 0, fmt.Errorf("controller: telemetry history record is %d bytes (limit %d)", size, maxTelemetryHistoryLineBytes)
		}
		sizes[i-start] = size
		total += size
	}
	for start < len(samples) && total > maxBytes {
		total -= sizes[start-(len(samples)-len(sizes))]
		start++
	}
	if start == len(samples) {
		return nil, 0, fmt.Errorf("controller: newest telemetry history record exceeds per-node byte ceiling %d", maxBytes)
	}
	return samples[start:], total, nil
}

func writeTelemetryHistoryRecord(dst io.Writer, record telemetryHistoryRecord) error {
	line, err := json.Marshal(&record)
	if err != nil {
		return err
	}
	if len(line)+1 > maxTelemetryHistoryLineBytes {
		return fmt.Errorf("controller: telemetry history record is %d bytes (limit %d)", len(line)+1, maxTelemetryHistoryLineBytes)
	}
	line = append(line, '\n')
	for len(line) > 0 {
		n, err := dst.Write(line)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		line = line[n:]
	}
	return nil
}

func rollbackTelemetryHistoryAppend(f *os.File, appendStart int64, p string) {
	if err := f.Truncate(appendStart); err != nil {
		log.Printf("controller: telemetry history: rollback failed append in %s: %v", p, err)
		return
	}
	if err := f.Sync(); err != nil {
		log.Printf("controller: telemetry history: sync rollback in %s: %v", p, err)
	}
}

func openTelemetryHistoryAppendFile(p string) (*os.File, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if err := revalidateSecureStoreDir(filepath.Dir(p)); err != nil {
			return nil, fmt.Errorf("controller: unsafe telemetry history parent for %s: %w", filepath.Base(p), err)
		}
		pathInfo, err := os.Lstat(p)
		if os.IsNotExist(err) {
			f, createErr := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_RDWR|os.O_APPEND, 0600)
			if os.IsExist(createErr) {
				continue
			}
			if createErr != nil {
				return nil, createErr
			}
			if err := validateOpenedStoreTemp(p, f); err != nil {
				_ = f.Close()
				_ = os.Remove(p)
				return nil, err
			}
			return f, nil
		}
		if err != nil {
			return nil, err
		}
		if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
			return nil, fmt.Errorf("controller: telemetry history path %s must be a regular file, not a symlink or special file", p)
		}
		if err := validateStoreFilePlatform(pathInfo); err != nil {
			return nil, fmt.Errorf("controller: telemetry history file %s is unsafe: %w", p, err)
		}
		f, err := os.OpenFile(p, os.O_RDWR|os.O_APPEND, 0600)
		if err != nil {
			return nil, err
		}
		openedInfo, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		if !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
			_ = f.Close()
			continue
		}
		if err := validateStoreFilePlatform(openedInfo); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("controller: opened telemetry history file %s is unsafe: %w", p, err)
		}
		return f, nil
	}
	return nil, fmt.Errorf("controller: telemetry history path %s changed repeatedly while opening", p)
}

// historyCompleteEnd returns the byte boundary immediately after the final complete JSONL newline.
// A file not ending in '\n' is an interrupted append under this writer's contract; the caller repairs
// it by truncating to this boundary before adding another record.
func historyCompleteEnd(ctx context.Context, f *os.File) (end int64, torn bool, err error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	info, err := f.Stat()
	if err != nil {
		return 0, false, err
	}
	size := info.Size()
	if size == 0 {
		return 0, false, nil
	}
	var last [1]byte
	if _, err := f.ReadAt(last[:], size-1); err != nil {
		return 0, false, err
	}
	if last[0] == '\n' {
		return size, false, nil
	}
	buf := make([]byte, historyIOChunkBytes)
	for cursor := size; cursor > 0; {
		if err := ctx.Err(); err != nil {
			return 0, false, err
		}
		blockStart := cursor - int64(len(buf))
		if blockStart < 0 {
			blockStart = 0
		}
		want := int(cursor - blockStart)
		n, readErr := f.ReadAt(buf[:want], blockStart)
		if readErr != nil && readErr != io.EOF {
			return 0, false, readErr
		}
		for i := n - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				return blockStart + int64(i) + 1, true, nil
			}
		}
		cursor = blockStart
	}
	return 0, true, nil
}

func rewriteHistoryWithBatchContext(ctx context.Context, p string, samples []telemetryHistoryRecord, cap int, targetBytes, batchBytes int64) (int, error) {
	source, err := openTelemetryHistoryFile(p)
	if err != nil {
		return 0, err
	}
	sourceOpen := true
	defer func() {
		if sourceOpen {
			_ = source.Close()
		}
	}()
	if targetBytes < batchBytes {
		targetBytes = batchBytes
	}
	oldLineLimit := cap - len(samples)
	if oldLineLimit < 0 {
		oldLineLimit = 0
	}
	oldByteLimit := targetBytes - batchBytes
	start, end, oldKept, err := historyTailBounds(ctx, source, oldLineLimit, oldByteLimit)
	if err != nil {
		return 0, err
	}
	dir := filepath.Dir(p)
	if err := revalidateSecureStoreDir(dir); err != nil {
		return 0, fmt.Errorf("controller: unsafe parent for %s: %w", filepath.Base(p), err)
	}
	tmpFile, err := os.CreateTemp(dir, "."+filepath.Base(p)+".tmp-*")
	if err != nil {
		return 0, fmt.Errorf("controller: rewrite %s: %w", filepath.Base(p), err)
	}
	tmp := tmpFile.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmp)
		}
	}()
	if err := tmpFile.Chmod(0600); err != nil {
		_ = tmpFile.Close()
		return 0, fmt.Errorf("controller: protect %s rewrite file: %w", filepath.Base(p), err)
	}
	if err := validateOpenedStoreTemp(tmp, tmpFile); err != nil {
		_ = tmpFile.Close()
		return 0, err
	}
	if end > start {
		if err := copyHistoryContext(ctx, tmpFile, io.NewSectionReader(source, start, end-start)); err != nil {
			_ = tmpFile.Close()
			return 0, fmt.Errorf("controller: stream retained history for %s: %w", filepath.Base(p), err)
		}
	}
	for i := range samples {
		if err := ctx.Err(); err != nil {
			_ = tmpFile.Close()
			return 0, err
		}
		if err := writeTelemetryHistoryRecord(tmpFile, samples[i]); err != nil {
			_ = tmpFile.Close()
			return 0, err
		}
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return 0, fmt.Errorf("controller: sync rewritten %s: %w", filepath.Base(p), err)
	}
	if err := tmpFile.Close(); err != nil {
		return 0, fmt.Errorf("controller: close rewritten %s: %w", filepath.Base(p), err)
	}
	if err := source.Close(); err != nil {
		return 0, fmt.Errorf("controller: close source %s after rewrite: %w", filepath.Base(p), err)
	}
	sourceOpen = false
	if err := revalidateSecureStoreDir(dir); err != nil {
		return 0, fmt.Errorf("controller: unsafe parent for %s: %w", filepath.Base(p), err)
	}
	if err := replaceStoreFileAtomic(tmp, p); err != nil {
		return 0, fmt.Errorf("controller: install rewritten %s: %w", filepath.Base(p), err)
	}
	removeTemp = false
	if err := syncStoreDirectory(dir); err != nil {
		// The exact replacement is already visible and its bytes were fsync'd. Retrying the batch in the
		// current process would duplicate it, so report the directory durability problem without turning
		// this committed append into a flush failure.
		log.Printf("controller: telemetry history: sync directory for committed rewrite %s: %v", filepath.Base(p), err)
	}
	return oldKept + len(samples), nil
}

func (h *telemetryHistory) markInflightOnDisk(t TenantID, nodeID string, samples []telemetryHistoryRecord) {
	if len(samples) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entryLocked(t, nodeID)
	// flushOnce passes the exact drained slice through writeBatch. Pointer identity prevents a direct
	// helper/test write from accidentally marking an unrelated in-flight batch durable.
	if len(e.inflight) > 0 && &e.inflight[0] == &samples[0] {
		e.inflightOnDisk = true
	}
}

func historyFileSize(p string) (int64, error) {
	f, err := openTelemetryHistoryFile(p)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// query returns the historical resource projection without changing the Store contract that predates
// probe history. Probe-only records are never interpreted as zero load.
func (h *telemetryHistory) query(t TenantID, nodeID string, from, to time.Time) ([]ResourceSample, error) {
	return h.queryContext(context.Background(), t, nodeID, from, to)
}

func (h *telemetryHistory) queryContext(ctx context.Context, t TenantID, nodeID string, from, to time.Time) ([]ResourceSample, error) {
	snapshot, err := h.querySnapshotWithFilterContext(ctx, t, nodeID, from, to, telemetryHistoryProbeFilter{})
	if err != nil {
		return nil, err
	}
	return snapshot.Resources, nil
}

func (h *telemetryHistory) queryProbes(t TenantID, nodeID string, from, to time.Time) ([]ProbeHistorySample, error) {
	return h.queryProbesContext(context.Background(), t, nodeID, from, to)
}

func (h *telemetryHistory) queryProbesContext(ctx context.Context, t TenantID, nodeID string, from, to time.Time) ([]ProbeHistorySample, error) {
	snapshot, err := h.querySnapshotWithFilterContext(ctx, t, nodeID, from, to, telemetryHistoryProbeFilter{all: true})
	if err != nil {
		return nil, err
	}
	return snapshot.Probes, nil
}

func (h *telemetryHistory) querySnapshot(t TenantID, nodeID string, from, to time.Time) (TelemetryHistorySnapshot, error) {
	return h.querySnapshotContext(context.Background(), t, nodeID, from, to)
}

func (h *telemetryHistory) querySnapshotContext(ctx context.Context, t TenantID, nodeID string, from, to time.Time) (TelemetryHistorySnapshot, error) {
	return h.querySnapshotWithFilterContext(ctx, t, nodeID, from, to, telemetryHistoryProbeFilter{all: true})
}

func (h *telemetryHistory) querySnapshotFilteredContext(ctx context.Context, t TenantID, nodeID string, from, to time.Time, options TelemetryHistoryQueryOptions) (TelemetryHistorySnapshot, error) {
	return h.querySnapshotWithFilterContext(ctx, t, nodeID, from, to, telemetryHistoryProbeFilter{seriesID: options.ProbeSeriesID})
}

func (h *telemetryHistory) querySnapshotWithFilterContext(ctx context.Context, t TenantID, nodeID string, from, to time.Time, probeFilter telemetryHistoryProbeFilter) (TelemetryHistorySnapshot, error) {
	records, err := h.queryRecordsWithFilterContext(ctx, t, nodeID, from, to, probeFilter)
	if err != nil {
		return TelemetryHistorySnapshot{}, err
	}
	resources := make([]ResourceSample, 0, len(records))
	var probes []ProbeHistorySample
	for i, record := range records {
		if i%128 == 0 {
			if err := ctx.Err(); err != nil {
				return TelemetryHistorySnapshot{}, err
			}
		}
		if record.Resource != nil && inWindow(record.Resource.TS, from, to) {
			resources = append(resources, cloneResourceSample(*record.Resource))
		}
		for _, sample := range record.ProbeAttempts {
			if inWindow(sample.CheckedAt, from, to) {
				probes = append(probes, cloneProbeHistorySample(sample))
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return TelemetryHistorySnapshot{}, err
	}
	return TelemetryHistorySnapshot{
		Resources: sortAndDedupeResourceSamples(resources),
		Probes:    dedupeProbeHistorySamples(probes),
	}, nil
}

func cloneResourceSample(sample ResourceSample) ResourceSample {
	if sample.CpuPct != nil {
		cpu := *sample.CpuPct
		sample.CpuPct = &cpu
	}
	return sample
}

func cloneProbeHistorySample(sample ProbeHistorySample) ProbeHistorySample {
	if sample.LatencyMS != nil {
		latency := *sample.LatencyMS
		sample.LatencyMS = &latency
	}
	return sample
}

// queryRecords merges the durable JSONL with both the in-flight flush batch and the ordinary in-memory
// buffer. It snapshots volatile records BEFORE reading disk: whether a concurrent flush drains, writes,
// or completes afterwards, each resource/probe observation remains visible on at least one side. The
// typed query functions perform exact deduplication after this merge. Returns nil when disabled.
func (h *telemetryHistory) queryRecords(t TenantID, nodeID string, from, to time.Time) ([]telemetryHistoryRecord, error) {
	return h.queryRecordsWithFilterContext(context.Background(), t, nodeID, from, to, telemetryHistoryProbeFilter{all: true})
}

func (h *telemetryHistory) queryRecordsContext(ctx context.Context, t TenantID, nodeID string, from, to time.Time) ([]telemetryHistoryRecord, error) {
	return h.queryRecordsWithFilterContext(ctx, t, nodeID, from, to, telemetryHistoryProbeFilter{all: true})
}

func (h *telemetryHistory) queryRecordsWithFilterContext(ctx context.Context, t TenantID, nodeID string, from, to time.Time, probeFilter telemetryHistoryProbeFilter) ([]telemetryHistoryRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cap := h.capFor(t)
	if cap <= 0 {
		return nil, nil
	}

	// Snapshot only the newest logical volatile suffix. inflight is included until its exact slice has
	// reached disk; afterwards inflightOnDisk makes disk the sole source during flush completion.
	var volatileRaw []telemetryHistoryRecord
	h.mu.Lock()
	if byNode := h.nodes[t]; byNode != nil {
		if e := byNode[nodeID]; e != nil {
			includeInflight := !e.inflightOnDisk
			volatileCount := len(e.buf)
			if includeInflight {
				volatileCount += len(e.inflight)
			}
			keep := minInt(cap, volatileCount)
			volatileRaw = make([]telemetryHistoryRecord, 0, keep)
			skip := volatileCount - keep
			if includeInflight {
				if skip < len(e.inflight) {
					volatileRaw = append(volatileRaw, e.inflight[skip:]...)
					skip = 0
				} else {
					skip -= len(e.inflight)
				}
			}
			if skip < len(e.buf) {
				volatileRaw = append(volatileRaw, e.buf[skip:]...)
			}
		}
	}
	h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	volatile := make([]telemetryHistoryRecord, 0, len(volatileRaw))
	for i, record := range volatileRaw {
		if i%128 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		record = filterTelemetryHistoryRecordProbes(record, probeFilter)
		if historyRecordInWindow(record, from, to) {
			volatile = append(volatile, record)
		}
	}

	var out []telemetryHistoryRecord
	// Volatile records are newer than every durable record, so they consume logical-cap slots even when
	// they fall outside the requested time window. Reading only the remaining durable suffix enforces a
	// cap reduction immediately without parsing or materializing older slack records.
	diskLimit := cap - len(volatileRaw)
	if h.dir != "" && diskLimit > 0 {
		p, err := h.nodeFile(t, nodeID)
		if err != nil {
			return nil, err
		}
		disk, err := readHistoryJSONLTail(ctx, p, from, to, diskLimit, h.fileByteLimit(), probeFilter)
		if err != nil {
			return nil, err
		}
		out = disk
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append(out, volatile...), nil
}

func filterTelemetryHistoryRecordProbes(record telemetryHistoryRecord, filter telemetryHistoryProbeFilter) telemetryHistoryRecord {
	if filter.all {
		return record
	}
	if filter.seriesID == "" || len(record.ProbeAttempts) == 0 {
		record.ProbeAttempts = nil
		return record
	}
	filtered := make([]ProbeHistorySample, 0, 1)
	for _, sample := range record.ProbeAttempts {
		if sample.SeriesID == filter.seriesID {
			filtered = append(filtered, sample)
		}
	}
	record.ProbeAttempts = filtered
	return record
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func historyRecordInWindow(record telemetryHistoryRecord, from, to time.Time) bool {
	if record.Resource != nil && inWindow(record.Resource.TS, from, to) {
		return true
	}
	for _, sample := range record.ProbeAttempts {
		if inWindow(sample.CheckedAt, from, to) {
			return true
		}
	}
	return false
}

type resourceSampleValueKey struct {
	intervalMS int64
	cpuPresent bool
	cpuBits    uint64
	load1Bits  uint64
	load5Bits  uint64
	load15Bits uint64
	memTotalKB uint64
	memAvailKB uint64
}

func resourceSampleValue(s ResourceSample) resourceSampleValueKey {
	key := resourceSampleValueKey{
		intervalMS: s.IntervalMS,
		load1Bits:  math.Float64bits(s.Load1),
		load5Bits:  math.Float64bits(s.Load5),
		load15Bits: math.Float64bits(s.Load15),
		memTotalKB: s.MemTotalKB,
		memAvailKB: s.MemAvailKB,
	}
	if s.CpuPct != nil {
		key.cpuPresent = true
		key.cpuBits = math.Float64bits(*s.CpuPct)
	}
	return key
}

// sortAndDedupeResourceSamples orders samples chronologically and removes only exact duplicates at
// the same instant. That is the shape produced when a query sees both disk and the still-in-flight
// batch, or when a partial append is retried. Distinct observations at the same timestamp survive.
func sortAndDedupeResourceSamples(samples []ResourceSample) []ResourceSample {
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].TS.Before(samples[j].TS) })
	out := samples[:0]
	seenAtTimestamp := make(map[resourceSampleValueKey]struct{})
	var timestamp time.Time
	haveTimestamp := false
	for _, sample := range samples {
		if !haveTimestamp || !sample.TS.Equal(timestamp) {
			clear(seenAtTimestamp)
			timestamp = sample.TS
			haveTimestamp = true
		}
		key := resourceSampleValue(sample)
		if _, duplicate := seenAtTimestamp[key]; duplicate {
			continue
		}
		seenAtTimestamp[key] = struct{}{}
		out = append(out, sample)
	}
	return out
}

type probeHistoryIdentity struct {
	id             string
	typeName       string
	host           string
	port           int
	url            string
	expectedStatus int
	checkedAt      int64
}

func probeHistorySampleIdentity(sample ProbeHistorySample) probeHistoryIdentity {
	return probeHistoryIdentity{
		id: sample.ID, typeName: sample.Type, host: sample.Host, port: sample.Port,
		url: sample.URL, expectedStatus: sample.ExpectedStatus,
		checkedAt: sample.CheckedAt.UnixNano(),
	}
}

// dedupeProbeHistorySamples applies the exact identity contract across overlapping high-fidelity
// windows, the rc.9 latest-result fallback, disk/inflight overlap, and the first post-restart repeat.
// Input order determines the retained outcome; the production projector visits probe_samples first so
// its optional cadence enriches the fallback. A later exact duplicate may only fill a missing cadence.
func dedupeProbeHistorySamples(samples []ProbeHistorySample) []ProbeHistorySample {
	if len(samples) == 0 {
		return nil
	}
	sort.SliceStable(samples, func(i, j int) bool {
		if samples[i].CheckedAt.Equal(samples[j].CheckedAt) {
			return samples[i].SeriesID < samples[j].SeriesID
		}
		return samples[i].CheckedAt.Before(samples[j].CheckedAt)
	})
	out := make([]ProbeHistorySample, 0, len(samples))
	seen := make(map[probeHistoryIdentity]int, len(samples))
	for _, sample := range samples {
		key := probeHistorySampleIdentity(sample)
		if index, duplicate := seen[key]; duplicate {
			if out[index].IntervalMS == 0 && sample.IntervalMS > 0 {
				out[index].IntervalMS = sample.IntervalMS
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, sample)
	}
	return out
}

func inWindow(ts, from, to time.Time) bool {
	return !ts.Before(from) && !ts.After(to)
}

// readHistoryJSONLTail reads only the newest bounded append-order suffix, then filters that suffix by
// the requested time window. A missing file is empty; corrupt lines are skipped (best-effort
// observability). Both the record cap and physical byte budget are applied before JSON decoding, so a
// wide/slack file cannot turn a small query into a full-file parse.
func readHistoryJSONLTail(ctx context.Context, p string, from, to time.Time, maxRecords int, maxBytes int64, probeFilter telemetryHistoryProbeFilter) ([]telemetryHistoryRecord, error) {
	f, err := openTelemetryHistoryFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	start, end, _, err := historyTailBounds(ctx, f, maxRecords, maxBytes)
	if err != nil {
		return nil, err
	}
	if start >= end {
		return nil, nil
	}
	var out []telemetryHistoryRecord
	sc := bufio.NewScanner(io.NewSectionReader(f, start, end-start))
	sc.Buffer(make([]byte, 0, historyIOChunkBytes), maxTelemetryHistoryLineBytes)
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var record telemetryHistoryRecord
		if json.Unmarshal(sc.Bytes(), &record) != nil {
			continue
		}
		record = filterTelemetryHistoryRecordProbes(record, probeFilter)
		if historyRecordInWindow(record, from, to) {
			out = append(out, record)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func openTelemetryHistoryFile(p string) (*os.File, error) {
	pathInfo, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("controller: telemetry history path %s must be a regular file, not a symlink or special file", p)
	}
	if err := validateStoreFilePlatform(pathInfo); err != nil {
		return nil, fmt.Errorf("controller: telemetry history file %s is unsafe: %w", p, err)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	openedInfo, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		_ = f.Close()
		return nil, fmt.Errorf("controller: telemetry history path %s changed while opening", p)
	}
	if err := validateStoreFilePlatform(openedInfo); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("controller: opened telemetry history file %s is unsafe: %w", p, err)
	}
	return f, nil
}

// historyTailBounds finds the newest suffix containing at most maxLines and maxBytes. It scans fixed
// chunks backwards and stores no line bodies or offset table, so memory is O(chunk) regardless of file
// or configured record cap. start/end delimit complete JSONL lines in the source file.
func historyTailBounds(ctx context.Context, f *os.File, maxLines int, maxBytes int64) (start, end int64, lines int, err error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		return 0, 0, 0, err
	}
	end = info.Size()
	start = end
	if end == 0 || maxLines <= 0 || maxBytes <= 0 {
		return start, end, 0, nil
	}

	// Ignore the writer's ordinary terminal newline while discovering the final line start. Interior
	// blank/corrupt lines still consume physical retention slots, matching append/compaction semantics.
	scanEnd := end
	var last [1]byte
	if _, err := f.ReadAt(last[:], end-1); err != nil {
		return 0, 0, 0, err
	}
	if last[0] == '\n' {
		scanEnd--
	}
	if scanEnd == 0 {
		return end, end, 0, nil
	}

	buf := make([]byte, historyIOChunkBytes)
	cursor := scanEnd
	lineEnd := scanEnd
	admit := func(lineStart int64) bool {
		if lines >= maxLines || end-lineStart > maxBytes {
			return false
		}
		start = lineStart
		lines++
		return true
	}
	for cursor > 0 {
		if err := ctx.Err(); err != nil {
			return 0, 0, 0, err
		}
		blockStart := cursor - int64(len(buf))
		if blockStart < 0 {
			blockStart = 0
		}
		want := int(cursor - blockStart)
		n, readErr := f.ReadAt(buf[:want], blockStart)
		if readErr != nil && readErr != io.EOF {
			return 0, 0, 0, readErr
		}
		for i := n - 1; i >= 0; i-- {
			if buf[i] != '\n' {
				continue
			}
			lineStart := blockStart + int64(i) + 1
			if lineStart > lineEnd {
				continue
			}
			if !admit(lineStart) {
				return start, end, lines, nil
			}
			lineEnd = blockStart + int64(i)
		}
		cursor = blockStart
	}
	// The first line has no preceding newline delimiter.
	if !admit(0) {
		return start, end, lines, nil
	}
	return start, end, lines, nil
}

func countHistoryLinesBounded(ctx context.Context, p string, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	f, err := openTelemetryHistoryFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()
	const maxInt64 = int64(^uint64(0) >> 1)
	_, _, lines, err := historyTailBounds(ctx, f, limit, maxInt64)
	return lines, err
}

func countLines(p string) int {
	f, err := os.Open(p)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, historyIOChunkBytes), maxTelemetryHistoryLineBytes)
	for sc.Scan() {
		n++
	}
	return n
}

// compactJSONL retains the focused-test seam and applies the production byte target.
func compactJSONL(p string, cap int) (int, error) {
	return compactJSONLContext(context.Background(), p, cap, historyCompactTargetBytes)
}

// compactJSONLContext atomically and durably rewrites p to its newest line/byte-bounded suffix. The
// source suffix is streamed into an owned same-directory temporary file, then fsync'd and published via
// the same replace + parent-directory-sync custody sequence as writeBytesDurable. No full-file or
// full-output byte/string collection is materialized.
func compactJSONLContext(ctx context.Context, p string, cap int, maxBytes int64) (int, error) {
	source, err := openTelemetryHistoryFile(p)
	if err != nil {
		return 0, err
	}
	sourceOpen := true
	defer func() {
		if sourceOpen {
			_ = source.Close()
		}
	}()
	start, end, kept, err := historyTailBounds(ctx, source, cap, maxBytes)
	if err != nil {
		return 0, err
	}
	dir := filepath.Dir(p)
	if err := revalidateSecureStoreDir(dir); err != nil {
		return 0, fmt.Errorf("controller: unsafe parent for %s: %w", filepath.Base(p), err)
	}
	tmpFile, err := os.CreateTemp(dir, "."+filepath.Base(p)+".tmp-*")
	if err != nil {
		return 0, fmt.Errorf("controller: compact %s: %w", filepath.Base(p), err)
	}
	tmp := tmpFile.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmp)
		}
	}()
	if err := tmpFile.Chmod(0600); err != nil {
		_ = tmpFile.Close()
		return 0, fmt.Errorf("controller: protect %s compaction file: %w", filepath.Base(p), err)
	}
	if err := validateOpenedStoreTemp(tmp, tmpFile); err != nil {
		_ = tmpFile.Close()
		return 0, err
	}
	if err := copyHistoryContext(ctx, tmpFile, io.NewSectionReader(source, start, end-start)); err != nil {
		_ = tmpFile.Close()
		return 0, fmt.Errorf("controller: stream compact %s: %w", filepath.Base(p), err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return 0, fmt.Errorf("controller: sync compacted %s: %w", filepath.Base(p), err)
	}
	if err := tmpFile.Close(); err != nil {
		return 0, fmt.Errorf("controller: close compacted %s: %w", filepath.Base(p), err)
	}
	if err := source.Close(); err != nil {
		return 0, fmt.Errorf("controller: close source %s after compaction: %w", filepath.Base(p), err)
	}
	sourceOpen = false
	if err := revalidateSecureStoreDir(dir); err != nil {
		return 0, fmt.Errorf("controller: unsafe parent for %s: %w", filepath.Base(p), err)
	}
	if err := replaceStoreFileAtomic(tmp, p); err != nil {
		return 0, fmt.Errorf("controller: install compacted %s: %w", filepath.Base(p), err)
	}
	removeTemp = false
	if err := syncStoreDirectory(dir); err != nil {
		return 0, fmt.Errorf("controller: sync directory for compacted %s: %w", filepath.Base(p), err)
	}
	return kept, nil
}

func copyHistoryContext(ctx context.Context, dst io.Writer, src io.Reader) error {
	buf := make([]byte, historyIOChunkBytes)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			written := 0
			for written < n {
				if err := ctx.Err(); err != nil {
					return err
				}
				m, writeErr := dst.Write(buf[written:n])
				if writeErr != nil {
					return writeErr
				}
				if m <= 0 {
					return io.ErrShortWrite
				}
				written += m
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
