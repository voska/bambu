package cmd

import (
	"context"
	"errors"
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
	Light  bool   `help:"Turn the chamber light on for the frame, then restore its reported mode."`
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
	light, err := capture(g, p.Host, code, out, c.Light, nil)
	if err != nil {
		return err
	}
	data := map[string]any{"printer": p.Name, "snapshot": out}
	if light != nil {
		data["light"] = light
	}
	return g.Out.Print(output.View{
		Data:  data,
		Human: func(h *output.Human) { h.Line("%s", out) },
		Plain: [][]string{{"snapshot"}, {out}},
		Quiet: out,
	})
}

// capture shares the same light restoration path for camera and monitor frames.
func capture(g *Globals, host, code, out string, lit bool, conn printer.Conn) (light map[string]string, err error) {
	if !lit {
		return nil, camera.Snapshot(g.Ctx, host, code, out)
	}
	if conn == nil {
		p, targetErr := g.Target()
		if targetErr != nil {
			return nil, targetErr
		}
		conn, err = g.Connect(p)
		if err != nil {
			return nil, err
		}
		defer conn.Close()
	}
	before, err := readLight(g.Ctx, conn)
	if err != nil {
		return nil, err
	}
	light = map[string]string{"node": chamberLight, "before": before, "during": "on", "after": before}
	if before != "on" {
		// Cleanup also runs if switching on failed after publishing, or capture was interrupted.
		defer func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(g.Ctx), 15*time.Second)
			defer cancel()
			r, restoreErr := setLight(ctx, conn, before, 10*time.Second)
			light["after"] = r.After
			if restoreErr != nil {
				err = errfmt.Wrap(errfmt.As(restoreErr).Code, errors.Join(err, restoreErr), "could not restore chamber light to %s", before).
					WithData("light", light).WithData("snapshot", out).WithHint("check `bambu light status` and restore the light explicitly")
			}
		}()
		if _, err = setLight(g.Ctx, conn, "on", 10*time.Second); err != nil {
			return light, err
		}
	}
	return light, camera.Snapshot(g.Ctx, host, code, out)
}
