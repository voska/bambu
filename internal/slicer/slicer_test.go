package slicer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/job"
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
	nt := c.Filaments[0]["nozzle_temperature"].([]any)
	if len(nt) != 2 || nt[0] != "215" || nt[1] != "215" {
		t.Errorf("scalar must broadcast to array length: %v", nt)
	}
	if fm := c.Filaments[0]["fan_max_speed"].([]any); len(fm) != 1 || fm[0] != "30" {
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
	rq.Filaments = []string{"Generic PLA"}
	c, err := Build(ix, rq)
	if err != nil || c.Presets["filament"] != "Generic PLA" {
		t.Errorf("--filament override: %v %v", c, err)
	}
	rq.Filaments = []string{""}
	if c, err := Build(ix, rq); err != nil || c.Presets["filament"] != "Bambu PLA Basic @BBL TP" {
		t.Errorf(`--filament "" keeps the recipe's filament: %v %v`, c, err)
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
// and writes result.json (FAKE_RESULT's, if set), or fails like the real CLI when FAKE_FAIL is set.
// It records its arguments, one per line, in FAKE_ARGS.
const fakeStudio = `#!/bin/sh
out=""; name=""
[ -n "$FAKE_ARGS" ] && printf '%s\n' "$@" > "$FAKE_ARGS"
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
if [ -n "$FAKE_RESULT" ]; then cp "$FAKE_RESULT" "$out/result.json"; exit 0; fi
echo '{"return_code": 0, "error_string": "Success.", "sliced_plates": [{"warning_message": ""}]}' > "$out/result.json"
`

func readArgs(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(os.Getenv("FAKE_ARGS"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRunWithFakeStudio(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	dir := t.TempDir()
	t.Setenv("FAKE_ARGS", filepath.Join(dir, "args"))
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
	if args := readArgs(t); strings.Contains(args, "--filament-colour") || strings.Contains(args, "--load-custom-gcodes") ||
		strings.Count(strings.Split(args, "--load-filaments\n")[1], ";") != 0 {
		t.Fatalf("single filament: same Studio invocation as before:\n%s", args)
	}
	if len(sum.FilamentChanges) != 0 || sum.FilamentChanges == nil || !reflect.DeepEqual(sum.FilamentPresets, []string{"Bambu PLA Basic @BBL TP"}) {
		t.Fatalf("%+v %+v", sum.FilamentChanges, sum.FilamentPresets)
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

func TestNextLayerTop(t *testing.T) {
	proc := map[string]any{"initial_layer_print_height": "0.2", "layer_height": "0.2"}
	for z, want := range map[float64]float64{4.0: 4.2, 0.2: 0.4, 5.2: 5.4} {
		if got, err := NextLayerTop(proc, z); err != nil || got != want {
			t.Errorf("%g: %g %v", z, got, err)
		}
	}
	for _, z := range []float64{4.1, 0.1} {
		if _, err := NextLayerTop(proc, z); errfmt.As(err).Code != errfmt.ExitUsage {
			t.Errorf("%g must be refused: %v", z, err)
		}
	}
	_, err := NextLayerTop(proc, 4.1)
	if e := errfmt.As(err); !strings.Contains(e.Message, "use 4 or 4.2") {
		t.Errorf("suggest the neighbouring layer tops: %q", e.Message)
	}
	thick := map[string]any{"initial_layer_print_height": "0.3", "layer_height": "0.12"}
	if got, err := NextLayerTop(thick, 0.54); err != nil || got != 0.66 {
		t.Errorf("0.3 first layer, 0.12 after: %g %v", got, err)
	}
}

func TestBuildTwoFilaments(t *testing.T) {
	r := recipe.Recipe{Process: "0.20mm Standard", Filament: "Bambu PLA Basic", FilamentOverrides: map[string]any{"nozzle_temperature": "215"}}
	rq := req(r, "fan_max_speed=30")
	rq.Filaments = []string{"Generic PLA", "Bambu PLA Basic"}
	c, err := Build(index(t), rq)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Filaments) != 2 || c.Presets["filament"] != "Generic PLA" || !reflect.DeepEqual(c.FilamentPresets, []string{"Generic PLA", "Bambu PLA Basic @BBL TP"}) {
		t.Fatalf("%v %v", c.Presets, c.FilamentPresets)
	}
	for i, f := range c.Filaments {
		if fm := f["fan_max_speed"].([]any); fm[0] != "30" {
			t.Errorf("filament %d: --set applies to every filament: %v", i+1, fm)
		}
		if nt := f["nozzle_temperature"].([]any); nt[0] != "215" {
			t.Errorf("filament %d: recipe overrides apply to every filament: %v", i+1, nt)
		}
	}
}

// twoFilamentRun slices with the recorded two-filament 3MF and result.json standing in for Bambu Studio's output.
func twoFilamentRun(t *testing.T, mut func(*Request)) (*Summary, Request, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "studio.sh")
	_ = os.WriteFile(bin, []byte(fakeStudio), 0o755)
	t.Setenv("FAKE_ARGS", filepath.Join(dir, "args"))
	t.Setenv("FAKE_3MF", testutil.FromDir(t, "../job/testdata/two_filament", dir, "prepared"))
	result, _ := filepath.Abs("../job/testdata/two_filament/result.json") // the fake runs in the work dir
	t.Setenv("FAKE_RESULT", result)
	model := filepath.Join(dir, "part.3mf")
	_ = os.WriteFile(model, []byte("PK"), 0o600)
	rq := req(recipe.Recipe{Name: "t", Process: "0.20mm Standard", Filament: "Bambu PLA Basic"})
	rq.Model, rq.Name, rq.OutDir, rq.WorkDir = model, "two", filepath.Join(dir, "out"), filepath.Join(dir, "work")
	rq.Filaments, rq.Colors, rq.ChangeZs = []string{"Generic PLA", "Bambu PLA Basic"}, []string{"ffffff", "#000000"}, []float64{4.0}
	if mut != nil {
		mut(&rq)
	}
	sum, err := Run(context.Background(), &Studio{Binary: bin, Resources: "testdata/resources"}, index(t), rq)
	return sum, rq, err
}

func TestRunTwoFilaments(t *testing.T) {
	sum, rq, err := twoFilamentRun(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	args := readArgs(t)
	for _, want := range []string{
		"--load-filaments\n" + filepath.Join(rq.WorkDir, "filament.json") + ";" + filepath.Join(rq.WorkDir, "filament_2.json") + "\n",
		"--filament-colour\n#FFFFFF;#000000\n",
		"--load-custom-gcodes\n" + filepath.Join(rq.WorkDir, "custom_gcode.json") + "\n",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("Studio args missing %q:\n%s", want, args)
		}
	}
	b, _ := os.ReadFile(filepath.Join(rq.WorkDir, "custom_gcode.json"))
	var cg map[string]any
	_ = json.Unmarshal(b, &cg)
	want := map[string]any{"mode": "MultiAsSingle", "gcodes": []any{map[string]any{
		"type": "ToolChange", "print_z": 4.2, "extruder": float64(2), "color": "#000000", "extra": "",
	}}}
	if !reflect.DeepEqual(cg, want) {
		t.Fatalf("custom G-code: the layer-slider change Studio stores (filament 2 from print_z 4.2):\n%s", b)
	}
	if !reflect.DeepEqual(sum.FilamentChanges, []job.FilamentChange{{Layer: 21, Z: 4.2, Filament: 2}}) || !sum.PrimeTower {
		t.Fatalf("%+v prime_tower=%v", sum.FilamentChanges, sum.PrimeTower)
	}
	f1, f2 := sum.Filaments[0], sum.Filaments[1]
	if *f1.ModelG != 16.02 || *f1.WasteG != 0.97 || *f2.ModelG != 2.89 || *f2.WasteG != 0.23 || f2.UsedG != 3.12 {
		t.Fatalf("grams: %+v %+v", f1, f2)
	}
	if !exists(sum.Gcode3MF) {
		t.Fatal("output missing")
	}
}

func TestRunTwoFilamentsRefusedBeforeSlicing(t *testing.T) {
	cases := map[string]func(*Request){
		"no change height":        func(r *Request) { r.ChangeZs = nil },
		"change height off-layer": func(r *Request) { r.ChangeZs = []float64{4.1} },
		"one colour":              func(r *Request) { r.Colors = r.Colors[:1] },
		"bad colour":              func(r *Request) { r.Colors[1] = "black" },
		"heights not ascending": func(r *Request) {
			r.Filaments, r.Colors, r.ChangeZs = append(r.Filaments, "Generic PLA"), append(r.Colors, "FF0000"), []float64{4.0, 2.0}
		},
		"change height, one filament": func(r *Request) { r.Filaments, r.Colors = r.Filaments[:1], nil },
	}
	for name, mut := range cases {
		_, rq, err := twoFilamentRun(t, mut)
		if errfmt.As(err).Code != errfmt.ExitUsage {
			t.Errorf("%s: want usage error, got %v", name, err)
		}
		if exists(filepath.Join(rq.WorkDir, "result.json")) {
			t.Errorf("%s: Studio must not run", name)
		}
	}
}

func TestRunTwoFilamentsChangeNotInGcode(t *testing.T) {
	// the recorded G-code changes at Z 4.2; asking for a change after Z 3.0 must not pass as a good slice
	_, rq, err := twoFilamentRun(t, func(r *Request) { r.ChangeZs = []float64{3.0} })
	e := errfmt.As(err)
	if e.Code != errfmt.ExitSliceFailed || !strings.Contains(e.Message, "filament change") {
		t.Fatalf("want slice failure, got %v", err)
	}
	if exists(filepath.Join(rq.OutDir, "two.gcode.3mf")) || exists(filepath.Join(rq.OutDir, "two.summary.json")) {
		t.Fatal("a slice without the asked-for change must not reach the output dir")
	}
}
