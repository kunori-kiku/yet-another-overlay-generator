package controller

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// telemetry_history.go is the controller's bounded, per-(tenant,node) RESOURCE-sample history — the
// durable backing for the node-detail CPU/RAM charts (plan-2). It is layered strictly ON TOP of the
// telemetry heartbeat: RecordTelemetry appends one sample IN-MEMORY (O(1), NO disk IO — preserving the
// RecordTelemetry "a 30s heartbeat must never fsync" DoS invariant), and a SEPARATE background flusher
// (FileStore only) drains the buffer to append-only per-node JSONL off the heartbeat path. It owns its
// OWN mutex — never the store-wide mu nor telemetryMu — so history can never stall or deadlock a beat.

// DefaultTelemetryHistoryCap is the per-node hard cap on retained resource samples applied when the
// operator has not configured one: 20160 ≈ 7 days at a 30s heartbeat. 0 disables history entirely.
const DefaultTelemetryHistoryCap = 20160

// historyCompactSlack: a durable JSONL is compacted to its last `cap` lines only once it grows past
// cap×slack lines, so the common flush is a pure append and the O(cap) rewrite is amortized.
const historyCompactSlack = 2

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

// resourceSampleFromMetrics projects metrics["resource"] into a ResourceSample. ok=false when the key is
// absent or malformed — a heartbeat without a usable resource metric simply adds no history sample
// (tolerant, never an error on the heartbeat path).
func resourceSampleFromMetrics(metrics map[string]json.RawMessage, at time.Time, interval time.Duration) (ResourceSample, bool) {
	raw, present := metrics["resource"]
	if !present || len(raw) == 0 {
		return ResourceSample{}, false
	}
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

func finiteTelemetryNumber(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// nodeHist is one node's in-memory history state: the not-yet-flushed samples plus (durable mode) the
// known JSONL line count for amortized compaction (-1 until counted once from disk).
type nodeHist struct {
	buf []ResourceSample
	// inflight is the batch drained from buf and currently being written. Query snapshots it
	// alongside buf so a read racing the drain/write window cannot temporarily lose samples.
	inflight  []ResourceSample
	fileLines int
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
	writeBatch func(TenantID, string, []ResourceSample, int) error

	capMu       sync.RWMutex
	capByTenant map[TenantID]int
	defaultCap  int
	// capLoader reads a tenant's persisted cap from settings (FileStore: GetSettings→EffectiveHistoryCap;
	// nil for MemStore). It is called ONLY from the flusher (off the heartbeat path) to SEED an unseen
	// tenant's cap on its first flush — so a tenant that persisted cap=0 (history disabled) is honored
	// across a controller restart (the in-memory cache starts empty; without this seed the flush would
	// use defaultCap>0 and write to disk data the operator disabled). append never calls it.
	capLoader func(TenantID) int

	stop    chan struct{}
	stopped chan struct{}
}

func newTelemetryHistory(dir string, defaultCap int, capLoader func(TenantID) int) *telemetryHistory {
	h := &telemetryHistory{
		nodes:       map[TenantID]map[string]*nodeHist{},
		dir:         dir,
		capByTenant: map[TenantID]int{},
		defaultCap:  defaultCap,
		capLoader:   capLoader,
	}
	h.writeBatch = h.writeJSONL
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
	h.setCap(t, h.capLoader(t)) // capLoader reads settings (disk) — flusher only, never append
}

// capFor returns the cached per-node sample cap for a tenant (defaultCap until the store seeds one). No
// disk IO — safe on the heartbeat append path.
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
	defer h.capMu.Unlock()
	h.capByTenant[t] = cap
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
	cap := h.capFor(t)
	if cap <= 0 {
		return // history disabled
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entryLocked(t, nodeID)
	e.buf = append(e.buf, s)
	if over := len(e.buf) - cap; over > 0 {
		e.buf = e.buf[over:]
	}
}

// start launches the background flusher (FileStore only). MemStore (dir=="") keeps everything in memory,
// so there is nothing to flush.
func (h *telemetryHistory) start() {
	if h.dir == "" {
		return
	}
	h.stop = make(chan struct{})
	h.stopped = make(chan struct{})
	go h.run(historyFlushInterval)
}

func (h *telemetryHistory) run(interval time.Duration) {
	defer close(h.stopped)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			h.flushOnce() // best-effort final drain on shutdown
			return
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
	close(h.stop)
	<-h.stopped
}

type flushJob struct {
	t       TenantID
	nodeID  string
	samples []ResourceSample
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
			e.buf = nil
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
}

// requeueInflight atomically moves the failed in-flight batch back to the FRONT of the buffer. The
// batch is older than samples appended while the write ran, so this preserves chronological order.
// flushMu ensures two failed writes cannot invert their batches while requeueing.
func (h *telemetryHistory) requeueInflight(t TenantID, nodeID string, cap int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.entryLocked(t, nodeID)
	requeued := make([]ResourceSample, 0, len(e.inflight)+len(e.buf))
	requeued = append(requeued, e.inflight...)
	requeued = append(requeued, e.buf...)
	e.inflight = nil
	e.buf = requeued
	if over := len(e.buf) - cap; over > 0 {
		e.buf = e.buf[over:]
	}
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

// writeJSONL appends the samples to the node's JSONL, then compacts to the last `cap` lines when the file
// exceeds cap×slack (amortized — most flushes are pure appends). The line count is tracked in memory,
// counted once from disk on the first flush after start.
func (h *telemetryHistory) writeJSONL(t TenantID, nodeID string, samples []ResourceSample, cap int) error {
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
	var batch bytes.Buffer
	enc := json.NewEncoder(&batch)
	for i := range samples {
		if err := enc.Encode(&samples[i]); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	if _, werr := f.Write(batch.Bytes()); werr != nil {
		// The append FAILED (possibly partially): the batch did NOT durably land, so returning the
		// error lets flushOnce requeue and retry — correct here.
		f.Close()
		return werr
	}
	// The batch is now durably appended. A Close() error must NOT be reported as a flush failure: it
	// would make flushOnce requeue and DUPLICATE these already-written lines on the next flush. Log and
	// continue — the fileLines bookkeeping below still runs on the appended lines (they are on disk), so
	// the running count and the compaction trigger stay correct.
	if cerr := f.Close(); cerr != nil {
		log.Printf("controller: telemetry history: close after append to %s: %v (batch already durable; continuing)", p, cerr)
	}

	h.mu.Lock()
	e := h.entryLocked(t, nodeID)
	if e.fileLines < 0 {
		e.fileLines = countLines(p) // count once (includes the batch just appended)
	} else {
		e.fileLines += len(samples)
	}
	over := e.fileLines > cap*historyCompactSlack
	h.mu.Unlock()

	if over {
		if kept, cerr := compactJSONL(p, cap); cerr == nil {
			h.mu.Lock()
			h.entryLocked(t, nodeID).fileLines = kept
			h.mu.Unlock()
		}
	}
	return nil
}

// query returns the node's samples within [from, to] (inclusive), merging the durable JSONL with both
// the in-flight flush batch and the ordinary in-memory buffer. It snapshots volatile samples BEFORE
// reading disk: whether a concurrent flush drains, writes, or completes afterwards, the sample is then
// visible in at least one side of the merge. Exact same-timestamp retry/partial-write duplicates are
// removed after a stable sort. Returns nil when history is disabled (cap<=0).
func (h *telemetryHistory) query(t TenantID, nodeID string, from, to time.Time) ([]ResourceSample, error) {
	if h.capFor(t) <= 0 {
		return nil, nil
	}

	var volatile []ResourceSample
	h.mu.Lock()
	if byNode := h.nodes[t]; byNode != nil {
		if e := byNode[nodeID]; e != nil {
			volatile = make([]ResourceSample, 0, len(e.inflight)+len(e.buf))
			for _, s := range e.inflight {
				if inWindow(s.TS, from, to) {
					volatile = append(volatile, s)
				}
			}
			for _, s := range e.buf {
				if inWindow(s.TS, from, to) {
					volatile = append(volatile, s)
				}
			}
		}
	}
	h.mu.Unlock()

	var out []ResourceSample
	if h.dir != "" {
		p, err := h.nodeFile(t, nodeID)
		if err != nil {
			return nil, err
		}
		disk, err := readJSONL(p, from, to)
		if err != nil {
			return nil, err
		}
		out = disk
	}
	out = append(out, volatile...)
	return sortAndDedupeResourceSamples(out), nil
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

func inWindow(ts, from, to time.Time) bool {
	return !ts.Before(from) && !ts.After(to)
}

// readJSONL reads a per-node history file, returning the samples within [from, to]. A missing file is an
// empty result (not an error); a corrupt line is skipped (tolerant — best-effort observability).
func readJSONL(p string, from, to time.Time) ([]ResourceSample, error) {
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []ResourceSample
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var s ResourceSample
		if json.Unmarshal(sc.Bytes(), &s) != nil {
			continue
		}
		if inWindow(s.TS, from, to) {
			out = append(out, s)
		}
	}
	return out, sc.Err()
}

func countLines(p string) int {
	f, err := os.Open(p)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		n++
	}
	return n
}

// compactJSONL atomically rewrites p to keep only its last `cap` lines (temp + rename); returns the kept
// count.
func compactJSONL(p string, cap int) (int, error) {
	lines, err := tailLines(p, cap)
	if err != nil {
		return 0, err
	}
	var compacted bytes.Buffer
	for _, ln := range lines {
		compacted.WriteString(ln)
		compacted.WriteByte('\n')
	}
	if err := writeBytesDurable(p, compacted.Bytes()); err != nil {
		return 0, err
	}
	return len(lines), nil
}

func tailLines(p string, n int) ([]string, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var all []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		all = append(all, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all, nil
}
