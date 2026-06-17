//go:build !nolsblk
// +build !nolsblk

package collector

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"

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
	Size       *int64            `json:"size"`
	Fstype     *string           `json:"fstype"`
	Mountpoint *string           `json:"mountpoint"`
	Fsused     *int64            `json:"fsused"`
	FsusePct   *string           `json:"fsuse%"`
	Children   []lsblkJSONDevice `json:"children"`
}

func lsblkStringPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func int64PtrToString(i *int64) string {
	if i == nil {
		return ""
	}
	return strconv.FormatInt(*i, 10)
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
			int64PtrToString(d.Size),
			int64PtrToString(d.Fsused),
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

	args := []string{"--json", "-b", "-o", "NAME,SIZE,FSTYPE,MOUNTPOINT,FSUSED,FSUSE%"}
	// When running inside a container with the host rootfs mounted under
	// --path.rootfs (e.g. /host/root), the in-container lsblk cannot see the
	// host's device-mapper/LVM topology or filesystem usage. Pointing lsblk at
	// the host root via --sysroot makes it read the host's sysfs, udev and
	// mountinfo, so LVM volumes and their FSUSED/FSUSE% are reported correctly.
	if *rootfsPath != "" && *rootfsPath != "/" {
		args = append(args, "--sysroot", *rootfsPath)
	}
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
