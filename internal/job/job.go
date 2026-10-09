// Package job inspects sliced .gcode.3mf files and builds the LAN print command.
package job

import (
	"archive/zip"
	"crypto/md5" //nolint:gosec // MD5 is the printer's gcode checksum
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/voska/bambu/internal/errfmt"
)

// BedTypes maps Bambu Studio's curr_bed_type to the plate id used in plate_N.json and printer config.
var BedTypes = map[string]string{
	"Textured PEI Plate": "textured_plate", "Cool Plate": "cool_plate", "Engineering Plate": "eng_plate",
	"High Temp Plate": "hot_plate", "Supertack Plate": "supertack_plate",
}

// PlateTempKey is the filament setting holding the bed temperature for a plate.
var PlateTempKey = map[string]string{
	"textured_plate": "textured_plate_temp", "cool_plate": "cool_plate_temp", "eng_plate": "eng_plate_temp",
	"hot_plate": "hot_plate_temp", "supertack_plate": "supertack_plate_temp",
}

// KeySettings are surfaced in slice summaries and preflight output.
var KeySettings = []string{
	"layer_height", "initial_layer_print_height", "wall_loops", "top_shell_layers", "bottom_shell_layers",
	"sparse_infill_density", "sparse_infill_pattern", "line_width", "outer_wall_line_width", "inner_wall_line_width",
	"sparse_infill_line_width", "brim_type", "brim_width", "seam_position", "elefant_foot_compensation",
	"enable_support", "support_type", "curr_bed_type", "nozzle_temperature", "nozzle_temperature_initial_layer",
	"fan_min_speed", "fan_max_speed", "overhang_fan_speed", "filament_max_volumetric_speed",
}

// Filament is one filament used by the plate.
type Filament struct {
	Index           int      `json:"index"` // 0-based project filament index
	Type            string   `json:"type"`
	FilamentID      string   `json:"filament_id"`
	Color           string   `json:"color"`
	UsedG           float64  `json:"used_g"` // includes its share of purge and prime tower
	UsedM           float64  `json:"used_m"`
	ModelG          *float64 `json:"model_g,omitempty"` // set by slice: model + support only
	WasteG          *float64 `json:"waste_g,omitempty"` // set by slice: purge + prime tower
	NozzleTemp      string   `json:"nozzle_temp,omitempty"`
	NozzleTempRange []string `json:"nozzle_temp_range,omitempty"`
}

// FilamentChange is an AMS filament change inside the printed layers: Filament (1-based) starts on Layer, at Z.
type FilamentChange struct {
	Layer    int     `json:"layer"`
	Z        float64 `json:"z"`
	Filament int     `json:"filament"`
}

// Job is what bambu needs to know about a sliced plate.
type Job struct {
	File            string           `json:"file"`
	Plate           int              `json:"plate"`
	GcodeParam      string           `json:"gcode_param"`
	GcodeMD5        string           `json:"gcode_md5"`
	GcodeMD5OK      *bool            `json:"gcode_md5_ok"`
	PrinterModel    string           `json:"printer_model"`
	PrinterPreset   string           `json:"printer_preset"`
	ProcessPreset   string           `json:"process_preset"`
	FilamentPresets []string         `json:"filament_presets"`
	NozzleDiameter  string           `json:"nozzle_diameter"`
	NozzleType      string           `json:"nozzle_type"`
	BedType         string           `json:"bed_type"`
	BedTemp         string           `json:"bed_temp"`
	NozzleTemp      string           `json:"nozzle_temp"`
	NozzleTempFirst string           `json:"nozzle_temp_initial"`
	NozzleTempRange [2]string        `json:"nozzle_temp_range"`
	Filaments       []Filament       `json:"filaments"`
	FilamentChanges []FilamentChange `json:"filament_changes"`
	PredictionS     int              `json:"time_s"`
	TotalTime       string           `json:"time"`
	WeightG         float64          `json:"weight_g"`
	Layers          int              `json:"layers"`
	Settings        map[string]any   `json:"settings"`
	Objects         []string         `json:"objects"`
}

type sliceInfo struct {
	Plates []struct {
		Metadata []struct {
			Key   string `xml:"key,attr"`
			Value string `xml:"value,attr"`
		} `xml:"metadata"`
		Objects []struct {
			Name string `xml:"name,attr"`
		} `xml:"object"`
		Filaments []struct {
			ID     string `xml:"id,attr"`
			Type   string `xml:"type,attr"`
			TrayID string `xml:"tray_info_idx,attr"`
			Color  string `xml:"color,attr"`
			UsedG  string `xml:"used_g,attr"`
			UsedM  string `xml:"used_m,attr"`
		} `xml:"filament"`
	} `xml:"plate"`
}

var (
	reLayers = regexp.MustCompile(`total layer number: (\d+)`)
	reTime   = regexp.MustCompile(`total estimated time: ([^\n;]+)`)
	reTool   = regexp.MustCompile(`^T(\d+)$`)
)

// Inspect reads a sliced .gcode.3mf.
func Inspect(path string, plate int) (*Job, error) {
	abs, _ := filepath.Abs(path)
	zr, err := zip.OpenReader(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errfmt.New(errfmt.ExitNotFound, "file not found: %s", path).WithHint("slice first: bambu slice <model> --recipe <name>")
		}
		return nil, errfmt.Wrap(errfmt.ExitUsage, err, "%s is not a 3MF", path).WithHint("pass the .gcode.3mf produced by `bambu slice`")
	}
	defer func() { _ = zr.Close() }()
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	read := func(name string) ([]byte, bool) {
		f, ok := files[name]
		if !ok {
			return nil, false
		}
		rc, err := f.Open()
		if err != nil {
			return nil, false
		}
		defer func() { _ = rc.Close() }()
		b, err := io.ReadAll(io.LimitReader(rc, 1<<30))
		return b, err == nil
	}
	j := &Job{File: abs, Plate: plate, GcodeParam: fmt.Sprintf("Metadata/plate_%d.gcode", plate), Settings: map[string]any{}}
	gcode, ok := read(j.GcodeParam)
	if !ok {
		return nil, errfmt.New(errfmt.ExitUsage, "%s has no %s (not sliced?)", filepath.Base(path), j.GcodeParam).
			WithHint("slice it: bambu slice <model> --recipe <name>")
	}
	sum := md5.Sum(gcode) //nolint:gosec // printer checksum
	j.GcodeMD5 = hex.EncodeToString(sum[:])
	if b, ok := read(j.GcodeParam + ".md5"); ok {
		v := strings.EqualFold(strings.TrimSpace(string(b)), j.GcodeMD5)
		j.GcodeMD5OK = &v
	}
	head := string(gcode[:min(len(gcode), 4000)])
	if m := reLayers.FindStringSubmatch(head); m != nil {
		j.Layers, _ = strconv.Atoi(m[1])
	}
	if m := reTime.FindStringSubmatch(head); m != nil {
		j.TotalTime = strings.TrimSpace(m[1])
	}
	j.FilamentChanges = FilamentChanges(gcode)

	ps := map[string]any{}
	if b, ok := read("Metadata/project_settings.config"); ok {
		_ = json.Unmarshal(b, &ps)
	}
	var si sliceInfo
	if b, ok := read("Metadata/slice_info.config"); ok {
		_ = xml.Unmarshal(b, &si)
	}
	for _, p := range si.Plates {
		meta := map[string]string{}
		for _, m := range p.Metadata {
			meta[m.Key] = m.Value
		}
		if meta["index"] != strconv.Itoa(plate) {
			continue
		}
		f, _ := strconv.ParseFloat(meta["prediction"], 64)
		j.PredictionS = int(f)
		j.WeightG, _ = strconv.ParseFloat(meta["weight"], 64)
		for _, o := range p.Objects {
			j.Objects = append(j.Objects, o.Name)
		}
		for _, fl := range p.Filaments {
			id, _ := strconv.Atoi(fl.ID)
			g, _ := strconv.ParseFloat(fl.UsedG, 64)
			m, _ := strconv.ParseFloat(fl.UsedM, 64)
			i := id - 1
			j.Filaments = append(j.Filaments, Filament{
				Index: i, Type: fl.Type, FilamentID: fl.TrayID, Color: fl.Color, UsedG: g, UsedM: m,
				NozzleTemp: filamentSetting(ps, "nozzle_temperature", i),
				NozzleTempRange: []string{
					filamentSetting(ps, "nozzle_temperature_range_low", i), filamentSetting(ps, "nozzle_temperature_range_high", i),
				},
			})
		}
	}
	idx := 0
	if len(j.Filaments) > 0 {
		idx = j.Filaments[0].Index
	}
	j.PrinterModel = pick(ps, "printer_model", 0)
	j.PrinterPreset = pick(ps, "printer_settings_id", 0)
	j.ProcessPreset = pick(ps, "print_settings_id", 0)
	j.FilamentPresets = strs(ps["filament_settings_id"])
	j.NozzleDiameter = pick(ps, "nozzle_diameter", 0)
	j.NozzleType = pick(ps, "nozzle_type", 0)
	if b, ok := read(fmt.Sprintf("Metadata/plate_%d.json", plate)); ok {
		var pj struct {
			BedType string `json:"bed_type"`
		}
		_ = json.Unmarshal(b, &pj)
		j.BedType = pj.BedType
	}
	if j.BedType == "" {
		j.BedType = BedTypes[pick(ps, "curr_bed_type", 0)]
	}
	j.BedTemp = filamentSetting(ps, PlateTempKey[j.BedType], idx)
	j.NozzleTemp = filamentSetting(ps, "nozzle_temperature", idx)
	j.NozzleTempFirst = filamentSetting(ps, "nozzle_temperature_initial_layer", idx)
	j.NozzleTempRange = [2]string{filamentSetting(ps, "nozzle_temperature_range_low", idx), filamentSetting(ps, "nozzle_temperature_range_high", idx)}
	for _, k := range KeySettings {
		if v, ok := ps[k]; ok {
			j.Settings[k] = v
		}
	}
	return j, nil
}

func pick(ps map[string]any, key string, i int) string {
	switch v := ps[key].(type) {
	case string:
		return v
	case []any:
		if i < len(v) {
			if s, ok := v[i].(string); ok {
				return s
			}
		}
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				return s
			}
		}
	}
	return ""
}

// filamentSetting is filament i's (0-based) value of a project setting. Settings that vary by extruder variant hold one
// entry per (filament, variant) pair, found through filament_self_index and filament_extruder_variant; the installed
// nozzle's variant is "<extruder_type> <nozzle_volume_type>" (PrintConfig get_index_for_extruder). Plain indexing reads
// the wrong row whenever an earlier filament has more than one variant (rows: f1 Standard, f1 High Flow, f2 Standard).
func filamentSetting(ps map[string]any, key string, i int) string {
	v, ok := ps[key].([]any)
	self, variants := strs(ps["filament_self_index"]), strs(ps["filament_extruder_variant"])
	if !ok || len(v) != len(self) || len(v) != len(variants) {
		return pick(ps, key, i)
	}
	want := pick(ps, "extruder_type", 0) + " " + pick(ps, "nozzle_volume_type", 0)
	row := -1
	for k, f := range self {
		if f != strconv.Itoa(i+1) {
			continue
		}
		if row < 0 || variants[k] == want {
			row = k
		}
		if variants[k] == want {
			break
		}
	}
	if row < 0 {
		return ""
	}
	s, _ := v[row].(string)
	return s
}

// FilamentChanges lists the AMS filament changes inside the printed layers. Before layer 1 the start G-code loads the
// first filament (T0) and runs T1000/T1100 macros; T255 at the end unloads. Only T0..T254 after a layer marker count.
func FilamentChanges(gcode []byte) []FilamentChange {
	out := []FilamentChange{}
	layer, z := 0, 0.0
	for _, line := range strings.Split(string(gcode), "\n") {
		switch {
		case strings.HasPrefix(line, "; Z_HEIGHT:"):
			z, _ = strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "; Z_HEIGHT:")), 64)
		case strings.HasPrefix(line, "; layer num/total_layer_count:"):
			n, _, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(line, "; layer num/total_layer_count:")), "/")
			layer, _ = strconv.Atoi(n)
		case layer > 0:
			code, _, _ := strings.Cut(line, ";")
			if m := reTool.FindStringSubmatch(strings.TrimSpace(code)); m != nil {
				if t, _ := strconv.Atoi(m[1]); t < 255 {
					out = append(out, FilamentChange{Layer: layer, Z: z, Filament: t + 1})
				}
			}
		}
	}
	return out
}

func strs(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			out = append(out, fmt.Sprint(x))
		}
		return out
	}
	return nil
}
