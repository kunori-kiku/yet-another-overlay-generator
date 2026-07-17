//go:build linux

package agent

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/devicemetric"
)

const (
	maxDeviceAttributeBytes     = 4 << 10
	maxMountInfoBytes           = 2 << 20
	maxBlockDiscoveryCandidates = 1024
	maxGPUDiscoveryCandidates   = 256
	maxNvidiaDiscoveryRows      = 256
	linuxAccountingSector       = uint64(512)
)

var (
	pciAddressPattern       = regexp.MustCompile(`(?i)^([0-9a-f]{4,8}):([0-9a-f]{2}):([0-9a-f]{2})\.([0-7])$`)
	errDeviceDiscoveryLimit = errors.New("device discovery limit exceeded")
)

func configureDeviceCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateDeviceCommand(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// The trusted tool should not fork, but killing its dedicated group also closes inherited stdout
	// if a broken wrapper leaves a descendant behind. The direct kill is a fallback for races.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}

type linuxDeviceNumber struct {
	major uint64
	minor uint64
}

func (d linuxDeviceNumber) String() string {
	return strconv.FormatUint(d.major, 10) + ":" + strconv.FormatUint(d.minor, 10)
}

type linuxBlockCandidate struct {
	name         string
	realPath     string
	relativePath string
	device       linuxDeviceNumber
	partition    uint64
	parentPath   string
	seriesID     string
	identityKind string
	parentID     string
	capacity     uint64
	vendor       string
	model        string
	status       devicemetric.Status
	counter      *diskCounterSnapshot
}

type linuxMountCandidate struct {
	device     linuxDeviceNumber
	root       []byte
	mountPoint []byte
	fsType     []byte
	block      *linuxBlockCandidate
}

type linuxGPUCandidate struct {
	realPath     string
	relativePath string
	seriesID     string
	pci          string
	localUnique  string
	vendorCode   string
	vendor       string
	label        string
	model        string
	status       devicemetric.Status
	values       map[devicemetric.NumericKey]float64
	vramTotal    uint64
}

type nvidiaToolGPU struct {
	uuid      string
	pci       string
	label     string
	model     string
	status    devicemetric.Status
	values    map[devicemetric.NumericKey]float64
	vramTotal uint64
}

func (c *deviceCollector) collectPlatform(ctx context.Context, now time.Time) (devicemetric.InventoryMetric, devicemetric.SamplesMetric, bool) {
	diskInventory, diskSamples, nextPrevious, diskErr := c.collectLinuxDisks(ctx, now)
	c.previous = nextPrevious
	diskMetric, diskNumeric, boundErr := finalizeDeviceMetrics(diskInventory, diskSamples)
	if diskErr == nil {
		diskErr = boundErr
	}
	if diskErr == nil {
		c.publishDiskPartial(diskMetric)
	} else if len(diskMetric.Devices) > 0 {
		c.publishBlockPartial(diskMetric)
	}
	if diskErr != nil {
		return diskMetric, diskNumeric, false
	}
	gpuInventory, gpuSamples, gpuErr := c.collectLinuxGPUs(ctx)
	if gpuErr != nil {
		if ctx.Err() != nil {
			return diskMetric, diskNumeric, false
		}
		gpuInventory = c.previousInventoryForKind(devicemetric.KindGPU)
		gpuSamples = nil
	}
	inventory, samples, err := finalizeDeviceMetrics(append(diskInventory, gpuInventory...), append(diskSamples, gpuSamples...))
	if err == nil {
		return inventory, samples, true
	}
	previousGPUs := c.previousInventoryForKind(devicemetric.KindGPU)
	inventory, samples, err = finalizeDeviceMetrics(append(diskInventory, previousGPUs...), diskSamples)
	if err == nil {
		return inventory, samples, true
	}
	return diskMetric, diskNumeric, true
}

func (c *deviceCollector) collectLinuxDisks(ctx context.Context, now time.Time) ([]devicemetric.InventoryEntry, []devicemetric.Sample, map[string]diskCounterSnapshot, error) {
	blocks, err := enumerateLinuxBlocks(ctx, c.deps.SysRoot, now)
	if err != nil {
		return nil, nil, make(map[string]diskCounterSnapshot), err
	}
	resolveLinuxBlockIdentities(ctx, blocks)
	if ctx.Err() != nil {
		return nil, nil, make(map[string]diskCounterSnapshot), ctx.Err()
	}
	resolvedBlocks := blocks[:0]
	for _, block := range blocks {
		if block.seriesID != "" {
			resolvedBlocks = append(resolvedBlocks, block)
		}
	}
	blocks = resolvedBlocks
	byDevice := make(map[linuxDeviceNumber]*linuxBlockCandidate, len(blocks))
	byPath := make(map[string]*linuxBlockCandidate, len(blocks))
	for _, block := range blocks {
		byDevice[block.device] = block
		byPath[block.realPath] = block
	}
	resolveLinuxBlockParents(ctx, c.deps.SysRoot, blocks, byPath)
	if ctx.Err() != nil {
		return nil, nil, make(map[string]diskCounterSnapshot), ctx.Err()
	}

	inventory := make([]devicemetric.InventoryEntry, 0, len(blocks))
	samples := make([]devicemetric.Sample, 0, len(blocks))
	nextPrevious := make(map[string]diskCounterSnapshot, len(blocks))
	for _, block := range blocks {
		if ctx.Err() != nil {
			break
		}
		inventory = append(inventory, devicemetric.InventoryEntry{
			SeriesID: block.seriesID, Kind: devicemetric.KindBlockDevice,
			Label:          sanitizeDeviceDisplay(block.name, devicemetric.MaxLabelBytes),
			ParentSeriesID: block.parentID,
			Vendor:         sanitizeDeviceDisplay(block.vendor, devicemetric.MaxVendorBytes),
			Model:          sanitizeDeviceDisplay(block.model, devicemetric.MaxModelBytes),
			CapacityBytes:  block.capacity, Status: block.status,
		})
		if block.counter == nil {
			continue
		}
		current := *block.counter
		nextPrevious[block.seriesID] = current
		if previous, ok := c.previous[block.seriesID]; ok {
			values := diskCounterValues(previous, current)
			if len(values) > 0 {
				samples = append(samples, devicemetric.Sample{SeriesID: block.seriesID, Kind: devicemetric.KindBlockDevice, Values: values})
			}
		}
	}

	partialInventory, _, err := finalizeDeviceMetrics(inventory, samples)
	if err != nil {
		return inventory, samples, nextPrevious, err
	}
	if len(partialInventory.Devices) > 0 {
		c.publishBlockPartial(partialInventory)
	}
	mounts, err := readLinuxMounts(ctx, c.deps.ProcRoot, c.deps.SysRoot, byDevice)
	if err != nil {
		return inventory, samples, nextPrevious, err
	}
	for _, mount := range mounts {
		if ctx.Err() != nil {
			break
		}
		seriesID := devicemetric.SeriesID(devicemetric.KindFilesystem, frameDeviceIdentity("filesystem-v1", []byte(mount.block.seriesID), mount.root))
		entry := devicemetric.InventoryEntry{
			SeriesID: seriesID, Kind: devicemetric.KindFilesystem,
			Label:          sanitizeDeviceDisplay(strings.ToValidUTF8(string(mount.mountPoint), "�"), devicemetric.MaxLabelBytes),
			ParentSeriesID: mount.block.seriesID,
			MountPoint:     sanitizeDeviceDisplay(strings.ToValidUTF8(string(mount.mountPoint), "�"), devicemetric.MaxMountPointBytes),
			FSType:         sanitizeDeviceDisplay(strings.ToValidUTF8(string(mount.fsType), "�"), devicemetric.MaxFSTypeBytes),
			Status:         devicemetric.StatusCollectionError,
		}
		if entry.Label == "" {
			entry.Label = "Filesystem"
		}
		if entry.MountPoint == "" || entry.FSType == "" {
			continue
		}
		stat, err := c.deps.StatFilesystem(string(mount.mountPoint))
		if err == nil {
			capacity, used, ok := filesystemUsage(stat)
			if ok {
				entry.CapacityBytes = capacity
				entry.Status = devicemetric.StatusOK
				samples = append(samples, devicemetric.Sample{
					SeriesID: seriesID, Kind: devicemetric.KindFilesystem,
					Values: map[devicemetric.NumericKey]float64{devicemetric.DiskFilesystemUsedPct: used},
				})
			} else {
				entry.Status = devicemetric.StatusMetricsUnavailable
			}
		}
		inventory = append(inventory, entry)
	}
	if ctx.Err() != nil {
		return inventory, samples, nextPrevious, ctx.Err()
	}
	return inventory, samples, nextPrevious, nil
}

func enumerateLinuxBlocks(ctx context.Context, sysRoot string, now time.Time) ([]*linuxBlockCandidate, error) {
	classRoot := filepath.Join(sysRoot, "class", "block")
	blocks := make([]*linuxBlockCandidate, 0, devicemetric.MaxDiskEntries)
	overflow := false
	walkErr := forEachDirectoryEntry(ctx, classRoot, func(entry os.DirEntry) bool {
		name := entry.Name()
		if excludedBlockName(name) {
			return true
		}
		classPath := filepath.Join(classRoot, name)
		realPath, relativePath, ok := resolveWithinRoot(sysRoot, classPath)
		if !ok {
			return true
		}
		device, ok := readLinuxDeviceNumber(filepath.Join(realPath, "dev"))
		if !ok {
			return true
		}
		devLink, _, ok := resolveWithinRoot(sysRoot, filepath.Join(sysRoot, "dev", "block", device.String()))
		if !ok || filepath.Clean(devLink) != filepath.Clean(realPath) {
			return true
		}
		candidate := &linuxBlockCandidate{
			name: name, realPath: realPath, relativePath: relativePath, device: device,
			status: devicemetric.StatusMetricsUnavailable,
		}
		candidate.partition, _ = readUintAttribute(filepath.Join(realPath, "partition"))
		if candidate.partition > 0 {
			candidate.parentPath = filepath.Dir(realPath)
		}
		capacityOK := false
		if sectors, present, valid := readOptionalUintAttribute(filepath.Join(realPath, "size")); present {
			if valid && sectors <= math.MaxUint64/linuxAccountingSector {
				candidate.capacity = sectors * linuxAccountingSector
				capacityOK = true
			} else {
				candidate.status = devicemetric.StatusCollectionError
			}
		}
		candidate.vendor = readDisplayAttribute(filepath.Join(realPath, "device", "vendor"))
		candidate.model = readDisplayAttribute(filepath.Join(realPath, "device", "model"))
		if raw, err := readBoundedFile(filepath.Join(realPath, "stat"), maxDeviceAttributeBytes); err == nil {
			if snapshot, ok := parseBlockStat(raw, now); ok {
				candidate.counter = &snapshot
				if capacityOK && candidate.status != devicemetric.StatusCollectionError {
					candidate.status = devicemetric.StatusOK
				}
			} else {
				candidate.status = devicemetric.StatusCollectionError
			}
		}
		if len(blocks) == maxBlockDiscoveryCandidates {
			overflow = true
			return false
		}
		blocks = append(blocks, candidate)
		return ctx.Err() == nil
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if overflow {
		return nil, errDeviceDiscoveryLimit
	}
	if walkErr != nil {
		return nil, walkErr
	}
	return blocks, nil
}

func resolveLinuxBlockIdentities(ctx context.Context, blocks []*linuxBlockCandidate) {
	byPath := make(map[string]*linuxBlockCandidate, len(blocks))
	for _, block := range blocks {
		byPath[block.realPath] = block
	}
	visiting := make(map[string]bool, len(blocks))
	var resolve func(*linuxBlockCandidate)
	resolve = func(block *linuxBlockCandidate) {
		if ctx.Err() != nil || block.seriesID != "" {
			return
		}
		if visiting[block.realPath] {
			block.identityKind = "sysfs-fallback-v1"
			block.seriesID = fallbackBlockSeriesID(block)
			return
		}
		visiting[block.realPath] = true
		defer delete(visiting, block.realPath)
		identitySources := []struct {
			kind string
			path string
		}{
			{"block-wwid-v1", filepath.Join(block.realPath, "wwid")},
			{"device-wwid-v1", filepath.Join(block.realPath, "device", "wwid")},
			{"serial-v1", filepath.Join(block.realPath, "device", "serial")},
			{"dm-uuid-v1", filepath.Join(block.realPath, "dm", "uuid")},
		}
		for _, source := range identitySources {
			if ctx.Err() != nil {
				return
			}
			if value, ok := readIdentityAttribute(source.path); ok {
				block.identityKind = source.kind
				block.seriesID = devicemetric.SeriesID(devicemetric.KindBlockDevice, frameDeviceIdentity(source.kind, value))
				return
			}
		}
		if block.partition > 0 {
			if parent := byPath[block.parentPath]; parent != nil {
				resolve(parent)
				block.identityKind = "partition-v1"
				block.seriesID = devicemetric.SeriesID(devicemetric.KindBlockDevice, frameDeviceIdentity(
					"partition-v1", []byte(parent.seriesID), []byte(strconv.FormatUint(block.partition, 10)),
				))
				return
			}
		}
		block.identityKind = "sysfs-fallback-v1"
		block.seriesID = fallbackBlockSeriesID(block)
	}
	for _, block := range blocks {
		if ctx.Err() != nil {
			break
		}
		resolve(block)
	}

	// Ambiguous supposedly-strong identifiers must never merge distinct devices. Re-key colliding
	// non-partitions first, then derive every partition from its final parent. Any genuinely
	// duplicate partition identity falls back locally without changing an unrelated sibling.
	counts := countBlockSeries(blocks)
	for _, block := range blocks {
		if ctx.Err() != nil {
			break
		}
		if block.partition == 0 && block.seriesID != "" && counts[block.seriesID] > 1 {
			block.identityKind = "sysfs-fallback-v1"
			block.seriesID = fallbackBlockSeriesID(block)
		}
	}
	recomputePartitionIdentities(blocks, byPath)
	counts = countBlockSeries(blocks)
	for _, block := range blocks {
		if block.seriesID != "" && counts[block.seriesID] > 1 {
			block.identityKind = "sysfs-fallback-v1"
			block.seriesID = fallbackBlockSeriesID(block)
		}
	}
	// A partition can itself be the parent of an unusual stacked partition. Refresh its descendants
	// once after a collision fallback, then defensively separate any remaining duplicate.
	recomputePartitionIdentities(blocks, byPath)
	counts = countBlockSeries(blocks)
	for _, block := range blocks {
		if block.seriesID != "" && counts[block.seriesID] > 1 {
			block.identityKind = "sysfs-fallback-v1"
			block.seriesID = fallbackBlockSeriesID(block)
		}
	}
}

func countBlockSeries(blocks []*linuxBlockCandidate) map[string]int {
	counts := make(map[string]int, len(blocks))
	for _, block := range blocks {
		if block.seriesID != "" {
			counts[block.seriesID]++
		}
	}
	return counts
}

func recomputePartitionIdentities(blocks []*linuxBlockCandidate, byPath map[string]*linuxBlockCandidate) {
	done := make(map[string]bool, len(blocks))
	visiting := make(map[string]bool, len(blocks))
	var refresh func(*linuxBlockCandidate)
	refresh = func(block *linuxBlockCandidate) {
		if done[block.realPath] || visiting[block.realPath] || block.identityKind != "partition-v1" {
			return
		}
		visiting[block.realPath] = true
		if parent := byPath[block.parentPath]; parent != nil {
			refresh(parent)
			block.seriesID = devicemetric.SeriesID(devicemetric.KindBlockDevice, frameDeviceIdentity(
				"partition-v1", []byte(parent.seriesID), []byte(strconv.FormatUint(block.partition, 10)),
			))
		}
		delete(visiting, block.realPath)
		done[block.realPath] = true
	}
	for _, block := range blocks {
		refresh(block)
	}
}

func resolveLinuxBlockParents(ctx context.Context, sysRoot string, blocks []*linuxBlockCandidate, byPath map[string]*linuxBlockCandidate) {
	for _, block := range blocks {
		if ctx.Err() != nil {
			break
		}
		if block.partition > 0 {
			if parent := byPath[block.parentPath]; parent != nil {
				block.parentID = parent.seriesID
				continue
			}
		}
		parents := make(map[string]struct{}, 2)
		_ = forEachDirectoryEntry(ctx, filepath.Join(block.realPath, "slaves"), func(slave os.DirEntry) bool {
			realPath, _, ok := resolveWithinRoot(sysRoot, filepath.Join(block.realPath, "slaves", slave.Name()))
			if !ok {
				return true
			}
			if parent := byPath[realPath]; parent != nil {
				parents[parent.seriesID] = struct{}{}
			}
			return ctx.Err() == nil
		})
		if len(parents) == 1 {
			for parent := range parents {
				block.parentID = parent
			}
		}
	}
}

func fallbackBlockSeriesID(block *linuxBlockCandidate) string {
	return devicemetric.SeriesID(devicemetric.KindBlockDevice, frameDeviceIdentity(
		"sysfs-fallback-v1", []byte(filepath.ToSlash(block.relativePath)), []byte(block.device.String()),
	))
}

func diskCounterValues(previous, current diskCounterSnapshot) map[devicemetric.NumericKey]float64 {
	elapsed := current.SampledAt.Sub(previous.SampledAt)
	seconds := elapsed.Seconds()
	if elapsed <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return nil
	}
	values := make(map[devicemetric.NumericKey]float64, 3)
	if current.ReadSectors >= previous.ReadSectors {
		delta := current.ReadSectors - previous.ReadSectors
		if delta <= math.MaxUint64/linuxAccountingSector {
			value := float64(delta*linuxAccountingSector) / seconds
			if !math.IsInf(value, 0) && !math.IsNaN(value) {
				values[devicemetric.DiskReadBytesPerSecond] = value
			}
		}
	}
	if current.WriteSectors >= previous.WriteSectors {
		delta := current.WriteSectors - previous.WriteSectors
		if delta <= math.MaxUint64/linuxAccountingSector {
			value := float64(delta*linuxAccountingSector) / seconds
			if !math.IsInf(value, 0) && !math.IsNaN(value) {
				values[devicemetric.DiskWriteBytesPerSecond] = value
			}
		}
	}
	if current.IOMillis >= previous.IOMillis {
		value := float64(current.IOMillis-previous.IOMillis) / (float64(elapsed) / float64(time.Millisecond)) * 100
		if !math.IsInf(value, 0) && !math.IsNaN(value) && value <= 100.5 {
			values[devicemetric.DiskIOBusyPct] = min(value, 100)
		}
	}
	return values
}

func parseBlockStat(raw []byte, sampledAt time.Time) (diskCounterSnapshot, bool) {
	fields := strings.Fields(string(raw))
	if len(fields) < 11 {
		return diskCounterSnapshot{}, false
	}
	read, errRead := strconv.ParseUint(fields[2], 10, 64)
	write, errWrite := strconv.ParseUint(fields[6], 10, 64)
	ioMillis, errIO := strconv.ParseUint(fields[9], 10, 64)
	if errRead != nil || errWrite != nil || errIO != nil {
		return diskCounterSnapshot{}, false
	}
	return diskCounterSnapshot{ReadSectors: read, WriteSectors: write, IOMillis: ioMillis, SampledAt: sampledAt}, true
}

func readLinuxMounts(ctx context.Context, procRoot, sysRoot string, byDevice map[linuxDeviceNumber]*linuxBlockCandidate) ([]linuxMountCandidate, error) {
	raw, err := readBoundedFileContext(ctx, filepath.Join(procRoot, "self", "mountinfo"), maxMountInfoBytes)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]linuxMountCandidate)
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if ctx.Err() != nil {
			break
		}
		fields := bytes.Split(line, []byte{' '})
		if len(fields) < 10 {
			continue
		}
		separator := -1
		for i := 6; i < len(fields); i++ {
			if bytes.Equal(fields[i], []byte{'-'}) {
				separator = i
				break
			}
		}
		if separator < 0 || separator+3 >= len(fields) || len(fields[3]) == 0 || len(fields[4]) == 0 || len(fields[5]) == 0 {
			continue
		}
		device, ok := parseLinuxDeviceNumber(fields[2])
		if !ok {
			continue
		}
		block := byDevice[device]
		if block == nil || !deviceLinkMatches(sysRoot, device, block.realPath) {
			continue
		}
		root, rootOK := decodeMountInfoPath(fields[3])
		mountPoint, mountOK := decodeMountInfoPath(fields[4])
		fsType, fsOK := decodeMountInfoPath(fields[separator+1])
		if !rootOK || !mountOK || !fsOK || len(mountPoint) == 0 || len(fsType) == 0 {
			continue
		}
		key := device.String() + "\x00" + string(root)
		candidate := linuxMountCandidate{device: device, root: root, mountPoint: mountPoint, fsType: fsType, block: block}
		current, exists := byKey[key]
		if !exists || len(candidate.mountPoint) < len(current.mountPoint) ||
			(len(candidate.mountPoint) == len(current.mountPoint) && bytes.Compare(candidate.mountPoint, current.mountPoint) < 0) {
			byKey[key] = candidate
		}
	}
	out := make([]linuxMountCandidate, 0, len(byKey))
	for _, mount := range byKey {
		out = append(out, mount)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].device != out[j].device {
			if out[i].device.major != out[j].device.major {
				return out[i].device.major < out[j].device.major
			}
			return out[i].device.minor < out[j].device.minor
		}
		return bytes.Compare(out[i].root, out[j].root) < 0
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out, nil
}

func decodeMountInfoPath(value []byte) ([]byte, bool) {
	out := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			out = append(out, value[i])
			continue
		}
		if i+3 >= len(value) {
			return nil, false
		}
		code := string(value[i+1 : i+4])
		switch code {
		case "040":
			out = append(out, ' ')
		case "011":
			out = append(out, '\t')
		case "012":
			out = append(out, '\n')
		case "134":
			out = append(out, '\\')
		default:
			return nil, false
		}
		i += 3
	}
	return out, true
}

func filesystemUsage(stat filesystemStat) (uint64, float64, bool) {
	if stat.BlockSize == 0 || stat.Free > stat.Blocks || stat.Blocks == 0 || stat.Blocks > math.MaxUint64/stat.BlockSize {
		return 0, 0, false
	}
	capacity := stat.Blocks * stat.BlockSize
	used := float64(stat.Blocks-stat.Free) / float64(stat.Blocks) * 100
	if math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 100 {
		return 0, 0, false
	}
	return capacity, used, true
}

func statFilesystem(path string) (filesystemStat, error) {
	var raw syscall.Statfs_t
	if err := syscall.Statfs(path, &raw); err != nil {
		return filesystemStat{}, errors.New("filesystem stat failed")
	}
	blockSize := raw.Frsize
	if blockSize <= 0 {
		blockSize = raw.Bsize
	}
	if blockSize <= 0 {
		return filesystemStat{}, errors.New("filesystem stat unavailable")
	}
	return filesystemStat{Blocks: raw.Blocks, Free: raw.Bfree, BlockSize: uint64(blockSize)}, nil
}

func (c *deviceCollector) collectLinuxGPUs(ctx context.Context) ([]devicemetric.InventoryEntry, []devicemetric.Sample, error) {
	sysfs, err := enumerateLinuxGPUs(ctx, c.deps.SysRoot)
	if err != nil {
		return nil, nil, err
	}
	nvidiaRows, providerStatus := c.queryNvidia(ctx)
	usedTool := make([]bool, len(nvidiaRows))

	candidates := make([]linuxGPUCandidate, 0, len(sysfs)+len(nvidiaRows))
	for _, gpu := range sysfs {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if gpu.vendorCode == "0x10de" {
			matched := matchNvidiaToolRow(gpu, nvidiaRows, usedTool)
			if matched >= 0 {
				usedTool[matched] = true
				row := nvidiaRows[matched]
				gpu.status, gpu.values, gpu.vramTotal = row.status, row.values, row.vramTotal
				if row.model != "" {
					gpu.model, gpu.label = row.model, row.label
				}
			} else {
				gpu.status = providerStatus
			}
		}
		candidates = append(candidates, gpu)
	}
	for i, row := range nvidiaRows {
		if usedTool[i] {
			continue
		}
		gpu := linuxGPUCandidate{
			pci: row.pci, localUnique: row.uuid, vendorCode: "0x10de", vendor: "NVIDIA",
			label: row.label, model: row.model, status: row.status, values: row.values, vramTotal: row.vramTotal,
		}
		candidates = append(candidates, gpu)
	}
	assignGPUIdentities(candidates)
	entries := make([]devicemetric.InventoryEntry, 0, len(candidates))
	samples := make([]devicemetric.Sample, 0, len(candidates))
	for _, gpu := range candidates {
		entry, sample := gpuMetricRows(gpu)
		entries = append(entries, entry)
		if sample != nil {
			samples = append(samples, *sample)
		}
	}
	return entries, samples, nil
}

func matchNvidiaToolRow(gpu linuxGPUCandidate, rows []nvidiaToolGPU, used []bool) int {
	if gpu.localUnique != "" {
		for i, row := range rows {
			if !used[i] && row.uuid != "" && strings.EqualFold(row.uuid, gpu.localUnique) {
				return i
			}
		}
	}
	if gpu.pci != "" {
		for i, row := range rows {
			if !used[i] && row.pci == gpu.pci {
				return i
			}
		}
	}
	return -1
}

func enumerateLinuxGPUs(ctx context.Context, sysRoot string) ([]linuxGPUCandidate, error) {
	paths := make(map[string]struct{})
	overflow := false
	addPath := func(path string) bool {
		if _, exists := paths[path]; exists {
			return true
		}
		if len(paths) == maxGPUDiscoveryCandidates {
			overflow = true
			return false
		}
		paths[path] = struct{}{}
		return true
	}
	drmRoot := filepath.Join(sysRoot, "class", "drm")
	drmErr := forEachDirectoryEntry(ctx, drmRoot, func(entry os.DirEntry) bool {
		name := entry.Name()
		if isDRMCardName(name) {
			if realPath, _, ok := resolveWithinRoot(sysRoot, filepath.Join(drmRoot, name, "device")); ok {
				return addPath(realPath) && ctx.Err() == nil
			}
		}
		return ctx.Err() == nil
	})
	if drmErr != nil && !errors.Is(drmErr, os.ErrNotExist) {
		return nil, drmErr
	}
	if overflow || ctx.Err() != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errDeviceDiscoveryLimit
	}
	pciRoot := filepath.Join(sysRoot, "bus", "pci", "devices")
	pciErr := forEachDirectoryEntry(ctx, pciRoot, func(entry os.DirEntry) bool {
		path := filepath.Join(pciRoot, entry.Name())
		class, ok := readHexAttribute(filepath.Join(path, "class"))
		if ok && class>>16 == 0x03 {
			if realPath, _, ok := resolveWithinRoot(sysRoot, path); ok {
				return addPath(realPath) && ctx.Err() == nil
			}
		}
		return ctx.Err() == nil
	})
	if pciErr != nil && !errors.Is(pciErr, os.ErrNotExist) {
		return nil, pciErr
	}
	if overflow || ctx.Err() != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errDeviceDiscoveryLimit
	}
	realPaths := make([]string, 0, len(paths))
	for path := range paths {
		realPaths = append(realPaths, path)
	}
	sort.Strings(realPaths)
	out := make([]linuxGPUCandidate, 0, len(realPaths))
	for _, path := range realPaths {
		if ctx.Err() != nil {
			break
		}
		_, relativePath, ok := resolveWithinRoot(sysRoot, path)
		if !ok {
			continue
		}
		vendorRaw, vendorPresent, vendorValid := readOptionalHexAttribute(filepath.Join(path, "vendor"))
		vendorCode := ""
		status := devicemetric.StatusUnsupported
		if vendorPresent {
			if vendorValid && vendorRaw <= 0xffff {
				vendorCode = "0x" + strings.ToLower(strconv.FormatUint(vendorRaw, 16))
			} else {
				status = devicemetric.StatusCollectionError
			}
		}
		vendor, label := gpuVendorDisplay(vendorCode)
		candidate := linuxGPUCandidate{
			realPath: path, relativePath: relativePath, pci: pciFromSysfs(path),
			vendorCode: vendorCode, vendor: vendor, label: label, status: status,
		}
		candidate.localUnique = firstIdentityString(filepath.Join(path, "uuid"), filepath.Join(path, "unique_id"))
		candidate.model = firstDisplayString(filepath.Join(path, "product_name"), filepath.Join(path, "model"))
		if candidate.model != "" {
			candidate.label = vendor + " " + candidate.model
		}
		if vendorCode == "0x1002" {
			collectAMDGPU(sysRoot, path, &candidate)
		} else if vendorCode == "0x10de" {
			candidate.status = devicemetric.StatusToolMissing
		}
		out = append(out, candidate)
	}
	return out, nil
}

func collectAMDGPU(sysRoot, path string, gpu *linuxGPUCandidate) {
	driver, _, ok := resolveWithinRoot(sysRoot, filepath.Join(path, "driver"))
	if !ok {
		gpu.status = devicemetric.StatusDriverUnavailable
		return
	}
	if filepath.Base(driver) != "amdgpu" {
		gpu.status = devicemetric.StatusUnsupported
		return
	}
	values := make(map[devicemetric.NumericKey]float64, 2)
	valid, missing, invalid := 0, false, false
	if value, present, ok := readOptionalPercentAttribute(filepath.Join(path, "gpu_busy_percent")); present {
		if ok {
			values[devicemetric.GPUUtilizationPct] = value
			valid++
		} else {
			invalid = true
		}
	} else {
		missing = true
	}
	used, usedPresent, usedOK := readOptionalUintAttribute(filepath.Join(path, "mem_info_vram_used"))
	total, totalPresent, totalOK := readOptionalUintAttribute(filepath.Join(path, "mem_info_vram_total"))
	if usedPresent && !usedOK || totalPresent && !totalOK {
		invalid = true
	}
	if !usedPresent || !totalPresent {
		missing = true
	} else if usedOK && totalOK {
		if total == 0 {
			missing = true
		} else if used <= total {
			gpu.vramTotal = total
			values[devicemetric.GPUVRAMUsedPct] = float64(used) / float64(total) * 100
			valid++
		} else {
			invalid = true
		}
	}
	gpu.values = values
	switch {
	case invalid:
		gpu.status = devicemetric.StatusCollectionError
	case missing:
		gpu.status = devicemetric.StatusMetricsUnavailable
	case valid > 0:
		gpu.status = devicemetric.StatusOK
	default:
		gpu.status = devicemetric.StatusMetricsUnavailable
	}
}

func readOptionalUintAttribute(path string) (uint64, bool, bool) {
	if _, err := os.Stat(path); err != nil {
		return 0, false, false
	}
	value, ok := readUintAttribute(path)
	return value, true, ok
}

func readOptionalPercentAttribute(path string) (float64, bool, bool) {
	if _, err := os.Stat(path); err != nil {
		return 0, false, false
	}
	raw, err := readBoundedFile(path, 128)
	if err != nil {
		return 0, true, false
	}
	value, ok := parsePercentAttribute(raw)
	return value, true, ok
}

func (c *deviceCollector) queryNvidia(ctx context.Context) ([]nvidiaToolGPU, devicemetric.Status) {
	path, ok := c.deps.ResolveNvidiaSMI()
	if !ok {
		return nil, devicemetric.StatusToolMissing
	}
	childCtx, cancel := context.WithTimeout(ctx, deviceCommandTimeout)
	defer cancel()
	raw, err := c.deps.Run.Run(childCtx, path,
		"--query-gpu=uuid,pci.bus_id,name,utilization.gpu,memory.used,memory.total",
		"--format=csv,noheader,nounits",
	)
	if err != nil {
		if errors.Is(err, errDeviceCommandExit) {
			return nil, devicemetric.StatusDriverUnavailable
		}
		return nil, devicemetric.StatusCollectionError
	}
	if len(raw) > maxDeviceCommandOutput {
		return nil, devicemetric.StatusCollectionError
	}
	rows, malformed := parseNvidiaCSV(raw)
	if len(rows) == 0 {
		if malformed {
			return nil, devicemetric.StatusCollectionError
		}
		return nil, devicemetric.StatusDriverUnavailable
	}
	if malformed {
		return rows, devicemetric.StatusCollectionError
	}
	return rows, devicemetric.StatusDriverUnavailable
}

func parseNvidiaCSV(raw []byte) ([]nvidiaToolGPU, bool) {
	reader := csv.NewReader(bytes.NewReader(raw))
	reader.FieldsPerRecord = 6
	reader.TrimLeadingSpace = true
	rows := make([]nvidiaToolGPU, 0, 4)
	malformed := false
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			malformed = true
			break
		}
		if len(rows) == maxNvidiaDiscoveryRows {
			return nil, true
		}
		for i := range record {
			record[i] = strings.TrimSpace(record[i])
		}
		uuid := strings.ToLower(boundedIdentityString(record[0]))
		pci, pciOK := normalizePCIAddress(record[1])
		if uuid == "" && !pciOK {
			malformed = true
			continue
		}
		row := nvidiaToolGPU{uuid: uuid, pci: pci, status: devicemetric.StatusOK, values: make(map[devicemetric.NumericKey]float64, 2)}
		row.model = sanitizeDeviceDisplay(record[2], devicemetric.MaxModelBytes)
		row.label = sanitizeDeviceDisplay("NVIDIA "+row.model, devicemetric.MaxLabelBytes)
		if row.model == "" {
			row.label = "NVIDIA GPU"
		}
		invalid, missing := false, false
		if isNvidiaMissing(record[3]) {
			missing = true
		} else if value, ok := parsePercentString(record[3]); ok {
			row.values[devicemetric.GPUUtilizationPct] = value
		} else {
			invalid = true
		}
		if isNvidiaMissing(record[4]) || isNvidiaMissing(record[5]) {
			missing = true
		} else if used, usedOK := parseDecimalUint(record[4]); usedOK {
			if total, totalOK := parseDecimalUint(record[5]); totalOK && total > 0 && used <= total && total <= math.MaxUint64/(1<<20) {
				row.vramTotal = total * (1 << 20)
				row.values[devicemetric.GPUVRAMUsedPct] = float64(used) / float64(total) * 100
			} else {
				invalid = true
			}
		} else {
			invalid = true
		}
		switch {
		case invalid:
			row.status = devicemetric.StatusCollectionError
			malformed = true
		case missing:
			row.status = devicemetric.StatusMetricsUnavailable
		}
		rows = append(rows, row)
	}
	normalized, conflicts := normalizeNvidiaRows(rows)
	return normalized, malformed || conflicts
}

func normalizeNvidiaRows(rows []nvidiaToolGPU) ([]nvidiaToolGPU, bool) {
	sort.Slice(rows, func(i, j int) bool { return nvidiaToolRowLess(rows[i], rows[j]) })
	visited := make([]bool, len(rows))
	out := make([]nvidiaToolGPU, 0, len(rows))
	conflict := false
	for start := range rows {
		if visited[start] {
			continue
		}
		component := []int{start}
		visited[start] = true
		for cursor := 0; cursor < len(component); cursor++ {
			left := rows[component[cursor]]
			for candidate := range rows {
				if visited[candidate] || !sameNvidiaIdentity(left, rows[candidate]) {
					continue
				}
				visited[candidate] = true
				component = append(component, candidate)
			}
		}
		chosen := rows[component[0]]
		componentConflict := false
		for _, index := range component[1:] {
			if !equalNvidiaToolRow(chosen, rows[index]) {
				componentConflict = true
			}
		}
		if componentConflict {
			chosen.status = devicemetric.StatusCollectionError
			chosen.values = map[devicemetric.NumericKey]float64{}
			chosen.vramTotal = 0
			conflict = true
		}
		out = append(out, chosen)
	}
	sort.Slice(out, func(i, j int) bool { return nvidiaToolRowLess(out[i], out[j]) })
	return out, conflict
}

func sameNvidiaIdentity(left, right nvidiaToolGPU) bool {
	return left.uuid != "" && right.uuid != "" && strings.EqualFold(left.uuid, right.uuid) ||
		left.pci != "" && left.pci == right.pci
}

func equalNvidiaToolRow(left, right nvidiaToolGPU) bool {
	if !strings.EqualFold(left.uuid, right.uuid) || left.pci != right.pci || left.label != right.label ||
		left.model != right.model || left.status != right.status || left.vramTotal != right.vramTotal || len(left.values) != len(right.values) {
		return false
	}
	for key, value := range left.values {
		if rightValue, ok := right.values[key]; !ok || rightValue != value {
			return false
		}
	}
	return true
}

func nvidiaToolRowLess(left, right nvidiaToolGPU) bool {
	if (left.uuid != "") != (right.uuid != "") {
		return left.uuid != ""
	}
	leftUUID, rightUUID := strings.ToLower(left.uuid), strings.ToLower(right.uuid)
	if leftUUID != rightUUID {
		return leftUUID < rightUUID
	}
	if (left.pci != "") != (right.pci != "") {
		return left.pci != ""
	}
	if left.pci != right.pci {
		return left.pci < right.pci
	}
	if left.model != right.model {
		return left.model < right.model
	}
	if left.vramTotal != right.vramTotal {
		return left.vramTotal < right.vramTotal
	}
	if left.status != right.status {
		return left.status < right.status
	}
	return nvidiaValuesKey(left.values) < nvidiaValuesKey(right.values)
}

func nvidiaValuesKey(values map[devicemetric.NumericKey]float64) string {
	valueKey := func(key devicemetric.NumericKey) string {
		value, present := values[key]
		if !present {
			return "0"
		}
		return "1:" + strconv.FormatFloat(value, 'g', -1, 64)
	}
	return valueKey(devicemetric.GPUUtilizationPct) + "\x00" + valueKey(devicemetric.GPUVRAMUsedPct)
}

func assignGPUIdentities(gpus []linuxGPUCandidate) {
	counts := make(map[string]int, len(gpus))
	for i := range gpus {
		gpus[i].seriesID = preferredGPUSeriesID(gpus[i])
		counts[gpus[i].seriesID]++
	}
	for i := range gpus {
		if counts[gpus[i].seriesID] < 2 || gpus[i].relativePath == "" {
			continue
		}
		gpus[i].seriesID = devicemetric.SeriesID(devicemetric.KindGPU, frameDeviceIdentity(
			"gpu-sysfs-fallback-v1", []byte(filepath.ToSlash(gpus[i].relativePath)),
		))
		gpus[i].status = devicemetric.StatusCollectionError
	}
}

func preferredGPUSeriesID(gpu linuxGPUCandidate) string {
	var canonical []byte
	if gpu.relativePath == "" && gpu.localUnique != "" {
		canonical = frameDeviceIdentity("gpu-nvidia-uuid-v1", []byte(strings.ToLower(gpu.localUnique)))
	} else if gpu.relativePath != "" && gpu.localUnique != "" {
		if gpu.vendorCode == "0x10de" {
			canonical = frameDeviceIdentity("gpu-nvidia-uuid-v1", []byte(strings.ToLower(gpu.localUnique)))
		} else {
			canonical = frameDeviceIdentity("gpu-local-unique-v1", []byte(gpu.localUnique))
		}
	} else if gpu.pci != "" {
		canonical = frameDeviceIdentity("gpu-pci-v1", []byte(gpu.pci))
	} else {
		canonical = frameDeviceIdentity("gpu-sysfs-fallback-v1", []byte(filepath.ToSlash(gpu.relativePath)))
	}
	return devicemetric.SeriesID(devicemetric.KindGPU, canonical)
}

func gpuMetricRows(gpu linuxGPUCandidate) (devicemetric.InventoryEntry, *devicemetric.Sample) {
	seriesID := gpu.seriesID
	if seriesID == "" {
		seriesID = preferredGPUSeriesID(gpu)
	}
	entry := devicemetric.InventoryEntry{
		SeriesID: seriesID, Kind: devicemetric.KindGPU,
		Label:          sanitizeDeviceDisplay(gpu.label, devicemetric.MaxLabelBytes),
		Vendor:         sanitizeDeviceDisplay(gpu.vendor, devicemetric.MaxVendorBytes),
		Model:          sanitizeDeviceDisplay(gpu.model, devicemetric.MaxModelBytes),
		VRAMTotalBytes: gpu.vramTotal, Status: gpu.status,
	}
	if entry.Label == "" {
		entry.Label = "GPU"
	}
	if entry.Vendor == "" {
		entry.Vendor = "Unknown"
	}
	if len(gpu.values) == 0 {
		return entry, nil
	}
	values := make(map[devicemetric.NumericKey]float64, len(gpu.values))
	for key, value := range gpu.values {
		values[key] = value
	}
	return entry, &devicemetric.Sample{SeriesID: seriesID, Kind: devicemetric.KindGPU, Values: values}
}

func resolveTrustedNvidiaSMI() (string, bool) {
	for _, path := range []string{"/usr/bin/nvidia-smi", "/usr/local/bin/nvidia-smi", "/usr/local/nvidia/bin/nvidia-smi"} {
		if resolved, ok := trustedRootExecutable(path); ok {
			return resolved, true
		}
	}
	return "", false
}

func trustedRootExecutable(path string) (string, bool) {
	if !filepath.IsAbs(path) || !rootOwnsSafePath(path) {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(resolved) || !rootOwnsSafePath(resolved) {
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return "", false
	}
	return resolved, true
}

func rootOwnsSafePath(path string) bool {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return false
	}
	current := string(filepath.Separator)
	components := strings.Split(strings.TrimPrefix(clean, current), string(filepath.Separator))
	if len(components) == 1 && components[0] == "" {
		components = nil
	}
	paths := append([]string{current}, components...)
	for i, component := range paths {
		if i > 0 {
			current = filepath.Join(current, component)
		}
		info, err := os.Lstat(current)
		if err != nil {
			return false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return false
		}
		// Symlink permissions are not meaningful on Linux; its root-owned, non-writable parent is
		// the mutation boundary. Every resolved target component is checked in a second pass.
		if info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0o022 != 0 {
			return false
		}
	}
	return true
}

func resolveWithinRoot(root, path string) (string, string, bool) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", false
	}
	if resolvedRoot, resolveErr := filepath.EvalSymlinks(rootAbs); resolveErr == nil {
		rootAbs = resolvedRoot
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", false
	}
	resolvedAbs, err := filepath.Abs(resolved)
	if err != nil {
		return "", "", false
	}
	relative, err := filepath.Rel(rootAbs, resolvedAbs)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	info, err := os.Stat(resolvedAbs)
	if err != nil || !info.IsDir() {
		return "", "", false
	}
	return resolvedAbs, relative, true
}

func readBoundedFile(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("device attribute unavailable")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(raw)) > maxBytes {
		return nil, errors.New("device attribute invalid")
	}
	return raw, nil
}

func readBoundedFileContext(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("device attribute unavailable")
	}
	defer file.Close()
	var output bytes.Buffer
	buffer := make([]byte, 32<<10)
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			if int64(output.Len()+read) > maxBytes {
				return nil, errors.New("device attribute invalid")
			}
			_, _ = output.Write(buffer[:read])
		}
		if errors.Is(readErr, io.EOF) {
			return output.Bytes(), nil
		}
		if readErr != nil {
			return nil, errors.New("device attribute invalid")
		}
	}
}

func forEachDirectoryEntry(ctx context.Context, path string, visit func(os.DirEntry) bool) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	directory, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.ErrNotExist
		}
		return errors.New("device directory unavailable")
	}
	defer directory.Close()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		entries, readErr := directory.ReadDir(32)
		for _, entry := range entries {
			if ctx.Err() != nil || !visit(entry) {
				return ctx.Err()
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return errors.New("device directory invalid")
		}
	}
}

func readIdentityAttribute(path string) ([]byte, bool) {
	raw, err := readBoundedFile(path, maxDeviceAttributeBytes)
	if err != nil {
		return nil, false
	}
	raw = bytes.TrimSuffix(raw, []byte{'\n'})
	raw = bytes.TrimSuffix(raw, []byte{'\r'})
	if len(raw) == 0 {
		return nil, false
	}
	return raw, true
}

func readDisplayAttribute(path string) string {
	raw, err := readBoundedFile(path, maxDeviceAttributeBytes)
	if err != nil {
		return ""
	}
	return strings.ToValidUTF8(string(raw), "�")
}

func readUintAttribute(path string) (uint64, bool) {
	raw, err := readBoundedFile(path, 128)
	if err != nil {
		return 0, false
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return value, err == nil
}

func readHexAttribute(path string) (uint64, bool) {
	raw, err := readBoundedFile(path, 128)
	if err != nil {
		return 0, false
	}
	text := strings.TrimSpace(string(raw))
	text = strings.TrimPrefix(strings.ToLower(text), "0x")
	value, err := strconv.ParseUint(text, 16, 64)
	return value, err == nil
}

func readOptionalHexAttribute(path string) (uint64, bool, bool) {
	if _, err := os.Stat(path); err != nil {
		return 0, false, false
	}
	value, ok := readHexAttribute(path)
	return value, true, ok
}

func readLinuxDeviceNumber(path string) (linuxDeviceNumber, bool) {
	raw, err := readBoundedFile(path, 128)
	if err != nil {
		return linuxDeviceNumber{}, false
	}
	return parseLinuxDeviceNumber(bytes.TrimSpace(raw))
}

func parseLinuxDeviceNumber(raw []byte) (linuxDeviceNumber, bool) {
	parts := bytes.Split(raw, []byte{':'})
	if len(parts) != 2 {
		return linuxDeviceNumber{}, false
	}
	major, errMajor := strconv.ParseUint(string(parts[0]), 10, 64)
	minor, errMinor := strconv.ParseUint(string(parts[1]), 10, 64)
	return linuxDeviceNumber{major: major, minor: minor}, errMajor == nil && errMinor == nil
}

func deviceLinkMatches(sysRoot string, device linuxDeviceNumber, want string) bool {
	resolved, _, ok := resolveWithinRoot(sysRoot, filepath.Join(sysRoot, "dev", "block", device.String()))
	return ok && filepath.Clean(resolved) == filepath.Clean(want)
}

func frameDeviceIdentity(namespace string, parts ...[]byte) []byte {
	var out bytes.Buffer
	out.WriteString(namespace)
	out.WriteByte(0)
	for _, part := range parts {
		out.WriteString(strconv.Itoa(len(part)))
		out.WriteByte(':')
		out.Write(part)
		out.WriteByte(0)
	}
	return out.Bytes()
}

func excludedBlockName(name string) bool {
	return strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "zram")
}

func isDRMCardName(name string) bool {
	if !strings.HasPrefix(name, "card") || len(name) == len("card") {
		return false
	}
	for _, r := range name[len("card"):] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func normalizePCIAddress(value string) (string, bool) {
	matches := pciAddressPattern.FindStringSubmatch(strings.TrimSpace(value))
	if matches == nil {
		return "", false
	}
	domain, err := strconv.ParseUint(matches[1], 16, 32)
	if err != nil || domain > 0xffff {
		return "", false
	}
	return strings.ToLower(
		fmtHex(domain, 4) + ":" + matches[2] + ":" + matches[3] + "." + matches[4],
	), true
}

func fmtHex(value uint64, width int) string {
	text := strconv.FormatUint(value, 16)
	if len(text) < width {
		text = strings.Repeat("0", width-len(text)) + text
	}
	return text
}

func pciFromSysfs(path string) string {
	if pci, ok := normalizePCIAddress(filepath.Base(path)); ok {
		return pci
	}
	raw, err := readBoundedFile(filepath.Join(path, "uevent"), maxDeviceAttributeBytes)
	if err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "PCI_SLOT_NAME=") {
				if pci, ok := normalizePCIAddress(strings.TrimPrefix(line, "PCI_SLOT_NAME=")); ok {
					return pci
				}
			}
		}
	}
	return ""
}

func gpuVendorDisplay(code string) (string, string) {
	switch code {
	case "0x10de":
		return "NVIDIA", "NVIDIA GPU"
	case "0x1002":
		return "AMD", "AMD GPU"
	case "0x8086":
		return "Intel", "Intel GPU"
	default:
		return "Unknown", "GPU"
	}
}

func firstIdentityString(paths ...string) string {
	for _, path := range paths {
		if raw, ok := readIdentityAttribute(path); ok {
			return boundedIdentityString(string(raw))
		}
	}
	return ""
}

func firstDisplayString(paths ...string) string {
	for _, path := range paths {
		if value := sanitizeDeviceDisplay(readDisplayAttribute(path), devicemetric.MaxModelBytes); value != "" {
			return value
		}
	}
	return ""
}

func boundedIdentityString(value string) string {
	value = strings.TrimSpace(value)
	if isNvidiaMissing(value) || value == "" || len(value) > maxDeviceAttributeBytes {
		return ""
	}
	return value
}

func isNvidiaMissing(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "n/a", "[not supported]", "not supported", "-":
		return true
	default:
		return false
	}
}

func parsePercentAttribute(raw []byte) (float64, bool) {
	return parsePercentString(strings.TrimSpace(string(raw)))
}

func parsePercentString(value string) (float64, bool) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return parsed, err == nil && parsed >= 0 && parsed <= 100 && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
}

func parseDecimalUint(value string) (uint64, bool) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	return parsed, err == nil
}
