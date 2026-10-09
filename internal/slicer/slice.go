package slicer

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/job"
	"github.com/voska/bambu/internal/recipe"
)

// SolidOKPatterns are sparse_infill_pattern values the slicer accepts at 100% density (valid top-surface
// patterns, per PrintConfig.cpp validation). Others fail with -18 "... doesn't work at 100% density".
var SolidOKPatterns = map[string]bool{
	"concentric": true, "zig-zag": true, "monotonic": true, "monotonicline": true, "globalmonotonicline": true,
	"alignedrectilinear": true, "hilbertcurve": true, "archimedeanchords": true, "octagramspiral": true,
}

// CLIErrors describes Bambu Studio CLI return codes (src/libslic3r/Utils.hpp). The shell sees 256+code.
var CLIErrors = map[int]string{
	-1: "generic error", -2: "invalid parameters", -3: "input file not found", -4: "invalid 3MF",
	-5: "config file error (bad preset JSON)", -6: "model file can not be parsed (supported: STL, 3MF, OBJ, AMF)",
	-7: "unsupported 3MF version", -13: "failed exporting 3MF", -17: "process not compatible with printer",
	-18: "invalid setting value(s)", -24: "3MF too new for this Bambu Studio",
	-50: "no suitable objects on plate (outside the plate or too big?)", -51: "validation error (object outside printable area?)",
	-100: "slicing error", -101: "G-code path conflicts",
}

// Request is one slice job.
type Request struct {
	Model          string
	Recipe         recipe.Recipe
	MachinePreset  string    // e.g. "Bambu Lab X1 Carbon 0.4 nozzle"
	Plate          string    // installed plate id, e.g. "textured_plate"
	Filaments      []string  // filament presets in print order (default: the recipe's)
	Colors         []string  // spool colour per filament (RRGGBB); Studio sizes the purge between filaments from them
	ChangeZs       []float64 // filament n+1 starts on the first layer above ChangeZs[n-1]
	Sets           []string  // key=value overrides
	Name           string    // output base name
	OutDir         string    // where the .gcode.3mf/.png/.summary.json go
	WorkDir        string    // scratch dir for configs and slicer output
	AutoOrient     bool
	StepPython     string // interpreter with CadQuery for STEP input (default python3)
	SuggestedSlot  string
	RequirePreview bool
}

// Summary is the slice result (the --json contract).
type Summary struct {
	Name            string               `json:"name"`
	Recipe          string               `json:"recipe"`
	Description     string               `json:"recipe_description"`
	Model           string               `json:"model"`
	Gcode3MF        string               `json:"gcode_3mf"`
	PreviewPNG      string               `json:"preview_png"`
	SummaryJSON     string               `json:"summary_json"`
	TimeS           int                  `json:"time_s"`
	Time            string               `json:"time"`
	WeightG         float64              `json:"weight_g"`
	Layers          int                  `json:"layers"`
	Filaments       []job.Filament       `json:"filaments"`
	FilamentChanges []job.FilamentChange `json:"filament_changes"`
	PrimeTower      bool                 `json:"prime_tower"`
	FilamentPresets []string             `json:"filament_presets"`
	BedType         string               `json:"bed_type"`
	BedTemp         string               `json:"bed_temp"`
	NozzleTemp      string               `json:"nozzle_temp"`
	NozzleFirst     string               `json:"nozzle_temp_initial"`
	Presets         map[string]string    `json:"presets"`
	Overrides       map[string]any       `json:"overrides"`
	Notes           []string             `json:"notes"`
	Warnings        []string             `json:"warnings"`
	Settings        map[string]any       `json:"settings"`
	AutoOrient      bool                 `json:"auto_orient"`
	SlicerLog       string               `json:"slicer_log"`
	StudioVersion   string               `json:"studio_version,omitempty"`
	SuggestedSlot   string               `json:"suggested_slot,omitempty"`
	Objects         []Object             `json:"objects"`
}

// Object reports the slicer's dimensions, including brim, for one plate object.
type Object struct {
	Name      string     `json:"name"`
	Footprint [3]float64 `json:"footprint_incl_brim_mm"`
}

// Configs are the flattened configs handed to the CLI.
type Configs struct {
	Machine, Process map[string]any
	Filaments        []map[string]any // one per filament, in print order
	Presets          map[string]string
	FilamentPresets  []string
	Applied          map[string]any
	Notes            []string
}

// Build resolves presets for the target machine and applies recipe + --set overrides.
func Build(ix *Index, req Request) (*Configs, error) {
	machine, err := ix.Flatten(KindMachine, req.MachinePreset)
	if err != nil {
		return nil, err
	}
	procName, err := ix.Find(KindProcess, req.Recipe.Process, req.MachinePreset)
	if err != nil {
		return nil, err
	}
	process, err := ix.Flatten(KindProcess, procName)
	if err != nil {
		return nil, err
	}
	bases := req.presets()
	c := &Configs{Machine: machine, Process: process, Applied: map[string]any{}, Notes: []string{}}
	for _, base := range bases {
		name, err := ix.Find(KindFilament, base, req.MachinePreset)
		if err != nil {
			return nil, err
		}
		f, err := ix.Flatten(KindFilament, name)
		if err != nil {
			return nil, err
		}
		c.Filaments = append(c.Filaments, f)
		c.FilamentPresets = append(c.FilamentPresets, name)
	}
	filament := c.Filaments[0]
	c.Presets = map[string]string{"machine": req.MachinePreset, "process": procName, "filament": c.FilamentPresets[0]}
	if bed, ok := bedName(req.Plate); ok {
		process["curr_bed_type"] = bed
	}
	// filament overrides and filament --set keys apply to every filament
	apply := func(targets []map[string]any, overrides map[string]any) {
		keys := make([]string, 0, len(overrides))
		for k := range overrides {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, target := range targets {
				target[k] = coerce(target[k], overrides[k])
			}
			c.Applied[k] = targets[0][k]
		}
	}
	apply([]map[string]any{process}, req.Recipe.ProcessOverrides)
	apply(c.Filaments, req.Recipe.FilamentOverrides)
	for _, s := range req.Sets {
		k, v, ok := strings.Cut(s, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" {
			return nil, errfmt.New(errfmt.ExitUsage, "bad --set %q", s).WithHint("use --set key=value, e.g. --set wall_loops=4")
		}
		var targets []map[string]any
		switch {
		case has(filament, k):
			targets = c.Filaments
		case has(machine, k):
			targets = []map[string]any{machine}
		case has(process, k) || k == "curr_bed_type":
			targets = []map[string]any{process}
		default:
			return nil, errfmt.New(errfmt.ExitUsage, "unknown setting %q", k).
				WithHint("use a Bambu Studio config key (e.g. wall_loops, sparse_infill_density, brim_type); nothing was sliced")
		}
		var val any = v
		if strings.HasPrefix(v, "[") {
			var arr []any
			if json.Unmarshal([]byte(v), &arr) == nil {
				val = arr
			}
		}
		for _, target := range targets {
			target[k] = coerce(target[k], val)
		}
		c.Applied[k] = targets[0][k]
	}
	if _, ok := plateID(str(process["curr_bed_type"])); !ok {
		return nil, errfmt.New(errfmt.ExitUsage, "unsupported curr_bed_type %q", str(process["curr_bed_type"])).
			WithHint("one of: Textured PEI Plate, Cool Plate, Engineering Plate, High Temp Plate, Supertack Plate")
	}
	if strings.TrimSuffix(strings.TrimSpace(str(process["sparse_infill_density"])), "%") == "100" {
		if pat := str(process["sparse_infill_pattern"]); !SolidOKPatterns[pat] {
			c.Notes = append(c.Notes, fmt.Sprintf("sparse_infill_pattern %s is invalid at 100%% density; switched to zig-zag", pat))
			process["sparse_infill_pattern"] = "zig-zag"
			c.Applied["sparse_infill_pattern"] = "zig-zag"
		}
	}
	return c, nil
}

func has(m map[string]any, k string) bool { _, ok := m[k]; return ok }

// coerce shapes an override like the existing value: per-extruder-variant settings are string arrays,
// so a scalar is broadcast to the existing length; everything is stringified as the CLI expects.
func coerce(existing, v any) any {
	if arr, ok := v.([]any); ok {
		out := make([]any, len(arr))
		for i, x := range arr {
			out[i] = str(x)
		}
		return out
	}
	if ex, ok := existing.([]any); ok {
		n := len(ex)
		if n == 0 {
			n = 1
		}
		out := make([]any, n)
		for i := range out {
			out[i] = str(v)
		}
		return out
	}
	return str(v)
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case float64:
		return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", t), "0"), ".")
	}
	return fmt.Sprint(v)
}

func bedName(plate string) (string, bool) {
	for name, id := range job.BedTypes {
		if id == plate {
			return name, true
		}
	}
	return "", false
}

func plateID(bed string) (string, bool) {
	id, ok := job.BedTypes[bed]
	return id, ok
}

// NextLayerTop is the top of the first layer above z, which must itself be a layer top (first layer, then a fixed layer
// height). That is the print_z Bambu Studio stores for a filament change on that layer (IMSlider::add_code_as_tick): the
// first layer printed with the new filament.
func NextLayerTop(process map[string]any, z float64) (float64, error) {
	first, err1 := strconv.ParseFloat(str(process["initial_layer_print_height"]), 64)
	h, err2 := strconv.ParseFloat(str(process["layer_height"]), 64)
	if err1 != nil || err2 != nil || first <= 0 || h <= 0 {
		return 0, errfmt.New(errfmt.ExitConfig, "process preset has no usable initial_layer_print_height/layer_height (%v/%v)",
			process["initial_layer_print_height"], process["layer_height"]).WithHint("check the recipe's process preset")
	}
	if z < first-1e-6 {
		return 0, errfmt.New(errfmt.ExitUsage, "--filament-change-z %g is below the first layer top (%g mm)", z, first).
			WithHint("give the Z where the previous filament ends: the top of its last layer")
	}
	n := (z - first) / h
	if math.Abs(n-math.Round(n))*h > 1e-3 {
		return 0, errfmt.New(errfmt.ExitUsage, "--filament-change-z %g is not a layer top (%g mm first layer, then %g mm): use %g or %g",
			z, first, h, round3(first+math.Floor(n)*h), round3(first+math.Ceil(n)*h)).
			WithHint("the change happens between layers; give the top of the previous filament's last layer")
	}
	return round3(first + (math.Round(n)+1)*h), nil
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

var hexColor = regexp.MustCompile(`^[0-9A-Fa-f]{6}$`)

// presets are the filament presets in print order: the non-empty --filament values, else the recipe's.
func (req Request) presets() []string {
	var out []string
	for _, f := range req.Filaments {
		if strings.TrimSpace(f) != "" {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return []string{req.Recipe.Filament}
	}
	return out
}

// checkFilaments validates the multi-filament part of a request before anything is sliced.
func checkFilaments(req Request) ([]string, error) {
	n := len(req.presets())
	if len(req.ChangeZs) != n-1 {
		return nil, errfmt.New(errfmt.ExitUsage, "%d filament(s) need %d --filament-change-z, got %d", n, n-1, len(req.ChangeZs)).
			WithHint("pass one --filament per filament in print order, and one --filament-change-z per filament after the first")
	}
	for i := 1; i < len(req.ChangeZs); i++ {
		if req.ChangeZs[i] <= req.ChangeZs[i-1] {
			return nil, errfmt.New(errfmt.ExitUsage, "--filament-change-z heights must be ascending, got %v", req.ChangeZs).
				WithHint("filament n+1 starts above the nth height")
		}
	}
	if (n > 1 || len(req.Colors) > 0) && len(req.Colors) != n {
		return nil, errfmt.New(errfmt.ExitUsage, "%d filament(s) need %d --color, got %d", n, n, len(req.Colors)).
			WithHint("pass each spool's colour in filament order (bambu status shows them); Bambu Studio sizes the purge between filaments from the colours")
	}
	colors := make([]string, 0, len(req.Colors))
	for _, c := range req.Colors {
		h := strings.TrimPrefix(strings.TrimSpace(c), "#")
		if !hexColor.MatchString(h) {
			return nil, errfmt.New(errfmt.ExitUsage, "bad --color %q", c).WithHint("use the spool's colour as RRGGBB hex, e.g. --color FFFF00")
		}
		colors = append(colors, "#"+strings.ToUpper(h))
	}
	return colors, nil
}

var safeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// SafeName turns a name into a file-safe base name.
func SafeName(s string) string { return strings.Trim(safeName.ReplaceAllString(s, "-"), "-.") }

type result struct {
	ReturnCode   int    `json:"return_code"`
	ErrorString  string `json:"error_string"`
	SlicedPlates []struct {
		WarningMessage string `json:"warning_message"`
		Filaments      []struct {
			ID         int      `json:"id"`
			MainUsedG  *float64 `json:"main_used_g"`  // model + support
			TotalUsedG *float64 `json:"total_used_g"` // adds purge and prime tower
		} `json:"filaments"`
		FeatureTypeTimes map[string]float64 `json:"feature_type_times"`
		Objects          []struct {
			Name string                                  `json:"name"`
			BBox *struct{ Width, Depth, Height float64 } `json:"bbox"`
		} `json:"objects"`
	} `json:"sliced_plates"`
}

// Run slices req with st and writes outputs into req.OutDir.
func Run(ctx context.Context, st *Studio, ix *Index, req Request) (*Summary, error) {
	ext := strings.ToLower(filepath.Ext(req.Model))
	switch ext {
	case ".stl", ".3mf", ".obj", ".amf", ".step", ".stp":
	default:
		return nil, errfmt.New(errfmt.ExitUsage, "unsupported model type %q", ext).WithHint("use .stl, .3mf or .obj")
	}
	model, err := filepath.Abs(req.Model)
	if err != nil {
		return nil, errfmt.Wrap(errfmt.ExitUsage, err, "resolve model path")
	}
	if _, err := os.Stat(model); err != nil {
		return nil, errfmt.New(errfmt.ExitNotFound, "model not found: %s", req.Model)
	}
	colors, err := checkFilaments(req)
	if err != nil {
		return nil, err
	}
	cfgs, err := Build(ix, req)
	if err != nil {
		return nil, err
	}
	tops := make([]float64, 0, len(req.ChangeZs))
	for _, z := range req.ChangeZs {
		top, err := NextLayerTop(cfgs.Process, z)
		if err != nil {
			return nil, err
		}
		tops = append(tops, top)
	}
	// Absolute paths everywhere: the Bambu Studio CLI chdirs into its app bundle (macOS: Contents/Resources),
	// so relative --outputdir/--export-3mf paths land inside the bundle or fail with -13.
	work, err := filepath.Abs(req.WorkDir)
	if err != nil {
		return nil, errfmt.Wrap(errfmt.ExitConfig, err, "work dir")
	}
	out, err := filepath.Abs(req.OutDir)
	if err != nil {
		return nil, errfmt.Wrap(errfmt.ExitConfig, err, "output dir")
	}
	for _, d := range []string{work, out} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, errfmt.Wrap(errfmt.ExitConfig, err, "create %s", d)
		}
	}
	configs := map[string]map[string]any{"machine": cfgs.Machine, "process": cfgs.Process, "filament": cfgs.Filaments[0]}
	filKinds := []string{"filament"}
	for i, f := range cfgs.Filaments[1:] {
		kind := fmt.Sprintf("filament_%d", i+2)
		configs[kind] = f
		filKinds = append(filKinds, kind)
	}
	paths := map[string]string{}
	for kind, data := range configs {
		b, _ := json.MarshalIndent(data, "", " ")
		paths[kind] = filepath.Join(work, kind+".json")
		if err := os.WriteFile(paths[kind], b, 0o600); err != nil {
			return nil, errfmt.Wrap(errfmt.ExitConfig, err, "write %s config", kind)
		}
	}
	filPaths := make([]string, len(filKinds))
	for i, k := range filKinds {
		filPaths[i] = paths[k]
	}
	name := SafeName(req.Name)
	threemf := name + ".gcode.3mf"
	for _, stale := range []string{"result.json", "plate_1.gcode", threemf} {
		if err := os.Remove(filepath.Join(work, stale)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, errfmt.Wrap(errfmt.ExitConfig, err, "remove stale slicer output %s", stale)
		}
	}
	src := model
	if ext == ".step" || ext == ".stp" {
		src, err = prepareSTEP(ctx, req.StepPython, model, work)
		if err != nil {
			return nil, err
		}
	}
	orient := "0"
	if req.AutoOrient {
		orient = "1"
	}
	args := []string{
		"--slice", "0", "--arrange", "1", "--orient", orient,
		"--load-settings", paths["machine"] + ";" + paths["process"], "--load-filaments", strings.Join(filPaths, ";"),
	}
	if len(colors) > 0 {
		args = append(args, "--filament-colour", strings.Join(colors, ";"))
	}
	if len(tops) > 0 {
		// the change the GUI's layer slider stores (CustomGCode::Info::from_json); MultiAsSingle: one extruder, AMS swaps
		type item struct {
			Type     string  `json:"type"`
			PrintZ   float64 `json:"print_z"`
			Extruder int     `json:"extruder"`
			Color    string  `json:"color"`
			Extra    string  `json:"extra"`
		}
		cg := struct {
			Mode   string `json:"mode"`
			Gcodes []item `json:"gcodes"`
		}{Mode: "MultiAsSingle"}
		for i, z := range tops {
			cg.Gcodes = append(cg.Gcodes, item{Type: "ToolChange", PrintZ: z, Extruder: i + 2, Color: colors[i+1]})
		}
		b, _ := json.MarshalIndent(cg, "", " ")
		paths["custom_gcode"] = filepath.Join(work, "custom_gcode.json")
		if err := os.WriteFile(paths["custom_gcode"], b, 0o600); err != nil {
			return nil, errfmt.Wrap(errfmt.ExitConfig, err, "write custom G-code")
		}
		args = append(args, "--load-custom-gcodes", paths["custom_gcode"])
	}
	args = append(args, "--outputdir", work, "--export-3mf", threemf, src)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, st.Binary, args...) //nolint:gosec // discovered slicer binary, validated args
	cmd.Dir = work
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	logPath := filepath.Join(work, "slicer.log")
	_ = os.WriteFile(logPath, []byte(stdout.String()+stderr.String()), 0o600)

	var res result
	res.ReturnCode = 1
	if b, err := os.ReadFile(filepath.Join(work, "result.json")); err == nil { //nolint:gosec // work dir
		_ = json.Unmarshal(b, &res)
	} else if runErr == nil {
		res.ReturnCode = 0
	}
	if ctx.Err() != nil {
		return nil, errfmt.New(errfmt.ExitSliceFailed, "slicer timed out after 15 minutes").WithHint("simplify the model")
	}
	if runErr != nil || res.ReturnCode != 0 || !exists(filepath.Join(work, threemf)) {
		details := []string{}
		for _, l := range strings.Split(stderr.String(), "\n") {
			l = strings.TrimSpace(l)
			if l != "" && !strings.HasPrefix(l, "[") && !strings.Contains(l, "run found error") {
				details = append(details, l)
			}
		}
		if len(details) > 8 {
			details = details[:8]
		}
		msg := res.ErrorString
		if msg == "" {
			msg = CLIErrors[res.ReturnCode]
		}
		return nil, errfmt.New(errfmt.ExitSliceFailed, "slicer failed (%d): %s %s", res.ReturnCode, msg, strings.Join(details, "; ")).
			WithHint("%s; fix the recipe/--set values; log: %s", or(CLIErrors[res.ReturnCode], "see log"), logPath).
			WithData("return_code", res.ReturnCode).WithData("details", details).WithData("log", logPath)
	}
	if err := checkChanges(filepath.Join(work, threemf), tops, logPath); err != nil {
		return nil, err
	}
	png := filepath.Join(out, name+".png")
	if err := extract(filepath.Join(work, threemf), "Metadata/plate_1.png", png); err != nil {
		if req.RequirePreview {
			return nil, errfmt.Wrap(errfmt.ExitSliceFailed, err, "slicer did not produce a plate preview")
		}
		png = ""
	}
	final := filepath.Join(out, threemf)
	if err := move(filepath.Join(work, threemf), final); err != nil {
		return nil, errfmt.Wrap(errfmt.ExitError, err, "move output")
	}
	j, err := job.Inspect(final, 1)
	if err != nil {
		return nil, err
	}
	sum := &Summary{
		Name: name, Recipe: req.Recipe.Name, Description: req.Recipe.Description, Model: model,
		Gcode3MF: final, PreviewPNG: png, SummaryJSON: filepath.Join(out, name+".summary.json"),
		TimeS: j.PredictionS, Time: j.TotalTime, WeightG: j.WeightG, Layers: j.Layers, Filaments: j.Filaments,
		FilamentChanges: j.FilamentChanges, FilamentPresets: cfgs.FilamentPresets,
		BedType: j.BedType, BedTemp: j.BedTemp, NozzleTemp: j.NozzleTemp, NozzleFirst: j.NozzleTempFirst,
		Presets: cfgs.Presets, Overrides: cfgs.Applied, Notes: cfgs.Notes, Warnings: []string{}, Settings: j.Settings,
		AutoOrient: req.AutoOrient, SlicerLog: logPath, StudioVersion: st.Version,
		SuggestedSlot: req.SuggestedSlot, Objects: []Object{},
	}
	for n, p := range res.SlicedPlates {
		if n == 0 {
			for _, o := range p.Objects {
				if o.BBox != nil {
					dims := [3]float64{o.BBox.Width, o.BBox.Depth, o.BBox.Height}
					for i := range dims {
						dims[i] = math.Round(dims[i]*100) / 100
					}
					sum.Objects = append(sum.Objects, Object{Name: o.Name, Footprint: dims})
				}
			}
		}
		if p.WarningMessage != "" {
			sum.Warnings = append(sum.Warnings, p.WarningMessage)
		}
		sum.PrimeTower = sum.PrimeTower || p.FeatureTypeTimes["Prime tower"] > 0
		for _, u := range p.Filaments {
			for i := range sum.Filaments {
				if f := &sum.Filaments[i]; f.Index == u.ID-1 && u.MainUsedG != nil && u.TotalUsedG != nil {
					model, waste := math.Round(*u.MainUsedG*100)/100, math.Round((*u.TotalUsedG-*u.MainUsedG)*100)/100
					f.ModelG, f.WasteG = &model, &waste
				}
			}
		}
	}
	b, _ := json.MarshalIndent(sum, "", "  ")
	if err := os.WriteFile(sum.SummaryJSON, b, 0o600); err != nil {
		return nil, errfmt.Wrap(errfmt.ExitError, err, "write summary")
	}
	return sum, nil
}

// checkChanges reads the filament changes back from the sliced G-code. Bambu Studio drops a ToolChange without a word
// when the plate mode doesn't fit (ToolOrdering.cpp), so the G-code is the only proof the change is there.
func checkChanges(threemf string, tops []float64, logPath string) error {
	j, err := job.Inspect(threemf, 1)
	if err != nil {
		return err
	}
	want := make([]job.FilamentChange, len(tops))
	for i, z := range tops {
		want[i] = job.FilamentChange{Z: z, Filament: i + 2}
	}
	got := make([]job.FilamentChange, len(j.FilamentChanges))
	for i, c := range j.FilamentChanges {
		got[i] = job.FilamentChange{Z: round3(c.Z), Filament: c.Filament}
	}
	if !slices.Equal(got, want) {
		return errfmt.New(errfmt.ExitSliceFailed, "the slicer did not place the filament change(s) as asked: wanted %v, G-code has %v", want, j.FilamentChanges).
			WithHint("nothing was written to the output dir; check the model height and the log: %s", logPath).
			WithData("wanted", want).WithData("filament_changes", j.FilamentChanges).WithData("log", logPath)
	}
	return nil
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func move(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src) //nolint:gosec // our work dir
	if err != nil {
		return err //nolint:wrapcheck // wrapped by caller
	}
	defer func() { _ = in.Close() }()
	o, err := os.Create(dst) //nolint:gosec // output dir chosen by user
	if err != nil {
		return err //nolint:wrapcheck // wrapped by caller
	}
	if _, err := io.Copy(o, in); err != nil {
		_ = o.Close()
		return err //nolint:wrapcheck // wrapped by caller
	}
	return o.Close() //nolint:wrapcheck // wrapped by caller
}

func extract(zipPath, member, dst string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err //nolint:wrapcheck // best effort
	}
	defer func() { _ = zr.Close() }()
	for _, f := range zr.File {
		if f.Name != member {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err //nolint:wrapcheck // best effort
		}
		defer func() { _ = rc.Close() }()
		b, err := io.ReadAll(io.LimitReader(rc, 64<<20))
		if err != nil {
			return err //nolint:wrapcheck // best effort
		}
		return os.WriteFile(dst, b, 0o600) //nolint:wrapcheck // best effort
	}
	return errors.New("not found")
}
