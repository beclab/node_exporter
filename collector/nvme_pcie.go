package collector

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

var pciSpeedVersionMap = map[string]string{
	"2.5GT/s": "PCIe 1.0",
	"5GT/s":   "PCIe 2.0",
	"8GT/s":   "PCIe 3.0",
	"16GT/s":  "PCIe 4.0",
	"32GT/s":  "PCIe 5.0",
	"64GT/s":  "PCIe 6.0",
}

type NvmeDevice struct {
	HostNQN    string       `json:"HostNQN"`
	HostID     string       `json:"HostID"`
	Subsystems []Subsystems `json:"Subsystems"`
}
type Paths struct {
	Name      string `json:"Name"`
	Transport string `json:"Transport"`
	Address   string `json:"Address"`
	State     string `json:"State"`
}
type Subsystems struct {
	Name     string  `json:"Name"`
	Nqn      string  `json:"NQN"`
	IOPolicy string  `json:"IOPolicy"`
	Type     string  `json:"Type"`
	Paths    []Paths `json:"Paths"`
}

var nvmeCommand = exec.Command

type NvmePciCtl struct {
	NvmeCliPath string
	LsPciPath   string
	Timeout     time.Duration
}

func NvmeCliNew() *NvmePciCtl {
	nvme := &NvmePciCtl{
		Timeout: time.Second * 5,
	}
	if nvme.NvmeCliPath == "" {
		nvme.NvmeCliPath = "/usr/sbin/nvme"
	}
	if nvme.LsPciPath == "" {
		nvme.LsPciPath = "/usr/bin/lspci"
	}
	return nvme
}

var nvmeCliArgs = []string{"list-subsys", "-o", "json"}
var lspciArgs = []string{"-vv", "-s"}

type nvmePath struct {
	Name      string
	Transport string
	Address   string
}

func (npc *NvmePciCtl) nvmeSubsystemList() ([]nvmePath, error) {
	cmd := nvmeCommand(npc.NvmeCliPath, nvmeCliArgs...)
	out, err := CombinedOutputTimeout(cmd, npc.Timeout)
	if err != nil {
		return nil, fmt.Errorf("error running nvme cli with %s: %w", nvmeCliArgs, err)
	}
	var nv []NvmeDevice
	if err := json.Unmarshal(out, &nv); err != nil {
		return nil, fmt.Errorf("error unmarshalling nvmc cli output: %w", err)
	}

	nvmePaths := make([]nvmePath, 0)
	deviceSet := make(map[string]struct{})
	for _, device := range nv {
		for _, subsystem := range device.Subsystems {
			for _, p := range subsystem.Paths {
				if _, ok := deviceSet[p.Name]; !ok {
					if p.Transport != "pcie" {
						continue
					}
					nvmePaths = append(nvmePaths, nvmePath{
						Name:      p.Name,
						Transport: p.Transport,
						Address:   p.Address,
					})
					deviceSet[p.Name] = struct{}{}
				}
			}
		}
	}
	return nvmePaths, nil
}

func (npc *NvmePciCtl) lspcivvs(address string) (*PCIDevice, error) {
	args := append(lspciArgs, address)
	cmd := nvmeCommand(npc.LsPciPath, args...)
	out, err := CombinedOutputTimeout(cmd, npc.Timeout)
	if err != nil {
		return nil, fmt.Errorf("error running lscpi with %s: %w", args, err)
	}
	lspciParser := NewPCIParser()
	pciDevice, err := lspciParser.ParsePCIOutput(string(out))
	if err != nil {
		return nil, err
	}
	return pciDevice, nil
}

func (npc *NvmePciCtl) pciVersion(address string) (string, error) {
	pciDeivce, err := npc.lspcivvs(address)
	if err != nil {
		return "", err
	}
	lnkSpeed := pciDeivce.GetLnkStaSpeed()
	if version, ok := pciSpeedVersionMap[lnkSpeed]; ok {
		return version, nil
	}
	return "", fmt.Errorf("invalid lnk speed %s", lnkSpeed)
}
