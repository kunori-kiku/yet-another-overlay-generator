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

func TestParseProcStat(t *testing.T) {
	// A full modern line: user=100 nice=0 system=50 idle=800 iowait=20 irq=5 softirq=5 steal=0
	// guest=999 guest_nice=999 — guest/guest_nice MUST be excluded from total (kernel double-counts).
	total, idle, ok := parseProcStat([]byte("cpu  100 0 50 800 20 5 5 0 999 999\ncpu0 10 0 5 80 2 0 0 0\nintr 123\n"))
	// total = 100+0+50+800+20+5+5+0 = 980; idle = 800+20 = 820.
	if !ok || total != 980 || idle != 820 {
		t.Fatalf("parseProcStat = %d %d ok=%v, want 980/820", total, idle, ok)
	}
	// Old kernel (no iowait/steal columns): "cpu 100 0 50 800" → total 950, idle 800.
	if tot, id, ok := parseProcStat([]byte("cpu 100 0 50 800\n")); !ok || tot != 950 || id != 800 {
		t.Errorf("parseProcStat(short) = %d %d ok=%v, want 950/800", tot, id, ok)
	}
	// No aggregate cpu line / non-numeric column / empty → not ok.
	for _, bad := range []string{"", "cpu0 1 2 3 4 5\n", "intr 1 2 3\n", "cpu x y z w\n"} {
		if _, _, ok := parseProcStat([]byte(bad)); ok {
			t.Errorf("parseProcStat(%q) ok=true, want false", bad)
		}
	}
}

func TestResourceSampler(t *testing.T) {
	origLoad, origMem, origStat := loadavgFn, meminfoFn, statFn
	defer func() { loadavgFn, meminfoFn, statFn = origLoad, origMem, origStat }()
	statFn = func() ([]byte, error) { return nil, errors.New("no stat") } // cpu skipped: focus on load+mem

	// Happy path: load + mem parse → metrics["resource"], no conditions, no cpu (statFn errors).
	loadavgFn = func() ([]byte, error) { return []byte("1.00 2.00 3.00 1/1 1\n"), nil }
	meminfoFn = func() ([]byte, error) { return []byte("MemTotal: 2048 kB\nMemAvailable: 1024 kB\n"), nil }
	conds, metrics := (&resourceSampler{}).Sample(time.Now())
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
	if res.CpuPct != nil {
		t.Errorf("cpu_pct must be absent when /proc/stat is unreadable, got %v", *res.CpuPct)
	}

	// /proc/loadavg unreadable → best-effort (nil,nil): never fails the cycle.
	loadavgFn = func() ([]byte, error) { return nil, errors.New("no /proc") }
	if c, m := (&resourceSampler{}).Sample(time.Now()); c != nil || m != nil {
		t.Errorf("loadavg read error must yield (nil,nil), got (%v,%v)", c, m)
	}

	// loadavg OK but meminfo unreadable → still reports load, zero mem.
	loadavgFn = func() ([]byte, error) { return []byte("0.10 0.20 0.30\n"), nil }
	meminfoFn = func() ([]byte, error) { return nil, errors.New("no meminfo") }
	_, m := (&resourceSampler{}).Sample(time.Now())
	r := m[resourceMetricKey].(hostResource)
	if r.Load1 != 0.10 || r.MemTotalKB != 0 {
		t.Errorf("meminfo error should keep load + zero mem, got %+v", r)
	}
}

// TestResourceSampler_CPU covers the stateful /proc/stat delta: first beat omits cpu (no prior
// snapshot), a subsequent beat reports the busy fraction, and a non-advancing or wrapped counter omits
// it (a gap, never a fake 0) while resyncing the snapshot.
func TestResourceSampler_CPU(t *testing.T) {
	origLoad, origMem, origStat := loadavgFn, meminfoFn, statFn
	defer func() { loadavgFn, meminfoFn, statFn = origLoad, origMem, origStat }()
	loadavgFn = func() ([]byte, error) { return []byte("0.1 0.2 0.3\n"), nil }
	meminfoFn = func() ([]byte, error) { return nil, errors.New("skip mem") }
	cpuOf := func(m map[string]any) *float64 { return m[resourceMetricKey].(hostResource).CpuPct }

	s := &resourceSampler{}
	// Beat 1: total=1000 (busy 200, idle 800). No prior snapshot → cpu omitted.
	statFn = func() ([]byte, error) { return []byte("cpu 100 0 100 800 0 0 0 0\n"), nil }
	if _, m := s.Sample(time.Now()); cpuOf(m) != nil {
		t.Fatalf("first beat must omit cpu_pct, got %v", *cpuOf(m))
	}
	// Beat 2: total 1000→2000 (Δtotal 1000), idle 800→1400 (Δidle 600) → busy 400 → 40.0%.
	statFn = func() ([]byte, error) { return []byte("cpu 300 0 300 1400 0 0 0 0\n"), nil }
	if _, m := s.Sample(time.Now()); cpuOf(m) == nil || *cpuOf(m) != 40.0 {
		t.Fatalf("beat 2 cpu_pct = %v, want 40.0", cpuOf(m))
	}
	// Beat 3: counter did not advance (same fixture, Δtotal 0) → omit.
	if _, m := s.Sample(time.Now()); cpuOf(m) != nil {
		t.Errorf("non-advancing counter must omit cpu_pct, got %v", *cpuOf(m))
	}
	// Beat 4: counter WRAPPED backwards (total < prev) → omit, but resync the snapshot to the new value.
	statFn = func() ([]byte, error) { return []byte("cpu 10 0 10 80 0 0 0 0\n"), nil }
	if _, m := s.Sample(time.Now()); cpuOf(m) != nil {
		t.Errorf("wrapped counter must omit cpu_pct, got %v", *cpuOf(m))
	}
	// Beat 5: advances from the resynced snapshot (total 100→300 Δ200, idle 80→180 Δ100) → busy 100 → 50.0%.
	statFn = func() ([]byte, error) { return []byte("cpu 60 0 60 180 0 0 0 0\n"), nil }
	if _, m := s.Sample(time.Now()); cpuOf(m) == nil || *cpuOf(m) != 50.0 {
		t.Fatalf("beat 5 (after wrap resync) cpu_pct = %v, want 50.0", cpuOf(m))
	}
}

func TestBuildTelemetryRegistersResourceSampler(t *testing.T) {
	origLoad, origMem, origStat := loadavgFn, meminfoFn, statFn
	defer func() { loadavgFn, meminfoFn, statFn = origLoad, origMem, origStat }()
	loadavgFn = func() ([]byte, error) { return []byte("0.1 0.2 0.3\n"), nil }
	meminfoFn = func() ([]byte, error) { return nil, errors.New("skip mem") }

	tel := BuildTelemetry(t.TempDir())
	var rs Sampler
	for _, s := range tel.samplers {
		if s.Name() == "resource" {
			rs = s
		}
	}
	if rs == nil {
		t.Fatal("BuildTelemetry must register the resource sampler")
	}
	// The registered sampler must be STATEFUL across beats: two consecutive Sample calls on the SAME
	// registered instance must produce cpu_pct on the second (proving BuildTelemetry stores a pointer,
	// not a fresh value each beat — otherwise cpu utilization could never be computed).
	statFn = func() ([]byte, error) { return []byte("cpu 100 0 100 800 0 0 0 0\n"), nil }
	rs.Sample(time.Now()) // first beat: primes the snapshot
	statFn = func() ([]byte, error) { return []byte("cpu 300 0 300 1400 0 0 0 0\n"), nil }
	if _, m := rs.Sample(time.Now()); m[resourceMetricKey].(hostResource).CpuPct == nil {
		t.Error("the registered resource sampler must retain state across beats (cpu_pct on the 2nd beat)")
	}
}
