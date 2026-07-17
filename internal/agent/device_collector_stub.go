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

func (c *deviceCollector) collectPlatform(context.Context, time.Time) (devicemetric.InventoryMetric, devicemetric.SamplesMetric, bool) {
	inventory, samples, err := finalizeDeviceMetrics([]devicemetric.InventoryEntry{}, []devicemetric.Sample{})
	return inventory, samples, err == nil
}

func resolveTrustedNvidiaSMI() (string, bool) { return "", false }

func statFilesystem(string) (filesystemStat, error) {
	return filesystemStat{}, errors.New("filesystem statistics unsupported")
}
