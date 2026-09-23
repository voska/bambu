package slicer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/voska/bambu/internal/errfmt"
)

// Kinds of presets.
const (
	KindMachine  = "machine"
	KindProcess  = "process"
	KindFilament = "filament"
)

// Index is Bambu Studio's BBL vendor profile set (resources/profiles/BBL.json + BBL/<kind>/*.json).
type Index struct {
	dir   string                       // resources/profiles
	lists map[string]map[string]string // kind -> preset name -> sub_path
	mu    sync.Mutex
	cache map[string]map[string]any // "kind/name" -> raw json
}

// LoadIndex reads profiles/BBL.json under resources.
func LoadIndex(resources string) (*Index, error) {
	dir := filepath.Join(resources, "profiles")
	b, err := os.ReadFile(filepath.Join(dir, "BBL.json")) //nolint:gosec // slicer resources path
	if err != nil {
		return nil, errfmt.Wrap(errfmt.ExitConfig, err, "read Bambu Studio vendor profile index").
			WithHint("set [slicer] resources in config to the Bambu Studio resources dir (contains profiles/BBL.json)")
	}
	var vendor map[string]json.RawMessage
	if err := json.Unmarshal(b, &vendor); err != nil {
		return nil, errfmt.Wrap(errfmt.ExitConfig, err, "parse profiles/BBL.json")
	}
	ix := &Index{dir: dir, lists: map[string]map[string]string{}, cache: map[string]map[string]any{}}
	for _, kind := range []string{KindMachine, KindProcess, KindFilament} {
		var entries []struct {
			Name    string `json:"name"`
			SubPath string `json:"sub_path"`
		}
		_ = json.Unmarshal(vendor[kind+"_list"], &entries)
		m := map[string]string{}
		for _, e := range entries {
			m[e.Name] = e.SubPath
		}
		ix.lists[kind] = m
	}
	return ix, nil
}

func (ix *Index) raw(kind, name string) (map[string]any, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	key := kind + "/" + name
	if v, ok := ix.cache[key]; ok {
		return v, nil
	}
	sub, ok := ix.lists[kind][name]
	if !ok {
		return nil, errfmt.New(errfmt.ExitNotFound, "%s preset not found: %q", kind, name).
			WithHint("check the exact name in Bambu Studio (profiles/BBL/%s/)", kind)
	}
	b, err := os.ReadFile(filepath.Join(ix.dir, "BBL", filepath.FromSlash(sub))) //nolint:gosec // path from vendor index
	if err != nil {
		return nil, errfmt.Wrap(errfmt.ExitConfig, err, "read preset %q", name)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, errfmt.Wrap(errfmt.ExitConfig, err, "parse preset %q", name)
	}
	ix.cache[key] = m
	return m, nil
}

// Flatten resolves a system preset into a full config the Bambu Studio CLI accepts.
//
// The CLI does not resolve `inherits`/`include` in --load-settings/--load-filaments files: unflattened presets
// "slice" with a generic 200x200x100 bed, generic start G-code and zero filament density. The resolution order
// mirrors PresetBundle::load_vendor_configs_from_json: resolved parent, then each include (minus identity keys),
// then the preset's own keys.
func (ix *Index) Flatten(kind, name string) (map[string]any, error) {
	out, err := ix.resolve(kind, name, nil)
	if err != nil {
		return nil, err
	}
	delete(out, "inherits")
	delete(out, "include")
	delete(out, "instantiation")
	out["from"] = "system"
	return out, nil
}

func (ix *Index) resolve(kind, name string, seen []string) (map[string]any, error) {
	for _, s := range seen {
		if s == name {
			return nil, errfmt.New(errfmt.ExitConfig, "preset inheritance loop at %q", name)
		}
	}
	own, err := ix.raw(kind, name)
	if err != nil {
		return nil, err
	}
	seen = append(seen, name)
	out := map[string]any{}
	if parent, _ := own["inherits"].(string); parent != "" {
		p, err := ix.resolve(kind, parent, seen)
		if err != nil {
			return nil, err
		}
		for k, v := range p {
			out[k] = v
		}
	}
	for _, inc := range includes(own["include"]) {
		p, err := ix.resolve(kind, inc, seen)
		if err != nil {
			return nil, err
		}
		for k, v := range p {
			switch k {
			case "name", "type", "from", "setting_id":
			default:
				out[k] = v
			}
		}
	}
	for k, v := range own {
		out[k] = v
	}
	return out, nil
}

func includes(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Find resolves a generic preset name ("0.20mm Standard", "Bambu PLA Basic") to the concrete system preset
// compatible with machine ("Bambu Lab X1 Carbon 0.4 nozzle"). A name containing " @" is used as-is.
// Among compatible candidates an exact name wins, then the shortest name.
func (ix *Index) Find(kind, base, machine string) (string, error) {
	var cands []string
	for name := range ix.lists[kind] {
		if name == base || strings.HasPrefix(name, base+" @") {
			cands = append(cands, name)
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if (cands[i] == base) != (cands[j] == base) {
			return cands[i] == base
		}
		if len(cands[i]) != len(cands[j]) {
			return len(cands[i]) < len(cands[j])
		}
		return cands[i] < cands[j]
	})
	var incompatible []string
	for _, name := range cands {
		raw, err := ix.raw(kind, name)
		if err != nil {
			return "", err
		}
		if inst, _ := raw["instantiation"].(string); inst == "false" {
			continue
		}
		flat, err := ix.Flatten(kind, name)
		if err != nil {
			return "", err
		}
		if Compatible(flat, machine) {
			return name, nil
		}
		incompatible = append(incompatible, name)
	}
	if len(cands) == 0 {
		return "", errfmt.New(errfmt.ExitNotFound, "no %s preset named %q", kind, base).
			WithHint("use a Bambu Studio system preset name, e.g. \"Bambu PLA Basic\" or \"Generic PLA\"")
	}
	return "", errfmt.New(errfmt.ExitNotFound, "no %s preset %q is compatible with %s", kind, base, machine).
		WithHint("candidates: %s", strings.Join(incompatible, "; "))
}

// Compatible reports whether a flattened preset lists machine in compatible_printers (empty list = any).
func Compatible(flat map[string]any, machine string) bool {
	list, _ := flat["compatible_printers"].([]any)
	if len(list) == 0 {
		return true
	}
	for _, x := range list {
		if x == machine {
			return true
		}
	}
	return false
}

// Names lists instantiable preset names of a kind (for discovery / errors).
func (ix *Index) Names(kind string) []string {
	var out []string
	for n := range ix.lists[kind] {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// String form helper for error messages.
func (ix *Index) String() string { return fmt.Sprintf("profiles at %s", ix.dir) }
