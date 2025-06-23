package collector

import (
	"bufio"
	"regexp"
	"strings"
)

type PCIDevice struct {
	Address     string            `json:"address"`
	Description string            `json:"description"`
	Subsystem   string            `json:"subsystem,omitempty"`
	LnkSta      *LinkStatus       `json:"lnkSta,omitempty"`
	LnkCap      *LinkCapability   `json:"lnkCap,omitempty"`
	Properties  map[string]string `json:"properties"`
}

type LinkStatus struct {
	Speed string `json:"speed"`
	Width string `json:"width"`
	Raw   string `json:"raw"`
}

type LinkCapability struct {
	Speed string `json:"speed"`
	Width string `json:"width"`
	Raw   string `json:"raw"`
}

type PCIParser struct {
	speedRegex  *regexp.Regexp
	widthRegex  *regexp.Regexp
	lnkStaRegex *regexp.Regexp
	lnkCapRegex *regexp.Regexp
}

func NewPCIParser() *PCIParser {
	return &PCIParser{
		speedRegex:  regexp.MustCompile(`Speed\s+([0-9.]+GT/s)`),
		widthRegex:  regexp.MustCompile(`Width\s+(x\d+)`),
		lnkStaRegex: regexp.MustCompile(`LnkSta:\s+(.+)`),
		lnkCapRegex: regexp.MustCompile(`LnkCap:\s+(.+)`),
	}
}

func (p *PCIParser) ParsePCIOutput(text string) (*PCIDevice, error) {
	scanner := bufio.NewScanner(strings.NewReader(text))
	device := &PCIDevice{
		Properties: make(map[string]string),
	}

	var currentLine string
	for scanner.Scan() {
		line := scanner.Text()

		if strings.Contains(line, ":") && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, " ") {
			parts := strings.SplitN(line, " ", 2)
			if len(parts) >= 2 {
				device.Address = parts[0]
				device.Description = parts[1]
			}
			continue
		}

		if strings.Contains(line, "Subsystem:") {
			device.Subsystem = strings.TrimSpace(strings.TrimPrefix(line, "Subsystem:"))
			continue
		}

		if strings.HasPrefix(line, "\t\t") {
			currentLine += " " + strings.TrimSpace(line)
		} else {
			if currentLine != "" {
				p.parseLine(currentLine, device)
			}
			currentLine = strings.TrimSpace(line)
		}
	}

	if currentLine != "" {
		p.parseLine(currentLine, device)
	}

	return device, nil
}

func (p *PCIParser) parseLine(line string, device *PCIDevice) {
	if matches := p.lnkStaRegex.FindStringSubmatch(line); len(matches) > 1 {
		lnkSta := &LinkStatus{Raw: matches[1]}

		if speedMatches := p.speedRegex.FindStringSubmatch(matches[1]); len(speedMatches) > 1 {
			lnkSta.Speed = speedMatches[1]
		}

		if widthMatches := p.widthRegex.FindStringSubmatch(matches[1]); len(widthMatches) > 1 {
			lnkSta.Width = widthMatches[1]
		}

		device.LnkSta = lnkSta
		return
	}

	if matches := p.lnkCapRegex.FindStringSubmatch(line); len(matches) > 1 {
		lnkCap := &LinkCapability{Raw: matches[1]}

		if speedMatches := p.speedRegex.FindStringSubmatch(matches[1]); len(speedMatches) > 1 {
			lnkCap.Speed = speedMatches[1]
		}

		if widthMatches := p.widthRegex.FindStringSubmatch(matches[1]); len(widthMatches) > 1 {
			lnkCap.Width = widthMatches[1]
		}

		device.LnkCap = lnkCap
		return
	}

	if strings.Contains(line, ":") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			device.Properties[key] = value
		}
	}
}

func (device *PCIDevice) GetLnkStaSpeed() string {
	if device.LnkSta != nil {
		return device.LnkSta.Speed
	}
	return ""
}

func (device *PCIDevice) GetLnkCapSpeed() string {
	if device.LnkCap != nil {
		return device.LnkCap.Speed
	}
	return ""
}
