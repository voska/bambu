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

func TestMultiModelArrange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "studio.sh")
	_ = os.WriteFile(bin, []byte(fakeStudio), 0o755)
	t.Setenv("FAKE_3MF", testutil.Write(t, dir, "prepared", testutil.Opts{Objects: []string{"one.stl", "two.stl", "three.stl"}}))
	t.Setenv("FAKE_ARGS", filepath.Join(dir, "args"))
	r := req(recipe.Recipe{Name: "t", Process: "0.20mm Standard", Filament: "Bambu PLA Basic"})
	r.Name, r.OutDir, r.WorkDir = "batch", filepath.Join(dir, "out"), filepath.Join(dir, "work")
	for _, name := range []string{"one.stl", "two.stl", "three.stl"} {
		path := filepath.Join(dir, name)
		_ = os.WriteFile(path, []byte("solid x\nendsolid x\n"), 0o600)
		r.Models = append(r.Models, path)
	}
	sum, err := Run(context.Background(), &Studio{Binary: bin}, index(t), r)
	if err != nil {
		t.Fatal(err)
	}
	args := readArgs(t)
	if !strings.Contains(args, "--arrange\n0\n") || !strings.Contains(args, "arranged.3mf") || sum.PartSpacingMM < 3 || len(sum.Models) != 3 || sum.AutoOrient {
		t.Fatalf("summary=%+v args=%s", sum, args)
	}
	r.PartSpacingMM = 2
	if _, err := Run(context.Background(), &Studio{Binary: bin}, index(t), r); errfmt.As(err).Code != errfmt.ExitUsage {
		t.Fatalf("unsafe spacing: %v", err)
	}
	r.PartSpacingMM = 3
	r.Models = append(r.Models, filepath.Join(dir, "missing.stl"))
	if _, err := Run(context.Background(), &Studio{Binary: bin}, index(t), r); errfmt.As(err).Code != errfmt.ExitNotFound {
		t.Fatalf("missing input: %v", err)
	}
}
