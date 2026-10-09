package printer

import (
	"fmt"
	"strconv"
)

// amsStatus names print.ams_status >> 8 (Bambu Studio DevDefs.h AmsStatusMain). 0x03 is the resting value with filament loaded.
var amsStatus = map[int]string{
	0x00: "idle", 0x01: "filament_change", 0x02: "rfid_identifying", 0x03: "assist", 0x04: "calibration",
	0x07: "cold_pull", 0x10: "self_check", 0x20: "debug",
}

// AMSStatusName decodes print.ams_status.
func AMSStatusName(v int) string {
	main := (v & 0xFF00) >> 8
	if n, ok := amsStatus[main]; ok {
		return n
	}
	return fmt.Sprintf("ams_status_0x%02x", main)
}

// EmptyToolhead is ams.tray_now when no filament is in the toolhead; ExternalTray is the external spool.
const (
	EmptyToolhead = "255"
	ExternalTray  = "254"
)

// EmptyToolheadKnown reports whether tray_now 255 is known to mean "no filament in the toolhead" on model m: verified
// on the X1 series (X1 Carbon, firmware 01.12). H2-series reports differ, and other models are unverified.
func EmptyToolheadKnown(m Model) bool {
	return m.Alias == "X1C" || m.Alias == "X1" || m.Alias == "X1E"
}

// ToolheadSlot formats ams.tray_now: A1..D4, "external", or "" when nothing is loaded (or nothing was reported).
func ToolheadSlot(trayNow string) string {
	n, err := strconv.Atoi(trayNow)
	switch {
	case err != nil || trayNow == EmptyToolhead:
		return ""
	case trayNow == ExternalTray:
		return "external"
	}
	return TrayLabel(n)
}

var filamentChangeStages = map[string]bool{"changing_filament": true, "filament_unloading": true, "filament_loading": true}

// FilamentChanging reports an AMS filament change in progress.
func FilamentChanging(s Status) bool {
	return s.AMSStatus == "filament_change" || filamentChangeStages[s.Stage]
}

// defaultLoadTemp is Bambu Studio's command_ams_change_filament default when a tray reports no temperature range.
const defaultLoadTemp = 210

// LoadTemp is the temperature Bambu Studio sends for a tray in ams_change_filament: the middle of its nozzle range
// (StatusPanel::on_ams_load_curr).
func LoadTemp(t *Tray) int {
	if t == nil || t.TempMin == 0 || t.TempMax == 0 {
		return defaultLoadTemp
	}
	return (t.TempMin + t.TempMax) / 2
}

// AMSChangeFilament builds the print.ams_change_filament command for an AMS tray, as Bambu Studio sends it
// (DeviceManager.cpp command_ams_change_filament): the printer unloads the toolhead, loads trayID and purges.
// Verified on an X1 Carbon (firmware 01.12, Developer Mode) on 2026-10-08, during a pause: A4 -> A1.
func AMSChangeFilament(trayID, currTemp, tarTemp int) map[string]any {
	return map[string]any{
		"command": "ams_change_filament", "ams_id": trayID / 4, "slot_id": trayID % 4, "target": trayID,
		"curr_temp": currTemp, "tar_temp": tarTemp,
	}
}
