package slicer

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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
	Model         string
	Recipe        recipe.Recipe
	MachinePreset string   // e.g. "Bambu Lab X1 Carbon 0.4 nozzle"
	Plate         string   // installed plate id, e.g. "textured_plate"
	Filament      string   // optional filament preset override
	Sets          []string // key=value overrides
	Name          string   // output base name
	OutDir        string   // where the .gcode.3mf/.png/.summary.json go
	WorkDir       string   // scratch dir for configs and slicer output
	AutoOrient    bool
}

// Summary is the slice result (the --json contract).
type Summary struct {
	Name          string            `json:"name"`
	Recipe        string            `json:"recipe"`
	Description   string            `json:"recipe_description"`
	Model         string            `json:"model"`
	Gcode3MF      string            `json:"gcode_3mf"`
	PreviewPNG    string            `json:"preview_png"`
	SummaryJSON   string            `json:"summary_json"`
	TimeS         int               `json:"time_s"`
	Time          string            `json:"time"`
	WeightG       float64           `json:"weight_g"`
	Layers        int               `json:"layers"`
	Filaments     []job.Filament    `json:"filaments"`
	BedType       string            `json:"bed_type"`
	BedTemp       string            `json:"bed_temp"`
	NozzleTemp    string            `json:"nozzle_temp"`
	NozzleFirst   string            `json:"nozzle_temp_initial"`
	Presets       map[string]string `json:"presets"`
	Overrides     map[string]any    `json:"overrides"`
	Notes         []string          `json:"notes"`
	Warnings      []string          `json:"warnings"`
	Settings      map[string]any    `json:"settings"`
	AutoOrient    bool              `json:"auto_orient"`
	SlicerLog     string            `json:"slicer_log"`
	StudioVersion string            `json:"studio_version,omitempty"`
}

// Configs are the flattened configs handed to the CLI.
type Configs struct {
	Machine, Process, Filament map[string]any
	Presets                    map[string]string
	Applied                    map[string]any
	Notes                      []string
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
	filBase := req.Recipe.Filament
	if req.Filament != "" {
		filBase = req.Filament
	}
	filName, err := ix.Find(KindFilament, filBase, req.MachinePreset)
	if err != nil {
		return nil, err
	}
	process, err := ix.Flatten(KindProcess, procName)
	if err != nil {
		return nil, err
	}
	filament, err := ix.Flatten(KindFilament, filName)
	if err != nil {
		return nil, err
	}
	c := &Configs{
		Machine: machine, Process: process, Filament: filament, Applied: map[string]any{}, Notes: []string{},
		Presets: map[string]string{"machine": req.MachinePreset, "process": procName, "filament": filName},
	}
	if bed, ok := bedName(req.Plate); ok {
		process["curr_bed_type"] = bed
	}
	apply := func(target map[string]any, overrides map[string]any) {
		keys := make([]string, 0, len(overrides))
		for k := range overrides {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			target[k] = coerce(target[k], overrides[k])
			c.Applied[k] = target[k]
		}
	}
	apply(process, req.Recipe.ProcessOverrides)
	apply(filament, req.Recipe.FilamentOverrides)
	for _, s := range req.Sets {
		k, v, ok := strings.Cut(s, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" {
			return nil, errfmt.New(errfmt.ExitUsage, "bad --set %q", s).WithHint("use --set key=value, e.g. --set wall_loops=4")
		}
		var target map[string]any
		switch {
		case has(filament, k):
			target = filament
		case has(machine, k):
			target = machine
		case has(process, k) || k == "curr_bed_type":
			target = process
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
		target[k] = coerce(target[k], val)
		c.Applied[k] = target[k]
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

var safeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// SafeName turns a name into a file-safe base name.
func SafeName(s string) string { return strings.Trim(safeName.ReplaceAllString(s, "-"), "-.") }

type result struct {
	ReturnCode   int    `json:"return_code"`
	ErrorString  string `json:"error_string"`
	SlicedPlates []struct {
		WarningMessage string `json:"warning_message"`
	} `json:"sliced_plates"`
}

// Run slices req with st and writes outputs into req.OutDir.
func Run(ctx context.Context, st *Studio, ix *Index, req Request) (*Summary, error) {
	ext := strings.ToLower(filepath.Ext(req.Model))
	switch ext {
	case ".stl", ".3mf", ".obj", ".amf":
	case ".step", ".stp":
		return nil, errfmt.New(errfmt.ExitUsage, "STEP is not supported by the Bambu Studio CLI").
			WithHint("export an STL or 3MF from your CAD tool and slice that")
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
	cfgs, err := Build(ix, req)
	if err != nil {
		return nil, err
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
	paths := map[string]string{}
	for kind, data := range map[string]map[string]any{"machine": cfgs.Machine, "process": cfgs.Process, "filament": cfgs.Filament} {
		b, _ := json.MarshalIndent(data, "", " ")
		paths[kind] = filepath.Join(work, kind+".json")
		if err := os.WriteFile(paths[kind], b, 0o600); err != nil {
			return nil, errfmt.Wrap(errfmt.ExitConfig, err, "write %s config", kind)
		}
	}
	name := SafeName(req.Name)
	threemf := name + ".gcode.3mf"
	orient := "0"
	if req.AutoOrient {
		orient = "1"
	}
	args := []string{
		"--slice", "0", "--arrange", "1", "--orient", orient,
		"--load-settings", paths["machine"] + ";" + paths["process"], "--load-filaments", paths["filament"],
		"--outputdir", work, "--export-3mf", threemf, model,
	}
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
	if res.ReturnCode != 0 || !exists(filepath.Join(work, threemf)) {
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
	final := filepath.Join(out, threemf)
	if err := move(filepath.Join(work, threemf), final); err != nil {
		return nil, errfmt.Wrap(errfmt.ExitError, err, "move output")
	}
	png := filepath.Join(out, name+".png")
	if err := extract(final, "Metadata/plate_1.png", png); err != nil {
		png = ""
	}
	j, err := job.Inspect(final, 1)
	if err != nil {
		return nil, err
	}
	sum := &Summary{
		Name: name, Recipe: req.Recipe.Name, Description: req.Recipe.Description, Model: model,
		Gcode3MF: final, PreviewPNG: png, SummaryJSON: filepath.Join(out, name+".summary.json"),
		TimeS: j.PredictionS, Time: j.TotalTime, WeightG: j.WeightG, Layers: j.Layers, Filaments: j.Filaments,
		BedType: j.BedType, BedTemp: j.BedTemp, NozzleTemp: j.NozzleTemp, NozzleFirst: j.NozzleTempFirst,
		Presets: cfgs.Presets, Overrides: cfgs.Applied, Notes: cfgs.Notes, Warnings: []string{}, Settings: j.Settings,
		AutoOrient: req.AutoOrient, SlicerLog: logPath, StudioVersion: st.Version,
	}
	for _, p := range res.SlicedPlates {
		if p.WarningMessage != "" {
			sum.Warnings = append(sum.Warnings, p.WarningMessage)
		}
	}
	b, _ := json.MarshalIndent(sum, "", "  ")
	if err := os.WriteFile(sum.SummaryJSON, b, 0o600); err != nil {
		return nil, errfmt.Wrap(errfmt.ExitError, err, "write summary")
	}
	return sum, nil
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
