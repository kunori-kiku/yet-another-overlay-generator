// Package probemetric owns the typed active-probe telemetry result contract shared by the agent and
// controller. The latest-result metric remains backward compatible; the recent-sample metric adds a
// bounded attempt stream so probes that run faster than the heartbeat do not lose intermediate
// observations before history retention.
package probemetric

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetrymetric"
)

const (
	LatestMetricKey  = telemetrymetric.ProbeResultsKey
	SamplesMetricKey = telemetrymetric.ProbeSamplesKey

	// MaxRecentSamples bounds one heartbeat's completed-attempt window. At the maximum sixteen
	// configured probes this retains four full rounds while remaining comfortably below the shared
	// 64 KiB metrics-body limit, and reliable heartbeat snapshots preserve older windows in transit.
	MaxRecentSamples = 64
)

const (
	StatusPending = "pending"
	StatusSuccess = "success"
	StatusFailure = "failure"
)

const (
	FailureDNSFailed          = "dns_failed"
	FailureTimeout            = "timeout"
	FailurePermissionDenied   = "permission_denied"
	FailureConnectionRefused  = "connection_refused"
	FailureNetworkUnreachable = "network_unreachable"
	FailureNetworkError       = "network_error"
)

var validFailureReasons = map[string]struct{}{
	FailureDNSFailed:          {},
	FailureTimeout:            {},
	FailurePermissionDenied:   {},
	FailureConnectionRefused:  {},
	FailureNetworkUnreachable: {},
	FailureNetworkError:       {},
}

// Result is both the latest-result row and one completed history attempt. IntervalMS is additive and
// advisory: updated agents report the signed probe cadence; older agents omit it. CheckedAt is an
// agent wall-clock value and must be bounded against the outer telemetry sample time by the
// controller before it becomes a history timestamp.
type Result struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	Host          string   `json:"host"`
	Port          int      `json:"port,omitempty"`
	Status        string   `json:"status"`
	LatencyMS     *float64 `json:"latency_ms,omitempty"`
	CheckedAt     string   `json:"checked_at,omitempty"`
	FailureReason string   `json:"failure_reason,omitempty"`
	IntervalMS    int64    `json:"interval_ms,omitempty"`
}

// DecodeArray performs a tolerant, bounded decode suitable for an authenticated observability
// boundary: malformed rows are discarded without rejecting the whole heartbeat, and max bounds
// accepted rows rather than examined candidates so a bad prefix cannot suppress later valid data.
// The top-level raw metric has already passed the shared byte limit. Duplicate attempts are
// intentionally left for the history store's exact identity deduper.
func DecodeArray(raw json.RawMessage, max int, allowPending bool) []Result {
	if max <= 0 || len(raw) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('[') {
		return nil
	}
	out := make([]Result, 0, max)
	for decoder.More() {
		// Decode one bounded raw row even after the result cap has been reached. This validates the
		// complete JSON array without allocating a result slice proportional to attacker-chosen input;
		// the enclosing metric is already bounded to 64 KiB by telemetry admission.
		var row json.RawMessage
		if err := decoder.Decode(&row); err != nil {
			return nil
		}
		if len(out) == max {
			continue
		}
		var candidate Result
		if err := json.Unmarshal(row, &candidate); err != nil {
			// A syntactically valid but wrong-shaped row is no more authoritative than a row that fails
			// semantic validation. Discard it without letting it consume the accepted-result budget.
			continue
		}
		// Cadence is advisory metadata. A future agent may legitimately extend its range or
		// precision before this controller understands it; preserve the authenticated attempt but
		// clear cadence we cannot safely use for gap detection. Valid remains strict for locally
		// produced/stored values, while this wire boundary degrades the additive field to absent.
		if candidate.IntervalMS != 0 && !validIntervalMS(candidate.IntervalMS) {
			candidate.IntervalMS = 0
		}
		if Valid(candidate, allowPending) {
			out = append(out, candidate)
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim(']') {
		return nil
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil
	}
	return out
}

// Valid enforces the closed result shape before a row may enter durable history.
func Valid(result Result, allowPending bool) bool {
	probe := model.TelemetryProbe{ID: result.ID, Type: result.Type, Host: result.Host, Port: result.Port}
	if probepolicy.Validate([]model.TelemetryProbe{probe}) != nil {
		return false
	}
	if result.IntervalMS != 0 {
		// Version-1 policy expresses cadence in whole seconds. Treat interval_ms as either absent or
		// the exact effective signed cadence; accepting sub-minimum/fractional advisories would let a
		// compromised authenticated node distort gap detection without changing its signed policy.
		if !validIntervalMS(result.IntervalMS) {
			return false
		}
	}
	switch result.Status {
	case StatusPending:
		return allowPending && result.CheckedAt == "" && result.LatencyMS == nil && result.FailureReason == ""
	case StatusSuccess:
		return result.CheckedAt != "" && result.LatencyMS != nil && finiteNonNegative(*result.LatencyMS) && result.FailureReason == ""
	case StatusFailure:
		_, reasonOK := validFailureReasons[result.FailureReason]
		return result.CheckedAt != "" && result.LatencyMS == nil && reasonOK
	default:
		return false
	}
}

func validIntervalMS(intervalMS int64) bool {
	const millisecondsPerSecond = int64(time.Second / time.Millisecond)
	minIntervalMS := int64(probepolicy.MinIntervalSeconds) * millisecondsPerSecond
	maxIntervalMS := int64(probepolicy.MaxIntervalSeconds) * millisecondsPerSecond
	return intervalMS >= minIntervalMS &&
		intervalMS <= maxIntervalMS &&
		intervalMS%millisecondsPerSecond == 0
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// Completed reports whether a valid result represents an attempted check rather than initial
// pending state.
func Completed(result Result) bool {
	return result.Status == StatusSuccess || result.Status == StatusFailure
}

// SeriesID is a stable opaque identity for one exact executable destination. Reusing a human probe
// ID with a changed type/host/port therefore starts a different chart instead of splicing histories.
func SeriesID(result Result) string {
	h := sha256.New()
	h.Write([]byte(result.ID))
	h.Write([]byte{0})
	h.Write([]byte(result.Type))
	h.Write([]byte{0})
	h.Write([]byte(result.Host))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(result.Port)))
	return hex.EncodeToString(h.Sum(nil))
}
