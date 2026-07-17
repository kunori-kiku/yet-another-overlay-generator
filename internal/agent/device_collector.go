package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/devicemetric"
)

const (
	deviceCollectionTimeout = 3 * time.Second
	deviceCommandTimeout    = 2 * time.Second
	maxDeviceCommandOutput  = 64 << 10
)

var (
	errDeviceCommandOutputLimit = errors.New("device command output limit exceeded")
	errDeviceCommandStart       = errors.New("device command start failed")
	errDeviceCommandExit        = errors.New("device command exited unsuccessfully")
	errDeviceCommandIO          = errors.New("device command output failed")
)

type deviceCommandRunner interface {
	Run(ctx context.Context, path string, args ...string) (stdout []byte, err error)
}

type deviceCollectorDeps struct {
	ProcRoot          string
	SysRoot           string
	Now               func() time.Time
	CollectionTimeout time.Duration
	Run               deviceCommandRunner
	ResolveNvidiaSMI  func() (path string, ok bool)
	StatFilesystem    func(path string) (filesystemStat, error)
}

type filesystemStat struct {
	Blocks    uint64
	Free      uint64
	BlockSize uint64
}

type diskCounterSnapshot struct {
	ReadSectors  uint64
	WriteSectors uint64
	IOMillis     uint64
	SampledAt    time.Time
}

type deviceCollector struct {
	deps     deviceCollectorDeps
	previous map[string]diskCounterSnapshot

	mu            sync.Mutex
	inFlight      bool
	lastInventory devicemetric.InventoryMetric
}

func newDeviceCollector(deps deviceCollectorDeps) *deviceCollector {
	if deps.ProcRoot == "" {
		deps.ProcRoot = "/proc"
	}
	if deps.SysRoot == "" {
		deps.SysRoot = "/sys"
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.CollectionTimeout <= 0 || deps.CollectionTimeout > deviceCollectionTimeout {
		deps.CollectionTimeout = deviceCollectionTimeout
	}
	if deps.Run == nil {
		deps.Run = boundedDeviceCommandRunner{}
	}
	if deps.ResolveNvidiaSMI == nil {
		deps.ResolveNvidiaSMI = resolveTrustedNvidiaSMI
	}
	if deps.StatFilesystem == nil {
		deps.StatFilesystem = statFilesystem
	}
	return &deviceCollector{deps: deps, previous: make(map[string]diskCounterSnapshot)}
}

type deviceCollectionResult struct {
	inventory devicemetric.InventoryMetric
	samples   devicemetric.SamplesMetric
	complete  bool
}

// Collect enforces one deadline around the otherwise synchronous OS providers and permits at most
// one provider worker. If a kernel/driver call cannot be interrupted, the caller still returns on
// time and later calls cannot accumulate blocked goroutines. A prior inventory is returned with an
// explicit collection_error and no numeric values; absent values remain chart gaps.
func (c *deviceCollector) Collect(ctx context.Context, now time.Time) (devicemetric.InventoryMetric, devicemetric.SamplesMetric) {
	if now.IsZero() {
		now = c.deps.Now()
	}
	ctx, cancel := context.WithTimeout(ctx, c.deps.CollectionTimeout)
	defer cancel()

	c.mu.Lock()
	if c.inFlight || ctx.Err() != nil {
		inventory, samples := c.degradedSnapshotLocked()
		c.mu.Unlock()
		return inventory, samples
	}
	c.inFlight = true
	c.mu.Unlock()

	done := make(chan deviceCollectionResult, 1)
	go func() {
		inventory, samples, complete := c.collectPlatform(ctx, now)
		result := deviceCollectionResult{inventory: cloneDeviceInventory(inventory), samples: cloneDeviceSamples(samples), complete: complete}
		c.mu.Lock()
		if complete {
			c.lastInventory = cloneDeviceInventory(inventory)
		}
		c.inFlight = false
		c.mu.Unlock()
		done <- result
	}()

	select {
	case result := <-done:
		return c.acceptCollectionResult(ctx, result)
	case <-ctx.Done():
		c.mu.Lock()
		inventory, samples := c.degradedSnapshotLocked()
		c.mu.Unlock()
		return inventory, samples
	}
}

func (c *deviceCollector) acceptCollectionResult(ctx context.Context, result deviceCollectionResult) (devicemetric.InventoryMetric, devicemetric.SamplesMetric) {
	if ctx.Err() == nil && result.complete {
		return result.inventory, result.samples
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.degradedSnapshotLocked()
}

// publishDiskPartial preserves the last GPU inventory so a blocked optional GPU provider degrades
// known GPUs instead of making them appear to disappear.
func (c *deviceCollector) publishDiskPartial(inventory devicemetric.InventoryMetric) {
	c.publishPartialInventory(inventory, func(kind devicemetric.Kind) bool { return kind == devicemetric.KindGPU })
}

// publishBlockPartial also preserves known filesystems until mount discovery finishes.
func (c *deviceCollector) publishBlockPartial(inventory devicemetric.InventoryMetric) {
	c.publishPartialInventory(inventory, func(kind devicemetric.Kind) bool {
		return kind == devicemetric.KindFilesystem || kind == devicemetric.KindGPU
	})
}

func (c *deviceCollector) publishPartialInventory(inventory devicemetric.InventoryMetric, preserve func(devicemetric.Kind) bool) {
	c.mu.Lock()
	partial := cloneDeviceInventory(inventory)
	emptySamples := devicemetric.SamplesMetric{Samples: []devicemetric.Sample{}}
	for _, entry := range c.lastInventory.Devices {
		if !preserve(entry.Kind) {
			continue
		}
		candidate := cloneDeviceInventory(partial)
		candidate.Devices = append(candidate.Devices, entry)
		if devicemetric.ValidatePair(candidate, emptySamples) == nil {
			partial = candidate
		} else {
			partial.Truncated++
		}
	}
	c.lastInventory = partial
	c.mu.Unlock()
}

func (c *deviceCollector) degradedSnapshotLocked() (devicemetric.InventoryMetric, devicemetric.SamplesMetric) {
	inventory := cloneDeviceInventory(c.lastInventory)
	if inventory.Devices == nil {
		inventory.Devices = []devicemetric.InventoryEntry{}
	}
	for i := range inventory.Devices {
		inventory.Devices[i].Status = devicemetric.StatusCollectionError
	}
	return inventory, devicemetric.SamplesMetric{Samples: []devicemetric.Sample{}}
}

func (c *deviceCollector) previousInventoryForKind(kind devicemetric.Kind) []devicemetric.InventoryEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries := make([]devicemetric.InventoryEntry, 0)
	for _, entry := range c.lastInventory.Devices {
		if entry.Kind != kind {
			continue
		}
		entry.Status = devicemetric.StatusCollectionError
		entries = append(entries, entry)
	}
	return entries
}

func cloneDeviceInventory(metric devicemetric.InventoryMetric) devicemetric.InventoryMetric {
	return devicemetric.InventoryMetric{Devices: append([]devicemetric.InventoryEntry{}, metric.Devices...), Truncated: metric.Truncated}
}

func cloneDeviceSamples(metric devicemetric.SamplesMetric) devicemetric.SamplesMetric {
	out := devicemetric.SamplesMetric{Samples: make([]devicemetric.Sample, len(metric.Samples)), Truncated: metric.Truncated}
	for i, sample := range metric.Samples {
		out.Samples[i] = sample
		out.Samples[i].Values = make(map[devicemetric.NumericKey]float64, len(sample.Values))
		for key, value := range sample.Values {
			out.Samples[i].Values[key] = value
		}
	}
	return out
}

// boundedDeviceCommandRunner executes one already-resolved absolute program directly. Output is
// streamed through a cap; an overflowing or canceled child is killed and Wait is called exactly once
// so no process is left behind. Stderr is intentionally discarded and never reaches telemetry.
type boundedDeviceCommandRunner struct{}

func (boundedDeviceCommandRunner) Run(ctx context.Context, path string, args ...string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errDeviceCommandStart
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = nil
	cmd.Stderr = io.Discard
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	cmd.WaitDelay = 250 * time.Millisecond
	configureDeviceCommand(cmd)
	cmd.Cancel = func() error {
		terminateDeviceCommand(cmd)
		return nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errDeviceCommandIO
	}
	if err := cmd.Start(); err != nil {
		return nil, errDeviceCommandStart
	}

	type readResult struct {
		output   []byte
		err      error
		overflow bool
	}
	readDone := make(chan readResult, 1)
	go func() {
		var output bytes.Buffer
		_, readErr := io.Copy(&output, io.LimitReader(stdout, maxDeviceCommandOutput+1))
		readDone <- readResult{output: output.Bytes(), err: readErr, overflow: output.Len() > maxDeviceCommandOutput}
	}()

	var result readResult
	select {
	case result = <-readDone:
		if result.overflow || result.err != nil {
			terminateDeviceCommand(cmd)
		}
	case <-ctx.Done():
		terminateDeviceCommand(cmd)
		_ = stdout.Close()
		result = <-readDone
	}
	waitErr := cmd.Wait()
	if result.overflow {
		return nil, errDeviceCommandOutputLimit
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if result.err != nil {
		return nil, errDeviceCommandIO
	}
	if waitErr != nil {
		return nil, errDeviceCommandExit
	}
	return result.output, nil
}

func finalizeDeviceMetrics(entries []devicemetric.InventoryEntry, samples []devicemetric.Sample) (devicemetric.InventoryMetric, devicemetric.SamplesMetric, error) {
	inventory, numeric, err := devicemetric.BoundMetrics(entries, samples)
	if err != nil {
		// A provider bug must not leak an invalid payload into reliable admission. Preserve neither a
		// malformed identity nor a fabricated value; subsequent collections can recover normally.
		return devicemetric.InventoryMetric{Devices: []devicemetric.InventoryEntry{}}, devicemetric.SamplesMetric{Samples: []devicemetric.Sample{}}, err
	}
	return inventory, numeric, nil
}

func sanitizeDeviceDisplay(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	var out strings.Builder
	out.Grow(min(len(value), maxBytes))
	for _, r := range value {
		if !unicode.IsGraphic(r) {
			r = ' '
		}
		if out.Len()+utf8.RuneLen(r) > maxBytes {
			break
		}
		out.WriteRune(r)
	}
	return strings.TrimSpace(out.String())
}
