// Package testutil builds synthetic sliced 3MF files for tests.
package testutil

import (
	"archive/zip"
	"crypto/md5" //nolint:gosec // test fixture checksum
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Opts describes a synthetic sliced plate.
type Opts struct {
	PrinterModel string
	Nozzle       string
	BedType      string
	Filaments    []string // "PLA:GFA00:8.1"
	Presets      int      // number of filament presets in the project (default len(Filaments))
	CorruptMD5   bool
	NoGcode      bool
	NozzleTemp   string
}

// Write creates <dir>/<name>.gcode.3mf and returns its path.
func Write(t *testing.T, dir, name string, o Opts) string {
	t.Helper()
	if o.PrinterModel == "" {
		o.PrinterModel = "Bambu Lab X1 Carbon"
	}
	if o.Nozzle == "" {
		o.Nozzle = "0.4"
	}
	if o.BedType == "" {
		o.BedType = "textured_plate"
	}
	if len(o.Filaments) == 0 {
		o.Filaments = []string{"PLA:GFA00:8.1"}
	}
	if o.NozzleTemp == "" {
		o.NozzleTemp = "220"
	}
	path := filepath.Join(dir, name+".gcode.3mf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	add := func(n string, b []byte) {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(b)
	}
	gcode := []byte("; HEADER_BLOCK_START\n; model printing time: 20m 1s; total estimated time: 27m 14s\n; total layer number: 113\n; HEADER_BLOCK_END\nG28\n")
	if !o.NoGcode {
		add("Metadata/plate_1.gcode", gcode)
		sum := md5.Sum(gcode) //nolint:gosec // fixture
		md := hex.EncodeToString(sum[:])
		if o.CorruptMD5 {
			md = strings.Repeat("0", 32)
		}
		add("Metadata/plate_1.gcode.md5", []byte(md))
	}
	presets := o.Presets
	if presets == 0 {
		presets = len(o.Filaments)
	}
	fp := make([]string, presets)
	for i := range fp {
		fp[i] = "Bambu PLA Basic @BBL X1C"
	}
	ps := map[string]any{
		"printer_model": o.PrinterModel, "printer_settings_id": o.PrinterModel + " " + o.Nozzle + " nozzle",
		"print_settings_id": "0.20mm Standard @BBL X1C", "filament_settings_id": fp,
		"nozzle_diameter": []string{o.Nozzle}, "nozzle_type": []string{"hardened_steel"},
		"curr_bed_type": "Textured PEI Plate", "textured_plate_temp": []string{"55"},
		"nozzle_temperature": []string{o.NozzleTemp, o.NozzleTemp}, "nozzle_temperature_initial_layer": []string{o.NozzleTemp},
		"nozzle_temperature_range_low": []string{"190"}, "nozzle_temperature_range_high": []string{"240"},
		"wall_loops": "2", "brim_type": "no_brim",
	}
	b, _ := json.Marshal(ps)
	add("Metadata/project_settings.config", b)
	pj, _ := json.Marshal(map[string]any{"bed_type": o.BedType})
	add("Metadata/plate_1.json", pj)
	var fx strings.Builder
	total := 0.0
	for i, fl := range o.Filaments {
		var typ, id string
		var g float64
		_, _ = fmt.Sscanf(strings.ReplaceAll(fl, ":", " "), "%s %s %f", &typ, &id, &g)
		total += g
		fmt.Fprintf(&fx, `<filament id="%d" tray_info_idx="%s" type="%s" color="#00AE42" used_m="3.4" used_g="%.2f"/>`, i+1, id, typ, g)
	}
	si := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><config><plate><metadata key="index" value="1"/><metadata key="prediction" value="1634"/><metadata key="weight" value="%.2f"/><object identify_id="1" name="part.stl"/>%s</plate></config>`, total, fx.String())
	add("Metadata/slice_info.config", []byte(si))
	add("Metadata/plate_1.png", []byte("\x89PNG\r\n\x1a\n"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
