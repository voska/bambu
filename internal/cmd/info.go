package cmd

import (
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/alecthomas/kong"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/output"
	"github.com/voska/bambu/internal/printer"
	"github.com/voska/bambu/internal/recipe"
	"github.com/voska/bambu/internal/slicer"
)

// RecipeCmd groups recipe commands.
type RecipeCmd struct {
	List RecipeListCmd `cmd:"" help:"List recipes (built-in and user)."`
	Show RecipeShowCmd `cmd:"" help:"Show one recipe."`
}

// RecipeListCmd lists recipes.
type RecipeListCmd struct{}

// Run executes the command.
func (c *RecipeListCmd) Run(g *Globals) error {
	all, err := recipe.All(g.RecipesDir())
	if err != nil {
		return err
	}
	list := []recipe.Recipe{}
	plain := [][]string{{"name", "material", "intent", "process", "filament", "source"}}
	t := [][]string{{"NAME", "MATERIAL", "INTENT", "DESCRIPTION"}}
	for _, n := range recipe.Names(all) {
		r := all[n]
		list = append(list, r)
		plain = append(plain, []string{r.Name, r.Material, r.Intent, r.Process, r.Filament, r.Source})
		t = append(t, []string{r.Name, r.Material, r.Intent, r.Description})
	}
	return g.Out.Print(output.View{
		Data: map[string]any{"recipes": list, "user_dir": g.RecipesDir()},
		Human: func(h *output.Human) {
			h.Table(t)
			h.Line("%s", h.Dim("user recipes: "+g.RecipesDir()+"/*.json (same name overrides a built-in); brims are opt-in: --set brim_type=outer_only"))
		},
		Plain: plain,
		Quiet: strings.Join(recipe.Names(all), "\n"),
	})
}

// RecipeShowCmd shows a recipe.
type RecipeShowCmd struct {
	Name string `arg:"" help:"Recipe name."`
}

// Run executes the command.
func (c *RecipeShowCmd) Run(g *Globals) error {
	r, err := recipe.Get(g.RecipesDir(), c.Name)
	if err != nil {
		return err
	}
	return g.Out.Print(output.View{Data: r, Plain: [][]string{{"name", "process", "filament"}, {r.Name, r.Process, r.Filament}}, Quiet: r.Name})
}

// SlicerCmd groups slicer commands.
type SlicerCmd struct {
	Info SlicerInfoCmd `cmd:"" help:"Show the discovered Bambu Studio and whether the printer's presets resolve."`
}

// SlicerInfoCmd reports slicer discovery.
type SlicerInfoCmd struct{}

// Run executes the command.
func (c *SlicerInfoCmd) Run(g *Globals) error {
	st, ix, err := g.Studio()
	if err != nil {
		return err
	}
	st.DetectVersion(g.Ctx)
	data := map[string]any{
		"studio": st, "machine_presets": len(ix.Names(slicer.KindMachine)),
		"process_presets": len(ix.Names(slicer.KindProcess)), "filament_presets": len(ix.Names(slicer.KindFilament)),
	}
	if p, err := g.Target(); err == nil {
		if m, err := model(p); err == nil {
			mp := m.MachinePreset(p.NozzleString())
			_, ferr := ix.Flatten(slicer.KindMachine, mp)
			data["printer"] = map[string]any{"name": p.Name, "machine_preset": mp, "resolves": ferr == nil}
		}
	}
	return g.Out.Print(output.View{
		Data: data,
		Human: func(h *output.Human) {
			h.Line("Bambu Studio %s", st.Version)
			h.Line("  binary     %s", st.Binary)
			h.Line("  resources  %s", st.Resources)
			h.Line("  presets    %d machine, %d process, %d filament", data["machine_presets"], data["process_presets"], data["filament_presets"])
			if pi, ok := data["printer"].(map[string]any); ok {
				h.Line("  printer    %s -> %s (resolves: %v)", pi["name"], pi["machine_preset"], pi["resolves"])
			}
		},
		Plain: [][]string{{"binary", "resources", "version"}, {st.Binary, st.Resources, st.Version}},
		Quiet: st.Binary,
	})
}

// ExitCodesCmd prints the exit code table.
type ExitCodesCmd struct{}

// Run executes the command.
func (c *ExitCodesCmd) Run(g *Globals) error {
	t := errfmt.Table()
	rows := [][]string{{"code", "name", "description"}}
	for _, e := range t {
		rows = append(rows, []string{strconv.Itoa(e.Code), e.Name, e.Description})
	}
	return g.Out.Print(output.View{Data: t, Human: func(h *output.Human) { h.Table(rows) }, Plain: rows})
}

// VersionCmd prints build info.
type VersionCmd struct{}

// Run executes the command.
func (c *VersionCmd) Run(g *Globals) error {
	data := map[string]any{
		"version": g.Build.Version, "commit": g.Build.Commit, "date": g.Build.Date,
		"go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
	}
	return g.Out.Print(output.View{
		Data: data,
		Human: func(h *output.Human) {
			h.Line("bambu %s (%s, %s) %s/%s", g.Build.Version, orDefault(g.Build.Commit, "unknown"), runtime.Version(), runtime.GOOS, runtime.GOARCH)
		},
		Plain: [][]string{{"version", "commit"}, {g.Build.Version, g.Build.Commit}},
		Quiet: g.Build.Version,
	})
}

// SchemaCmd dumps the CLI tree.
type SchemaCmd struct {
	Command []string `arg:"" optional:"" help:"Limit to a command path, e.g. \"print send\"."`
}

type schemaFlag struct {
	Name     string `json:"name"`
	Short    string `json:"short,omitempty"`
	Type     string `json:"type"`
	Help     string `json:"help"`
	Default  string `json:"default,omitempty"`
	Env      string `json:"env,omitempty"`
	Required bool   `json:"required,omitempty"`
	Enum     string `json:"enum,omitempty"`
}

type schemaArg struct {
	Name     string `json:"name"`
	Help     string `json:"help"`
	Required bool   `json:"required"`
}

type schemaNode struct {
	Name     string       `json:"name"`
	Path     string       `json:"path"`
	Help     string       `json:"help"`
	Args     []schemaArg  `json:"args,omitempty"`
	Flags    []schemaFlag `json:"flags,omitempty"`
	Commands []schemaNode `json:"commands,omitempty"`
}

// Run executes the command.
func (c *SchemaCmd) Run(g *Globals, k *kong.Kong) error {
	root := describe(k.Model.Node, "")
	root.Name, root.Path = "bambu", "bambu"
	data := map[string]any{
		"name": "bambu", "version": g.Build.Version, "exit_codes": errfmt.Table(),
		"models": printer.Models, "tree": root,
	}
	if len(c.Command) > 0 {
		want := strings.Join(c.Command, " ")
		n, ok := findNode(root, "bambu "+want)
		if !ok {
			return errfmt.New(errfmt.ExitNotFound, "no command %q", want).WithHint("run: bambu schema")
		}
		return g.Out.JSON(n)
	}
	return g.Out.JSON(data)
}

func describe(n *kong.Node, prefix string) schemaNode {
	path := strings.TrimSpace(prefix + " " + n.Name)
	s := schemaNode{Name: n.Name, Path: path, Help: n.Help}
	for _, a := range n.Positional {
		s.Args = append(s.Args, schemaArg{Name: a.Name, Help: a.Help, Required: a.Required})
	}
	for _, f := range n.Flags {
		if f.Hidden || f.Name == "help" {
			continue
		}
		sf := schemaFlag{Name: "--" + f.Name, Type: f.Value.Target.Type().String(), Help: f.Help, Default: f.Default, Required: f.Required, Enum: f.Enum}
		if f.Short != 0 {
			sf.Short = "-" + string(f.Short)
		}
		if len(f.Envs) > 0 {
			sf.Env = f.Envs[0]
		}
		s.Flags = append(s.Flags, sf)
	}
	for _, ch := range n.Children {
		if ch.Hidden {
			continue
		}
		s.Commands = append(s.Commands, describe(ch, path))
	}
	sort.Slice(s.Commands, func(i, j int) bool { return s.Commands[i].Name < s.Commands[j].Name })
	return s
}

func findNode(n schemaNode, path string) (schemaNode, bool) {
	if n.Path == path {
		return n, true
	}
	for _, c := range n.Commands {
		if r, ok := findNode(c, path); ok {
			return r, true
		}
	}
	return schemaNode{}, false
}
