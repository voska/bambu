package recipe

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuiltinNoBrim(t *testing.T) {
	all, err := All("")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"prototype-pla", "solid-pla", "functional-pla", "functional-petg"} {
		r, ok := all[n]
		if !ok {
			t.Fatalf("missing %s", n)
		}
		if r.ProcessOverrides["brim_type"] != "no_brim" {
			t.Errorf("%s: brims must be opt-in", n)
		}
		if r.Source != "builtin" {
			t.Errorf("%s source %s", n, r.Source)
		}
	}
}

func TestUserOverride(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "solid-pla.json"), []byte(`{"description":"mine","process":"0.16mm Optimal","filament":"Generic PLA"}`), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "my-asa.json"), []byte(`{"process":"0.20mm Strength","filament":"Bambu ASA"}`), 0o600)
	r, err := Get(dir, "solid-pla")
	if err != nil || r.Description != "mine" {
		t.Fatalf("override: %+v %v", r, err)
	}
	if _, err := Get(dir, "my-asa"); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(dir, "nope"); err == nil {
		t.Fatal("unknown recipe accepted")
	}
	_ = os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{"process":""}`), 0o600)
	if _, err := All(dir); err == nil {
		t.Fatal("invalid recipe accepted")
	}
}
