//go:build !nolsblk
// +build !nolsblk

package collector

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
)

const lsblkDiskSubsystem = "disk"

var lsblkPath = kingpin.Flag(
	"collector.lsblk.path",
	"Path to the lsblk binary.",
).Default("/bin/lsblk").String()

var lsblkTimeout = kingpin.Flag(
	"collector.lsblk.timeout",
	"Timeout for running lsblk.",
).Default("5s").Duration()

type lsblkCollector struct {
	logger   *slog.Logger
	infoDesc *prometheus.Desc
}

func init() {
	registerCollector("lsblk", defaultEnabled, NewLsblkCollector)
}

// NewLsblkCollector returns a collector that exposes lsblk(8) JSON output as metrics.
func NewLsblkCollector(logger *slog.Logger) (Collector, error) {
	return &lsblkCollector{
		logger: logger,
		infoDesc: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, lsblkDiskSubsystem, "lsblk_info"),
			"Non-numeric block device fields from lsblk --json, value is always 1.",
			[]string{"name", "parent", "fstype", "mountpoint", "size", "fsused", "fsuse_percent"},
			nil,
		),
	}, nil
}

type lsblkJSONRoot struct {
	Blockdevices []lsblkJSONDevice `json:"blockdevices"`
}

type lsblkJSONDevice struct {
	Name       string            `json:"name"`
	Size       *string           `json:"size"`
	Fstype     *string           `json:"fstype"`
	Mountpoint *string           `json:"mountpoint"`
	Fsused     *string           `json:"fsused"`
	FsusePct   *string           `json:"fsuse%"`
	Children   []lsblkJSONDevice `json:"children"`
}

func lsblkStringPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// lsblkMountpointForLabels maps mount paths under --path.rootfs to the host view (same as filesystem collector).
func lsblkMountpointForLabels(mp *string) string {
	if mp == nil || *mp == "" {
		return ""
	}
	return rootfsStripPrefix(*mp)
}

func (c *lsblkCollector) emitRecursive(ch chan<- prometheus.Metric, devices []lsblkJSONDevice, parent string) {
	for _, d := range devices {
		ch <- prometheus.MustNewConstMetric(c.infoDesc, prometheus.GaugeValue, 1,
			d.Name,
			parent,
			lsblkStringPtr(d.Fstype),
			lsblkMountpointForLabels(d.Mountpoint),
			lsblkStringPtr(d.Size),
			lsblkStringPtr(d.Fsused),
			lsblkStringPtr(d.FsusePct),
		)
		if len(d.Children) > 0 {
			c.emitRecursive(ch, d.Children, d.Name)
		}
	}
}

func (c *lsblkCollector) Update(ch chan<- prometheus.Metric) error {
	path := *lsblkPath
	if path == "" {
		return fmt.Errorf("collector.lsblk.path is empty")
	}
	if _, err := os.Stat(path); err != nil {
		c.logger.Debug("lsblk binary not available", "path", path, "err", err)
		return ErrNoData
	}

	args := []string{"--json", "-o", "NAME,SIZE,FSTYPE,MOUNTPOINT,FSUSED,FSUSE%"}
	cmd := execCommand(path, args...)
	out, err := CombinedOutputTimeout(cmd, *lsblkTimeout)
	if err != nil {
		return fmt.Errorf("lsblk failed: %w", err)
	}

	var root lsblkJSONRoot
	if err := json.Unmarshal(out, &root); err != nil {
		return fmt.Errorf("lsblk json parse error: %w", err)
	}

	c.emitRecursive(ch, root.Blockdevices, "")
	return nil
}
