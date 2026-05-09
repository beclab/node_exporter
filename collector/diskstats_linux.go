// Copyright 2015 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !nodiskstats
// +build !nodiskstats

package collector

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/procfs/blockdevice"
)

const (
	secondsPerTick = 1.0 / 1000.0

	// Read sectors and write sectors are the "standard UNIX 512-byte sectors, not any device- or filesystem-specific block size."
	// See also https://www.kernel.org/doc/Documentation/block/stat.txt
	unixSectorSize = 512.0

	diskstatsDefaultIgnoredDevices = "^(z?ram|loop|fd|(h|s|v|xv)d[a-z]|nvme\\d+n\\d+p)\\d+$"

	// See udevadm(8).
	udevDevicePropertyPrefix = "E:"

	// Udev device properties.
	udevDMLVLayer               = "DM_LV_LAYER"
	udevDMLVName                = "DM_LV_NAME"
	udevDMName                  = "DM_NAME"
	udevDMUUID                  = "DM_UUID"
	udevDMVGName                = "DM_VG_NAME"
	udevIDATA                   = "ID_ATA"
	udevIDATARotationRateRPM    = "ID_ATA_ROTATION_RATE_RPM"
	udevIDATASATA               = "ID_ATA_SATA"
	udevIDATASATASignalRateGen1 = "ID_ATA_SATA_SIGNAL_RATE_GEN1"
	udevIDATASATASignalRateGen2 = "ID_ATA_SATA_SIGNAL_RATE_GEN2"
	udevIDATAWriteCache         = "ID_ATA_WRITE_CACHE"
	udevIDATAWriteCacheEnabled  = "ID_ATA_WRITE_CACHE_ENABLED"
	udevIDBus                   = "ID_BUS"
	udevIDFSType                = "ID_FS_TYPE"
	udevIDFSUsage               = "ID_FS_USAGE"
	udevIDFSUUID                = "ID_FS_UUID"
	udevIDFSVersion             = "ID_FS_VERSION"
	udevIDModel                 = "ID_MODEL"
	udevIDPath                  = "ID_PATH"
	udevIDRevision              = "ID_REVISION"
	udevIDSerialShort           = "ID_SERIAL_SHORT"
	udevIDWWN                   = "ID_WWN"
	udevSCSIIdentSerial         = "SCSI_IDENT_SERIAL"
)

type typedFactorDesc struct {
	desc      *prometheus.Desc
	valueType prometheus.ValueType
}

type udevInfo map[string]string

func (d *typedFactorDesc) mustNewConstMetric(value float64, labels ...string) prometheus.Metric {
	return prometheus.MustNewConstMetric(d.desc, d.valueType, value, labels...)
}

type diskstatsCollector struct {
	deviceFilter            deviceFilter
	fs                      blockdevice.FS
	infoDesc                typedFactorDesc
	descs                   []typedFactorDesc
	filesystemInfoDesc      typedFactorDesc
	deviceMapperInfoDesc    typedFactorDesc
	ataDescs                map[string]typedFactorDesc
	smartctlDesc            typedFactorDesc
	logger                  *slog.Logger
	getUdevDeviceProperties func(uint32, uint32) (udevInfo, error)
	smartctl                *Smartctl
	nvmePciCtl              *NvmePciCtl
}

func init() {
	registerCollector("diskstats", defaultEnabled, NewDiskstatsCollector)
}

// NewDiskstatsCollector returns a new Collector exposing disk device stats.
// Docs from https://www.kernel.org/doc/Documentation/iostats.txt
func NewDiskstatsCollector(logger *slog.Logger) (Collector, error) {
	var diskLabelNames = []string{"device"}
	fs, err := blockdevice.NewFS(*procPath, *sysPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sysfs: %w", err)
	}

	deviceFilter, err := newDiskstatsDeviceFilter(logger)
	if err != nil {
		return nil, fmt.Errorf("failed to parse device filter flags: %w", err)
	}

	collector := diskstatsCollector{
		deviceFilter: deviceFilter,
		fs:           fs,
		infoDesc: typedFactorDesc{
			desc: prometheus.NewDesc(prometheus.BuildFQName(namespace, diskSubsystem, "info"),
				"Info of /sys/block/<block_device>.",
				[]string{"device", "major", "minor", "path", "wwn", "model", "serial", "revision", "rotational", "bus", "removable"},
				nil,
			), valueType: prometheus.GaugeValue,
		},
		descs: []typedFactorDesc{
			{
				desc: readsCompletedDesc, valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "reads_merged_total"),
					"The total number of reads merged.",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
			{
				desc: readBytesDesc, valueType: prometheus.CounterValue,
			},
			{
				desc: readTimeSecondsDesc, valueType: prometheus.CounterValue,
			},
			{
				desc: writesCompletedDesc, valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "writes_merged_total"),
					"The number of writes merged.",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
			{
				desc: writtenBytesDesc, valueType: prometheus.CounterValue,
			},
			{
				desc: writeTimeSecondsDesc, valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "io_now"),
					"The number of I/Os currently in progress.",
					diskLabelNames,
					nil,
				), valueType: prometheus.GaugeValue,
			},
			{
				desc: ioTimeSecondsDesc, valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "io_time_weighted_seconds_total"),
					"The weighted # of seconds spent doing I/Os.",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "discards_completed_total"),
					"The total number of discards completed successfully.",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "discards_merged_total"),
					"The total number of discards merged.",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "discarded_sectors_total"),
					"The total number of sectors discarded successfully.",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "discard_time_seconds_total"),
					"This is the total number of seconds spent by all discards.",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "flush_requests_total"),
					"The total number of flush requests completed successfully",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "flush_requests_time_seconds_total"),
					"This is the total number of seconds spent by all flush requests.",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
		},
		filesystemInfoDesc: typedFactorDesc{
			desc: prometheus.NewDesc(prometheus.BuildFQName(namespace, diskSubsystem, "filesystem_info"),
				"Info about disk filesystem.",
				[]string{"device", "type", "usage", "uuid", "version"},
				nil,
			), valueType: prometheus.GaugeValue,
		},
		deviceMapperInfoDesc: typedFactorDesc{
			desc: prometheus.NewDesc(prometheus.BuildFQName(namespace, diskSubsystem, "device_mapper_info"),
				"Info about disk device mapper.",
				[]string{"device", "name", "uuid", "vg_name", "lv_name", "lv_layer"},
				nil,
			), valueType: prometheus.GaugeValue,
		},
		ataDescs: map[string]typedFactorDesc{
			udevIDATAWriteCache: {
				desc: prometheus.NewDesc(prometheus.BuildFQName(namespace, diskSubsystem, "ata_write_cache"),
					"ATA disk has a write cache.",
					[]string{"device"},
					nil,
				), valueType: prometheus.GaugeValue,
			},
			udevIDATAWriteCacheEnabled: {
				desc: prometheus.NewDesc(prometheus.BuildFQName(namespace, diskSubsystem, "ata_write_cache_enabled"),
					"ATA disk has its write cache enabled.",
					[]string{"device"},
					nil,
				), valueType: prometheus.GaugeValue,
			},
			udevIDATARotationRateRPM: {
				desc: prometheus.NewDesc(prometheus.BuildFQName(namespace, diskSubsystem, "ata_rotation_rate_rpm"),
					"ATA disk rotation rate in RPMs (0 for SSDs).",
					[]string{"device"},
					nil,
				), valueType: prometheus.GaugeValue,
			},
		},
		smartctlDesc: typedFactorDesc{
			desc: prometheus.NewDesc(prometheus.BuildFQName(namespace, diskSubsystem, "smartctl_info"),
				"Info of smartctl command.",
				[]string{"device", "name", "type", "serial", "model", "vendor", "health_ok", "firmware", "capacity", "protocol", "logical_block_size", "physical_block_size", "rotational", "pcie_version", "sata_version"},
				nil,
			), valueType: prometheus.GaugeValue,
		},
		logger:     logger,
		smartctl:   SmartctlNew(),
		nvmePciCtl: NvmeCliNew(),
	}

	// Only enable getting device properties from udev if the directory is readable.
	if stat, err := os.Stat(*udevDataPath); err != nil || !stat.IsDir() {
		logger.Error("Failed to open directory, disabling udev device properties", "path", *udevDataPath)
	} else {
		collector.getUdevDeviceProperties = getUdevDeviceProperties
	}

	return &collector, nil
}

func (c *diskstatsCollector) Update(ch chan<- prometheus.Metric) error {
	diskStats, err := c.fs.ProcDiskstats()
	if err != nil {
		return fmt.Errorf("couldn't get diskstats: %w", err)
	}

	smartctlResult, err := c.scan()
	if err != nil {
		c.logger.Info("Failed to get smartctl result", "err", err)
	}

	nvmePaths, err := c.nvmePciCtl.nvmeSubsystemList()
	if err != nil {
		c.logger.Info("Failed to get nvme subsys list", "err", err)
	}
	deviceToPcieVersionMap := make(map[string]string)
	for _, p := range nvmePaths {
		deviceToPcieVersionMap[p.Name+"n1"] = p.Address
	}

	for _, stats := range diskStats {
		dev := stats.DeviceName
		if c.deviceFilter.ignored(dev) {
			continue
		}
		var pcieVersion string
		if address, ok := deviceToPcieVersionMap[dev]; ok {
			pcieVersion, _ = c.nvmePciCtl.pciVersion(address)
			pcieVersion = fmt.Sprintf("%s, %s", pcieVersion, versionToSpeedMap[pcieVersion])
		}

		smartJSON, exists := getDeviceResult(smartctlResult, stats.DeviceName)
		if !exists {
			c.logger.Info("get device result from smartctl failed", "deviceName", stats.DeviceName)
		}

		info, err := getUdevDeviceProperties(stats.MajorNumber, stats.MinorNumber)
		if err != nil {
			c.logger.Debug("Failed to parse udev info", "err", err)
		}

		// This is usually the serial printed on the disk label.
		serial := info[udevSCSIIdentSerial]

		// If it's undefined, fallback to ID_SERIAL_SHORT instead.
		if serial == "" {
			serial = info[udevIDSerialShort]
		}

		queueStats, err := c.fs.SysBlockDeviceQueueStats(dev)
		// Block Device Queue stats may not exist for all devices.
		if err != nil && !os.IsNotExist(err) {
			c.logger.Debug("Failed to get block device queue stats", "device", dev, "err", err)
		}

		ch <- c.infoDesc.mustNewConstMetric(1.0,
			dev,
			fmt.Sprint(stats.MajorNumber),
			fmt.Sprint(stats.MinorNumber),
			info[udevIDPath],
			info[udevIDWWN],
			info[udevIDModel],
			serial,
			info[udevIDRevision],
			strconv.FormatUint(queueStats.Rotational, 2),
			info[udevIDBus],
			readSysBlockRemovable(dev),
		)

		statCount := stats.IoStatsCount - 3 // Total diskstats record count, less MajorNumber, MinorNumber and DeviceName

		for i, val := range []float64{
			float64(stats.ReadIOs),
			float64(stats.ReadMerges),
			float64(stats.ReadSectors) * unixSectorSize,
			float64(stats.ReadTicks) * secondsPerTick,
			float64(stats.WriteIOs),
			float64(stats.WriteMerges),
			float64(stats.WriteSectors) * unixSectorSize,
			float64(stats.WriteTicks) * secondsPerTick,
			float64(stats.IOsInProgress),
			float64(stats.IOsTotalTicks) * secondsPerTick,
			float64(stats.WeightedIOTicks) * secondsPerTick,
			float64(stats.DiscardIOs),
			float64(stats.DiscardMerges),
			float64(stats.DiscardSectors),
			float64(stats.DiscardTicks) * secondsPerTick,
			float64(stats.FlushRequestsCompleted),
			float64(stats.TimeSpentFlushing) * secondsPerTick,
		} {
			if i >= statCount {
				break
			}
			ch <- c.descs[i].mustNewConstMetric(val, dev)
		}
		if smartJSON != nil {
			fieldDesc := prometheus.NewDesc(
				prometheus.BuildFQName(namespace, diskSubsystem, "temp_celsius"),
				"temp_celsius from smartctl",
				[]string{"device"},
				nil,
			)
			ch <- prometheus.MustNewConstMetric(fieldDesc, prometheus.GaugeValue, float64(smartJSON.Temperature.Current), dev)

			fieldDesc = prometheus.NewDesc(
				prometheus.BuildFQName(namespace, diskSubsystem, "power_on_hours"),
				"power_on_hours from smartctl",
				[]string{"device"},
				nil,
			)
			powerOnHours := int64(smartJSON.PowerOnTime.Hours)
			if smartJSON.Device.Type == "nvme" {
				powerOnHours = smartJSON.NvmeSmartHealthInformationLog.PowerOnHours
			}
			sataVersion := ""
			if smartJSON.Device.Type == "sat" {
				sataVersion = fmt.Sprintf("%s, %s", smartJSON.SataVersion.Name, smartJSON.SataInterfaceSpeed.Current.String)
			}
			ch <- prometheus.MustNewConstMetric(fieldDesc, prometheus.GaugeValue, float64(powerOnHours), dev)

			ch <- c.smartctlDesc.mustNewConstMetric(1.0,
				dev,
				smartJSON.Device.Name,
				smartJSON.Device.Type,
				smartJSON.SerialNumber,
				smartJSON.ModelName,
				smartJSON.Vendor,
				strconv.FormatBool(smartJSON.SmartStatus.Passed),
				smartJSON.FirmwareVersion,
				strconv.FormatInt(smartJSON.UserCapacity.Bytes, 10),
				smartJSON.Device.Protocol,
				strconv.FormatInt(int64(smartJSON.LogicalBlockSize), 10),
				strconv.FormatInt(int64(smartJSON.PhysicalBlockSize), 10),
				func() string {
					if smartJSON.RotationRate > 0 {
						return "1"
					}
					return "0"
				}(),
				pcieVersion,
				sataVersion,
			)

			fieldDesc = prometheus.NewDesc(
				prometheus.BuildFQName(namespace, diskSubsystem, "data_bytes_written"),
				"data_bytes_written from smartctl",
				[]string{"device"},
				nil,
			)
			dataBytesWritten := int64(0)
			if smartJSON.Device.Type == "nvme" {
				dataBytesWritten = smartJSON.NvmeSmartHealthInformationLog.DataUnitsWritten * 512000
			} else {
				for _, t := range smartJSON.AtaSmartAttributes.Table {
					if t.Name == "Total_LBAs_Written" {
						dataBytesWritten = t.Raw.Value * int64(smartJSON.LogicalBlockSize)
					}
				}
			}
			ch <- prometheus.MustNewConstMetric(fieldDesc, prometheus.CounterValue, float64(dataBytesWritten), dev)

			fieldDesc = prometheus.NewDesc(
				prometheus.BuildFQName(namespace, diskSubsystem, "data_bytes_read"),
				"data_bytes_read from smartctl",
				[]string{"device"},
				nil,
			)
			dataBytesRead := int64(0)
			if smartJSON.Device.Type == "nvme" {
				dataBytesRead = smartJSON.NvmeSmartHealthInformationLog.DataUnitsRead * 512000
			} else {
				for _, t := range smartJSON.AtaSmartAttributes.Table {
					if t.Name == "Total_LBAs_Read" {
						dataBytesRead = t.Raw.Value * int64(smartJSON.LogicalBlockSize)
					}
				}
			}
			ch <- prometheus.MustNewConstMetric(fieldDesc, prometheus.CounterValue, float64(dataBytesRead), dev)
		}

		if fsType := info[udevIDFSType]; fsType != "" {
			ch <- c.filesystemInfoDesc.mustNewConstMetric(1.0, dev,
				fsType,
				info[udevIDFSUsage],
				info[udevIDFSUUID],
				info[udevIDFSVersion],
			)
		}

		if name := info[udevDMName]; name != "" {
			ch <- c.deviceMapperInfoDesc.mustNewConstMetric(1.0, dev,
				name,
				info[udevDMUUID],
				info[udevDMVGName],
				info[udevDMLVName],
				info[udevDMLVLayer],
			)
		}

		if ata := info[udevIDATA]; ata != "" {
			for attr, desc := range c.ataDescs {
				str, ok := info[attr]
				if !ok {
					c.logger.Debug("Udev attribute does not exist", "attribute", attr)
					continue
				}

				if value, err := strconv.ParseFloat(str, 64); err == nil {
					ch <- desc.mustNewConstMetric(value, dev)
				} else {
					c.logger.Error("Failed to parse ATA value", "err", err)
				}
			}
		}
	}
	return nil
}

// readSysBlockRemovable returns the contents of /sys/block/<dev>/removable
// (typically "0" or "1"). Returns an empty string if the file is missing or
// cannot be read (e.g. for partitions or virtual devices).
func readSysBlockRemovable(dev string) string {
	data, err := os.ReadFile(sysFilePath("block/" + dev + "/removable"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func getUdevDeviceProperties(major, minor uint32) (udevInfo, error) {
	filename := udevDataFilePath(fmt.Sprintf("b%d:%d", major, minor))

	data, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer data.Close()

	info := make(udevInfo)

	scanner := bufio.NewScanner(data)
	for scanner.Scan() {
		line := scanner.Text()

		// We're only interested in device properties.
		if !strings.HasPrefix(line, udevDevicePropertyPrefix) {
			continue
		}

		line = strings.TrimPrefix(line, udevDevicePropertyPrefix)

		/* TODO: After we drop support for Go 1.17, the condition below can be simplified to:

		if name, value, found := strings.Cut(line, "="); found {
			info[name] = value
		}
		*/
		if fields := strings.SplitN(line, "=", 2); len(fields) == 2 {
			info[fields[0]] = fields[1]
		}
	}

	return info, nil
}

func (c *diskstatsCollector) scan() (map[string]*smartctlDeviceJSON, error) {
	if c.smartctl == nil {
		return nil, fmt.Errorf("smartctl is nil in diskstatsCollector")
	}
	devices, err := c.smartctl.scan()
	if err != nil {
		return nil, fmt.Errorf("smartctl scan device error: %w", err)
	}
	result := make(map[string]*smartctlDeviceJSON)
	for _, device := range devices {
		res, err := c.smartctl.scanDevice(device.Name, device.Type)
		if err != nil {
			return nil, fmt.Errorf("smarctl scan device: %s, error: %w", device.Name, err)
		}
		if strings.Contains(strings.ToUpper(res.SCSIVendor), "QEMU") {
			continue
		}
		result[device.Name] = res
	}
	return result, nil
}

func getDeviceResult(deviceMap map[string]*smartctlDeviceJSON, shortDeviceName string) (*smartctlDeviceJSON, bool) {
	fullPath := "/dev/" + shortDeviceName
	if r, exists := deviceMap[fullPath]; exists {
		return r, true
	}

	for devPath := range deviceMap {
		if strings.HasPrefix(devPath, "/dev/") {
			deviceName := strings.TrimPrefix(devPath, "/dev/")
			if deviceName == shortDeviceName {
				fullPath = devPath
			}
			if strings.HasPrefix(shortDeviceName, deviceName) {
				fullPath = devPath
			}
			if strings.HasPrefix(deviceName, shortDeviceName) {
				fullPath = devPath
			}
		}
	}
	r, exists := deviceMap[fullPath]
	return r, exists
}
