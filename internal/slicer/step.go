package slicer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/voska/bambu/internal/errfmt"
)

func prepareSTEP(ctx context.Context, python, model, work string) (string, error) {
	if python == "" {
		python = "python3"
	}
	out := filepath.Join(work, "step-input.stl")
	if err := os.Remove(out); err != nil && !os.IsNotExist(err) {
		return "", errfmt.Wrap(errfmt.ExitConfig, err, "remove old STEP mesh")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	script := "import sys\nimport cadquery as cq\ncq.exporters.export(cq.importers.importStep(sys.argv[1]), sys.argv[2], tolerance=0.01, angularTolerance=0.1)\n"
	cmd := exec.CommandContext(ctx, python, "-c", script, model, out) //nolint:gosec // configured interpreter, fixed script, model passed as argv
	data, err := cmd.CombinedOutput()
	if err != nil {
		code, message := errfmt.ExitSliceFailed, "STEP tessellation failed"
		var execErr *exec.Error
		if errors.As(err, &execErr) || errors.Is(err, os.ErrNotExist) || strings.Contains(string(data), "No module named 'cadquery'") {
			code, message = errfmt.ExitConfig, "STEP input needs a Python interpreter with CadQuery"
		}
		return "", errfmt.New(code, "%s", message).WithHint("set [slicer] step_python to a CadQuery interpreter, or pass an STL")
	}
	info, err := os.Stat(out)
	if err != nil || info.Size() == 0 {
		return "", errfmt.New(errfmt.ExitSliceFailed, "STEP tessellation produced no mesh")
	}
	return out, nil
}
