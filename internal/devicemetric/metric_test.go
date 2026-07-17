package devicemetric

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetryprotocol"
)

func TestDeviceMetricDefinitionsAndSeriesIdentity(t *testing.T) {
	want := []NumericDefinition{
		{Key: DiskFilesystemUsedPct, Kind: KindFilesystem, Unit: "%"},
		{Key: DiskReadBytesPerSecond, Kind: KindBlockDevice, Unit: "B/s"},
		{Key: DiskWriteBytesPerSecond, Kind: KindBlockDevice, Unit: "B/s"},
		{Key: DiskIOBusyPct, Kind: KindBlockDevice, Unit: "%"},
		{Key: GPUUtilizationPct, Kind: KindGPU, Unit: "%"},
		{Key: GPUVRAMUsedPct, Kind: KindGPU, Unit: "%"},
	}
	got := NumericDefinitions()
	if len(got) != len(want) {
		t.Fatalf("NumericDefinitions length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("NumericDefinitions[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	got[0].Unit = "mutated"
	if NumericDefinitions()[0].Unit != "%" {
		t.Fatal("NumericDefinitions returned shared mutable storage")
	}

	identity := []byte("raw-wwid-that-must-remain-local")
	first := SeriesID(KindBlockDevice, identity)
	if len(first) != 64 || first != SeriesID(KindBlockDevice, identity) {
		t.Fatalf("SeriesID = %q, want stable lowercase SHA-256", first)
	}
	if first == SeriesID(KindFilesystem, identity) || first == SeriesID(KindBlockDevice, append(identity, 0)) {
		t.Fatal("SeriesID did not separate kind or canonical identity")
	}
	if strings.Contains(first, string(identity)) {
		t.Fatal("SeriesID exposed canonical identity")
	}
	if got, want := SeriesID(KindBlockDevice, []byte("disk-wwid")), "123776c02c7377c01a3c25f77e118a754833a488ebeb74fd7d23127ec3ba73f4"; got != want {
		t.Fatalf("pinned SeriesID = %q, want %q", got, want)
	}

	deviceID := SeriesID(KindBlockDevice, []byte("disk-wwid"))
	historyID, err := HistorySeriesID(KindBlockDevice, deviceID)
	if err != nil || len(historyID) != 64 {
		t.Fatalf("HistorySeriesID(valid) = %q, %v", historyID, err)
	}
	if want := "e46b4472ce38f7e678d8852cb9a9d3e70a7c688671ce7e697bb9b1ed46856b22"; historyID != want {
		t.Fatalf("pinned HistorySeriesID = %q, want %q", historyID, want)
	}
	if again, err := HistorySeriesID(KindBlockDevice, deviceID); err != nil || again != historyID {
		t.Fatalf("HistorySeriesID is not deterministic: %q, %v", again, err)
	}
	if other, err := HistorySeriesID(KindFilesystem, deviceID); err != nil || other == historyID {
		t.Fatalf("HistorySeriesID is not kind-separated: %q, %v", other, err)
	}
	for name, selector := range map[string]struct {
		kind Kind
		id   string
	}{
		"unknown kind": {kind: "future", id: deviceID},
		"raw id":       {kind: KindGPU, id: "serial-123"},
		"uppercase id": {kind: KindGPU, id: strings.ToUpper(deviceID)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := HistorySeriesID(selector.kind, selector.id); err == nil {
				t.Fatalf("HistorySeriesID(%q, %q) succeeded", selector.kind, selector.id)
			}
		})
	}
}

func TestDeviceMetricValidationRejectsInvalidRows(t *testing.T) {
	blockID := SeriesID(KindBlockDevice, []byte("disk"))
	fsID := SeriesID(KindFilesystem, []byte("disk\x00/"))
	entries := []InventoryEntry{
		{SeriesID: fsID, Kind: KindFilesystem, Label: "root", ParentSeriesID: blockID, MountPoint: "/", FSType: "ext4", CapacityBytes: 10, Status: StatusOK},
		{SeriesID: blockID, Kind: KindBlockDevice, Label: "sda", CapacityBytes: 10, Status: StatusOK},
	}
	validInventory, _, err := BoundMetrics(entries, []Sample{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateInventory(validInventory); err != nil {
		t.Fatalf("ValidateInventory(valid) = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(InventoryMetric) InventoryMetric
	}{
		{name: "duplicate", mutate: func(metric InventoryMetric) InventoryMetric {
			metric.Devices = append(metric.Devices, metric.Devices[0])
			return metric
		}},
		{name: "bad status", mutate: func(metric InventoryMetric) InventoryMetric {
			metric.Devices[0].Status = "mystery"
			return metric
		}},
		{name: "raw id", mutate: func(metric InventoryMetric) InventoryMetric {
			metric.Devices[0].SeriesID = "serial-123"
			return metric
		}},
		{name: "oversize", mutate: func(metric InventoryMetric) InventoryMetric {
			metric.Devices[0].Label = strings.Repeat("x", MaxLabelBytes+1)
			return metric
		}},
		{name: "control", mutate: func(metric InventoryMetric) InventoryMetric {
			metric.Devices[0].Label = "bad\nlabel"
			return metric
		}},
		{name: "format control", mutate: func(metric InventoryMetric) InventoryMetric {
			metric.Devices[0].Label = "bad\u202elabel"
			return metric
		}},
		{name: "blank filesystem type", mutate: func(metric InventoryMetric) InventoryMetric {
			for i := range metric.Devices {
				if metric.Devices[i].Kind == KindFilesystem {
					metric.Devices[i].FSType = "   "
				}
			}
			return metric
		}},
		{name: "filesystem missing parent", mutate: func(metric InventoryMetric) InventoryMetric {
			for i := range metric.Devices {
				if metric.Devices[i].Kind == KindFilesystem {
					metric.Devices[i].ParentSeriesID = ""
				}
			}
			return metric
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copyMetric := InventoryMetric{Devices: append([]InventoryEntry(nil), validInventory.Devices...)}
			if err := ValidateInventory(test.mutate(copyMetric)); err == nil {
				t.Fatal("ValidateInventory accepted invalid metric")
			}
		})
	}

	_, validSamples, err := BoundMetrics(entries, []Sample{
		{SeriesID: blockID, Kind: KindBlockDevice, Values: map[NumericKey]float64{DiskReadBytesPerSecond: 0, DiskIOBusyPct: 1}},
		{SeriesID: fsID, Kind: KindFilesystem, Values: map[NumericKey]float64{DiskFilesystemUsedPct: 50}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSamples(validSamples); err != nil {
		t.Fatalf("ValidateSamples(valid) = %v", err)
	}
	invalid := []SamplesMetric{
		{Samples: []Sample{{SeriesID: blockID, Kind: KindBlockDevice, Values: map[NumericKey]float64{GPUUtilizationPct: 1}}}},
		{Samples: []Sample{{SeriesID: blockID, Kind: KindBlockDevice, Values: map[NumericKey]float64{DiskIOBusyPct: 101}}}},
		{Samples: []Sample{{SeriesID: blockID, Kind: KindBlockDevice, Values: map[NumericKey]float64{DiskReadBytesPerSecond: math.NaN()}}}},
		{Samples: []Sample{{SeriesID: blockID, Kind: KindBlockDevice, Values: map[NumericKey]float64{}}}},
		{Samples: validSamples.Samples, SampledAt: "not-a-timestamp"},
	}
	for i, metric := range invalid {
		if err := ValidateSamples(metric); err == nil {
			t.Fatalf("ValidateSamples invalid case %d succeeded", i)
		}
	}
	blankVendor := InventoryMetric{Devices: []InventoryEntry{{
		SeriesID: testDeviceSeriesID(1), Kind: KindGPU, Label: "GPU", Vendor: "   ", Status: StatusUnsupported,
	}}}
	if err := ValidateInventory(blankVendor); err == nil {
		t.Fatal("ValidateInventory accepted a whitespace-only GPU vendor")
	}
}

func TestDeviceMetricTruncationIsDeterministicAndBounded(t *testing.T) {
	var entries []InventoryEntry
	var samples []Sample
	for i := 0; i < MaxDiskEntries+7; i++ {
		id := SeriesID(KindBlockDevice, []byte{byte(i), 1})
		entries = append(entries, InventoryEntry{
			SeriesID: id, Kind: KindBlockDevice, Label: "d", Status: StatusOK,
		})
		samples = append(samples, Sample{SeriesID: id, Kind: KindBlockDevice, Values: map[NumericKey]float64{DiskReadBytesPerSecond: float64(i)}})
	}
	for i := 0; i < MaxGPUEntries+3; i++ {
		id := SeriesID(KindGPU, []byte{byte(i), 2})
		entries = append(entries, InventoryEntry{
			SeriesID: id, Kind: KindGPU, Label: "GPU", Vendor: "vendor", Model: "model", Status: StatusOK,
		})
		samples = append(samples, Sample{SeriesID: id, Kind: KindGPU, Values: map[NumericKey]float64{GPUUtilizationPct: float64(i)}})
	}

	// Cardinality-only input proves the exact 64/16 independent caps.
	inventory, _, err := BoundMetrics(entries, []Sample{})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Truncated != 10 || len(inventory.Devices) != MaxDiskEntries+MaxGPUEntries {
		t.Fatalf("bounded inventory = %d rows + %d truncated, want %d + 10", len(inventory.Devices), inventory.Truncated, MaxDiskEntries+MaxGPUEntries)
	}
	if err := ValidateInventory(inventory); err != nil {
		t.Fatalf("ValidateInventory(bounded) = %v", err)
	}
	rawInventory, err := json.Marshal(inventory)
	if err != nil || len(rawInventory) > telemetryprotocol.MaxMetricsBytes {
		t.Fatalf("encoded inventory = %d bytes, err %v", len(rawInventory), err)
	}
	if strings.Contains(string(rawInventory), "raw-wwid") || strings.Contains(string(rawInventory), "/sys/") {
		t.Fatal("encoded inventory exposed a local canonical identity")
	}

	// Escape-heavy metadata exercises the actual Go JSON budget. Exact accounting remains true even
	// when the byte ceiling removes more rows than the cardinality ceiling.
	heavyEntries := append([]InventoryEntry(nil), entries...)
	for i := range heavyEntries {
		heavyEntries[i].Label = strings.Repeat("<&>", 30)
	}
	heavyInventory, boundedSamples, err := BoundMetrics(heavyEntries, samples)
	if err != nil {
		t.Fatal(err)
	}
	if len(heavyInventory.Devices)+heavyInventory.Truncated != len(heavyEntries) {
		t.Fatalf("inventory truncation accounting = %d + %d, input %d", len(heavyInventory.Devices), heavyInventory.Truncated, len(heavyEntries))
	}
	if len(boundedSamples.Samples)+boundedSamples.Truncated != len(samples) {
		t.Fatalf("sample truncation accounting = %d + %d, input %d", len(boundedSamples.Samples), boundedSamples.Truncated, len(samples))
	}
	if err := ValidateSamples(boundedSamples); err != nil {
		t.Fatalf("ValidateSamples(bounded) = %v", err)
	}
	rawSamples, err := json.Marshal(boundedSamples)
	if err != nil || len(rawSamples) > telemetryprotocol.MaxMetricsBytes {
		t.Fatalf("encoded samples = %d bytes, err %v", len(rawSamples), err)
	}
	if got := encodedPairLen(heavyInventory, boundedSamples); got > MaxEncodedDevicePairBytes {
		t.Fatalf("encoded device pair = %d bytes, limit %d", got, MaxEncodedDevicePairBytes)
	}
	kinds := make(map[Kind]bool)
	for _, entry := range heavyInventory.Devices {
		kinds[entry.Kind] = true
	}
	if !kinds[KindBlockDevice] || !kinds[KindGPU] {
		t.Fatalf("byte-pressure output erased a device category: %v", kinds)
	}

	reversed := append([]InventoryEntry(nil), heavyEntries...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	againInventory, againSamples, err := BoundMetrics(reversed, samples)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(struct {
		Inventory InventoryMetric
		Samples   SamplesMetric
	}{againInventory, againSamples})
	wantPair, _ := json.Marshal(struct {
		Inventory InventoryMetric
		Samples   SamplesMetric
	}{heavyInventory, boundedSamples})
	if string(again) != string(wantPair) {
		t.Fatal("device-pair bounds depend on discovery order")
	}
}

func TestDeviceMetricPairIntegrityRejectsProducerDrift(t *testing.T) {
	blockID := SeriesID(KindBlockDevice, []byte("disk"))
	entries := []InventoryEntry{{SeriesID: blockID, Kind: KindBlockDevice, Label: "disk", Status: StatusOK}}
	tests := []struct {
		name    string
		entries []InventoryEntry
		samples []Sample
	}{
		{name: "orphan sample", entries: entries, samples: []Sample{{SeriesID: SeriesID(KindBlockDevice, []byte("other")), Kind: KindBlockDevice, Values: map[NumericKey]float64{DiskIOBusyPct: 0}}}},
		{name: "wrong kind", entries: entries, samples: []Sample{{SeriesID: blockID, Kind: KindGPU, Values: map[NumericKey]float64{GPUUtilizationPct: 0}}}},
		{name: "duplicate inventory", entries: append(entries, entries[0]), samples: []Sample{}},
		{name: "duplicate sample", entries: entries, samples: []Sample{
			{SeriesID: blockID, Kind: KindBlockDevice, Values: map[NumericKey]float64{DiskIOBusyPct: 0}},
			{SeriesID: blockID, Kind: KindBlockDevice, Values: map[NumericKey]float64{DiskReadBytesPerSecond: 0}},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := BoundMetrics(test.entries, test.samples); err == nil {
				t.Fatal("BoundMetrics accepted producer drift")
			}
		})
	}

	validInventory, validSamples, err := BoundMetrics(entries, []Sample{{
		SeriesID: blockID, Kind: KindBlockDevice, Values: map[NumericKey]float64{DiskIOBusyPct: 0},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePair(validInventory, validSamples); err != nil {
		t.Fatalf("ValidatePair(valid) = %v", err)
	}
	orphan := validSamples
	orphan.Samples = cloneSamples(validSamples.Samples)
	orphan.Samples[0].SeriesID = testDeviceSeriesID(99)
	if err := ValidatePair(validInventory, orphan); err == nil {
		t.Fatal("ValidatePair accepted an orphan sample")
	}
	wrongKind := SamplesMetric{Samples: []Sample{{
		SeriesID: blockID, Kind: KindGPU, Values: map[NumericKey]float64{GPUUtilizationPct: 0},
	}}}
	if err := ValidatePair(validInventory, wrongKind); err == nil {
		t.Fatal("ValidatePair accepted a sample with the inventory identity's wrong kind")
	}

	large := InventoryMetric{Devices: make([]InventoryEntry, 0, MaxDiskEntries)}
	for i := 0; i < MaxDiskEntries/2; i++ {
		parentID := testDeviceSeriesID(i + 1)
		large.Devices = append(large.Devices, InventoryEntry{
			SeriesID: parentID, Kind: KindBlockDevice, Label: strings.Repeat("b", MaxLabelBytes),
			Vendor: strings.Repeat("v", MaxVendorBytes), Model: strings.Repeat("m", MaxModelBytes), Status: StatusOK,
		})
	}
	for i := 0; i < MaxDiskEntries/2; i++ {
		large.Devices = append(large.Devices, InventoryEntry{
			SeriesID: testDeviceSeriesID(100 + i), Kind: KindFilesystem,
			Label: strings.Repeat("f", MaxLabelBytes), ParentSeriesID: testDeviceSeriesID(i + 1),
			MountPoint: "/" + strings.Repeat("p", MaxMountPointBytes-1),
			FSType:     strings.Repeat("t", MaxFSTypeBytes), Status: StatusOK,
		})
	}
	if err := ValidateInventory(large); err != nil {
		t.Fatalf("independently valid large inventory = %v", err)
	}
	emptySamples := SamplesMetric{Samples: []Sample{}}
	if err := ValidateSamples(emptySamples); err != nil {
		t.Fatalf("independently valid empty samples = %v", err)
	}
	if err := ValidatePair(large, emptySamples); err == nil {
		t.Fatal("ValidatePair accepted independently valid DTOs over the shared byte cap")
	}
}

func TestDeviceMetricParentRelationshipsAreValidatedBeforeTruncation(t *testing.T) {
	dangling := []InventoryEntry{{
		SeriesID: testDeviceSeriesID(1), Kind: KindFilesystem, Label: "/dangling",
		ParentSeriesID: testDeviceSeriesID(99), MountPoint: "/dangling", FSType: "ext4", Status: StatusOK,
	}}
	if _, _, err := BoundMetrics(dangling, []Sample{}); err == nil {
		t.Fatal("BoundMetrics accepted a producer-supplied unknown parent")
	}

	wrongKind := []InventoryEntry{
		{SeriesID: testDeviceSeriesID(1), Kind: KindBlockDevice, Label: "disk", Status: StatusOK},
		{SeriesID: testDeviceSeriesID(2), Kind: KindFilesystem, Label: "/a", ParentSeriesID: testDeviceSeriesID(1), MountPoint: "/a", FSType: "ext4", Status: StatusOK},
		{SeriesID: testDeviceSeriesID(3), Kind: KindFilesystem, Label: "/b", ParentSeriesID: testDeviceSeriesID(2), MountPoint: "/b", FSType: "ext4", Status: StatusOK},
	}
	if _, _, err := BoundMetrics(wrongKind, []Sample{}); err == nil {
		t.Fatal("BoundMetrics accepted a retained non-block parent")
	}

	cycle := make([]InventoryEntry, MaxDiskEntries+1)
	for i := range cycle {
		cycle[i] = InventoryEntry{SeriesID: testDeviceSeriesID(i + 1), Kind: KindBlockDevice, Label: "disk", Status: StatusOK}
	}
	cycle[MaxDiskEntries-1].ParentSeriesID = cycle[MaxDiskEntries].SeriesID
	cycle[MaxDiskEntries].ParentSeriesID = cycle[MaxDiskEntries-1].SeriesID
	if _, _, err := BoundMetrics(cycle, []Sample{}); err == nil {
		t.Fatal("BoundMetrics hid a producer parent cycle behind cardinality truncation")
	}
}

func testDeviceSeriesID(value int) string {
	return fmt.Sprintf("%064x", value)
}
