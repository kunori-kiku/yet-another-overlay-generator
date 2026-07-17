package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/devicemetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probemetric"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

// telemetry_history.go serves the operator-gated node telemetry-history query: it reads one coherent
// resource/probe/device snapshot from the controller and aggregates bounded bucketed series for node-detail
// charts. Operator mux ONLY — never the agent/anonymous muxes (observability, but still
// operator-authenticated like every other node view).

const (
	// maxHistoryBuckets is a GLOBAL response budget, not a per-series allowance. The server widens
	// the shared step for the number of resource/probe/device streams selected by the request, preventing
	// sixteen legal probe series from multiplying a nominal 1000-bucket cap into a multi-megabyte
	// response. The historical constant name remains because focused tests and comments use it.
	maxHistoryBuckets = 1000
	// minHistoryStep preserves the existing explicit-step contract for faster configured agents.
	minHistoryStep = time.Second
	// autoHistoryStepFloor is both Auto's fallback and its minimum. Faster observed or advertised
	// sampling must not manufacture a denser history series than the panel can usefully render.
	autoHistoryStepFloor = 30 * time.Second
	// maxHistoryRange bounds the query window (defense-in-depth; the retained history is capped anyway).
	maxHistoryRange = 366 * 24 * time.Hour
	// maxProbeHistorySeries bounds destination cardinality in one response. When a probe id is reused
	// for a new exact target, the older series remains distinct but falls out once it is no longer among
	// the sixteen most recently attempted series in the requested window.
	maxProbeHistorySeries = 16
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

// effectiveHistoryStepForStreams applies the global response-bucket budget after the ordinary
// cadence/requested-step rules. Stable epoch anchoring can expose one partial bucket at each edge, so
// each selected stream receives floor(global/streams) slots and reserves one of them for that edge.
// A request that omits probe filtering remains wire-compatible (all probe series are returned), but
// its shared step becomes coarser instead of multiplying the response cap by up to sixteen.
func effectiveHistoryStepForStreams(
	window, requested time.Duration,
	samples []controller.ResourceSample,
	streams int,
) time.Duration {
	step := effectiveHistoryStep(window, requested, samples)
	if streams < 1 {
		streams = 1
	}
	perStream := maxHistoryBuckets / streams
	if perStream < 2 {
		perStream = 2
	}
	budgetStep := ceilDurationDiv(window, int64(perStream-1))
	if budgetStep > step {
		return budgetStep
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
	Step     string                `json:"step"`     // the EFFECTIVE step (may be widened from the request)
	Disabled bool                  `json:"disabled"` // history retention is off for this fleet (cap 0)
	Buckets  []historyBucket       `json:"buckets"`
	Probes   []probeHistorySeries  `json:"probes"`
	Devices  []deviceHistorySeries `json:"devices"`
}

type probeHistoryBucket struct {
	T              time.Time      `json:"t"`
	Attempts       int            `json:"attempts"`
	Successes      int            `json:"successes"`
	Failures       int            `json:"failures"`
	IntervalMS     int64          `json:"interval_ms,omitempty"`
	LatencyMS      *metricAgg     `json:"latency_ms,omitempty"`
	FailureReasons map[string]int `json:"failure_reasons,omitempty"`
}

type probeHistorySeries struct {
	SeriesID       string               `json:"series_id"`
	ID             string               `json:"id"`
	Type           string               `json:"type"`
	Host           string               `json:"host,omitempty"`
	Port           int                  `json:"port,omitempty"`
	URL            string               `json:"url,omitempty"`
	ExpectedStatus int                  `json:"expected_status,omitempty"`
	IntervalMS     int64                `json:"interval_ms,omitempty"`
	Buckets        []probeHistoryBucket `json:"buckets"`
}

type deviceHistoryBucket struct {
	T       time.Time            `json:"t"`
	Metrics map[string]metricAgg `json:"metrics"`
}

type deviceHistorySeries struct {
	SeriesID string                `json:"series_id"`
	DeviceID string                `json:"device_id"`
	Kind     string                `json:"kind"`
	Buckets  []deviceHistoryBucket `json:"buckets"`
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

type probeHistorySelector struct {
	ID             string
	Type           string
	Host           string
	Port           int
	URL            string
	ExpectedStatus int
}

func (selector probeHistorySelector) matches(sample controller.ProbeHistorySample) bool {
	return sample.ID == selector.ID &&
		sample.Type == selector.Type &&
		sample.Host == selector.Host &&
		sample.Port == selector.Port &&
		sample.URL == selector.URL &&
		sample.ExpectedStatus == selector.ExpectedStatus
}

func (selector probeHistorySelector) seriesID() string {
	return probemetric.SeriesID(probemetric.Result{
		ID: selector.ID, Type: selector.Type, Host: selector.Host, Port: selector.Port,
		URL: selector.URL, ExpectedStatus: selector.ExpectedStatus,
	})
}

type telemetryHistoryEncodingOptions struct {
	includeProbes  bool
	probeSelector  *probeHistorySelector
	includeDevices bool
	deviceSelector *deviceHistorySelector
}

type deviceHistorySelector struct {
	Kind     devicemetric.Kind
	DeviceID string
	SeriesID string
}

func filterProbeHistorySamples(
	samples []controller.ProbeHistorySample,
	options telemetryHistoryEncodingOptions,
) []controller.ProbeHistorySample {
	if !options.includeProbes {
		return nil
	}
	if options.probeSelector == nil {
		return samples
	}
	out := make([]controller.ProbeHistorySample, 0, len(samples))
	for _, sample := range samples {
		if options.probeSelector.matches(sample) {
			out = append(out, sample)
		}
	}
	return out
}

func filterDeviceHistorySamples(
	samples []controller.DeviceHistorySample,
	options telemetryHistoryEncodingOptions,
) []controller.DeviceHistorySample {
	if !options.includeDevices || options.deviceSelector == nil {
		return nil
	}
	selector := options.deviceSelector
	out := make([]controller.DeviceHistorySample, 0, len(samples))
	for _, sample := range samples {
		if sample.SeriesID == selector.SeriesID && sample.DeviceID == selector.DeviceID && sample.Kind == selector.Kind {
			out = append(out, sample)
		}
	}
	return out
}

func telemetryHistoryStreamCount(history controller.TelemetryHistorySnapshot) int {
	streams := 0
	if len(history.Resources) > 0 {
		streams++
	}
	series := make(map[string]struct{})
	for _, sample := range history.Probes {
		series[sample.SeriesID] = struct{}{}
	}
	probeStreams := len(series)
	if probeStreams > maxProbeHistorySeries {
		probeStreams = maxProbeHistorySeries
	}
	streams += probeStreams
	if len(history.Devices) > 0 {
		streams++
	}
	if streams == 0 {
		return 1
	}
	return streams
}

// aggregateProbeHistory keeps exact executable destinations separate, selects at most the sixteen
// series with the newest attempt in the requested window, and applies the same stable epoch bucket grid
// as resource history. Every completed response carrying latency contributes it, including a URL
// response whose exact status mismatched; transport failures still form latency gaps. Zero milliseconds
// remains a valid measurement, never an outage sentinel.
func aggregateProbeHistory(samples []controller.ProbeHistorySample, step time.Duration) []probeHistorySeries {
	if step <= 0 || len(samples) == 0 {
		return nil
	}
	type seriesAcc struct {
		seriesID       string
		id             string
		typeName       string
		host           string
		port           int
		url            string
		expectedStatus int
		intervalMS     int64
		intervalAt     time.Time
		latest         time.Time
		samples        []controller.ProbeHistorySample
	}
	bySeries := make(map[string]*seriesAcc)
	for _, sample := range samples {
		series := bySeries[sample.SeriesID]
		if series == nil {
			series = &seriesAcc{
				seriesID: sample.SeriesID, id: sample.ID, typeName: sample.Type,
				host: sample.Host, port: sample.Port,
				url: sample.URL, expectedStatus: sample.ExpectedStatus,
			}
			bySeries[sample.SeriesID] = series
		}
		series.samples = append(series.samples, sample)
		if series.latest.IsZero() || sample.CheckedAt.After(series.latest) {
			series.latest = sample.CheckedAt
		}
		if sample.IntervalMS > 0 && (series.intervalAt.IsZero() || !sample.CheckedAt.Before(series.intervalAt)) {
			series.intervalMS = sample.IntervalMS
			series.intervalAt = sample.CheckedAt
		}
	}
	ordered := make([]*seriesAcc, 0, len(bySeries))
	for _, series := range bySeries {
		ordered = append(ordered, series)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].latest.Equal(ordered[j].latest) {
			return ordered[i].seriesID < ordered[j].seriesID
		}
		return ordered[i].latest.After(ordered[j].latest)
	})
	if len(ordered) > maxProbeHistorySeries {
		ordered = ordered[:maxProbeHistorySeries]
	}

	type bucketAcc struct {
		attempts, successes, failures int
		latency                       aggAcc
		failureReasons                map[string]int
		intervalMS                    int64
		intervalAt                    time.Time
	}
	out := make([]probeHistorySeries, 0, len(ordered))
	for _, series := range ordered {
		byStart := make(map[int64]*bucketAcc)
		var starts []int64
		for _, sample := range series.samples {
			start := historyBucketStart(sample.CheckedAt, step).UnixNano()
			bucket := byStart[start]
			if bucket == nil {
				bucket = &bucketAcc{}
				byStart[start] = bucket
				starts = append(starts, start)
			}
			bucket.attempts++
			if sample.IntervalMS > 0 && (bucket.intervalAt.IsZero() || !sample.CheckedAt.Before(bucket.intervalAt)) {
				bucket.intervalMS = sample.IntervalMS
				bucket.intervalAt = sample.CheckedAt
			}
			if sample.LatencyMS != nil {
				bucket.latency.add(*sample.LatencyMS)
			}
			switch sample.Status {
			case probemetric.StatusSuccess:
				bucket.successes++
			case probemetric.StatusFailure:
				bucket.failures++
				if sample.FailureReason != "" {
					if bucket.failureReasons == nil {
						bucket.failureReasons = make(map[string]int)
					}
					bucket.failureReasons[sample.FailureReason]++
				}
			}
		}
		sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
		buckets := make([]probeHistoryBucket, 0, len(starts))
		for _, start := range starts {
			acc := byStart[start]
			bucket := probeHistoryBucket{
				T: time.Unix(0, start).UTC(), Attempts: acc.attempts,
				Successes: acc.successes, Failures: acc.failures,
				IntervalMS: acc.intervalMS, FailureReasons: acc.failureReasons,
			}
			if acc.latency.n > 0 {
				latency := acc.latency.result()
				bucket.LatencyMS = &latency
			}
			buckets = append(buckets, bucket)
		}
		out = append(out, probeHistorySeries{
			SeriesID: series.seriesID, ID: series.id, Type: series.typeName,
			Host: series.host, Port: series.port,
			URL: series.url, ExpectedStatus: series.expectedStatus, IntervalMS: series.intervalMS,
			Buckets: buckets,
		})
	}
	return out
}

// aggregateDeviceHistory emits at most the one exact opaque device selected by the request. It uses
// devicemetric.NumericDefinitions as the sole numeric authority, so an unknown or wrong-kind value
// supplied by a future or faulty Store cannot escape through this encoder. Missing values remain
// absent gaps; an observed zero is accumulated as an ordinary measurement.
func aggregateDeviceHistory(
	samples []controller.DeviceHistorySample,
	selector *deviceHistorySelector,
	step time.Duration,
) []deviceHistorySeries {
	if selector == nil || step <= 0 || len(samples) == 0 {
		return nil
	}
	definitions := make(map[devicemetric.NumericKey]devicemetric.NumericDefinition)
	for _, definition := range devicemetric.NumericDefinitions() {
		if definition.Kind == selector.Kind {
			definitions[definition.Key] = definition
		}
	}
	type bucketAcc struct {
		metrics map[devicemetric.NumericKey]*aggAcc
	}
	byStart := make(map[int64]*bucketAcc)
	starts := make([]int64, 0)
	for _, sample := range samples {
		if sample.SeriesID != selector.SeriesID || sample.DeviceID != selector.DeviceID || sample.Kind != selector.Kind {
			continue
		}
		start := historyBucketStart(sample.TS, step).UnixNano()
		bucket := byStart[start]
		for key, value := range sample.Values {
			definition, ok := definitions[key]
			if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 ||
				(definition.Unit == "%" && value > 100) {
				continue
			}
			if bucket == nil {
				bucket = &bucketAcc{metrics: make(map[devicemetric.NumericKey]*aggAcc)}
				byStart[start] = bucket
				starts = append(starts, start)
			}
			accumulator := bucket.metrics[key]
			if accumulator == nil {
				accumulator = &aggAcc{}
				bucket.metrics[key] = accumulator
			}
			accumulator.add(value)
		}
	}
	if len(starts) == 0 {
		return nil
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	buckets := make([]deviceHistoryBucket, 0, len(starts))
	for _, start := range starts {
		accumulator := byStart[start]
		metrics := make(map[string]metricAgg, len(accumulator.metrics))
		for _, definition := range devicemetric.NumericDefinitions() {
			if definition.Kind != selector.Kind {
				continue
			}
			if metric := accumulator.metrics[definition.Key]; metric != nil && metric.n > 0 {
				metrics[string(definition.Key)] = metric.result()
			}
		}
		if len(metrics) == 0 {
			continue
		}
		buckets = append(buckets, deviceHistoryBucket{
			T: time.Unix(0, start).UTC(), Metrics: metrics,
		})
	}
	if len(buckets) == 0 {
		return nil
	}
	return []deviceHistorySeries{{
		SeriesID: selector.SeriesID, DeviceID: selector.DeviceID,
		Kind: string(selector.Kind), Buckets: buckets,
	}}
}

type telemetryHistoryFamilyEncoder func(
	*historyResponse,
	controller.TelemetryHistorySnapshot,
	time.Duration,
	telemetryHistoryEncodingOptions,
)

// telemetryHistoryFamilyEncoders is the API half of telemetrymetric's chart-family contract. The
// handler iterates telemetrymetric.ChartFamilies rather than calling family aggregators directly;
// exact parity and runtime-fixture tests make a new retained family fail until it has a wire encoder.
// The encoders deliberately preserve the established additive wire shape (`buckets` and `probes`).
var telemetryHistoryFamilyEncoders = map[telemetrymetric.ChartFamily]telemetryHistoryFamilyEncoder{
	telemetrymetric.ChartFamilyResource: func(response *historyResponse, history controller.TelemetryHistorySnapshot, step time.Duration, _ telemetryHistoryEncodingOptions) {
		response.Buckets = aggregateHistory(history.Resources, step)
		if response.Buckets == nil {
			response.Buckets = []historyBucket{}
		}
	},
	telemetrymetric.ChartFamilyProbe: func(response *historyResponse, history controller.TelemetryHistorySnapshot, step time.Duration, options telemetryHistoryEncodingOptions) {
		response.Probes = aggregateProbeHistory(filterProbeHistorySamples(history.Probes, options), step)
		if response.Probes == nil {
			response.Probes = []probeHistorySeries{}
		}
	},
	telemetrymetric.ChartFamilyDevice: func(response *historyResponse, history controller.TelemetryHistorySnapshot, step time.Duration, options telemetryHistoryEncodingOptions) {
		response.Devices = aggregateDeviceHistory(filterDeviceHistorySamples(history.Devices, options), options.deviceSelector, step)
		if response.Devices == nil {
			response.Devices = []deviceHistorySeries{}
		}
	},
}

func validateTelemetryHistoryFamilyEncoderRegistry() error {
	if err := telemetrymetric.ValidateCatalog(telemetrymetric.All()); err != nil {
		return fmt.Errorf("telemetry catalog: %w", err)
	}
	families := make(map[telemetrymetric.ChartFamily]struct{})
	for _, family := range telemetrymetric.ChartFamilies() {
		families[family] = struct{}{}
		if telemetryHistoryFamilyEncoders[family] == nil {
			return fmt.Errorf("charted family %q has no API encoder", family)
		}
	}
	for family, encoder := range telemetryHistoryFamilyEncoders {
		if encoder == nil {
			return fmt.Errorf("API history encoder %q is nil", family)
		}
		if _, ok := families[family]; !ok {
			return fmt.Errorf("API history encoder %q is not a charted catalog family", family)
		}
	}
	if len(telemetryHistoryFamilyEncoders) != len(families) {
		return fmt.Errorf("API encoder/catalog family cardinality %d/%d", len(telemetryHistoryFamilyEncoders), len(families))
	}
	return nil
}

func init() {
	if err := validateTelemetryHistoryFamilyEncoderRegistry(); err != nil {
		panic("api: invalid telemetry history family encoder registry: " + err.Error())
	}
}

func encodeTelemetryHistoryFamilies(
	history controller.TelemetryHistorySnapshot,
	step time.Duration,
	options telemetryHistoryEncodingOptions,
) (historyResponse, error) {
	var response historyResponse
	for _, family := range telemetrymetric.ChartFamilies() {
		encoder, ok := telemetryHistoryFamilyEncoders[family]
		if !ok || encoder == nil {
			return historyResponse{}, fmt.Errorf("api: telemetry history chart family %q has no encoder", family)
		}
		encoder(&response, history, step, options)
	}
	return response, nil
}

func parseDeviceHistoryEncodingOptions(q url.Values) (bool, *deviceHistorySelector, *apierr.Error) {
	include := false
	if values, present := q["include_devices"]; present {
		if len(values) != 1 {
			return false, nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "include_devices")
		}
		switch values[0] {
		case "true":
			include = true
		case "false":
		default:
			return false, nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "include_devices")
		}
	}

	kindValues, hasKind := q["device_kind"]
	idValues, hasID := q["device_id"]
	if hasKind && len(kindValues) != 1 {
		return false, nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "device_kind")
	}
	if hasID && len(idValues) != 1 {
		return false, nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "device_id")
	}
	if !include {
		if hasKind || hasID {
			return false, nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "include_devices")
		}
		return false, nil, nil
	}
	if !hasKind || kindValues[0] == "" {
		return false, nil, apierr.New(apierr.CodeReqFieldRequired).With("field", "device_kind")
	}
	if !hasID || idValues[0] == "" {
		return false, nil, apierr.New(apierr.CodeReqFieldRequired).With("field", "device_id")
	}
	kind := devicemetric.Kind(kindValues[0])
	switch kind {
	case devicemetric.KindBlockDevice, devicemetric.KindFilesystem, devicemetric.KindGPU:
	default:
		return false, nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "device_kind")
	}
	seriesID, err := devicemetric.HistorySeriesID(kind, idValues[0])
	if err != nil {
		return false, nil, apierr.New(apierr.CodeReqFieldInvalid).With("field", "device_id")
	}
	return true, &deviceHistorySelector{Kind: kind, DeviceID: idValues[0], SeriesID: seriesID}, nil
}

func parseTelemetryHistoryEncodingOptions(q url.Values) (telemetryHistoryEncodingOptions, *apierr.Error) {
	options := telemetryHistoryEncodingOptions{includeProbes: true}
	includeDevices, deviceSelector, deviceErr := parseDeviceHistoryEncodingOptions(q)
	if deviceErr != nil {
		return telemetryHistoryEncodingOptions{}, deviceErr
	}
	options.includeDevices = includeDevices
	options.deviceSelector = deviceSelector
	if q.Has("include_probes") {
		raw := q.Get("include_probes")
		switch raw {
		case "true":
		case "false":
			options.includeProbes = false
		default:
			return telemetryHistoryEncodingOptions{}, apierr.New(apierr.CodeReqFieldInvalid).With("field", "include_probes")
		}
	}

	selectorFields := []string{
		"probe_id", "probe_type", "probe_host", "probe_port", "probe_url", "probe_expected_status",
	}
	hasSelector := false
	for _, field := range selectorFields {
		if q.Has(field) {
			hasSelector = true
			break
		}
	}
	if !hasSelector {
		return options, nil
	}
	if !options.includeProbes {
		return telemetryHistoryEncodingOptions{}, apierr.New(apierr.CodeReqFieldInvalid).With("field", "include_probes")
	}
	for _, field := range []string{"probe_id", "probe_type"} {
		if q.Get(field) == "" {
			return telemetryHistoryEncodingOptions{}, apierr.New(apierr.CodeReqFieldRequired).With("field", field)
		}
	}

	probe := model.TelemetryProbe{
		ID: q.Get("probe_id"), Type: q.Get("probe_type"),
	}
	switch probe.Type {
	case model.TelemetryProbeTCP:
		if q.Get("probe_host") == "" {
			return telemetryHistoryEncodingOptions{}, apierr.New(apierr.CodeReqFieldRequired).With("field", "probe_host")
		}
		if q.Has("probe_url") || q.Has("probe_expected_status") {
			return telemetryHistoryEncodingOptions{}, apierr.New(apierr.CodeReqFieldInvalid).With("field", "probe_type")
		}
		if q.Get("probe_port") == "" {
			return telemetryHistoryEncodingOptions{}, apierr.New(apierr.CodeReqFieldRequired).With("field", "probe_port")
		}
		probe.Host = q.Get("probe_host")
		port, err := strconv.Atoi(q.Get("probe_port"))
		if err != nil {
			return telemetryHistoryEncodingOptions{}, apierr.New(apierr.CodeReqFieldInvalid).With("field", "probe_port")
		}
		probe.Port = port
	case model.TelemetryProbeICMP:
		if q.Get("probe_host") == "" {
			return telemetryHistoryEncodingOptions{}, apierr.New(apierr.CodeReqFieldRequired).With("field", "probe_host")
		}
		if q.Has("probe_port") || q.Has("probe_url") || q.Has("probe_expected_status") {
			return telemetryHistoryEncodingOptions{}, apierr.New(apierr.CodeReqFieldInvalid).With("field", "probe_type")
		}
		probe.Host = q.Get("probe_host")
	case model.TelemetryProbeURL:
		if q.Has("probe_host") || q.Has("probe_port") {
			return telemetryHistoryEncodingOptions{}, apierr.New(apierr.CodeReqFieldInvalid).With("field", "probe_type")
		}
		if q.Get("probe_url") == "" {
			return telemetryHistoryEncodingOptions{}, apierr.New(apierr.CodeReqFieldRequired).With("field", "probe_url")
		}
		if q.Get("probe_expected_status") == "" {
			return telemetryHistoryEncodingOptions{}, apierr.New(apierr.CodeReqFieldRequired).With("field", "probe_expected_status")
		}
		expectedStatus, err := strconv.Atoi(q.Get("probe_expected_status"))
		if err != nil || expectedStatus < 100 || expectedStatus > 599 {
			return telemetryHistoryEncodingOptions{}, apierr.New(apierr.CodeReqFieldInvalid).With("field", "probe_expected_status")
		}
		probe.URL = q.Get("probe_url")
		probe.ExpectedStatus = expectedStatus
	default:
		return telemetryHistoryEncodingOptions{}, apierr.New(apierr.CodeReqFieldInvalid).With("field", "probe_type")
	}
	if err := probepolicy.Validate([]model.TelemetryProbe{probe}); err != nil {
		return telemetryHistoryEncodingOptions{}, apierr.New(apierr.CodeReqFieldInvalid).With("field", "probe")
	}
	options.probeSelector = &probeHistorySelector{
		ID: probe.ID, Type: probe.Type, Host: probe.Host, Port: probe.Port,
		URL: probe.URL, ExpectedStatus: probe.ExpectedStatus,
	}
	return options, nil
}

// HandleNodeHistory serves GET ?node=<id>&from=<RFC3339>&to=<RFC3339>&step=<duration>, with optional
// include_probes=false or one exact type-specific probe selector, plus zero or one exact device
// selector. ICMP/TCP select by host and optional TCP port; URL selects by exact URL and effective
// expected status. Devices require include_devices=true with both closed kind and opaque device ID.
// Operator-gated (routed through the op() adapter, which applies the method guard + identity check).
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
	encodingOptions, optionsErr := parseTelemetryHistoryEncodingOptions(q)
	if optionsErr != nil {
		return nil, optionsErr
	}
	// step is optional; Auto chooses the finest Resolution that honors both the normal heartbeat
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
		response, encodeErr := encodeTelemetryHistoryFamilies(controller.TelemetryHistorySnapshot{}, step, encodingOptions)
		if encodeErr != nil {
			return nil, codedErr(apierr.CodeInternal, encodeErr)
		}
		response.Step = step.String()
		response.Disabled = true
		return response, nil
	}

	var history controller.TelemetryHistorySnapshot
	if encodingOptions.includeProbes && encodingOptions.probeSelector == nil && !encodingOptions.includeDevices {
		// Omitted selectors preserve the established additive all-probes/no-devices response for old
		// callers. Device history has no equivalent broad compatibility mode.
		history, err = h.store.QueryTelemetryHistorySnapshot(ctx, tenant, nodeID, from, to)
	} else {
		queryOptions := controller.TelemetryHistoryQueryOptions{
			AllProbeSeries: encodingOptions.includeProbes && encodingOptions.probeSelector == nil,
		}
		if encodingOptions.probeSelector != nil {
			queryOptions.ProbeSeriesID = encodingOptions.probeSelector.seriesID()
		}
		if encodingOptions.deviceSelector != nil {
			queryOptions.DeviceSeriesID = encodingOptions.deviceSelector.SeriesID
		}
		history, err = h.store.QueryTelemetryHistorySnapshotFiltered(
			ctx, tenant, nodeID, from, to, queryOptions,
		)
	}
	if err != nil {
		return nil, codedErr(apierr.CodeInternalStorage, err)
	}
	history.Probes = filterProbeHistorySamples(history.Probes, encodingOptions)
	history.Devices = filterDeviceHistorySamples(history.Devices, encodingOptions)
	step := effectiveHistoryStepForStreams(
		to.Sub(from), requestedStep, history.Resources, telemetryHistoryStreamCount(history),
	)
	response, encodeErr := encodeTelemetryHistoryFamilies(history, step, encodingOptions)
	if encodeErr != nil {
		return nil, codedErr(apierr.CodeInternal, encodeErr)
	}
	response.Step = step.String()
	return response, nil
}
