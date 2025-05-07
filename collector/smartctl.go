package collector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/prometheus/node_exporter/collector/utils"
	"os/exec"
	"time"
)

var execCommand = exec.Command

type Smartctl struct {
	Path    string
	NoCheck string
	UseSudo bool
	Timeout time.Duration
}

func SmartctlNew() *Smartctl {
	s := &Smartctl{
		Timeout: time.Second * 5,
	}
	if s.Path == "" {
		s.Path = "/usr/sbin/smartctl"
	}
	switch s.NoCheck {
	case "never", "sleep", "standby", "idle":
	case "":
		s.NoCheck = "standby"
	default:
		return nil
	}

	if s.Timeout == 0 {
		s.Timeout = time.Second * 30
	}
	return s
}

var scanArgs = []string{"--json", "--scan-open"}

type scanDevice struct {
	Name string
	Type string
}

func (s *Smartctl) scan() ([]scanDevice, error) {
	cmd := execCommand(s.Path, scanArgs...)
	if s.UseSudo {
		cmd = execCommand("sudo", append([]string{"-n", s.Path}, scanArgs...)...)
	}
	out, err := CombinedOutputTimeout(cmd, s.Timeout)
	if err != nil {
		return nil, fmt.Errorf("error running smartctl with %s: %w", scanArgs, err)
	}
	var scan smartctlScanJSON
	if err := json.Unmarshal(out, &scan); err != nil {
		return nil, fmt.Errorf("error unmarshalling smartctl scan output: %w", err)
	}
	devices := make([]scanDevice, 0)
	for _, device := range scan.Devices {
		dev := scanDevice{
			Name: device.Name,
			Type: device.Type,
		}
		devices = append(devices, dev)
	}
	return devices, nil
}

func (s *Smartctl) scanDevice(deviceName, deviceType string) (*smartctlDeviceJSON, error) {
	args := []string{"--json", "--all", deviceName, "--device", deviceType, "--nocheck=" + s.NoCheck}
	cmd := execCommand(s.Path, args...)
	if s.UseSudo {
		cmd = execCommand("sudo", append([]string{"-n", s.Path}, args...)...)
	}
	var device smartctlDeviceJSON
	out, err := CombinedOutputTimeout(cmd, s.Timeout)
	if err != nil {
		// Error running the command and unable to parse the JSON, then bail
		if jsonErr := json.Unmarshal(out, &device); jsonErr != nil {
			return nil, fmt.Errorf("error running smartctl with %s: %w", args, err)
		}

		// If we were able to parse the result, then only exit if we get an error
		// as sometimes we can get warnings, that still produce data.
		if len(device.Smartctl.Messages) > 0 &&
			device.Smartctl.Messages[0].Severity == "error" &&
			device.Smartctl.Messages[0].String != "" {
			return nil, fmt.Errorf("error running smartctl with %s got smartctl error message: %s", args, device.Smartctl.Messages[0].String)
		}
	}
	if err := json.Unmarshal(out, &device); err != nil {
		return nil, fmt.Errorf("error unable to unmarshall response %s: %w", args, err)
	}
	return &device, nil
}

func CombinedOutputTimeout(c *exec.Cmd, timeout time.Duration) ([]byte, error) {
	var b bytes.Buffer
	c.Stdout = &b
	c.Stderr = &b
	if err := c.Start(); err != nil {
		return nil, err
	}
	err := utils.WaitTimeout(c, timeout)
	return b.Bytes(), err
}

type smartctlDeviceJSON struct {
	JSONFormatVersion []int `json:"json_format_version"`
	Smartctl          struct {
		Version      []int    `json:"version"`
		PreRelease   bool     `json:"pre_release"`
		SvnRevision  string   `json:"svn_revision"`
		PlatformInfo string   `json:"platform_info"`
		BuildInfo    string   `json:"build_info"`
		Argv         []string `json:"argv"`
		Messages     []struct {
			Severity string `json:"severity"`
			String   string `json:"string"`
		} `json:"messages"`
		ExitStatus int `json:"exit_status"`
	} `json:"smartctl"`
	Device struct {
		Name     string `json:"name"`
		InfoName string `json:"info_name"`
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
	} `json:"device"`
	Vendor                           string `json:"vendor"`
	Product                          string `json:"product"`
	ModelFamily                      string `json:"model_family"`
	ModelName                        string `json:"model_name"`
	SerialNumber                     string `json:"serial_number"`
	FirmwareVersion                  string `json:"firmware_version"`
	SCSIVendor                       string `json:"scsi_vendor"`
	SCSIModelName                    string `json:"scsi_model_name"`
	SCSIRevision                     string `json:"scsi_revision"`
	SCSIVersion                      string `json:"scsi_version"`
	SCSIProtectionType               int    `json:"scsi_protection_type"`
	SCSIProtectionIntervalBytesPerLB int    `json:"scsi_protection_interval_bytes_per_lb"`
	SCSIGrownDefectList              int    `json:"scsi_grown_defect_list"`
	LogicalBlockSize                 int    `json:"logical_block_size"`
	PhysicalBlockSize                int    `json:"physical_block_size"`
	RotationRate                     int    `json:"rotation_rate"`
	SCSITransportProtocol            struct {
		Name string `json:"name"`
	} `json:"scsi_transport_protocol"`
	SCSIStartStopCycleCounter struct {
		SpecifiedCycleCountOverDeviceLifetime int `json:"specified_cycle_count_over_device_lifetime"`
		AccumulatedStartStopCycles            int `json:"accumulated_start_stop_cycles"`
	} `json:"scsi_start_stop_cycle_counter"`
	PowerOnTime struct {
		Hours   int `json:"hours"`
		Minutes int `json:"minutes"`
	} `json:"power_on_time"`
	Wwn struct {
		Naa int   `json:"naa"`
		Oui int   `json:"oui"`
		ID  int64 `json:"id"`
	} `json:"wwn"`
	UserCapacity struct {
		Bytes int64 `json:"bytes"`
	} `json:"user_capacity"`
	SmartStatus struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	NvmeSmartHealthInformationLog struct {
		CriticalWarning         int64 `json:"critical_warning"`
		Temperature             int64 `json:"temperature"`
		AvailableSpare          int64 `json:"available_spare"`
		AvailableSpareThreshold int64 `json:"available_spare_threshold"`
		PercentageUsed          int64 `json:"percentage_used"`
		DataUnitsRead           int64 `json:"data_units_read"`
		DataUnitsWritten        int64 `json:"data_units_written"`
		HostReads               int64 `json:"host_reads"`
		HostWrites              int64 `json:"host_writes"`
		ControllerBusyTime      int64 `json:"controller_busy_time"`
		PowerCycles             int64 `json:"power_cycles"`
		PowerOnHours            int64 `json:"power_on_hours"`
		UnsafeShutdowns         int64 `json:"unsafe_shutdowns"`
		MediaErrors             int64 `json:"media_errors"`
		NumErrLogEntries        int64 `json:"num_err_log_entries"`
		WarningTempTime         int64 `json:"warning_temp_time"`
		CriticalCompTime        int64 `json:"critical_comp_time"`
	} `json:"nvme_smart_health_information_log"`
	Temperature struct {
		Current int `json:"current"`
	} `json:"temperature"`
	AtaSmartAttributes struct {
		Revision int `json:"revision"`
		Table    []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Value      int64  `json:"value"`
			Worst      int64  `json:"worst"`
			Thresh     int64  `json:"thresh"`
			WhenFailed string `json:"when_failed"`
			Flags      struct {
				Value         int64  `json:"value"`
				String        string `json:"string"`
				Prefailure    bool   `json:"prefailure"`
				UpdatedOnline bool   `json:"updated_online"`
				Performance   bool   `json:"performance"`
				ErrorRate     bool   `json:"error_rate"`
				EventCount    bool   `json:"event_count"`
				AutoKeep      bool   `json:"auto_keep"`
			} `json:"flags"`
			Raw struct {
				Value  int64  `json:"value"`
				String string `json:"string"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	ScsiErrorCounterLog struct {
		Read struct {
			ErrorsCorrectedByEccfast         int    `json:"errors_corrected_by_eccfast"`
			ErrorsCorrectedByEccdelayed      int    `json:"errors_corrected_by_eccdelayed"`
			ErrorsCorrectedByRereadsRewrites int    `json:"errors_corrected_by_rereads_rewrites"`
			TotalErrorsCorrected             int    `json:"total_errors_corrected"`
			CorrectionAlgorithmInvocations   int    `json:"correction_algorithm_invocations"`
			GigabytesProcessed               string `json:"gigabytes_processed"`
			TotalUncorrectedErrors           int    `json:"total_uncorrected_errors"`
		} `json:"read"`
		Write struct {
			ErrorsCorrectedByEccfast         int    `json:"errors_corrected_by_eccfast"`
			ErrorsCorrectedByEccdelayed      int    `json:"errors_corrected_by_eccdelayed"`
			ErrorsCorrectedByRereadsRewrites int    `json:"errors_corrected_by_rereads_rewrites"`
			TotalErrorsCorrected             int    `json:"total_errors_corrected"`
			CorrectionAlgorithmInvocations   int    `json:"correction_algorithm_invocations"`
			GigabytesProcessed               string `json:"gigabytes_processed"`
			TotalUncorrectedErrors           int    `json:"total_uncorrected_errors"`
		} `json:"write"`
		Verify struct {
			ErrorsCorrectedByEccfast         int    `json:"errors_corrected_by_eccfast"`
			ErrorsCorrectedByEccdelayed      int    `json:"errors_corrected_by_eccdelayed"`
			ErrorsCorrectedByRereadsRewrites int    `json:"errors_corrected_by_rereads_rewrites"`
			TotalErrorsCorrected             int    `json:"total_errors_corrected"`
			CorrectionAlgorithmInvocations   int    `json:"correction_algorithm_invocations"`
			GigabytesProcessed               string `json:"gigabytes_processed"`
			TotalUncorrectedErrors           int    `json:"total_uncorrected_errors"`
		} `json:"verify"`
	} `json:"scsi_error_counter_log"`
}
type smartctlScanJSON struct {
	JSONFormatVersion []int `json:"json_format_version"`
	Smartctl          struct {
		Version      []int    `json:"version"`
		PreRelease   bool     `json:"pre_release"`
		SvnRevision  string   `json:"svn_revision"`
		PlatformInfo string   `json:"platform_info"`
		BuildInfo    string   `json:"build_info"`
		Argv         []string `json:"argv"`
		ExitStatus   int      `json:"exit_status"`
	} `json:"smartctl"`
	Devices []struct {
		Name     string `json:"name"`
		InfoName string `json:"info_name"`
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
	} `json:"devices"`
}
