// Package recipe provides slicing recipes: embedded defaults plus user recipes from a directory.
//
// A recipe names presets generically ("0.20mm Standard", "Bambu PLA Basic"); the slicer resolves the concrete
// system preset for the target printer model and nozzle. Override values may be a string (broadcast to array
// settings) or a list of strings.
package recipe

import (
	"embed"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/voska/bambu/internal/errfmt"
)

//go:embed recipes/*.json
var builtin embed.FS

// Recipe is a named set of presets and overrides.
type Recipe struct {
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	Intent            string         `json:"intent,omitempty"`
	Material          string         `json:"material,omitempty"`
	Process           string         `json:"process"`
	Filament          string         `json:"filament"`
	ProcessOverrides  map[string]any `json:"process_overrides,omitempty"`
	FilamentOverrides map[string]any `json:"filament_overrides,omitempty"`
	Source            string         `json:"source"` // "builtin" or the user file path
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// All returns built-in recipes overlaid with user recipes from dir (user wins on name clash).
func All(dir string) (map[string]Recipe, error) {
	out := map[string]Recipe{}
	entries, _ := builtin.ReadDir("recipes")
	for _, e := range entries {
		b, _ := builtin.ReadFile("recipes/" + e.Name())
		r, err := parse(b, strings.TrimSuffix(e.Name(), ".json"), "builtin")
		if err != nil {
			return nil, err
		}
		out[r.Name] = r
	}
	if dir == "" {
		return out, nil
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // user recipes dir
		if err != nil {
			return nil, errfmt.Wrap(errfmt.ExitConfig, err, "read recipe %s", f)
		}
		r, err := parse(b, strings.TrimSuffix(filepath.Base(f), ".json"), f)
		if err != nil {
			return nil, err
		}
		out[r.Name] = r
	}
	return out, nil
}

func parse(b []byte, name, source string) (Recipe, error) {
	var r Recipe
	if err := json.Unmarshal(b, &r); err != nil {
		return r, errfmt.Wrap(errfmt.ExitConfig, err, "parse recipe %s", source)
	}
	r.Name, r.Source = name, source
	if !nameRe.MatchString(name) {
		return r, errfmt.New(errfmt.ExitConfig, "invalid recipe name %q (file %s)", name, source).WithHint("use lowercase letters, digits, - or _")
	}
	if r.Process == "" || r.Filament == "" {
		return r, errfmt.New(errfmt.ExitConfig, "recipe %s needs \"process\" and \"filament\"", source)
	}
	return r, nil
}

// Get returns one recipe.
func Get(dir, name string) (Recipe, error) {
	all, err := All(dir)
	if err != nil {
		return Recipe{}, err
	}
	r, ok := all[name]
	if !ok {
		return Recipe{}, errfmt.New(errfmt.ExitNotFound, "unknown recipe %q", name).WithHint("available: %s", strings.Join(Names(all), ", "))
	}
	return r, nil
}

// Names returns sorted recipe names.
func Names(all map[string]Recipe) []string {
	out := make([]string, 0, len(all))
	for n := range all {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
