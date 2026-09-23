package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/voska/bambu/internal/camera"
	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/printer"
)

// MonitorCmd follows a job until it ends.
type MonitorCmd struct {
	SnapshotAtLayer int           `name:"snapshot-at-layer" help:"Save one camera frame when this layer is reached (0 = off)."`
	SnapshotDir     string        `name:"snapshot-dir" default:"." help:"Directory for snapshots." type:"path"`
	Exec            string        `help:"Shell command run on events (snapshot, error, final) with BAMBU_* env vars."`
	Timeout         time.Duration `help:"Give up after this long (exit 14 if the job is still active). 0 = no limit."`
	StartGrace      time.Duration `name:"start-grace" default:"120s" help:"If no job is active, how long to wait for one to start."`
}

// Run executes the command. Exit: 0 FINISH, 12 FAILED, 13 PAUSE, 14 timeout.
func (c *MonitorCmd) Run(g *Globals) error {
	p, err := g.Target()
	if err != nil {
		return err
	}
	code, _, err := g.Code(p)
	if err != nil {
		return err
	}
	conn, err := g.Dial(g.Ctx, p.Host, p.Serial, code)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Pushall(g.Ctx); err != nil {
		return err
	}
	ctx := g.Ctx
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	start := time.Now()
	seenActive := false
	snapTaken := false
	last, lastBlocking := "", false
	refresh := time.NewTicker(5 * time.Minute)
	defer refresh.Stop()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	var final printer.Status
	for {
		s := printer.Summarize(conn.State())
		final = s
		seenActive = seenActive || printer.ActiveStates[s.State]
		if k := changeKey(s); k != last {
			last = k
			ev := newEvent("status", s)
			if err := g.Out.Event(ev, ev.human()); err != nil {
				return err
			}
		}
		if s.Errors.Blocking && !lastBlocking {
			c.hook(g, "error", s, "")
		}
		lastBlocking = s.Errors.Blocking
		if c.SnapshotAtLayer > 0 && !snapTaken && s.State == "RUNNING" && s.Job.Layer >= c.SnapshotAtLayer {
			snapTaken = true // one attempt; a failure is reported, not retried
			if m, _ := model(p); m.Camera != printer.CameraRTSPS {
				ev := Event{TS: time.Now().Format(time.RFC3339), Event: "message", Message: "snapshot skipped: camera not supported for " + p.Model}
				_ = g.Out.Event(ev, ev.human())
			} else {
				out := filepath.Join(c.SnapshotDir, fmt.Sprintf("%s-layer%d-%s.jpg", safe(s.Job.Name), s.Job.Layer, time.Now().Format("150405")))
				if err := camera.Snapshot(g.Ctx, p.Host, code, out); err != nil {
					ev := Event{TS: time.Now().Format(time.RFC3339), Event: "message", Message: "snapshot failed: " + errfmt.As(err).Message}
					_ = g.Out.Event(ev, ev.human())
				} else {
					abs, _ := filepath.Abs(out)
					ev := newEvent("snapshot", s)
					ev.Snapshot = abs
					_ = g.Out.Event(ev, ev.human())
					c.hook(g, "snapshot", s, abs)
				}
			}
		}
		idleTooLong := !seenActive && time.Since(start) > c.StartGrace
		if (seenActive && (s.State == "FINISH" || s.State == "FAILED" || s.State == "PAUSE")) || idleTooLong {
			break
		}
		select {
		case <-ctx.Done():
			if g.Ctx.Err() != nil { // interrupted by the user
				return nil
			}
			ev := newEvent("final", final)
			_ = g.Out.Event(ev, ev.human())
			c.hook(g, "final", final, "")
			if printer.ActiveStates[final.State] {
				return errfmt.Exit(errfmt.ExitTimeout)
			}
			return nil
		case <-conn.Updates():
		case <-tick.C:
		case <-refresh.C:
			_, _ = conn.Pushall(ctx)
		}
	}
	ev := newEvent("final", final)
	_ = g.Out.Event(ev, ev.human())
	c.hook(g, "final", final, "")
	switch final.State {
	case "FAILED":
		return errfmt.Exit(errfmt.ExitPrintFailed)
	case "PAUSE":
		return errfmt.Exit(errfmt.ExitPrintPaused)
	}
	return nil
}

func (c *MonitorCmd) hook(g *Globals, event string, s printer.Status, snapshot string) {
	if c.Exec == "" {
		return
	}
	shell, flag := "/bin/sh", "-c"
	if runtime.GOOS == "windows" {
		shell, flag = "cmd", "/C"
	}
	ctx, cancel := context.WithTimeout(g.Ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, flag, c.Exec) //nolint:gosec // user-supplied hook, by design
	cmd.Env = append(os.Environ(),
		"BAMBU_EVENT="+event, "BAMBU_STATE="+s.State, "BAMBU_STAGE="+s.Stage, "BAMBU_JOB="+s.Job.Name,
		"BAMBU_LAYER="+strconv.Itoa(s.Job.Layer), "BAMBU_TOTAL_LAYERS="+strconv.Itoa(s.Job.TotalLayers),
		"BAMBU_PERCENT="+strconv.Itoa(s.Job.Percent), "BAMBU_SNAPSHOT="+snapshot, "BAMBU_BLOCKING="+strconv.FormatBool(s.Errors.Blocking))
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr // never pollute stdout (NDJSON)
	if err := cmd.Run(); err != nil {
		g.Out.Warn("--exec hook failed (%s): %v", event, err)
	}
}

func safe(s string) string {
	if s == "" {
		return "job"
	}
	out := []rune{}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			out = append(out, r)
		} else {
			out = append(out, '-')
		}
	}
	return string(out)
}
