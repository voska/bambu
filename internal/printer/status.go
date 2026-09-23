// Package printer talks to a Bambu Lab printer over LAN MQTT and turns its push_status reports into a stable summary.
package printer

import (
	"fmt"
	"strconv"
	"strings"
)

// MQTTSignatureRequired is the print.fun bit that is set while Developer Mode is OFF (ha-bambulab const.py).
const MQTTSignatureRequired = 0x20000000

// IdleStates are gcode_states in which a new job may be started.
var IdleStates = map[string]bool{"IDLE": true, "FINISH": true, "FAILED": true}

// ActiveStates are gcode_states of a job in progress.
var ActiveStates = map[string]bool{"PREPARE": true, "RUNNING": true, "PAUSE": true, "SLICING": true}

// Status is the stable, redacted summary of a printer report (the --json contract).
type Status struct {
	State       string      `json:"state"`
	Stage       string      `json:"stage"`
	Job         Job         `json:"job"`
	Errors      Errors      `json:"errors"`
	Temps       Temps       `json:"temps"`
	Nozzle      Nozzle      `json:"nozzle"`
	AMS         []Tray      `json:"ams"`
	ActiveTray  string      `json:"active_tray,omitempty"`
	External    string      `json:"external_spool,omitempty"` // filament type on the external spool holder, if any
	Protections Protections `json:"protections"`
	DevMode     *bool       `json:"dev_mode"`
	SDCard      bool        `json:"sdcard"`
	Liveview    bool        `json:"camera_lan_liveview"`
	WifiSignal  string      `json:"wifi_signal,omitempty"`
}

// Job describes the current or last job.
type Job struct {
	Name         string `json:"name,omitempty"`
	File         string `json:"file,omitempty"`
	Percent      int    `json:"percent"`
	Layer        int    `json:"layer"`
	TotalLayers  int    `json:"total_layers"`
	RemainingMin int    `json:"remaining_min"`
}

// Errors holds the error state. Blocking is true for a print_error or any non-informational HMS code.
type Errors struct {
	PrintError string `json:"print_error,omitempty"`
	HMS        []HMS  `json:"hms"`
	Blocking   bool   `json:"blocking"`
}

// Temps are current/target temperatures in °C.
type Temps struct {
	Nozzle       float64 `json:"nozzle"`
	NozzleTarget float64 `json:"nozzle_target"`
	Bed          float64 `json:"bed"`
	BedTarget    float64 `json:"bed_target"`
	Chamber      float64 `json:"chamber,omitempty"`
}

// Nozzle is the installed nozzle.
type Nozzle struct {
	Diameter string `json:"diameter"`
	TypeCode string `json:"type_code,omitempty"`
	Material string `json:"material,omitempty"`
}

// Tray is one AMS slot (or the external spool).
type Tray struct {
	Slot       string `json:"slot"`
	TrayID     int    `json:"tray_id"`
	Loaded     bool   `json:"loaded"`
	Type       string `json:"type,omitempty"`
	FilamentID string `json:"filament_id,omitempty"`
	Name       string `json:"name,omitempty"`
	Color      string `json:"color,omitempty"`
	RemainPct  *int   `json:"remain_pct"`
	RemainG    *int   `json:"remain_g"`
	TempMin    int    `json:"nozzle_temp_min,omitempty"`
	TempMax    int    `json:"nozzle_temp_max,omitempty"`
}

// Protections are the printer's AI / lidar safety features (read-only; bambu never changes them).
type Protections struct {
	FirstLayerInspector      *bool  `json:"first_layer_inspector"`
	SpaghettiDetector        *bool  `json:"spaghetti_detector"`
	PrintingMonitor          *bool  `json:"printing_monitor"`
	BuildplateMarkerDetector *bool  `json:"buildplate_marker_detector"`
	PrintHalt                *bool  `json:"print_halt"`
	HaltSensitivity          string `json:"halt_print_sensitivity,omitempty"`
}

// TrayLabel formats a global tray id (ams_index*4 + slot_index) as A1..D4.
func TrayLabel(id int) string {
	if id < 0 || id >= 16 {
		return fmt.Sprintf("tray%d", id)
	}
	return fmt.Sprintf("%c%d", "ABCD"[id/4], id%4+1)
}

// ParseSlot parses "1".."4" (first AMS) or "A1".."D4" into a global tray id.
// The external spool is not supported for sending in v0.1.
func ParseSlot(s string) (int, error) {
	u := strings.ToUpper(strings.TrimSpace(s))
	switch {
	case u == "EXT" || u == "EXTERNAL":
		return 0, fmt.Errorf("the external spool is not supported yet: load the filament into an AMS slot")
	case len(u) == 1 && u[0] >= '1' && u[0] <= '4':
		return int(u[0] - '1'), nil
	case len(u) == 2 && u[0] >= 'A' && u[0] <= 'D' && u[1] >= '1' && u[1] <= '4':
		return int(u[0]-'A')*4 + int(u[1]-'1'), nil
	}
	return 0, fmt.Errorf("invalid slot %q: use 1-4 (first AMS) or A1-D4", s)
}

var nozzleMaterial = map[string]string{"00": "stainless_steel", "01": "hardened_steel", "05": "tungsten_carbide"}

// NozzleMaterial decodes a nozzle_type code like "HX01" (older firmware reports the name directly).
func NozzleMaterial(code string) string {
	if code == "" {
		return ""
	}
	if strings.Contains(code, "_") || len(code) != 4 {
		return code
	}
	prefix := ""
	if code[1] == 'H' || code[1] == 'E' {
		prefix = "high_flow_"
	}
	if m, ok := nozzleMaterial[code[2:4]]; ok {
		return prefix + m
	}
	return prefix + "unknown"
}

// Summarize builds a Status from a merged print.push_status object.
func Summarize(p map[string]any) Status {
	s := Status{
		State: str(p["gcode_state"]),
		Job: Job{
			Name: str(p["subtask_name"]), File: str(p["gcode_file"]),
			Percent: num(p["mc_percent"]), Layer: num(p["layer_num"]), TotalLayers: num(p["total_layer_num"]),
			RemainingMin: num(p["mc_remaining_time"]),
		},
		Temps: Temps{
			Nozzle: flt(p["nozzle_temper"]), NozzleTarget: flt(p["nozzle_target_temper"]),
			Bed: flt(p["bed_temper"]), BedTarget: flt(p["bed_target_temper"]), Chamber: flt(p["chamber_temper"]),
		},
		Nozzle:     Nozzle{Diameter: str(p["nozzle_diameter"]), TypeCode: str(p["nozzle_type"])},
		SDCard:     p["sdcard"] == true,
		WifiSignal: str(p["wifi_signal"]),
	}
	s.Nozzle.Material = NozzleMaterial(s.Nozzle.TypeCode)
	if v, ok := p["stg_cur"]; ok {
		s.Stage = StageName(num(v))
	}
	if fun := str(p["fun"]); fun != "" {
		if n, err := strconv.ParseUint(fun, 16, 64); err == nil {
			dm := n&MQTTSignatureRequired == 0
			s.DevMode = &dm
		}
	}
	if pe := int64(num(p["print_error"])); pe != 0 {
		s.Errors.PrintError = PrintErrorCode(pe)
		s.Errors.Blocking = true
	}
	s.Errors.HMS = []HMS{}
	for _, h := range list(p["hms"]) {
		m := obj(h)
		d := DecodeHMS(int64(num(m["attr"])), int64(num(m["code"])))
		s.Errors.HMS = append(s.Errors.HMS, d)
		if !d.Informational {
			s.Errors.Blocking = true
		}
	}
	ams := obj(p["ams"])
	s.ActiveTray = str(ams["tray_now"])
	s.AMS = []Tray{}
	for _, a := range list(ams["ams"]) {
		am := obj(a)
		unit := num(am["id"])
		for _, t := range list(am["tray"]) {
			tm := obj(t)
			s.AMS = append(s.AMS, tray(unit*4+num(tm["id"]), tm))
		}
	}
	s.External = str(obj(p["vt_tray"])["tray_type"])
	x := obj(p["xcam"])
	s.Protections = Protections{
		FirstLayerInspector: boolp(x["first_layer_inspector"]), SpaghettiDetector: boolp(x["spaghetti_detector"]),
		PrintingMonitor: boolp(x["printing_monitor"]), BuildplateMarkerDetector: boolp(x["buildplate_marker_detector"]),
		PrintHalt: boolp(x["print_halt"]), HaltSensitivity: str(x["halt_print_sensitivity"]),
	}
	rtsp := str(obj(p["ipcam"])["rtsp_url"])
	s.Liveview = rtsp != "" && rtsp != "disable"
	return s
}

func tray(id int, t map[string]any) Tray {
	tr := Tray{Slot: TrayLabel(id), TrayID: id, Type: str(t["tray_type"])}
	tr.Loaded = tr.Type != ""
	if !tr.Loaded {
		tr.Type = ""
		return tr
	}
	tr.FilamentID = str(t["tray_info_idx"])
	tr.Name = str(t["tray_sub_brands"])
	if c := str(t["tray_color"]); len(c) >= 6 {
		tr.Color = "#" + c[:6]
	}
	tr.TempMin, tr.TempMax = num(t["nozzle_temp_min"]), num(t["nozzle_temp_max"])
	if r, ok := t["remain"]; ok && num(r) >= 0 {
		pct := num(r)
		tr.RemainPct = &pct
		if w := num(t["tray_weight"]); w > 0 {
			g := (pct*w + 50) / 100
			tr.RemainG = &g
		}
	}
	return tr
}

// --- loose JSON helpers (printer fields arrive as strings or numbers depending on firmware) ---

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

func num(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err == nil {
			return int(n)
		}
	}
	return 0
}

func flt(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	}
	return 0
}

func boolp(v any) *bool {
	if b, ok := v.(bool); ok {
		return &b
	}
	return nil
}

func obj(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func list(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}
