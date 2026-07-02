package agent

import (
	"errors"
	"testing"
	"time"
)

func TestParseLoadavg(t *testing.T) {
	l1, l5, l15, ok := parseLoadavg([]byte("0.52 0.58 0.59 1/834 12345\n"))
	if !ok || l1 != 0.52 || l5 != 0.58 || l15 != 0.59 {
		t.Fatalf("parseLoadavg = %v %v %v ok=%v", l1, l5, l15, ok)
	}
	for _, bad := range []string{"", "0.1 0.2", "x y z", "0.1 y 0.3"} {
		if _, _, _, ok := parseLoadavg([]byte(bad)); ok {
			t.Errorf("parseLoadavg(%q) ok=true, want false", bad)
		}
	}
}

func TestParseMeminfo(t *testing.T) {
	mem := "MemTotal:       16384000 kB\nMemFree:  100 kB\nMemAvailable:    8192000 kB\nBuffers: 5 kB\n"
	total, avail, ok := parseMeminfo([]byte(mem))
	if !ok || total != 16384000 || avail != 8192000 {
		t.Fatalf("parseMeminfo = %d %d ok=%v", total, avail, ok)
	}
	// Missing MemAvailable (old kernel) → not ok; empty → not ok.
	if _, _, ok := parseMeminfo([]byte("MemTotal: 100 kB\n")); ok {
		t.Error("parseMeminfo without MemAvailable should be ok=false")
	}
	if _, _, ok := parseMeminfo([]byte("")); ok {
		t.Error("parseMeminfo(empty) should be ok=false")
	}
}

func TestResourceSampler(t *testing.T) {
	origLoad, origMem := loadavgFn, meminfoFn
	defer func() { loadavgFn, meminfoFn = origLoad, origMem }()

	// Happy path: both files parse → metrics["resource"], no conditions.
	loadavgFn = func() ([]byte, error) { return []byte("1.00 2.00 3.00 1/1 1\n"), nil }
	meminfoFn = func() ([]byte, error) { return []byte("MemTotal: 2048 kB\nMemAvailable: 1024 kB\n"), nil }
	conds, metrics := resourceSampler{}.Sample(time.Now())
	if conds != nil {
		t.Errorf("resourceSampler emits no conditions, got %v", conds)
	}
	res, ok := metrics[resourceMetricKey].(hostResource)
	if !ok {
		t.Fatalf("metrics[%q] missing/wrong type: %#v", resourceMetricKey, metrics)
	}
	if res.Load1 != 1.0 || res.Load15 != 3.0 || res.MemTotalKB != 2048 || res.MemAvailKB != 1024 {
		t.Errorf("resource = %+v, want load 1/_/3 + mem 2048/1024", res)
	}

	// /proc/loadavg unreadable → best-effort (nil,nil): never fails the cycle.
	loadavgFn = func() ([]byte, error) { return nil, errors.New("no /proc") }
	if c, m := (resourceSampler{}).Sample(time.Now()); c != nil || m != nil {
		t.Errorf("loadavg read error must yield (nil,nil), got (%v,%v)", c, m)
	}

	// loadavg OK but meminfo unreadable → still reports load, zero mem.
	loadavgFn = func() ([]byte, error) { return []byte("0.10 0.20 0.30\n"), nil }
	meminfoFn = func() ([]byte, error) { return nil, errors.New("no meminfo") }
	_, m := resourceSampler{}.Sample(time.Now())
	r := m[resourceMetricKey].(hostResource)
	if r.Load1 != 0.10 || r.MemTotalKB != 0 {
		t.Errorf("meminfo error should keep load + zero mem, got %+v", r)
	}
}

func TestBuildTelemetryRegistersResourceSampler(t *testing.T) {
	tel := BuildTelemetry(t.TempDir())
	found := false
	for _, s := range tel.samplers {
		if s.Name() == "resource" {
			found = true
		}
	}
	if !found {
		t.Error("BuildTelemetry must register the resource sampler")
	}
}
