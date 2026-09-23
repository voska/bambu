package cmd

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/voska/bambu/internal/camera"
	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/output"
	"github.com/voska/bambu/internal/printer"
)

// CameraCmd groups camera commands.
type CameraCmd struct {
	Snapshot SnapshotCmd `cmd:"" help:"Save one camera frame as JPEG (X1/H2/P2S series; needs ffmpeg and LAN Mode Liveview)."`
}

// SnapshotCmd grabs one frame.
type SnapshotCmd struct {
	Output string `short:"o" help:"Output .jpg (default ./<printer>-<time>.jpg)." type:"path"`
}

// Run executes the command.
func (c *SnapshotCmd) Run(g *Globals) error {
	p, err := g.Target()
	if err != nil {
		return err
	}
	m, err := model(p)
	if err != nil {
		return err
	}
	if m.Camera != printer.CameraRTSPS {
		return errfmt.New(errfmt.ExitUsage, "camera snapshots are not supported for %s yet", m.PrinterModel).
			WithHint("P1/A1-series printers use a different (port 6000) camera protocol; contributions welcome")
	}
	code, _, err := g.Code(p)
	if err != nil {
		return err
	}
	out := c.Output
	if out == "" {
		out = fmt.Sprintf("%s-%s.jpg", p.Name, time.Now().Format("20060102-150405"))
	}
	out, _ = filepath.Abs(out)
	if err := camera.Snapshot(g.Ctx, p.Host, code, out); err != nil {
		return err
	}
	return g.Out.Print(output.View{
		Data:  map[string]any{"printer": p.Name, "snapshot": out},
		Human: func(h *output.Human) { h.Line("%s", out) },
		Plain: [][]string{{"snapshot"}, {out}},
		Quiet: out,
	})
}
