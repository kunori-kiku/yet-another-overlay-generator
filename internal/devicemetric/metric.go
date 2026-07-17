// Package devicemetric owns the bounded, typed device-telemetry contract shared by the agent and
// controller. Inventory is deliberately separate from numeric samples: labels, capacities, and
// provider status are categorical metadata and must never be mistaken for chart values.
package devicemetric

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/telemetryprotocol"
)

type Kind string

const (
	KindBlockDevice Kind = "block_device"
	KindFilesystem  Kind = "filesystem"
	KindGPU         Kind = "gpu"
)

type Status string

const (
	StatusOK                 Status = "ok"
	StatusToolMissing        Status = "tool_missing"
	StatusDriverUnavailable  Status = "driver_unavailable"
	StatusMetricsUnavailable Status = "metrics_unavailable"
	StatusUnsupported        Status = "unsupported"
	StatusCollectionError    Status = "collection_error"
)

type NumericKey string

const (
	DiskFilesystemUsedPct   NumericKey = "disk_filesystem_used_pct"
	DiskReadBytesPerSecond  NumericKey = "disk_read_bytes_per_second"
	DiskWriteBytesPerSecond NumericKey = "disk_write_bytes_per_second"
	DiskIOBusyPct           NumericKey = "disk_io_busy_pct"
	GPUUtilizationPct       NumericKey = "gpu_utilization_pct"
	GPUVRAMUsedPct          NumericKey = "gpu_vram_used_pct"
)

const (
	MaxDiskEntries = 64
	MaxGPUEntries  = 16
	// MaxEncodedDevicePairBytes reserves enough of the shared heartbeat envelope for the existing
	// core metrics and the maximum signed active-probe latest-result set. It covers both device
	// values plus their eventual top-level JSON keys; plan 7 must register the pair atomically.
	MaxEncodedDevicePairBytes = 24 << 10

	MaxLabelBytes      = 128
	MaxMountPointBytes = 256
	MaxFSTypeBytes     = 64
	MaxVendorBytes     = 64
	MaxModelBytes      = 128
)

type NumericDefinition struct {
	Key  NumericKey
	Kind Kind
	Unit string
}

type InventoryEntry struct {
	SeriesID       string `json:"series_id"`
	Kind           Kind   `json:"kind"`
	Label          string `json:"label"`
	ParentSeriesID string `json:"parent_series_id,omitempty"`
	MountPoint     string `json:"mount_point,omitempty"`
	FSType         string `json:"fs_type,omitempty"`
	Vendor         string `json:"vendor,omitempty"`
	Model          string `json:"model,omitempty"`
	CapacityBytes  uint64 `json:"capacity_bytes,omitempty"`
	VRAMTotalBytes uint64 `json:"vram_total_bytes,omitempty"`
	Status         Status `json:"status"`
}

type Sample struct {
	SeriesID string                 `json:"series_id"`
	Kind     Kind                   `json:"kind"`
	Values   map[NumericKey]float64 `json:"values"`
}

type InventoryMetric struct {
	Devices   []InventoryEntry `json:"devices"`
	Truncated int              `json:"truncated,omitempty"`
}

type SamplesMetric struct {
	Samples   []Sample `json:"samples"`
	Truncated int      `json:"truncated,omitempty"`
}

var numericDefinitions = [...]NumericDefinition{
	{Key: DiskFilesystemUsedPct, Kind: KindFilesystem, Unit: "%"},
	{Key: DiskReadBytesPerSecond, Kind: KindBlockDevice, Unit: "B/s"},
	{Key: DiskWriteBytesPerSecond, Kind: KindBlockDevice, Unit: "B/s"},
	{Key: DiskIOBusyPct, Kind: KindBlockDevice, Unit: "%"},
	{Key: GPUUtilizationPct, Kind: KindGPU, Unit: "%"},
	{Key: GPUVRAMUsedPct, Kind: KindGPU, Unit: "%"},
}

var numericKind = func() map[NumericKey]Kind {
	out := make(map[NumericKey]Kind, len(numericDefinitions))
	for _, definition := range numericDefinitions {
		out[definition.Key] = definition.Kind
	}
	return out
}()

// NumericDefinitions returns a copy of the closed chart contract in deterministic order.
func NumericDefinitions() []NumericDefinition {
	out := make([]NumericDefinition, len(numericDefinitions))
	copy(out, numericDefinitions[:])
	return out
}

// SeriesID turns a locally held canonical hardware identity into an opaque, domain-separated wire
// identity. Callers must discard the canonical identity after hashing it.
func SeriesID(kind Kind, canonicalIdentity []byte) string {
	h := sha256.New()
	h.Write([]byte("yaog-device-series-v1"))
	h.Write([]byte{0})
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write(canonicalIdentity)
	return hex.EncodeToString(h.Sum(nil))
}

// BoundMetrics sorts before applying the independent disk-related and GPU caps, retains samples only
// for inventory rows that survived, and enforces one shared encoded budget for the eventual metric
// pair. Deterministic largest-contribution removal accounts for every removed inventory and sample
// row without systematically sacrificing the lexically last device kind.
func BoundMetrics(entries []InventoryEntry, samples []Sample) (InventoryMetric, SamplesMetric, error) {
	allInventory := make(map[string]Kind, len(entries))
	for _, entry := range entries {
		if err := validateInventory(InventoryMetric{Devices: []InventoryEntry{entry}}, true); err != nil {
			return InventoryMetric{}, SamplesMetric{}, fmt.Errorf("bound device inventory: %w", err)
		}
		if _, exists := allInventory[entry.SeriesID]; exists {
			return InventoryMetric{}, SamplesMetric{}, fmt.Errorf("bound device inventory: duplicate series id")
		}
		allInventory[entry.SeriesID] = entry.Kind
	}
	if err := validateInventoryRelationships(entries, false); err != nil {
		return InventoryMetric{}, SamplesMetric{}, fmt.Errorf("bound device inventory: %w", err)
	}
	seenSamples := make(map[string]struct{}, len(samples))
	for _, sample := range samples {
		if err := ValidateSamples(SamplesMetric{Samples: []Sample{sample}}); err != nil {
			return InventoryMetric{}, SamplesMetric{}, fmt.Errorf("bound device samples: %w", err)
		}
		kind, exists := allInventory[sample.SeriesID]
		if !exists || kind != sample.Kind {
			return InventoryMetric{}, SamplesMetric{}, fmt.Errorf("bound device samples: inventory identity mismatch")
		}
		if _, exists := seenSamples[sample.SeriesID]; exists {
			return InventoryMetric{}, SamplesMetric{}, fmt.Errorf("bound device samples: duplicate series id")
		}
		seenSamples[sample.SeriesID] = struct{}{}
	}

	devices := append([]InventoryEntry{}, entries...)
	sort.Slice(devices, func(i, j int) bool {
		return less(devices[i].Kind, devices[i].SeriesID, devices[j].Kind, devices[j].SeriesID)
	})
	devices, inventoryTruncated := capInventory(devices)
	inventory := InventoryMetric{Devices: devices, Truncated: inventoryTruncated}

	allowed := make(map[string]struct{}, len(devices))
	for _, entry := range devices {
		allowed[entry.SeriesID] = struct{}{}
	}
	bounded := cloneSamples(samples)
	sort.Slice(bounded, func(i, j int) bool {
		return less(bounded[i].Kind, bounded[i].SeriesID, bounded[j].Kind, bounded[j].SeriesID)
	})
	filtered := bounded[:0]
	sampleTruncated := 0
	for _, sample := range bounded {
		if _, ok := allowed[sample.SeriesID]; !ok {
			sampleTruncated++
			continue
		}
		filtered = append(filtered, sample)
	}
	filtered, cappedSamples := capSamples(filtered)
	sampleMetric := SamplesMetric{Samples: filtered, Truncated: sampleTruncated + cappedSamples}

	for encodedPairLen(inventory, sampleMetric) > MaxEncodedDevicePairBytes && len(inventory.Devices) > 0 {
		removeAt := largestRemovableInventoryIndex(inventory.Devices, sampleMetric.Samples)
		removed := inventory.Devices[removeAt].SeriesID
		inventory.Devices = append(inventory.Devices[:removeAt], inventory.Devices[removeAt+1:]...)
		inventory.Truncated++
		for i := len(sampleMetric.Samples) - 1; i >= 0; i-- {
			if sampleMetric.Samples[i].SeriesID != removed {
				continue
			}
			sampleMetric.Samples = append(sampleMetric.Samples[:i], sampleMetric.Samples[i+1:]...)
			sampleMetric.Truncated++
			break
		}
	}
	if err := ValidatePair(inventory, sampleMetric); err != nil {
		return InventoryMetric{}, SamplesMetric{}, fmt.Errorf("bound device metrics: %w", err)
	}
	return inventory, sampleMetric, nil
}

// ValidatePair enforces the cross-metric identity and shared-byte invariants that independent DTO
// validation cannot prove. Every numeric row must have an exact inventory row of the same kind;
// inventory without a current numeric sample remains valid and represents a chart gap.
func ValidatePair(inventory InventoryMetric, samples SamplesMetric) error {
	if err := ValidateInventory(inventory); err != nil {
		return err
	}
	if err := ValidateSamples(samples); err != nil {
		return err
	}
	identities := make(map[string]Kind, len(inventory.Devices))
	for _, entry := range inventory.Devices {
		identities[entry.SeriesID] = entry.Kind
	}
	for _, sample := range samples.Samples {
		if kind, ok := identities[sample.SeriesID]; !ok || kind != sample.Kind {
			return fmt.Errorf("device sample has no matching inventory identity")
		}
	}
	if encodedPairLen(inventory, samples) > MaxEncodedDevicePairBytes {
		return fmt.Errorf("device metric pair exceeds encoded byte cap")
	}
	return nil
}

func largestRemovableInventoryIndex(devices []InventoryEntry, samples []Sample) int {
	if len(devices) == 1 {
		return 0
	}
	kindCounts := make(map[Kind]int, 3)
	sampleSizes := make(map[string]int, len(samples))
	for _, sample := range samples {
		sampleSizes[sample.SeriesID] = encodedLen(sample)
	}
	for _, entry := range devices {
		kindCounts[entry.Kind]++
	}
	best, bestSize := -1, -1
	for i, entry := range devices {
		// Preserve at least one representative of every present kind while another kind still has
		// multiple rows. This avoids byte-pressure systematically erasing the lexically last GPU kind.
		if kindCounts[entry.Kind] == 1 && len(kindCounts) > 1 {
			continue
		}
		size := encodedLen(entry) + sampleSizes[entry.SeriesID]
		if size > bestSize || (size == bestSize && (best == -1 || less(devices[best].Kind, devices[best].SeriesID, entry.Kind, entry.SeriesID))) {
			best, bestSize = i, size
		}
	}
	if best >= 0 {
		return best
	}
	return len(devices) - 1
}

func ValidateInventory(metric InventoryMetric) error {
	return validateInventory(metric, metric.Truncated > 0)
}

func validateInventory(metric InventoryMetric, allowMissingParents bool) error {
	if metric.Devices == nil {
		return fmt.Errorf("device inventory devices must be an array")
	}
	if metric.Truncated < 0 {
		return fmt.Errorf("device inventory has negative truncated count")
	}
	diskCount, gpuCount := 0, 0
	seen := make(map[string]struct{}, len(metric.Devices))
	for i, entry := range metric.Devices {
		if !validKind(entry.Kind) {
			return fmt.Errorf("device inventory row %d has invalid kind", i)
		}
		if !validStatus(entry.Status) {
			return fmt.Errorf("device inventory row %d has invalid status", i)
		}
		if !validSeriesID(entry.SeriesID) {
			return fmt.Errorf("device inventory row %d has invalid series id", i)
		}
		if _, exists := seen[entry.SeriesID]; exists {
			return fmt.Errorf("device inventory has duplicate series id")
		}
		seen[entry.SeriesID] = struct{}{}
		if i > 0 && !less(metric.Devices[i-1].Kind, metric.Devices[i-1].SeriesID, entry.Kind, entry.SeriesID) {
			return fmt.Errorf("device inventory is not strictly sorted")
		}
		if err := validDisplay(entry.Label, MaxLabelBytes, true); err != nil {
			return fmt.Errorf("device inventory row %d label: %w", i, err)
		}
		for name, value := range map[string]struct {
			text string
			max  int
		}{
			"mount point":     {entry.MountPoint, MaxMountPointBytes},
			"filesystem type": {entry.FSType, MaxFSTypeBytes},
			"vendor":          {entry.Vendor, MaxVendorBytes},
			"model":           {entry.Model, MaxModelBytes},
		} {
			if err := validDisplay(value.text, value.max, false); err != nil {
				return fmt.Errorf("device inventory row %d %s: %w", i, name, err)
			}
		}
		if entry.ParentSeriesID != "" && (!validSeriesID(entry.ParentSeriesID) || entry.ParentSeriesID == entry.SeriesID) {
			return fmt.Errorf("device inventory row %d has invalid parent series id", i)
		}
		switch entry.Kind {
		case KindBlockDevice:
			diskCount++
			if entry.Status != StatusOK && entry.Status != StatusMetricsUnavailable && entry.Status != StatusCollectionError {
				return fmt.Errorf("block-device inventory row %d has invalid status", i)
			}
			if entry.MountPoint != "" || entry.FSType != "" || entry.VRAMTotalBytes != 0 {
				return fmt.Errorf("block-device inventory row %d has fields for another kind", i)
			}
		case KindFilesystem:
			diskCount++
			if entry.Status != StatusOK && entry.Status != StatusMetricsUnavailable && entry.Status != StatusCollectionError {
				return fmt.Errorf("filesystem inventory row %d has invalid status", i)
			}
			if entry.ParentSeriesID == "" || strings.TrimSpace(entry.MountPoint) == "" || strings.TrimSpace(entry.FSType) == "" || entry.Vendor != "" || entry.Model != "" || entry.VRAMTotalBytes != 0 {
				return fmt.Errorf("filesystem inventory row %d has invalid kind-specific fields", i)
			}
		case KindGPU:
			gpuCount++
			if strings.TrimSpace(entry.Vendor) == "" || entry.ParentSeriesID != "" || entry.MountPoint != "" || entry.FSType != "" || entry.CapacityBytes != 0 {
				return fmt.Errorf("GPU inventory row %d has fields for another kind", i)
			}
		}
	}
	if diskCount > MaxDiskEntries || gpuCount > MaxGPUEntries {
		return fmt.Errorf("device inventory exceeds entry cap")
	}
	if err := validateInventoryRelationships(metric.Devices, allowMissingParents); err != nil {
		return err
	}
	if encodedLen(metric) > telemetryprotocol.MaxMetricsBytes {
		return fmt.Errorf("device inventory exceeds encoded byte cap")
	}
	return nil
}

func validateInventoryRelationships(entries []InventoryEntry, allowMissingParents bool) error {
	kinds := make(map[string]Kind, len(entries))
	parents := make(map[string]string, len(entries))
	for _, entry := range entries {
		kinds[entry.SeriesID] = entry.Kind
		parents[entry.SeriesID] = entry.ParentSeriesID
	}
	for i, entry := range entries {
		if entry.ParentSeriesID == "" {
			continue
		}
		parentKind, retained := kinds[entry.ParentSeriesID]
		if !retained && !allowMissingParents {
			return fmt.Errorf("device inventory row %d has unknown parent", i)
		}
		if retained && parentKind != KindBlockDevice {
			return fmt.Errorf("device inventory row %d has non-block parent", i)
		}
	}
	colors := make(map[string]uint8, len(entries))
	var visit func(string) bool
	visit = func(seriesID string) bool {
		switch colors[seriesID] {
		case 1:
			return false
		case 2:
			return true
		}
		colors[seriesID] = 1
		parentID := parents[seriesID]
		if kinds[parentID] == KindBlockDevice && !visit(parentID) {
			return false
		}
		colors[seriesID] = 2
		return true
	}
	for seriesID, kind := range kinds {
		if kind == KindBlockDevice && !visit(seriesID) {
			return fmt.Errorf("device inventory has a retained parent cycle")
		}
	}
	return nil
}

func ValidateSamples(metric SamplesMetric) error {
	if metric.Samples == nil {
		return fmt.Errorf("device samples must be an array")
	}
	if metric.Truncated < 0 {
		return fmt.Errorf("device samples have negative truncated count")
	}
	diskCount, gpuCount := 0, 0
	seen := make(map[string]struct{}, len(metric.Samples))
	for i, sample := range metric.Samples {
		if !validKind(sample.Kind) || !validSeriesID(sample.SeriesID) {
			return fmt.Errorf("device sample row %d has invalid identity", i)
		}
		if _, exists := seen[sample.SeriesID]; exists {
			return fmt.Errorf("device samples have duplicate series id")
		}
		seen[sample.SeriesID] = struct{}{}
		if i > 0 && !less(metric.Samples[i-1].Kind, metric.Samples[i-1].SeriesID, sample.Kind, sample.SeriesID) {
			return fmt.Errorf("device samples are not strictly sorted")
		}
		if len(sample.Values) == 0 {
			return fmt.Errorf("device sample row %d has no values", i)
		}
		for key, value := range sample.Values {
			kind, exists := numericKind[key]
			if !exists || kind != sample.Kind {
				return fmt.Errorf("device sample row %d has invalid numeric key", i)
			}
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				return fmt.Errorf("device sample row %d has invalid numeric value", i)
			}
			if isPercent(key) && value > 100 {
				return fmt.Errorf("device sample row %d has percentage outside range", i)
			}
		}
		if sample.Kind == KindGPU {
			gpuCount++
		} else {
			diskCount++
		}
	}
	if diskCount > MaxDiskEntries || gpuCount > MaxGPUEntries {
		return fmt.Errorf("device samples exceed entry cap")
	}
	if encodedLen(metric) > telemetryprotocol.MaxMetricsBytes {
		return fmt.Errorf("device samples exceed encoded byte cap")
	}
	return nil
}

func capInventory(entries []InventoryEntry) ([]InventoryEntry, int) {
	out := entries[:0]
	diskCount, gpuCount, truncated := 0, 0, 0
	for _, entry := range entries {
		if entry.Kind == KindGPU {
			if gpuCount == MaxGPUEntries {
				truncated++
				continue
			}
			gpuCount++
		} else {
			if diskCount == MaxDiskEntries {
				truncated++
				continue
			}
			diskCount++
		}
		out = append(out, entry)
	}
	return out, truncated
}

func capSamples(samples []Sample) ([]Sample, int) {
	out := samples[:0]
	diskCount, gpuCount, truncated := 0, 0, 0
	for _, sample := range samples {
		if sample.Kind == KindGPU {
			if gpuCount == MaxGPUEntries {
				truncated++
				continue
			}
			gpuCount++
		} else {
			if diskCount == MaxDiskEntries {
				truncated++
				continue
			}
			diskCount++
		}
		out = append(out, sample)
	}
	return out, truncated
}

func cloneSamples(samples []Sample) []Sample {
	out := make([]Sample, len(samples))
	for i, sample := range samples {
		out[i] = sample
		if sample.Values != nil {
			out[i].Values = make(map[NumericKey]float64, len(sample.Values))
			for key, value := range sample.Values {
				out[i].Values[key] = value
			}
		}
	}
	return out
}

func less(aKind Kind, aID string, bKind Kind, bID string) bool {
	if aKind != bKind {
		return aKind < bKind
	}
	return aID < bID
}

func encodedLen(value any) int {
	raw, err := json.Marshal(value)
	if err != nil {
		return telemetryprotocol.MaxMetricsBytes + 1
	}
	return len(raw)
}

func encodedPairLen(inventory InventoryMetric, samples SamplesMetric) int {
	return encodedLen(struct {
		Inventory InventoryMetric `json:"device_inventory"`
		Samples   SamplesMetric   `json:"device_samples"`
	}{Inventory: inventory, Samples: samples})
}

func validKind(kind Kind) bool {
	return kind == KindBlockDevice || kind == KindFilesystem || kind == KindGPU
}

func validStatus(status Status) bool {
	switch status {
	case StatusOK, StatusToolMissing, StatusDriverUnavailable, StatusMetricsUnavailable, StatusUnsupported, StatusCollectionError:
		return true
	default:
		return false
	}
}

func validSeriesID(id string) bool {
	if len(id) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(id)
	return err == nil && hex.EncodeToString(decoded) == id
}

func validDisplay(value string, max int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("is empty")
	}
	if len(value) > max {
		return fmt.Errorf("is too long")
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("is not UTF-8")
	}
	for _, r := range value {
		if !unicode.IsGraphic(r) {
			return fmt.Errorf("contains a non-graphic character")
		}
	}
	return nil
}

func isPercent(key NumericKey) bool {
	return key == DiskFilesystemUsedPct || key == DiskIOBusyPct || key == GPUUtilizationPct || key == GPUVRAMUsedPct
}
