package probemetric

import (
	"encoding/json"
	"math"
	"testing"
)

func float64Pointer(value float64) *float64 { return &value }

func validSuccess() Result {
	return Result{
		ID: "dns", Type: "tcp", Host: "resolver.example", Port: 53,
		Status: StatusSuccess, LatencyMS: float64Pointer(12.5),
		CheckedAt: "2026-07-16T06:20:00Z", IntervalMS: 60_000,
	}
}

func TestResultValidation(t *testing.T) {
	if !Valid(validSuccess(), false) {
		t.Fatal("valid completed success was rejected")
	}
	failure := validSuccess()
	failure.Status = StatusFailure
	failure.LatencyMS = nil
	failure.FailureReason = FailureTimeout
	if !Valid(failure, false) {
		t.Fatal("valid completed failure was rejected")
	}
	pending := validSuccess()
	pending.Status = StatusPending
	pending.LatencyMS = nil
	pending.CheckedAt = ""
	if Valid(pending, false) || !Valid(pending, true) {
		t.Fatal("pending acceptance did not honor allowPending")
	}

	bad := []Result{
		{ID: "bad space", Type: "icmp", Host: "example.net", Status: StatusPending},
		{ID: "x", Type: "tcp", Host: "example.net", Status: StatusPending},
		{ID: "x", Type: "icmp", Host: "https://example.net", Status: StatusPending},
		func() Result { r := validSuccess(); r.LatencyMS = float64Pointer(math.NaN()); return r }(),
		func() Result { r := validSuccess(); r.FailureReason = FailureTimeout; return r }(),
		func() Result { r := failure; r.FailureReason = "raw platform error"; return r }(),
		func() Result { r := validSuccess(); r.IntervalMS = -1; return r }(),
		func() Result { r := validSuccess(); r.IntervalMS = 29_000; return r }(),
		func() Result { r := validSuccess(); r.IntervalMS = 30_001; return r }(),
		func() Result { r := validSuccess(); r.IntervalMS = 3_601_000; return r }(),
	}
	for i, candidate := range bad {
		if Valid(candidate, true) {
			t.Fatalf("invalid result %d was accepted: %+v", i, candidate)
		}
	}
	withoutCadence := validSuccess()
	withoutCadence.IntervalMS = 0
	if !Valid(withoutCadence, false) {
		t.Fatal("legacy result without interval_ms was rejected")
	}
}

func TestDecodeArrayBoundsAndFilters(t *testing.T) {
	valid := validSuccess()
	invalid := valid
	invalid.Status = "other"
	raw, err := json.Marshal([]Result{invalid, valid, valid})
	if err != nil {
		t.Fatal(err)
	}
	got := DecodeArray(raw, 2, false)
	if len(got) != 2 || got[0].ID != valid.ID || got[1].ID != valid.ID {
		t.Fatalf("DecodeArray = %+v, want two accepted rows despite the malformed leading candidate", got)
	}
	wrongShapeRaw, err := json.Marshal([]any{"wrong-shape", invalid, valid, valid})
	if err != nil {
		t.Fatal(err)
	}
	got = DecodeArray(wrongShapeRaw, 2, false)
	if len(got) != 2 {
		t.Fatalf("DecodeArray(wrong-shaped prefix) = %+v, want two later accepted rows", got)
	}
	if got := DecodeArray(json.RawMessage(`{"not":"an array"}`), 10, false); got != nil {
		t.Fatalf("malformed top-level metric decoded as %+v", got)
	}
	if got := DecodeArray(json.RawMessage(`[{},{}] trailing`), 1, true); got != nil {
		t.Fatalf("malformed tail after the admission bound decoded as %+v", got)
	}
	if got := DecodeArray(json.RawMessage(`[{}, {broken]`), 1, true); got != nil {
		t.Fatalf("malformed element after the admission bound decoded as %+v", got)
	}

	futureCadence := validSuccess()
	futureCadence.IntervalMS = 3_601_000
	raw, err = json.Marshal([]Result{futureCadence})
	if err != nil {
		t.Fatal(err)
	}
	got = DecodeArray(raw, 1, false)
	if len(got) != 1 || got[0].IntervalMS != 0 {
		t.Fatalf("future cadence should degrade to absent without dropping the attempt: %+v", got)
	}
}

func TestSeriesIDSeparatesExecutableDestination(t *testing.T) {
	base := validSuccess()
	want := SeriesID(base)
	if want == "" || want != SeriesID(base) {
		t.Fatal("SeriesID is empty or unstable")
	}
	variants := []Result{base, base, base, base}
	variants[0].ID = "other"
	variants[1].Type, variants[1].Port = "icmp", 0
	variants[2].Host = "other.example"
	variants[3].Port = 443
	for _, variant := range variants {
		if SeriesID(variant) == want {
			t.Fatalf("destination change did not split series: %+v", variant)
		}
	}
}
