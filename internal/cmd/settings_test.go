package cmd

import (
	"io"
	"reflect"
	"testing"

	"github.com/alecthomas/kong"

	"github.com/voska/bambu/internal/recipe"
	"github.com/voska/bambu/internal/slicer"
)

func settingConfigs(t *testing.T, sets []string) *slicer.Configs {
	t.Helper()
	var cli CLI
	parser, err := kong.New(&cli, kong.Writers(io.Discard, io.Discard), kong.Vars{"models": "X1C"})
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"slice", "part.stl", "--recipe", "prototype-pla"}
	for _, value := range sets {
		args = append(args, "--set", value)
	}
	if _, err := parser.Parse(args); err != nil {
		t.Fatal(err)
	}
	ix, err := slicer.LoadIndex("../slicer/testdata/resources")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := slicer.Build(ix, slicer.Request{Recipe: recipe.Recipe{Process: "0.20mm Standard", Filament: "Bambu PLA Basic"}, MachinePreset: "Test Printer 0.4 nozzle", Plate: "textured_plate", Sets: cli.Slice.Set})
	if err != nil {
		t.Fatalf("settings %v: %v", sets, err)
	}
	return cfg
}

func TestSliceSettingLegacyEscapesAndTrailingSeparator(t *testing.T) {
	for _, sets := range [][]string{{`machine_start_gcode=G28 ; first\,second,wall_loops=4`}, {"wall_loops=4,"}} {
		cfg := settingConfigs(t, sets)
		if cfg.Process["wall_loops"] != "4" {
			t.Fatalf("lost wall override: %v", cfg.Applied)
		}
		if len(cfg.Applied) == 2 && cfg.Machine["machine_start_gcode"] != "G28 ; first,second" {
			t.Fatalf("escaped comma changed: %v", cfg.Applied)
		}
	}
}

func TestSliceSettingJSONEscapes(t *testing.T) {
	cfg := settingConfigs(t, []string{`nozzle_temperature=["250,260","quoted \"text\"","back\\slash"]`})
	if !reflect.DeepEqual(cfg.Filaments[0]["nozzle_temperature"], []any{"250,260", `quoted "text"`, `back\slash`}) {
		t.Fatalf("JSON escapes changed: %v", cfg.Applied)
	}
}

func TestSliceSettingScalarBrackets(t *testing.T) {
	for _, value := range []string{`; [banner`, `; "banner`, `; banner=[text`} {
		cfg := settingConfigs(t, []string{"machine_start_gcode=" + value + ",wall_loops=4"})
		if cfg.Machine["machine_start_gcode"] != value || cfg.Process["wall_loops"] != "4" {
			t.Fatalf("scalar mistaken for JSON: %v", cfg.Applied)
		}
	}
}

func TestSliceSettingListsAndBatches(t *testing.T) {
	for _, sets := range [][]string{
		{`nozzle_temperature=["250","250"]`, "wall_loops=4", "sparse_infill_density=30%"},
		{`nozzle_temperature=["250","250"],wall_loops=4,sparse_infill_density=30%`},
		{`wall_loops=4,sparse_infill_density=30%,nozzle_temperature=["250","250"]`},
		{`nozzle_temperature=["250"\,"250"],wall_loops=4,sparse_infill_density=30%`},
	} {
		cfg := settingConfigs(t, sets)
		if !reflect.DeepEqual(cfg.Filaments[0]["nozzle_temperature"], []any{"250", "250"}) || cfg.Process["wall_loops"] != "4" || cfg.Process["sparse_infill_density"] != "30%" {
			t.Fatalf("overrides lost: %v", cfg.Applied)
		}
	}
}
