package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/output"
	"github.com/voska/bambu/internal/recipe"
	"github.com/voska/bambu/internal/slicer"
)

// SliceCmd slices a model.
type SliceCmd struct {
	Model      string   `arg:"" help:"Model file (.stl, .3mf, .obj)." type:"path"`
	Recipe     string   `short:"r" required:"" help:"Recipe name (see: bambu recipe list)."`
	Filament   string   `short:"f" help:"Filament preset to use instead of the recipe's (e.g. \"Generic PLA\", \"Bambu PLA Matte\")."`
	Set        []string `short:"s" help:"Override a Bambu Studio setting: key=value (repeatable)." placeholder:"KEY=VALUE"`
	Name       string   `short:"n" help:"Output base name (default <model>-<recipe>)."`
	Out        string   `short:"o" help:"Output directory (default: config output_dir, else current dir)." type:"path"`
	AutoOrient bool     `name:"auto-orient" help:"Let the slicer re-orient the model (default: keep the model's orientation)."`
}

// Run executes the command.
func (c *SliceCmd) Run(g *Globals) error {
	p, err := g.Target()
	if err != nil {
		return err
	}
	m, err := model(p)
	if err != nil {
		return err
	}
	r, err := recipe.Get(g.RecipesDir(), c.Recipe)
	if err != nil {
		return err
	}
	st, ix, err := g.Studio()
	if err != nil {
		return err
	}
	cfg, _ := g.ConfigFile()
	name := c.Name
	if name == "" {
		stem := strings.TrimSuffix(filepath.Base(c.Model), filepath.Ext(c.Model))
		name = stem + "-" + r.Name
	}
	name = slicer.SafeName(name)
	if name == "" {
		return errfmt.New(errfmt.ExitUsage, "invalid --name").WithHint("use letters, digits, . _ -")
	}
	out := c.Out
	if out == "" && cfg.OutputDir != "" {
		out = expand(cfg.OutputDir)
	}
	if out == "" {
		out = "."
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	g.Out.Hint("slicing %s with %s for %s (%s %s mm)…", filepath.Base(c.Model), r.Name, p.Name, m.Alias, p.NozzleString())
	st.DetectVersion(g.Ctx)
	sum, err := slicer.Run(g.Ctx, st, ix, slicer.Request{
		Model: c.Model, Recipe: r, MachinePreset: m.MachinePreset(p.NozzleString()), Plate: p.Plate,
		Filament: c.Filament, Sets: c.Set, Name: name, OutDir: out,
		WorkDir: filepath.Join(cache, "bambu", "slice", name), AutoOrient: c.AutoOrient,
	})
	if err != nil {
		return err
	}
	return g.Out.Print(output.View{
		Data:  sum,
		Human: func(h *output.Human) { humanSlice(h, sum) },
		Plain: [][]string{
			{"name", "gcode_3mf", "time_s", "weight_g", "layers", "filament_id", "bed_type"},
			{sum.Name, sum.Gcode3MF, strconv.Itoa(sum.TimeS), fmt.Sprintf("%.2f", sum.WeightG), strconv.Itoa(sum.Layers), firstFilamentID(sum), sum.BedType},
		},
		Quiet: sum.Gcode3MF,
	})
}

func firstFilamentID(s *slicer.Summary) string {
	if len(s.Filaments) > 0 {
		return s.Filaments[0].FilamentID
	}
	return ""
}

func humanSlice(h *output.Human, s *slicer.Summary) {
	f := struct{ Type, ID string }{}
	if len(s.Filaments) > 0 {
		f.Type, f.ID = s.Filaments[0].Type, s.Filaments[0].FilamentID
	}
	h.Line("%s  %s", h.Good("sliced"), h.Bold(s.Name))
	h.Line("  time      %s  (%d s)", s.Time, s.TimeS)
	h.Line("  filament  %.2f g %s (%s)   layers %d", s.WeightG, f.Type, f.ID, s.Layers)
	h.Line("  temps     bed %s C (%s)   nozzle %s C, first layer %s C", s.BedTemp, s.BedType, s.NozzleTemp, s.NozzleFirst)
	h.Line("  presets   %s | %s | %s", s.Presets["machine"], s.Presets["process"], s.Presets["filament"])
	keys := make([]string, 0, len(s.Settings))
	for k := range s.Settings {
		if k != "nozzle_temperature" && k != "nozzle_temperature_initial_layer" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, flat(s.Settings[k])))
	}
	h.Line("  settings  %s", strings.Join(parts, ", "))
	for _, n := range s.Notes {
		h.Line("  %s  %s", h.Warn("note"), n)
	}
	for _, w := range s.Warnings {
		h.Line("  %s  %s", h.Warn("warning"), w)
	}
	h.Line("  3mf       %s", s.Gcode3MF)
	h.Line("  preview   %s", s.PreviewPNG)
	h.Line("  summary   %s", s.SummaryJSON)
}

func flat(v any) string {
	if a, ok := v.([]any); ok {
		parts := make([]string, len(a))
		for i, x := range a {
			parts[i] = fmt.Sprint(x)
		}
		if len(parts) > 1 && allSame(parts) {
			return parts[0]
		}
		return strings.Join(parts, "/")
	}
	return fmt.Sprint(v)
}

func allSame(p []string) bool {
	for _, x := range p {
		if x != p[0] {
			return false
		}
	}
	return true
}
