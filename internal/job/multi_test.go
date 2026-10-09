package job

import (
	"reflect"
	"testing"

	"github.com/voska/bambu/internal/testutil"
)

// testdata/{single,two}_filament are trimmed recordings of real Bambu Studio 02.08 slices of the same 27-layer part:
// one filament, and two filaments with a layer-height change (filament 2 from layer 21, Z 4.2).
func inspectRecorded(t *testing.T, name string) *Job {
	t.Helper()
	j, err := Inspect(testutil.FromDir(t, "testdata/"+name, t.TempDir(), name), 1)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestInspectTwoFilaments(t *testing.T) {
	j := inspectRecorded(t, "two_filament")
	if len(j.Filaments) != 2 || j.Filaments[0].FilamentID != "GFL99" || j.Filaments[1].FilamentID != "GFA01" || j.Filaments[1].Index != 1 {
		t.Fatalf("filaments: %+v", j.Filaments)
	}
	if want := []FilamentChange{{Layer: 21, Z: 4.2, Filament: 2}}; !reflect.DeepEqual(j.FilamentChanges, want) {
		t.Fatalf("changes: %+v", j.FilamentChanges)
	}
	for _, f := range j.Filaments {
		if f.NozzleTemp != "220" || !reflect.DeepEqual(f.NozzleTempRange, []string{"190", "240"}) {
			t.Fatalf("per-filament temps: %+v", f)
		}
	}
}

func TestInspectSingleFilamentHasNoChanges(t *testing.T) {
	j := inspectRecorded(t, "single_filament")
	if len(j.Filaments) != 1 || j.FilamentChanges == nil || len(j.FilamentChanges) != 0 {
		t.Fatalf("%+v %+v", j.Filaments, j.FilamentChanges)
	}
	if j.NozzleTemp != "220" || j.NozzleTempRange != [2]string{"190", "240"} || j.BedTemp != "55" {
		t.Fatalf("temps: %+v", j)
	}
}

func TestFilamentSetting(t *testing.T) {
	std := map[string]any{"extruder_type": []any{"Direct Drive"}, "nozzle_volume_type": []any{"Standard"}}
	with := func(kv map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range std {
			m[k] = v
		}
		for k, v := range kv {
			m[k] = v
		}
		return m
	}
	arr := func(s ...string) []any {
		out := make([]any, len(s))
		for i, x := range s {
			out[i] = x
		}
		return out
	}
	cases := []struct {
		name string
		ps   map[string]any
		key  string
		want []string
	}{
		{"per variant, recorded layout", with(map[string]any{
			"nozzle_temperature": arr("220", "230", "240"), "filament_self_index": arr("1", "2", "2"),
			"filament_extruder_variant": arr("Direct Drive Standard", "Direct Drive Standard", "Direct Drive High Flow"),
		}), "nozzle_temperature", []string{"220", "230"}},
		{"first filament has two variants, high flow first", with(map[string]any{
			"nozzle_temperature": arr("250", "220", "230"), "filament_self_index": arr("1", "1", "2"),
			"filament_extruder_variant": arr("Direct Drive High Flow", "Direct Drive Standard", "Direct Drive Standard"),
		}), "nozzle_temperature", []string{"220", "230"}},
		{"high flow nozzle installed", map[string]any{
			"extruder_type": arr("Direct Drive"), "nozzle_volume_type": arr("High Flow"),
			"nozzle_temperature": arr("220", "230", "240"), "filament_self_index": arr("1", "2", "2"),
			"filament_extruder_variant": arr("Direct Drive Standard", "Direct Drive Standard", "Direct Drive High Flow"),
		}, "nozzle_temperature", []string{"220", "240"}},
		{"per filament, not per variant", with(map[string]any{
			"nozzle_temperature_range_low": arr("190", "200"), "filament_self_index": arr("1", "2", "2"),
			"filament_extruder_variant": arr("Direct Drive Standard", "Direct Drive Standard", "Direct Drive High Flow"),
		}), "nozzle_temperature_range_low", []string{"190", "200"}},
		{"no variant keys", map[string]any{"nozzle_temperature": arr("220")}, "nozzle_temperature", []string{"220", "220"}},
	}
	for _, c := range cases {
		got := []string{filamentSetting(c.ps, c.key, 0), filamentSetting(c.ps, c.key, 1)}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v want %v", c.name, got, c.want)
		}
	}
}

func TestFilamentChanges(t *testing.T) {
	g := []byte("M620 S0A\n    T0\nM621 S0A\nT1000\n; CHANGE_LAYER\n; Z_HEIGHT: 0.2\n; layer num/total_layer_count: 1/3\n" +
		"; CHANGE_LAYER\n; Z_HEIGHT: 0.4\n; layer num/total_layer_count: 2/3\nM620 S1A\nT1 ; change\nM621 S1A\n" +
		"; CHANGE_LAYER\n; Z_HEIGHT: 0.6\n; layer num/total_layer_count: 3/3\n    T2\nM620 S255\nT255\nM621 S255\n")
	want := []FilamentChange{{Layer: 2, Z: 0.4, Filament: 2}, {Layer: 3, Z: 0.6, Filament: 3}}
	if got := FilamentChanges(g); !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestAMSMappingPerFilament(t *testing.T) {
	j := inspectRecorded(t, "two_filament")
	if got := AMSMapping(j, []int{3, 0}); !reflect.DeepEqual(got, []int{3, 0}) {
		t.Fatal(got)
	}
	if got := ProjectFile(j, "p.gcode.3mf", []int{3, 0}, false)["ams_mapping"]; !reflect.DeepEqual(got, []int{3, 0}) {
		t.Fatal(got)
	}
}
