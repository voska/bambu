package job

import (
	"path/filepath"
	"regexp"
	"strings"
)

// RemoteName is the SD-card file name for a local .gcode.3mf (safe characters only).
func RemoteName(local string) string {
	base := regexp.MustCompile(`[^A-Za-z0-9._-]`).ReplaceAllString(filepath.Base(local), "-")
	if !strings.HasSuffix(base, ".gcode.3mf") {
		base = strings.TrimSuffix(base, ".3mf") + ".gcode.3mf"
	}
	return base
}

// AMSMapping returns Bambu Studio's "v0" ams_mapping: one entry per project filament, value = global tray id
// (ams_index*4 + slot_index), -1 for unused (SelectMachine.cpp get_ams_mapping_result).
func AMSMapping(j *Job, trayID int) []int {
	n := len(j.FilamentPresets)
	if n < 1 {
		n = 1
	}
	idx := 0
	if len(j.Filaments) > 0 {
		idx = j.Filaments[0].Index
	}
	if idx >= n {
		n = idx + 1
	}
	m := make([]int, n)
	for i := range m {
		m[i] = -1
	}
	m[idx] = trayID
	return m
}

// ProjectFile builds the MQTT print.project_file command.
//
// Verified on an X1 Carbon (firmware 01.12, Developer Mode) on 2026-09-23: this field set returns
// result "SUCCESS" and the printer goes RUNNING within seconds. sequence_id is filled in by the sender.
func ProjectFile(j *Job, remote string, trayID int, timelapse bool) map[string]any {
	return map[string]any{
		"command":        "project_file",
		"param":          j.GcodeParam,
		"url":            "file:///sdcard/" + remote,
		"subtask_name":   strings.TrimSuffix(remote, ".gcode.3mf"),
		"md5":            j.GcodeMD5,
		"project_id":     "0",
		"profile_id":     "0",
		"task_id":        "0",
		"subtask_id":     "0",
		"bed_type":       "auto",
		"timelapse":      timelapse,
		"bed_leveling":   true,
		"flow_cali":      true,
		"vibration_cali": true,
		"layer_inspect":  true,
		"use_ams":        true,
		"ams_mapping":    AMSMapping(j, trayID),
	}
}
