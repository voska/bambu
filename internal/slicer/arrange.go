package slicer

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/voska/bambu/internal/errfmt"
)

// Studio exposes object spacing only for sequential arrangement. Use that clearance
// in an arrange-only project, then slice its placements with the original presets.
// No sequential-print settings or reduced head clearances reach the sliced output.
func arrangeModels(ctx context.Context, st *Studio, req Request, cfgs *Configs, paths map[string]string, filPaths, sources []string, work string) (string, error) {
	machine, process := maps.Clone(cfgs.Machine), maps.Clone(cfgs.Process)
	machine["extruder_clearance_max_radius"] = str(req.PartSpacingMM + .01)
	process["print_sequence"] = "by object"
	for kind, data := range map[string]map[string]any{"arrange-machine": machine, "arrange-process": process} {
		b, _ := json.MarshalIndent(data, "", " ")
		paths[kind] = filepath.Join(work, kind+".json")
		if err := os.WriteFile(paths[kind], b, 0o600); err != nil {
			return "", errfmt.Wrap(errfmt.ExitConfig, err, "write arrangement config")
		}
	}
	arranged := filepath.Join(work, "arranged.3mf")
	if err := os.Remove(arranged); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", errfmt.Wrap(errfmt.ExitConfig, err, "remove stale arrangement")
	}
	orient := "0"
	if req.AutoOrient {
		orient = "1"
	}
	args := []string{
		"--arrange", "1", "--orient", orient, "--allow-rotations=0", "--avoid-extrusion-cali-region",
		"--load-settings", paths["arrange-machine"] + ";" + paths["arrange-process"], "--load-filaments", strings.Join(filPaths, ";"),
		"--outputdir", work, "--export-3mf", "arranged.3mf",
	}
	args = append(args, sources...)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, st.Binary, args...) //nolint:gosec // discovered slicer, validated file paths
	cmd.Dir = work
	data, err := cmd.CombinedOutput()
	log := filepath.Join(work, "arrange.log")
	_ = os.WriteFile(log, data, 0o600)
	if err != nil || !exists(arranged) {
		return "", errfmt.New(errfmt.ExitSliceFailed, "Studio arrangement failed").WithHint("split the batch or check input models; log: %s", log).WithData("log", log)
	}
	return arranged, nil
}
