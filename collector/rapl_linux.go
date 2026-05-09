// Copyright 2019 The Prometheus Authors
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

//go:build !norapl
// +build !norapl

package collector

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/procfs/sysfs"
)

const raplCollectorSubsystem = "rapl"

type raplCollector struct {
	fs     sysfs.FS
	logger *slog.Logger

	joulesMetricDesc *prometheus.Desc

	constraint0MaxPowerUWDesc   *prometheus.Desc
	constraint0PowerLimitUWDesc *prometheus.Desc
	constraint1PowerLimitUWDesc *prometheus.Desc
}

func init() {
	registerCollector(raplCollectorSubsystem, defaultEnabled, NewRaplCollector)
}

var (
	raplZoneLabel = kingpin.Flag("collector.rapl.enable-zone-label", "Enables service unit metric unit_start_time_seconds").Bool()
)

// NewRaplCollector returns a new Collector exposing RAPL metrics.
func NewRaplCollector(logger *slog.Logger) (Collector, error) {
	fs, err := sysfs.NewFS(*sysPath)

	if err != nil {
		return nil, err
	}

	joulesMetricDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, raplCollectorSubsystem, "joules_total"),
		"Current RAPL value in joules",
		[]string{"index", "path", "rapl_zone"}, nil,
	)

	constraintLabels := []string{"index", "path"}
	if *raplZoneLabel {
		constraintLabels = []string{"index", "path", "rapl_zone"}
	}

	collector := raplCollector{
		fs:               fs,
		logger:           logger,
		joulesMetricDesc: joulesMetricDesc,
		constraint0MaxPowerUWDesc: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, raplCollectorSubsystem, "constraint_0_max_power_uw"),
			"RAPL constraint_0 maximum power in microwatts (powercap constraint_0_max_power_uw).",
			constraintLabels, nil,
		),
		constraint0PowerLimitUWDesc: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, raplCollectorSubsystem, "constraint_0_power_limit_uw"),
			"RAPL constraint_0 power limit in microwatts (powercap constraint_0_power_limit_uw).",
			constraintLabels, nil,
		),
		constraint1PowerLimitUWDesc: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, raplCollectorSubsystem, "constraint_1_power_limit_uw"),
			"RAPL constraint_1 power limit in microwatts (powercap constraint_1_power_limit_uw).",
			constraintLabels, nil,
		),
	}
	return &collector, nil
}

// Update implements Collector and exposes RAPL related metrics.
func (c *raplCollector) Update(ch chan<- prometheus.Metric) error {
	// nil zones are fine when platform doesn't have powercap files present.
	zones, err := sysfs.GetRaplZones(c.fs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.logger.Debug("Platform doesn't have powercap files present", "err", err)
			return ErrNoData
		}
		if errors.Is(err, os.ErrPermission) {
			c.logger.Debug("Can't access powercap files", "err", err)
			return ErrNoData
		}
		return fmt.Errorf("failed to retrieve rapl stats: %w", err)
	}

	for _, rz := range zones {
		microJoules, err := rz.GetEnergyMicrojoules()
		if err != nil {
			if errors.Is(err, os.ErrPermission) {
				c.logger.Debug("Can't access energy_uj file", "zone", rz, "err", err)
				return ErrNoData
			}
			return err
		}

		joules := float64(microJoules) / 1000000.0

		if *raplZoneLabel {
			ch <- c.joulesMetricWithZoneLabel(rz, joules)
		} else {
			ch <- c.joulesMetric(rz, joules)
		}

		c.emitRAPLConstraintUW(ch, c.constraint0MaxPowerUWDesc, rz, "constraint_0_max_power_uw")
		c.emitRAPLConstraintUW(ch, c.constraint0PowerLimitUWDesc, rz, "constraint_0_power_limit_uw")
		c.emitRAPLConstraintUW(ch, c.constraint1PowerLimitUWDesc, rz, "constraint_1_power_limit_uw")
	}
	return nil
}

func (c *raplCollector) emitRAPLConstraintUW(ch chan<- prometheus.Metric, desc *prometheus.Desc, rz sysfs.RaplZone, fileName string) {
	p := filepath.Join(rz.Path, fileName)
	v, err := readUintFromFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		c.logger.Debug("skipping RAPL constraint file", "path", p, "err", err)
		return
	}

	index := strconv.Itoa(rz.Index)
	if *raplZoneLabel {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, float64(v), index, rz.Path, rz.Name)
		return
	}
	ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, float64(v), index, rz.Path)
}

func (c *raplCollector) joulesMetric(z sysfs.RaplZone, v float64) prometheus.Metric {
	index := strconv.Itoa(z.Index)
	descriptor := prometheus.NewDesc(
		prometheus.BuildFQName(
			namespace,
			raplCollectorSubsystem,
			fmt.Sprintf("%s_joules_total", SanitizeMetricName(z.Name)),
		),
		fmt.Sprintf("Current RAPL %s value in joules", z.Name),
		[]string{"index", "path"}, nil,
	)

	return prometheus.MustNewConstMetric(
		descriptor,
		prometheus.CounterValue,
		v,
		index,
		z.Path,
	)
}

func (c *raplCollector) joulesMetricWithZoneLabel(z sysfs.RaplZone, v float64) prometheus.Metric {
	index := strconv.Itoa(z.Index)

	return prometheus.MustNewConstMetric(
		c.joulesMetricDesc,
		prometheus.CounterValue,
		v,
		index,
		z.Path,
		z.Name,
	)
}
