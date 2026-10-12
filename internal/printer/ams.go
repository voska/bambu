package printer

import (
	"strconv"
	"strings"
)

// RawAMSTray returns a tray's telemetry by global tray id, or nil if absent.
func RawAMSTray(raw map[string]any, id int) map[string]any {
	for _, a := range list(obj(raw["ams"])["ams"]) {
		am := obj(a)
		if num(am["id"]) != id/4 {
			continue
		}
		for _, t := range list(am["tray"]) {
			tm := obj(t)
			if num(tm["id"]) == id%4 {
				return tm
			}
		}
	}
	return nil
}

// TrayPresent reads the AMS's physical filament detection; missing or malformed telemetry is unknown.
func TrayPresent(raw map[string]any, id int) *bool {
	bits, err := strconv.ParseUint(str(obj(raw["ams"])["tray_exist_bits"]), 16, 64)
	if err != nil || id < 0 || id >= 16 {
		return nil
	}
	present := bits&(1<<uint(id)) != 0
	return &present
}

// AMSFilamentSetting builds Studio's ams_filament_setting payload. Color is opaque RRGGBB.
func AMSFilamentSetting(id int, material, filamentID, color string, tempMin, tempMax int) map[string]any {
	return map[string]any{
		"command": "ams_filament_setting", "ams_id": id / 4, "slot_id": id % 4, "tray_id": id % 4,
		"tray_info_idx": filamentID, "setting_id": "", "tray_type": material, "tray_color": color + "FF",
		"nozzle_temp_min": tempMin, "nozzle_temp_max": tempMax,
	}
}

// AMSSettingsMatch verifies each writable field, including the color's alpha channel, against tray telemetry.
func AMSSettingsMatch(raw map[string]any, id int, body map[string]any) bool {
	t := RawAMSTray(raw, id)
	return t != nil && str(t["tray_type"]) == str(body["tray_type"]) &&
		str(t["tray_info_idx"]) == str(body["tray_info_idx"]) &&
		strings.EqualFold(str(t["tray_color"]), str(body["tray_color"])) &&
		num(t["nozzle_temp_min"]) == num(body["nozzle_temp_min"]) &&
		num(t["nozzle_temp_max"]) == num(body["nozzle_temp_max"])
}
