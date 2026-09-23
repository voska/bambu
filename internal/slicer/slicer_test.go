package slicer

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/recipe"
	"github.com/voska/bambu/internal/testutil"
)

const machine = "Test Printer 0.4 nozzle"

func index(t *testing.T) *Index {
	t.Helper()
	ix, err := LoadIndex("testdata/resources")
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func TestFlattenMachine(t *testing.T) {
	m, err := index(t).Flatten(KindMachine, machine)
	if err != nil {
		t.Fatal(err)
	}
	if m["printable_height"] != "250" {
		t.Errorf("parent chain not applied: %v", m["printable_height"])
	}
	if m["machine_start_gcode"] != ";===== test printer start =====\nG28\nG29" {
		t.Errorf("nameless include template not applied: %q", m["machine_start_gcode"])
	}
	if m["name"] != machine || m["setting_id"] != "TM001" || m["from"] != "system" {
		t.Errorf("identity keys: %v %v %v", m["name"], m["setting_id"], m["from"])
	}
	for _, k := range []string{"inherits", "include", "instantiation"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s must be dropped", k)
		}
	}
}

func TestFlattenFilamentIncludeOrder(t *testing.T) {
	f, err := index(t).Flatten(KindFilament, "Bambu PLA Basic @BBL TP")
	if err != nil {
		t.Fatal(err)
	}
	// own keys beat includes, includes beat parents; include identity keys are not copied
	if got := f["filament_flow_ratio"].([]any)[0]; got != "0.98" {
		t.Errorf("own key should win: %v", got)
	}
	if f["filament_extruder_variant"] == nil {
		t.Error("include key missing")
	}
	if f["setting_id"] != "TF001" || f["name"] != "Bambu PLA Basic @BBL TP" {
		t.Errorf("include identity leaked: %v %v", f["setting_id"], f["name"])
	}
	if f["filament_id"] != "GFA00" || f["filament_density"].([]any)[0] != "1.26" {
		t.Errorf("parent chain: %v %v", f["filament_id"], f["filament_density"])
	}
}

func TestFlattenLoopAndMissing(t *testing.T) {
	ix := index(t)
	if _, err := ix.Flatten(KindMachine, "Loop A"); err == nil || !strings.Contains(err.Error(), "loop") {
		t.Fatalf("want loop error, got %v", err)
	}
	if _, err := ix.Flatten(KindMachine, "Nope"); errfmt.As(err).Code != errfmt.ExitNotFound {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestFind(t *testing.T) {
	ix := index(t)
	cases := map[string]string{"0.20mm Standard": "0.20mm Standard @BBL TP", "Bambu PLA Basic": "Bambu PLA Basic @BBL TP", "Generic PLA": "Generic PLA"}
	for base, want := range cases {
		kind := KindFilament
		if strings.HasPrefix(base, "0.") {
			kind = KindProcess
		}
		got, err := ix.Find(kind, base, machine)
		if err != nil || got != want {
			t.Errorf("%s: %s %v", base, got, err)
		}
	}
	if _, err := ix.Find(KindFilament, "Bambu PLA Basic", "Unknown Printer 0.4 nozzle"); errfmt.As(err).Code != errfmt.ExitNotFound {
		t.Error("incompatible should fail")
	}
	if _, err := ix.Find(KindFilament, "Unobtainium", machine); errfmt.As(err).Code != errfmt.ExitNotFound {
		t.Error("unknown should fail")
	}
}

func req(r recipe.Recipe, sets ...string) Request {
	return Request{Recipe: r, MachinePreset: machine, Plate: "textured_plate", Sets: sets}
}

func TestBuild(t *testing.T) {
	ix := index(t)
	r := recipe.Recipe{
		Name: "t", Process: "0.20mm Standard", Filament: "Bambu PLA Basic",
		ProcessOverrides:  map[string]any{"brim_type": "no_brim", "sparse_infill_density": "100%"},
		FilamentOverrides: map[string]any{"nozzle_temperature": "215"},
	}
	c, err := Build(ix, req(r, "wall_loops=4", "fan_max_speed=30", "default_acceleration=[\"5000\",\"6000\"]"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Process["curr_bed_type"] != "Textured PEI Plate" {
		t.Errorf("bed from printer plate: %v", c.Process["curr_bed_type"])
	}
	if c.Process["brim_type"] != "no_brim" || c.Process["wall_loops"] != "4" {
		t.Errorf("overrides: %v %v", c.Process["brim_type"], c.Process["wall_loops"])
	}
	nt := c.Filament["nozzle_temperature"].([]any)
	if len(nt) != 2 || nt[0] != "215" || nt[1] != "215" {
		t.Errorf("scalar must broadcast to array length: %v", nt)
	}
	if fm := c.Filament["fan_max_speed"].([]any); len(fm) != 1 || fm[0] != "30" {
		t.Errorf("--set routed to filament: %v", fm)
	}
	if da := c.Process["default_acceleration"].([]any); da[1] != "6000" {
		t.Errorf("JSON array --set: %v", da)
	}
	if c.Process["sparse_infill_pattern"] != "zig-zag" || len(c.Notes) != 1 {
		t.Errorf("100%% grid must switch to zig-zag: %v %v", c.Process["sparse_infill_pattern"], c.Notes)
	}
	if c.Presets["filament"] != "Bambu PLA Basic @BBL TP" {
		t.Error(c.Presets)
	}
}

func TestBuildErrors(t *testing.T) {
	ix := index(t)
	r := recipe.Recipe{Process: "0.20mm Standard", Filament: "Bambu PLA Basic"}
	if _, err := Build(ix, req(r, "wall_loopz=3")); errfmt.As(err).Code != errfmt.ExitUsage {
		t.Errorf("unknown key: %v", err)
	}
	if _, err := Build(ix, req(r, "novalue")); errfmt.As(err).Code != errfmt.ExitUsage {
		t.Errorf("bad set: %v", err)
	}
	if _, err := Build(ix, req(r, "curr_bed_type=Glass")); errfmt.As(err).Code != errfmt.ExitUsage {
		t.Errorf("bad bed: %v", err)
	}
	rq := req(r)
	rq.Filament = "Generic PLA"
	c, err := Build(ix, rq)
	if err != nil || c.Presets["filament"] != "Generic PLA" {
		t.Errorf("--filament override: %v %v", c, err)
	}
}

func TestDiscoverEnv(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin", "bambu-studio")
	_ = os.MkdirAll(filepath.Dir(bin), 0o755)
	_ = os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "resources", "profiles"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "resources", "profiles", "BBL.json"), []byte("{}"), 0o600)
	t.Setenv("BAMBU_STUDIO_PATH", bin)
	st, err := Discover("", "")
	if err != nil || !exists(filepath.Join(st.Resources, "profiles", "BBL.json")) {
		t.Fatalf("%+v %v", st, err)
	}
	t.Setenv("BAMBU_STUDIO_PATH", filepath.Join(dir, "missing"))
	if _, err := Discover("", ""); errfmt.As(err).Code != errfmt.ExitConfig {
		t.Fatal("missing binary should be a config error")
	}
}

// fakeStudio emulates the Bambu Studio CLI: it copies a prepared 3MF to --outputdir/--export-3mf
// and writes result.json, or fails like the real CLI when FAKE_FAIL is set.
const fakeStudio = `#!/bin/sh
out=""; name=""
while [ $# -gt 0 ]; do
  case "$1" in
    --outputdir) out="$2"; shift ;;
    --export-3mf) name="$2"; shift ;;
  esac
  shift
done
if [ -n "$FAKE_FAIL" ]; then
  echo '{"return_code": -18, "error_string": "Invalid parameter value(s) included in the 3mf file."}' > "$out/result.json"
  echo "sparse_infill_pattern: grid doesn't work at 100% density" >&2
  exit 238
fi
cp "$FAKE_3MF" "$out/$name"
echo '{"return_code": 0, "error_string": "Success.", "sliced_plates": [{"warning_message": ""}]}' > "$out/result.json"
`

func TestRunWithFakeStudio(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "studio.sh")
	_ = os.WriteFile(bin, []byte(fakeStudio), 0o755)
	t.Setenv("FAKE_3MF", testutil.Write(t, dir, "prepared", testutil.Opts{}))
	model := filepath.Join(dir, "part.stl")
	_ = os.WriteFile(model, []byte("solid x\nendsolid x\n"), 0o600)
	st := &Studio{Binary: bin, Resources: "testdata/resources"}
	r := recipe.Recipe{Name: "t", Process: "0.20mm Standard", Filament: "Bambu PLA Basic"}
	rq := req(r)
	rq.Model, rq.Name, rq.OutDir, rq.WorkDir = model, "part-t", filepath.Join(dir, "out"), filepath.Join(dir, "work")
	sum, err := Run(context.Background(), st, index(t), rq)
	if err != nil {
		t.Fatal(err)
	}
	if !exists(sum.Gcode3MF) || !exists(sum.PreviewPNG) || !exists(sum.SummaryJSON) || sum.Layers != 113 || sum.WeightG != 8.1 {
		t.Fatalf("outputs: %+v", sum)
	}
	if !filepath.IsAbs(sum.Gcode3MF) {
		t.Fatal("output path must be absolute")
	}

	t.Setenv("FAKE_FAIL", "1")
	_, err = Run(context.Background(), st, index(t), rq)
	e := errfmt.As(err)
	if e.Code != errfmt.ExitSliceFailed || e.Data["return_code"] != -18 || !strings.Contains(e.Message, "100% density") {
		t.Fatalf("failure mapping: %+v", e)
	}

	rq.Model = filepath.Join(dir, "part.step")
	_ = os.WriteFile(rq.Model, []byte("ISO-10303-21;"), 0o600)
	if _, err := Run(context.Background(), st, index(t), rq); errfmt.As(err).Code != errfmt.ExitUsage {
		t.Fatalf("STEP must be rejected with usage: %v", err)
	}
}
