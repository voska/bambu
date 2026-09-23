package printer

import "fmt"

// HMS is one decoded Health Management System entry.
type HMS struct {
	Code          string `json:"code"`
	Module        string `json:"module"`
	Severity      string `json:"severity"`
	Informational bool   `json:"informational"`
	URL           string `json:"url"`
}

// InfoHMS are status-only codes that appear during normal prints and must not count as errors.
// Extend as more are observed. Codes with severity "info" are informational automatically.
var InfoHMS = map[string]string{
	"0C00_0300_0003_000B": "Inspecting first layer (lidar scan around layer 2)",
}

var (
	hmsModules  = map[int64]string{0x03: "motion_controller", 0x05: "mainboard", 0x07: "ams", 0x08: "toolhead", 0x0C: "xcam"}
	hmsSeverity = map[int64]string{1: "fatal", 2: "serious", 3: "common", 4: "info"}
)

// DecodeHMS decodes an {"attr":…, "code":…} pair (module = attr>>24, severity = code>>16, per ha-bambulab).
func DecodeHMS(attr, code int64) HMS {
	c := fmt.Sprintf("%04X_%04X_%04X_%04X", (attr>>16)&0xFFFF, attr&0xFFFF, (code>>16)&0xFFFF, code&0xFFFF)
	mod, ok := hmsModules[(attr>>24)&0xFF]
	if !ok {
		mod = "unknown"
	}
	sev, ok := hmsSeverity[(code>>16)&0xFFFF]
	if !ok {
		sev = "unknown"
	}
	_, info := InfoHMS[c]
	return HMS{
		Code: c, Module: mod, Severity: sev, Informational: info || sev == "info",
		URL: "https://wiki.bambulab.com/en/x1/troubleshooting/hmscode/" + c,
	}
}

// PrintErrorCode formats print.print_error as XXXX_XXXX.
func PrintErrorCode(e int64) string {
	return fmt.Sprintf("%04X_%04X", (e>>16)&0xFFFF, e&0xFFFF)
}
