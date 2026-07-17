//go:build !linux

package agent

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/devicemetric"
)

func configureDeviceCommand(*exec.Cmd) {}

func terminateDeviceCommand(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (c *deviceCollector) collectPlatform(
	context.Context,
	time.Time,
	map[string]diskCounterSnapshot,
) (devicemetric.InventoryMetric, devicemetric.SamplesMetric, map[string]diskCounterSnapshot, bool) {
	inventory, samples, err := finalizeDeviceMetrics([]devicemetric.InventoryEntry{}, []devicemetric.Sample{})
	return inventory, samples, map[string]diskCounterSnapshot{}, err == nil
}

func resolveTrustedNvidiaSMI() (string, bool) { return "", false }

func statFilesystem(string) (filesystemStat, error) {
	return filesystemStat{}, errors.New("filesystem statistics unsupported")
}
