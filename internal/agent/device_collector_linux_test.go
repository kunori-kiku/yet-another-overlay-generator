//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/devicemetric"
)

type fakeDeviceCommandRunner struct {
	stdout []byte
	err    error
	path   string
	args   []string
}

func (r *fakeDeviceCommandRunner) Run(_ context.Context, path string, args ...string) ([]byte, error) {
	r.path = path
	r.args = append([]string(nil), args...)
	return append([]byte(nil), r.stdout...), r.err
}

func TestDeviceCollectorDiskFilesystemAndGPUs(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	procRoot := filepath.Join(root, "proc")
	for _, path := range []string{
		filepath.Join(sysRoot, "class", "block"), filepath.Join(sysRoot, "dev", "block"),
		filepath.Join(sysRoot, "class", "drm"), filepath.Join(sysRoot, "bus", "pci", "devices"),
		filepath.Join(procRoot, "self"),
	} {
		mustMkdirAll(t, path)
	}

	sda := filepath.Join(sysRoot, "devices", "pci0000:00", "block", "sda")
	sda1 := filepath.Join(sda, "sda1")
	dm0 := filepath.Join(sysRoot, "devices", "virtual", "block", "dm-0")
	sdb := filepath.Join(sysRoot, "devices", "pci0000:00", "block", "sdb")
	makeBlockFixture(t, sysRoot, "sda", sda, "8:0", 2048, blockStatLine(100, 200, 300))
	mustWriteFile(t, filepath.Join(sda, "device", "wwid"), "WWID-DO-NOT-TRANSMIT\n")
	mustWriteFile(t, filepath.Join(sda, "device", "vendor"), "Acme\n")
	mustWriteFile(t, filepath.Join(sda, "device", "model"), "Fast Disk\n")
	makeBlockFixture(t, sysRoot, "sda1", sda1, "8:1", 1024, blockStatLine(10, 20, 30))
	mustWriteFile(t, filepath.Join(sda1, "partition"), "1\n")
	makeBlockFixture(t, sysRoot, "dm-0", dm0, "252:0", 512, blockStatLine(50, 60, 70))
	mustWriteFile(t, filepath.Join(dm0, "dm", "uuid"), "DM-UUID-DO-NOT-TRANSMIT\n")
	mustMkdirAll(t, filepath.Join(dm0, "slaves"))
	mustSymlink(t, sda1, filepath.Join(dm0, "slaves", "sda1"))
	makeBlockFixture(t, sysRoot, "sdb", sdb, "8:16", 4096, blockStatLine(1, 2, 3))
	mustWriteFile(t, filepath.Join(sdb, "device", "serial"), "SERIAL-DO-NOT-TRANSMIT\n")

	loop := filepath.Join(sysRoot, "devices", "virtual", "block", "loop0")
	makeBlockFixture(t, sysRoot, "loop0", loop, "7:0", 10, blockStatLine(0, 0, 0))
	mustMkdirAll(t, filepath.Join(sysRoot, "class", "block", "fake"))

	mountInfo := strings.Join([]string{
		"36 25 252:0 /subvol /very/long/path rw,relatime - ext4 /dev/mapper/vg rw",
		"37 25 252:0 /subvol /m rw,relatime - ext4 /dev/mapper/vg rw",
		"41 25 252:0 /tie /bb rw,relatime - ext4 /dev/mapper/vg rw",
		"42 25 252:0 /tie /aa rw,relatime - ext4 /dev/mapper/vg rw",
		"38 25 252:0 /other /other rw,relatime - xfs /dev/mapper/vg rw",
		"39 25 0:1 / /tmp rw - tmpfs tmpfs rw",
		"40 25 9:9 / /stale rw - ext4 /dev/stale rw",
	}, "\n") + "\n"
	mustWriteFile(t, filepath.Join(procRoot, "self", "mountinfo"), mountInfo)

	nvidiaPath := makePCIGPUFixture(t, sysRoot, "0000:01:00.0", "0x10de", "0x030000")
	makeDRMCardFixture(t, sysRoot, "card0", nvidiaPath)
	amdPath := makePCIGPUFixture(t, sysRoot, "0000:03:00.0", "0x1002", "0x030000")
	makeDRMCardFixture(t, sysRoot, "card1", amdPath)
	bindGPUDriverFixture(t, sysRoot, amdPath, "amdgpu")
	mustWriteFile(t, filepath.Join(amdPath, "product_name"), "Radeon Test\n")
	mustWriteFile(t, filepath.Join(amdPath, "gpu_busy_percent"), "0\n")
	mustWriteFile(t, filepath.Join(amdPath, "mem_info_vram_used"), "0\n")
	mustWriteFile(t, filepath.Join(amdPath, "mem_info_vram_total"), "1073741824\n")
	intelPath := makePCIGPUFixture(t, sysRoot, "0000:04:00.0", "0x8086", "0x030000")
	makeDRMCardFixture(t, sysRoot, "card2", intelPath)
	platformGPUPath := filepath.Join(sysRoot, "devices", "platform", "gpu0")
	mustMkdirAll(t, platformGPUPath)
	mustWriteFile(t, filepath.Join(platformGPUPath, "product_name"), "SoC Graphics\n")
	makeDRMCardFixture(t, sysRoot, "card3", platformGPUPath)
	// Connector/render entries must not create duplicate devices.
	mustMkdirAll(t, filepath.Join(sysRoot, "class", "drm", "card0-DP-1"))
	mustMkdirAll(t, filepath.Join(sysRoot, "class", "drm", "renderD128"))

	runner := &fakeDeviceCommandRunner{stdout: []byte(strings.Join([]string{
		`GPU-SECRET-UUID, 00000000:01:00.0, "A100, Special", 0, 0, 40960`,
		`GPU-TOOL-ONLY-UUID, 0000:02:00.0, Compute GPU, 50, 1024, 2048`,
	}, "\n") + "\n")}
	stats := map[string]filesystemStat{
		"/m":     {Blocks: 100, Free: 25, BlockSize: 4096},
		"/aa":    {Blocks: 50, Free: 25, BlockSize: 4096},
		"/other": {Blocks: 200, Free: 100, BlockSize: 4096},
	}
	collector := newDeviceCollector(deviceCollectorDeps{
		ProcRoot: procRoot, SysRoot: sysRoot, Run: runner,
		ResolveNvidiaSMI: func() (string, bool) { return "/test/nvidia-smi", true },
		StatFilesystem: func(path string) (filesystemStat, error) {
			stat, ok := stats[path]
			if !ok {
				return filesystemStat{}, errors.New("missing test stat")
			}
			return stat, nil
		},
	})

	t0 := time.Unix(1_700_000_000, 0)
	inventory, samples := collector.Collect(context.Background(), t0)
	if err := devicemetric.ValidateInventory(inventory); err != nil {
		t.Fatalf("first inventory invalid: %v", err)
	}
	if err := devicemetric.ValidateSamples(samples); err != nil {
		t.Fatalf("first samples invalid: %v", err)
	}
	if got, want := runner.args, []string{
		"--query-gpu=uuid,pci.bus_id,name,utilization.gpu,memory.used,memory.total",
		"--format=csv,noheader,nounits",
	}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("nvidia-smi args = %q, want %q", got, want)
	}

	byLabel := inventoryByLabel(inventory)
	for _, label := range []string{"sda", "sda1", "dm-0", "sdb", "/m", "/aa", "/other", "NVIDIA A100, Special", "NVIDIA Compute GPU", "AMD Radeon Test", "Intel GPU", "Unknown SoC Graphics"} {
		if _, ok := byLabel[label]; !ok {
			t.Fatalf("inventory missing %q: %+v", label, inventory.Devices)
		}
	}
	if _, ok := byLabel["loop0"]; ok {
		t.Fatal("pseudo block device was included")
	}
	if _, ok := byLabel["/bb"]; ok {
		t.Fatal("equal-length bind-mount dedupe did not choose lexical mount point")
	}
	if byLabel["sda1"].ParentSeriesID != byLabel["sda"].SeriesID {
		t.Fatal("partition parent relationship is not stable hash-to-hash")
	}
	if byLabel["dm-0"].ParentSeriesID != byLabel["sda1"].SeriesID {
		t.Fatal("single-slave dm parent relationship is not stable hash-to-hash")
	}
	if byLabel["/m"].ParentSeriesID != byLabel["dm-0"].SeriesID || byLabel["/m"].CapacityBytes != 409600 {
		t.Fatalf("filesystem metadata = %+v", byLabel["/m"])
	}
	if byLabel["Intel GPU"].Status != devicemetric.StatusUnsupported {
		t.Fatalf("Intel status = %q, want unsupported", byLabel["Intel GPU"].Status)
	}
	if byLabel["Unknown SoC Graphics"].Status != devicemetric.StatusUnsupported {
		t.Fatalf("platform DRM status = %q, want unsupported", byLabel["Unknown SoC Graphics"].Status)
	}
	missingToolCollector := newDeviceCollector(deviceCollectorDeps{
		SysRoot: sysRoot, ResolveNvidiaSMI: func() (string, bool) { return "", false },
	})
	missingGPUInventory, _, err := missingToolCollector.collectLinuxGPUs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	missingFound := false
	for _, entry := range missingGPUInventory {
		if entry.Vendor == "NVIDIA" {
			missingFound = true
			if entry.Status != devicemetric.StatusToolMissing {
				t.Fatalf("missing NVIDIA tool status = %q", entry.Status)
			}
		}
	}
	if !missingFound {
		t.Fatal("missing NVIDIA tool suppressed sysfs GPU inventory")
	}

	firstSamples := samplesByID(samples)
	if value := firstSamples[byLabel["/m"].SeriesID][devicemetric.DiskFilesystemUsedPct]; value != 75 {
		t.Fatalf("filesystem used = %v, want 75", value)
	}
	for _, label := range []string{"NVIDIA A100, Special", "AMD Radeon Test"} {
		values := firstSamples[byLabel[label].SeriesID]
		if utilization, ok := values[devicemetric.GPUUtilizationPct]; !ok || utilization != 0 {
			t.Fatalf("%s genuine zero utilization = %v, present %v", label, utilization, ok)
		}
		if vram, ok := values[devicemetric.GPUVRAMUsedPct]; !ok || vram != 0 {
			t.Fatalf("%s genuine zero VRAM = %v, present %v", label, vram, ok)
		}
	}
	for _, label := range []string{"sda", "sda1", "dm-0", "sdb"} {
		if _, ok := firstSamples[byLabel[label].SeriesID]; ok {
			t.Fatalf("first block observation fabricated rates for %s", label)
		}
	}

	encoded, err := json.Marshal(struct {
		Inventory devicemetric.InventoryMetric
		Samples   devicemetric.SamplesMetric
	}{inventory, samples})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"WWID-DO-NOT-TRANSMIT", "SERIAL-DO-NOT-TRANSMIT", "DM-UUID-DO-NOT-TRANSMIT",
		"GPU-SECRET-UUID", "GPU-TOOL-ONLY-UUID", "0000:01:00.0", "/subvol", sysRoot,
	} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("device telemetry exposed private identity %q", secret)
		}
	}

	// A valid second interval emits exact rates and true idle zeros.
	mustWriteFile(t, filepath.Join(sda, "stat"), blockStatLine(104, 208, 1300))
	mustWriteFile(t, filepath.Join(sda1, "stat"), blockStatLine(10, 20, 30))
	mustWriteFile(t, filepath.Join(dm0, "stat"), blockStatLine(52, 64, 170))
	mustWriteFile(t, filepath.Join(sdb, "stat"), blockStatLine(1, 2, 3))
	secondInventory, secondSamples := collector.Collect(context.Background(), t0.Add(2*time.Second))
	if err := devicemetric.ValidateInventory(secondInventory); err != nil {
		t.Fatal(err)
	}
	if err := devicemetric.ValidateSamples(secondSamples); err != nil {
		t.Fatal(err)
	}
	secondByLabel := inventoryByLabel(secondInventory)
	secondValues := samplesByID(secondSamples)
	sdaValues := secondValues[secondByLabel["sda"].SeriesID]
	if sdaValues[devicemetric.DiskReadBytesPerSecond] != 1024 || sdaValues[devicemetric.DiskWriteBytesPerSecond] != 2048 || sdaValues[devicemetric.DiskIOBusyPct] != 50 {
		t.Fatalf("sda delta values = %+v", sdaValues)
	}
	for _, key := range []devicemetric.NumericKey{devicemetric.DiskReadBytesPerSecond, devicemetric.DiskWriteBytesPerSecond, devicemetric.DiskIOBusyPct} {
		if value, ok := secondValues[secondByLabel["sda1"].SeriesID][key]; !ok || value != 0 {
			t.Fatalf("unchanged counter %s = %v, present %v", key, value, ok)
		}
	}

	// Lower-priority identity material cannot re-key an existing series. A changed highest-priority
	// identity deliberately does re-key the disk and partition, producing a first-observation gap.
	mustWriteFile(t, filepath.Join(sda, "device", "serial"), "LATER-SERIAL\n")
	mustWriteFile(t, filepath.Join(sda, "stat"), blockStatLine(106, 210, 1400))
	mustWriteFile(t, filepath.Join(sdb, "stat"), "malformed\n")
	thirdInventory, thirdSamples := collector.Collect(context.Background(), t0.Add(3*time.Second))
	thirdByLabel := inventoryByLabel(thirdInventory)
	if thirdByLabel["sda"].SeriesID != secondByLabel["sda"].SeriesID {
		t.Fatal("lower-priority serial changed a WWID-derived disk identity")
	}
	if thirdByLabel["sdb"].Status != devicemetric.StatusCollectionError {
		t.Fatalf("malformed counter status = %q", thirdByLabel["sdb"].Status)
	}
	if _, ok := samplesByID(thirdSamples)[thirdByLabel["sdb"].SeriesID]; ok {
		t.Fatal("malformed counter emitted a numeric sample")
	}

	mustWriteFile(t, filepath.Join(sda, "device", "wwid"), "REPLACEMENT-WWID\n")
	mustWriteFile(t, filepath.Join(sda, "stat"), blockStatLine(108, 212, 1500))
	mustWriteFile(t, filepath.Join(sdb, "stat"), blockStatLine(5, 6, 7))
	fourthInventory, fourthSamples := collector.Collect(context.Background(), t0.Add(4*time.Second))
	fourthByLabel := inventoryByLabel(fourthInventory)
	fourthValues := samplesByID(fourthSamples)
	if fourthByLabel["sda"].SeriesID == thirdByLabel["sda"].SeriesID || fourthByLabel["sda1"].SeriesID == thirdByLabel["sda1"].SeriesID {
		t.Fatal("changed parent identity did not re-key the disk and derived partition")
	}
	for _, label := range []string{"sda", "sda1", "sdb"} {
		if _, ok := fourthValues[fourthByLabel[label].SeriesID]; ok {
			t.Fatalf("first observation after reset/re-key emitted values for %s", label)
		}
	}
	mustWriteFile(t, filepath.Join(sdb, "stat"), blockStatLine(7, 10, 107))
	_, fifthSamples := collector.Collect(context.Background(), t0.Add(5*time.Second))
	if _, ok := samplesByID(fifthSamples)[fourthByLabel["sdb"].SeriesID]; !ok {
		t.Fatal("valid counter interval did not recover after malformed baseline")
	}
}

func TestDiskCounterValuesZeroResetAndInvalidElapsed(t *testing.T) {
	t0 := time.Unix(100, 0)
	previous := diskCounterSnapshot{ReadSectors: 100, WriteSectors: 200, IOMillis: 300, SampledAt: t0}
	unchanged := previous
	unchanged.SampledAt = t0.Add(time.Second)
	values := diskCounterValues(previous, unchanged)
	for _, key := range []devicemetric.NumericKey{devicemetric.DiskReadBytesPerSecond, devicemetric.DiskWriteBytesPerSecond, devicemetric.DiskIOBusyPct} {
		if value, ok := values[key]; !ok || value != 0 {
			t.Fatalf("unchanged %s = %v, present %v", key, value, ok)
		}
	}
	resetRead := diskCounterSnapshot{ReadSectors: 1, WriteSectors: 202, IOMillis: 400, SampledAt: t0.Add(time.Second)}
	values = diskCounterValues(previous, resetRead)
	if _, ok := values[devicemetric.DiskReadBytesPerSecond]; ok {
		t.Fatal("decreased read counter emitted a value")
	}
	if values[devicemetric.DiskWriteBytesPerSecond] != 1024 || values[devicemetric.DiskIOBusyPct] != 10 {
		t.Fatalf("independent valid deltas = %+v", values)
	}
	invalid := previous
	if values := diskCounterValues(previous, invalid); len(values) != 0 {
		t.Fatalf("zero elapsed emitted values: %+v", values)
	}
	invalid.SampledAt = t0.Add(-time.Second)
	if values := diskCounterValues(previous, invalid); len(values) != 0 {
		t.Fatalf("negative elapsed emitted values: %+v", values)
	}
	impossible := previous
	impossible.IOMillis += 1006
	impossible.SampledAt = t0.Add(time.Second)
	if _, ok := diskCounterValues(previous, impossible)[devicemetric.DiskIOBusyPct]; ok {
		t.Fatal("physically impossible busy interval emitted a percentage")
	}
}

func TestDeviceCollectorBlockIdentityPrecedenceAndCollisionFallback(t *testing.T) {
	root := t.TempDir()
	strongPath := filepath.Join(root, "strong")
	for path, value := range map[string]string{
		filepath.Join(strongPath, "wwid"):             "block-wwid\n",
		filepath.Join(strongPath, "device", "wwid"):   "device-wwid\n",
		filepath.Join(strongPath, "device", "serial"): "serial\n",
		filepath.Join(strongPath, "dm", "uuid"):       "dm-uuid\n",
	} {
		mustWriteFile(t, path, value)
	}
	strong := &linuxBlockCandidate{realPath: strongPath, relativePath: "devices/strong", device: linuxDeviceNumber{major: 8}}
	resolveLinuxBlockIdentities(context.Background(), []*linuxBlockCandidate{strong})
	wantStrong := devicemetric.SeriesID(devicemetric.KindBlockDevice, frameDeviceIdentity("block-wwid-v1", []byte("block-wwid")))
	if strong.seriesID != wantStrong {
		t.Fatalf("identity precedence selected %q, want block WWID hash %q", strong.seriesID, wantStrong)
	}

	leftPath := filepath.Join(root, "parent-left")
	rightPath := filepath.Join(root, "parent-right")
	mustWriteFile(t, filepath.Join(leftPath, "device", "serial"), "duplicate\n")
	mustWriteFile(t, filepath.Join(rightPath, "device", "serial"), "duplicate\n")
	left := &linuxBlockCandidate{realPath: leftPath, relativePath: "devices/a", device: linuxDeviceNumber{major: 8, minor: 1}}
	right := &linuxBlockCandidate{realPath: rightPath, relativePath: "devices/b", device: linuxDeviceNumber{major: 8, minor: 2}}
	child := &linuxBlockCandidate{
		realPath: filepath.Join(leftPath, "part1"), relativePath: "devices/a/part1",
		device: linuxDeviceNumber{major: 8, minor: 3}, partition: 1, parentPath: leftPath,
	}
	rightChild := &linuxBlockCandidate{
		realPath: filepath.Join(rightPath, "part1"), relativePath: "devices/b/part1",
		device: linuxDeviceNumber{major: 8, minor: 4}, partition: 1, parentPath: rightPath,
	}
	resolveLinuxBlockIdentities(context.Background(), []*linuxBlockCandidate{left, right, child, rightChild})
	if left.seriesID == right.seriesID || left.seriesID != fallbackBlockSeriesID(left) || right.seriesID != fallbackBlockSeriesID(right) {
		t.Fatalf("duplicate strong identifiers were not independently re-keyed: left %q right %q", left.seriesID, right.seriesID)
	}
	wantChild := devicemetric.SeriesID(devicemetric.KindBlockDevice, frameDeviceIdentity("partition-v1", []byte(left.seriesID), []byte("1")))
	if child.seriesID != wantChild {
		t.Fatalf("partition identity did not follow re-keyed parent: %q, want %q", child.seriesID, wantChild)
	}
	wantRightChild := devicemetric.SeriesID(devicemetric.KindBlockDevice, frameDeviceIdentity("partition-v1", []byte(right.seriesID), []byte("1")))
	if rightChild.seriesID != wantRightChild || rightChild.seriesID == child.seriesID {
		t.Fatalf("same-number sibling partitions did not follow distinct re-keyed parents: left %q right %q", child.seriesID, rightChild.seriesID)
	}
	remappedRoot := &linuxBlockCandidate{relativePath: left.relativePath, device: left.device}
	if fallbackBlockSeriesID(remappedRoot) != fallbackBlockSeriesID(left) {
		t.Fatal("fallback identity depended on the absolute injected sysfs root")
	}
}

func TestDeviceCollectorMalformedDiskCapacityIsExplicit(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	for _, path := range []string{filepath.Join(sysRoot, "class", "block"), filepath.Join(sysRoot, "dev", "block")} {
		mustMkdirAll(t, path)
	}
	badPath := filepath.Join(sysRoot, "devices", "block", "bad")
	overPath := filepath.Join(sysRoot, "devices", "block", "over")
	makeBlockFixture(t, sysRoot, "bad", badPath, "8:1", 1, blockStatLine(0, 0, 0))
	makeBlockFixture(t, sysRoot, "over", overPath, "8:2", 1, blockStatLine(0, 0, 0))
	mustWriteFile(t, filepath.Join(badPath, "size"), "not-a-number\n")
	mustWriteFile(t, filepath.Join(overPath, "size"), fmt.Sprintf("%d\n", uint64(^uint64(0))/linuxAccountingSector+1))
	blocks, err := enumerateLinuxBlocks(context.Background(), sysRoot, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 {
		t.Fatalf("enumerated blocks = %d, want 2", len(blocks))
	}
	for _, block := range blocks {
		if block.status != devicemetric.StatusCollectionError || block.capacity != 0 {
			t.Fatalf("invalid capacity %s = status %q, bytes %d", block.name, block.status, block.capacity)
		}
	}
}

func TestDeviceCollectorDiscoveryFailureDegradesKnownInventory(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	procRoot := filepath.Join(root, "proc")
	for _, path := range []string{
		filepath.Join(sysRoot, "class", "block"), filepath.Join(sysRoot, "dev", "block"),
		filepath.Join(procRoot, "self"),
	} {
		mustMkdirAll(t, path)
	}
	diskPath := filepath.Join(sysRoot, "devices", "block", "sda")
	makeBlockFixture(t, sysRoot, "sda", diskPath, "8:0", 1, blockStatLine(0, 0, 0))
	mustWriteFile(t, filepath.Join(procRoot, "self", "mountinfo"), "36 25 8:0 / /data rw - ext4 /dev/sda rw\n")
	collector := newDeviceCollector(deviceCollectorDeps{
		ProcRoot: procRoot, SysRoot: sysRoot,
		ResolveNvidiaSMI: func() (string, bool) { return "", false },
		StatFilesystem: func(string) (filesystemStat, error) {
			return filesystemStat{Blocks: 10, Free: 5, BlockSize: 4096}, nil
		},
	})
	initial, _ := collector.Collect(context.Background(), time.Unix(1, 0))
	if len(initial.Devices) != 2 {
		t.Fatalf("initial disk/filesystem inventory = %+v", initial)
	}
	mustWriteFile(t, filepath.Join(procRoot, "self", "mountinfo"), strings.Repeat("x", maxMountInfoBytes+1))
	inventory, samples := collector.Collect(context.Background(), time.Unix(2, 0))
	if len(inventory.Devices) != 2 || len(samples.Samples) != 0 {
		t.Fatalf("oversized mount discovery became a healthy disappearance: inventory %+v samples %+v", inventory, samples)
	}
	for _, entry := range inventory.Devices {
		if entry.Status != devicemetric.StatusCollectionError {
			t.Fatalf("failed discovery retained a healthy row: %+v", entry)
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	result := deviceCollectionResult{
		inventory: devicemetric.InventoryMetric{Devices: []devicemetric.InventoryEntry{{
			SeriesID: inventory.Devices[0].SeriesID, Kind: devicemetric.KindBlockDevice, Label: "sda", Status: devicemetric.StatusOK,
		}}},
		samples: devicemetric.SamplesMetric{Samples: []devicemetric.Sample{}}, complete: true,
	}
	accepted, acceptedSamples := collector.acceptCollectionResult(canceled, result)
	if len(accepted.Devices) != len(inventory.Devices) || len(acceptedSamples.Samples) != 0 {
		t.Fatalf("canceled completed result was accepted as healthy: inventory %+v samples %+v", accepted, acceptedSamples)
	}
	for _, entry := range accepted.Devices {
		if entry.Status != devicemetric.StatusCollectionError {
			t.Fatalf("canceled completed result retained a healthy row: %+v", entry)
		}
	}
}

func TestDeviceCollectorNVIDIABoundsAndStatuses(t *testing.T) {
	validRows, malformed := parseNvidiaCSV([]byte("GPU-a, 00000000:01:00.0, \"Name, With Comma\", 0, N/A, N/A\n"))
	if malformed || len(validRows) != 1 || validRows[0].uuid != "gpu-a" || validRows[0].pci != "0000:01:00.0" || validRows[0].status != devicemetric.StatusMetricsUnavailable {
		t.Fatalf("valid N/A CSV = %+v, malformed %v", validRows, malformed)
	}
	if validRows[0].values[devicemetric.GPUUtilizationPct] != 0 {
		t.Fatalf("valid NVIDIA zero lost: %+v", validRows[0].values)
	}
	if rows, malformed := parseNvidiaCSV([]byte("not,csv\n")); len(rows) != 0 || !malformed {
		t.Fatalf("malformed CSV = %+v, malformed %v", rows, malformed)
	}
	maxMiB := uint64(^uint64(0) / (1 << 20))
	maxRows, malformed := parseNvidiaCSV([]byte(fmt.Sprintf("GPU-max, 0000:09:00.0, Max, 0, 0, %d\n", maxMiB)))
	if malformed || len(maxRows) != 1 || maxRows[0].vramTotal != maxMiB*(1<<20) {
		t.Fatalf("maximum bounded NVIDIA VRAM = %+v, malformed %v", maxRows, malformed)
	}
	overflowRows, malformed := parseNvidiaCSV([]byte("GPU-over, 0000:09:00.0, Over, 0, 0, 17592186044416\n"))
	if !malformed || len(overflowRows) != 1 || overflowRows[0].status != devicemetric.StatusCollectionError || overflowRows[0].vramTotal != 0 {
		t.Fatalf("overflowing NVIDIA VRAM = %+v, malformed %v", overflowRows, malformed)
	}

	conflicting := []string{
		"GPU-conflict, 0000:05:00.0, First, 1, 1, 2",
		"GPU-conflict, 0000:06:00.0, Second, 2, 1, 2",
	}
	forward, forwardMalformed := parseNvidiaCSV([]byte(strings.Join(conflicting, "\n") + "\n"))
	reverse, reverseMalformed := parseNvidiaCSV([]byte(conflicting[1] + "\n" + conflicting[0] + "\n"))
	if !forwardMalformed || !reverseMalformed || len(forward) != 1 || len(reverse) != 1 ||
		forward[0].pci != reverse[0].pci || forward[0].status != devicemetric.StatusCollectionError || reverse[0].status != devicemetric.StatusCollectionError {
		t.Fatalf("order-dependent NVIDIA conflict: forward %+v/%v reverse %+v/%v", forward, forwardMalformed, reverse, reverseMalformed)
	}
	if len(forward[0].values) != 0 || forward[0].vramTotal != 0 || len(reverse[0].values) != 0 || reverse[0].vramTotal != 0 {
		t.Fatalf("conflicting NVIDIA identity retained disputed numeric values: forward %+v reverse %+v", forward[0], reverse[0])
	}
	identical, identicalMalformed := parseNvidiaCSV([]byte(strings.Repeat("GPU-same, 0000:07:00.0, Same, 0, 0, 1\n", 2)))
	if identicalMalformed || len(identical) != 1 || identical[0].status != devicemetric.StatusOK {
		t.Fatalf("identical NVIDIA duplicate = %+v, malformed %v", identical, identicalMalformed)
	}

	runner := &fakeDeviceCommandRunner{stdout: make([]byte, maxDeviceCommandOutput+1)}
	collector := newDeviceCollector(deviceCollectorDeps{
		Run: runner, ResolveNvidiaSMI: func() (string, bool) { return "/test/nvidia-smi", true },
	})
	if rows, status := collector.queryNvidia(context.Background()); len(rows) != 0 || status != devicemetric.StatusCollectionError {
		t.Fatalf("defensive oversize query = %d rows, status %q", len(rows), status)
	}
	runner.stdout, runner.err = nil, errDeviceCommandExit
	if _, status := collector.queryNvidia(context.Background()); status != devicemetric.StatusDriverUnavailable {
		t.Fatalf("driver exit status = %q", status)
	}
	runner.err = context.DeadlineExceeded
	if _, status := collector.queryNvidia(context.Background()); status != devicemetric.StatusCollectionError {
		t.Fatalf("timeout status = %q", status)
	}

	pathDir := t.TempDir()
	fakePath := filepath.Join(pathDir, "nvidia-smi")
	mustWriteFile(t, fakePath, "#!/bin/sh\n")
	if err := os.Chmod(fakePath, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)
	if resolved, ok := resolveTrustedNvidiaSMI(); ok && resolved == fakePath {
		t.Fatal("trusted resolver searched inherited PATH")
	}
	if _, ok := trustedRootExecutable(fakePath); ok {
		t.Fatal("trusted executable check accepted an unsafe/user-controlled path")
	}
	if resolved, ok := trustedRootExecutable("/bin/sh"); !ok || !filepath.IsAbs(resolved) {
		t.Fatalf("trusted executable check rejected root-controlled /bin/sh: %q, %v", resolved, ok)
	}
	presentEntry, _ := gpuMetricRows(linuxGPUCandidate{
		relativePath: "bus/pci/devices/0000:01:00.0", pci: "0000:01:00.0", vendorCode: "0x10de", vendor: "NVIDIA", label: "NVIDIA GPU", status: devicemetric.StatusOK,
		values: map[devicemetric.NumericKey]float64{devicemetric.GPUUtilizationPct: 0},
	})
	missingEntry, _ := gpuMetricRows(linuxGPUCandidate{
		relativePath: "bus/pci/devices/0000:01:00.0", pci: "0000:01:00.0", vendorCode: "0x10de", vendor: "NVIDIA", label: "NVIDIA GPU", status: devicemetric.StatusToolMissing,
	})
	if presentEntry.SeriesID != missingEntry.SeriesID {
		t.Fatal("NVIDIA series identity changed when metrics became unavailable")
	}
	toolAtFirstSlot, _ := gpuMetricRows(linuxGPUCandidate{
		pci: "0000:01:00.0", localUnique: "GPU-STABLE", vendorCode: "0x10de", vendor: "NVIDIA", label: "NVIDIA GPU", status: devicemetric.StatusOK,
	})
	toolAtSecondSlot, _ := gpuMetricRows(linuxGPUCandidate{
		pci: "0000:02:00.0", localUnique: "gpu-stable", vendorCode: "0x10de", vendor: "NVIDIA", label: "NVIDIA GPU", status: devicemetric.StatusOK,
	})
	if toolAtFirstSlot.SeriesID != toolAtSecondSlot.SeriesID {
		t.Fatal("tool-only NVIDIA UUID did not survive PCI movement or case normalization")
	}
	collidingSysfs := []linuxGPUCandidate{
		{relativePath: "devices/gpu-a", localUnique: "duplicate", vendorCode: "0x10de", status: devicemetric.StatusOK},
		{relativePath: "devices/gpu-b", localUnique: "DUPLICATE", vendorCode: "0x10de", status: devicemetric.StatusOK},
	}
	assignGPUIdentities(collidingSysfs)
	if collidingSysfs[0].seriesID == collidingSysfs[1].seriesID ||
		collidingSysfs[0].status != devicemetric.StatusCollectionError || collidingSysfs[1].status != devicemetric.StatusCollectionError {
		t.Fatalf("colliding sysfs GPU identities were not safely separated: %+v", collidingSysfs)
	}
	rows := []nvidiaToolGPU{
		{uuid: "other", pci: "0000:01:00.0"},
		{uuid: "stable-uuid", pci: "0000:02:00.0"},
	}
	if got := matchNvidiaToolRow(linuxGPUCandidate{localUnique: "STABLE-UUID", pci: "0000:01:00.0"}, rows, []bool{false, false}); got != 1 {
		t.Fatalf("NVIDIA match = row %d, want UUID-priority row 1", got)
	}
}

type blockingDeviceCommandRunner struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (r *blockingDeviceCommandRunner) Run(context.Context, string, ...string) ([]byte, error) {
	r.calls.Add(1)
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-r.release
	return nil, errDeviceCommandExit
}

func TestDeviceCollectorDeadlineDoesNotAccumulateProviderWorkers(t *testing.T) {
	root := t.TempDir()
	sysRoot := filepath.Join(root, "sys")
	procRoot := filepath.Join(root, "proc")
	for _, path := range []string{
		filepath.Join(sysRoot, "class", "block"), filepath.Join(sysRoot, "dev", "block"),
		filepath.Join(sysRoot, "class", "drm"), filepath.Join(sysRoot, "bus", "pci", "devices"),
		filepath.Join(procRoot, "self"),
	} {
		mustMkdirAll(t, path)
	}
	diskPath := filepath.Join(sysRoot, "devices", "block", "sda")
	makeBlockFixture(t, sysRoot, "sda", diskPath, "8:0", 1, blockStatLine(0, 0, 0))
	mustWriteFile(t, filepath.Join(procRoot, "self", "mountinfo"), "")
	runner := &blockingDeviceCommandRunner{started: make(chan struct{}, 1), release: make(chan struct{})}
	collector := newDeviceCollector(deviceCollectorDeps{
		ProcRoot: procRoot, SysRoot: sysRoot, Run: runner, CollectionTimeout: 30 * time.Millisecond,
		ResolveNvidiaSMI: func() (string, bool) { return "/test/nvidia-smi", true },
	})

	firstInventory, firstSamples := collector.Collect(context.Background(), time.Unix(1, 0))
	if runner.calls.Load() != 1 || len(firstInventory.Devices) != 1 || len(firstSamples.Samples) != 0 || firstInventory.Devices[0].Status != devicemetric.StatusCollectionError {
		t.Fatalf("deadline fallback = calls %d, inventory %+v, samples %+v", runner.calls.Load(), firstInventory, firstSamples)
	}
	started := time.Now()
	secondInventory, _ := collector.Collect(context.Background(), time.Unix(2, 0))
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("second call waited behind an uninterruptible provider: %s", elapsed)
	}
	if runner.calls.Load() != 1 || len(secondInventory.Devices) != 1 {
		t.Fatalf("in-flight provider accumulated workers: calls %d, inventory %+v", runner.calls.Load(), secondInventory)
	}
	close(runner.release)
	deadline := time.Now().Add(time.Second)
	for {
		collector.mu.Lock()
		inFlight := collector.inFlight
		collector.mu.Unlock()
		if !inFlight {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("released provider worker did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDeviceCollectorAMDPartialMetricsAndMissingAttributes(t *testing.T) {
	sysRoot := filepath.Join(t.TempDir(), "sys")
	path := filepath.Join(sysRoot, "bus", "pci", "devices", "0000:01:00.0")
	mustMkdirAll(t, path)
	bindGPUDriverFixture(t, sysRoot, path, "amdgpu")
	mustWriteFile(t, filepath.Join(path, "gpu_busy_percent"), "not-a-number\n")
	mustWriteFile(t, filepath.Join(path, "mem_info_vram_used"), "0\n")
	mustWriteFile(t, filepath.Join(path, "mem_info_vram_total"), "1024\n")
	gpu := linuxGPUCandidate{}
	collectAMDGPU(sysRoot, path, &gpu)
	if gpu.status != devicemetric.StatusCollectionError {
		t.Fatalf("malformed AMD status = %q, want collection_error", gpu.status)
	}
	if _, ok := gpu.values[devicemetric.GPUUtilizationPct]; ok {
		t.Fatal("malformed AMD utilization emitted a value")
	}
	if value, ok := gpu.values[devicemetric.GPUVRAMUsedPct]; !ok || value != 0 {
		t.Fatalf("independent valid AMD VRAM zero = %v, present %v", value, ok)
	}

	if err := os.Remove(filepath.Join(path, "gpu_busy_percent")); err != nil {
		t.Fatal(err)
	}
	gpu = linuxGPUCandidate{}
	collectAMDGPU(sysRoot, path, &gpu)
	if gpu.status != devicemetric.StatusMetricsUnavailable {
		t.Fatalf("missing AMD attribute status = %q, want metrics_unavailable", gpu.status)
	}
	if value, ok := gpu.values[devicemetric.GPUVRAMUsedPct]; !ok || value != 0 {
		t.Fatalf("available AMD VRAM with another metric missing = %v, present %v", value, ok)
	}
	if err := os.Remove(filepath.Join(path, "driver")); err != nil {
		t.Fatal(err)
	}
	gpu = linuxGPUCandidate{}
	collectAMDGPU(sysRoot, path, &gpu)
	if gpu.status != devicemetric.StatusDriverUnavailable || len(gpu.values) != 0 {
		t.Fatalf("unbound AMD driver = status %q, values %+v", gpu.status, gpu.values)
	}
}

func bindGPUDriverFixture(t *testing.T, sysRoot, devicePath, driver string) {
	t.Helper()
	driverPath := filepath.Join(sysRoot, "bus", "pci", "drivers", driver)
	mustMkdirAll(t, driverPath)
	mustSymlink(t, driverPath, filepath.Join(devicePath, "driver"))
}

func TestDeviceCollectorCommandRunnerKillsOverflowAndTimeout(t *testing.T) {
	if os.Getenv("YAOG_DEVICE_HELPER") == "1" {
		deviceCollectorCommandHelper()
		return
	}
	runner := boundedDeviceCommandRunner{}
	run := func(ctx context.Context, mode string) ([]byte, error) {
		t.Setenv("YAOG_DEVICE_HELPER", "1")
		return runner.Run(ctx, os.Args[0], "-test.run=TestDeviceCollectorCommandRunnerKillsOverflowAndTimeout", "--", mode)
	}
	if output, err := run(context.Background(), "exact"); err != nil || len(output) != maxDeviceCommandOutput {
		t.Fatalf("exact-cap output = %d bytes, err %v", len(output), err)
	}
	if _, err := run(context.Background(), "overflow"); !errors.Is(err, errDeviceCommandOutputLimit) {
		t.Fatalf("overflow error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := run(ctx, "sleep"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timed-out child was not promptly killed and reaped: %s", elapsed)
	}
	if _, err := run(context.Background(), "exit"); !errors.Is(err, errDeviceCommandExit) {
		t.Fatalf("nonzero exit error = %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started = time.Now()
	if _, err := run(ctx, "descendant"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("descendant-held pipe error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("descendant-held stdout escaped process-group cleanup: %s", elapsed)
	}
}

func deviceCollectorCommandHelper() {
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "exact":
		_, _ = os.Stdout.Write(make([]byte, maxDeviceCommandOutput))
	case "overflow":
		_, _ = os.Stdout.Write(make([]byte, maxDeviceCommandOutput+1))
	case "sleep":
		time.Sleep(5 * time.Second)
	case "exit":
		os.Exit(7)
	case "descendant":
		child := exec.Command("/bin/sh", "-c", "sleep 5")
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			os.Exit(8)
		}
	}
	os.Exit(0)
}

func makeBlockFixture(t *testing.T, sysRoot, name, realPath, device string, sectors uint64, stat string) {
	t.Helper()
	mustMkdirAll(t, realPath)
	mustSymlink(t, realPath, filepath.Join(sysRoot, "class", "block", name))
	mustSymlink(t, realPath, filepath.Join(sysRoot, "dev", "block", device))
	mustWriteFile(t, filepath.Join(realPath, "dev"), device+"\n")
	mustWriteFile(t, filepath.Join(realPath, "size"), strconvFormatUint(sectors)+"\n")
	mustWriteFile(t, filepath.Join(realPath, "stat"), stat)
}

func makePCIGPUFixture(t *testing.T, sysRoot, pci, vendor, class string) string {
	t.Helper()
	path := filepath.Join(sysRoot, "bus", "pci", "devices", pci)
	mustMkdirAll(t, path)
	mustWriteFile(t, filepath.Join(path, "vendor"), vendor+"\n")
	mustWriteFile(t, filepath.Join(path, "class"), class+"\n")
	mustWriteFile(t, filepath.Join(path, "uevent"), "PCI_SLOT_NAME="+pci+"\n")
	return path
}

func makeDRMCardFixture(t *testing.T, sysRoot, name, devicePath string) {
	t.Helper()
	card := filepath.Join(sysRoot, "class", "drm", name)
	mustMkdirAll(t, card)
	mustSymlink(t, devicePath, filepath.Join(card, "device"))
}

func blockStatLine(read, write, ioMillis uint64) string {
	return fmt.Sprintf("1 0 %d 0 1 0 %d 0 0 %d 0\n", read, write, ioMillis)
}

func inventoryByLabel(metric devicemetric.InventoryMetric) map[string]devicemetric.InventoryEntry {
	out := make(map[string]devicemetric.InventoryEntry, len(metric.Devices))
	for _, entry := range metric.Devices {
		out[entry.Label] = entry
	}
	return out
}

func samplesByID(metric devicemetric.SamplesMetric) map[string]map[devicemetric.NumericKey]float64 {
	out := make(map[string]map[devicemetric.NumericKey]float64, len(metric.Samples))
	for _, sample := range metric.Samples {
		out[sample.SeriesID] = sample.Values
	}
	return out
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(link))
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func strconvFormatUint(value uint64) string {
	return fmt.Sprintf("%d", value)
}
