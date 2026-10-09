package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/voska/bambu/internal/auth"
	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/notify"
	"github.com/voska/bambu/internal/printer"
)

// MonitorCmd follows a job until it ends.
type MonitorCmd struct {
	SnapshotAtLayer int           `name:"snapshot-at-layer" help:"Save one camera frame when this layer is reached (0 = off)."`
	SnapshotDir     string        `name:"snapshot-dir" default:"." help:"Directory for snapshots." type:"path"`
	Exec            string        `help:"Shell command run on events (snapshot, error, final) with BAMBU_* env vars."`
	Timeout         time.Duration `help:"Give up after this long (exit 14 if the job is still active). 0 = no limit."`
	StartGrace      time.Duration `name:"start-grace" default:"120s" help:"If no job is active, how long to wait for one to start."`
	Light           bool          `help:"Turn the chamber light on for the snapshot, then restore it."`
	Notify          bool          `help:"Send error, snapshot and final events to [ntfy] url/topic in config."`
	Watch           bool          `help:"Continue across pause, finish and failure until interrupted or --timeout."`
	Interval        time.Duration `default:"5s" help:"Status check interval (MQTT updates also trigger checks)."`
	PushallEvery    time.Duration `name:"pushall-every" default:"5m" help:"Full status refresh interval."`
}

// Run executes the command. Exit: 0 FINISH, 12 FAILED, 13 PAUSE, 14 timeout.
func (c *MonitorCmd) Run(g *Globals) error {
	if c.Interval <= 0 || c.PushallEvery <= 0 || c.Timeout < 0 || c.StartGrace < 0 || c.SnapshotAtLayer < 0 {
		return errfmt.New(errfmt.ExitUsage, "monitor intervals must be positive; timeout, start-grace and snapshot layer must not be negative")
	}
	if c.Notify {
		cfg, err := g.ConfigFile()
		if err != nil {
			return err
		}
		if cfg.Ntfy.URL == "" || cfg.Ntfy.Topic == "" {
			g.Out.Warn("--notify ignored: [ntfy] url/topic not set in config")
			c.Notify = false
		} else if err := notify.Validate(cfg.Ntfy); err != nil {
			return errfmt.Wrap(errfmt.ExitConfig, err, "invalid ntfy configuration")
		}
	}
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
	snapshot := ""
	last := ""
	lastBlocking := false
	lastTerminal := ""
	var lastFaults []string
	refresh := time.NewTicker(c.PushallEvery)
	defer refresh.Stop()
	tick := time.NewTicker(c.Interval)
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
		faults := blockingHMS(s, nil)
		if s.Errors.PrintError != "" {
			faults = append(faults, s.Errors.PrintError)
		}
		newFault := false
		for _, fault := range faults {
			newFault = newFault || !slices.Contains(lastFaults, fault)
		}
		if newFault {
			c.notification(g, p.Name, "error", s, "")
		}
		if s.Errors.Blocking && !lastBlocking {
			c.hook(g, "error", s, "")
		}
		lastBlocking = s.Errors.Blocking
		lastFaults = faults
		if c.SnapshotAtLayer > 0 && !snapTaken && s.State == "RUNNING" && s.Job.Layer >= c.SnapshotAtLayer {
			snapTaken = true // one attempt; a failure is reported, not retried
			if m, _ := model(p); m.Camera != printer.CameraRTSPS {
				ev := Event{TS: time.Now().Format(time.RFC3339), Event: "message", Message: "snapshot skipped: camera not supported for " + p.Model}
				_ = g.Out.Event(ev, ev.human())
			} else {
				out := filepath.Join(c.SnapshotDir, fmt.Sprintf("%s-layer%d-%s.jpg", safe(s.Job.Name), s.Job.Layer, time.Now().Format("150405")))
				if _, err := capture(g, p.Host, code, out, c.Light, conn); err != nil {
					ev := Event{TS: time.Now().Format(time.RFC3339), Event: "message", Message: "snapshot failed: " + errfmt.As(err).Message}
					_ = g.Out.Event(ev, ev.human())
				} else {
					abs, _ := filepath.Abs(out)
					snapshot = abs
					ev := newEvent("snapshot", s)
					ev.Snapshot = abs
					_ = g.Out.Event(ev, ev.human())
					c.hook(g, "snapshot", s, abs)
					c.notification(g, p.Name, "snapshot", s, abs)
				}
			}
		}
		idleTooLong := !seenActive && time.Since(start) > c.StartGrace
		terminal := s.State == "FINISH" || s.State == "FAILED" || s.State == "PAUSE"
		if c.Watch {
			if terminal {
				key := s.State + "|" + s.Job.Name
				if key != lastTerminal {
					c.final(g, p.Name, s, snapshot, monitorExit(s, false), false, true)
					lastTerminal = key
				}
			} else {
				lastTerminal = ""
			}
		}
		if !c.Watch && ((seenActive && (s.State == "FINISH" || s.State == "FAILED" || s.State == "PAUSE")) || idleTooLong) {
			break
		}
		select {
		case <-ctx.Done():
			if g.Ctx.Err() != nil { // interrupted by the user
				c.final(g, p.Name, printer.Summarize(conn.State()), snapshot, 0, true, false)
				return nil
			}
			exit := monitorExit(final, true)
			c.final(g, p.Name, final, snapshot, exit, false, lastTerminal == "")
			if exit != 0 {
				return errfmt.Exit(exit)
			}
			return nil
		case <-conn.Updates():
		case <-tick.C:
		case <-refresh.C:
			_, _ = conn.Pushall(ctx)
		}
	}
	exit := monitorExit(final, false)
	c.final(g, p.Name, final, snapshot, exit, false, true)
	if exit != 0 {
		return errfmt.Exit(exit)
	}
	return nil
}

func monitorExit(s printer.Status, timeout bool) int {
	switch s.State {
	case "FAILED":
		return errfmt.ExitPrintFailed
	case "PAUSE":
		return errfmt.ExitPrintPaused
	}
	if timeout && printer.ActiveStates[s.State] {
		return errfmt.ExitTimeout
	}
	return 0
}

func (c *MonitorCmd) final(g *Globals, name string, s printer.Status, snapshot string, exit int, interrupted, notifyFinal bool) {
	if notifyFinal && (s.State == "FINISH" || s.State == "FAILED" || s.State == "PAUSE") {
		c.notification(g, name, "final", s, "")
	}
	ev := newEvent("final", s)
	ev.Status, ev.Snapshot, ev.ExitCode = &s, snapshot, &exit
	ev.Interrupted = interrupted
	_ = g.Out.Event(ev, ev.human())
	if !interrupted {
		c.hook(g, "final", s, snapshot)
	}
}

func (c *MonitorCmd) notification(g *Globals, name, kind string, s printer.Status, snapshot string) {
	if !c.Notify {
		return
	}
	cfg, err := g.ConfigFile()
	if err != nil {
		g.Out.Warn("ntfy config failed")
		return
	}
	title, message, tags, priority := name+": "+s.State, s.Job.Name+": "+s.State+" ("+s.Stage+")", "white_check_mark", 3
	switch kind {
	case "error":
		title, message, tags, priority = name+": printer error", s.Job.Name+": "+strings.Join(blockingHMS(s, nil), ", ")+" print_error="+s.Errors.PrintError, "warning", 4
	case "snapshot":
		title, message, tags = fmt.Sprintf("%s: layer %d check", name, s.Job.Layer), fmt.Sprintf("%s layer %d/%d", s.Job.Name, s.Job.Layer, s.Job.TotalLayers), "camera"
	default:
		if s.State != "FINISH" {
			tags, priority = "warning", 5
		}
	}
	if err := notify.Send(g.Ctx, cfg.Ntfy, auth.NtfyToken(g.Keyring), title, message, tags, priority, snapshot); err != nil {
		g.Out.Warn("ntfy failed: %v", err)
		ev := Event{TS: time.Now().Format(time.RFC3339), Event: "message", Message: "ntfy failed: " + err.Error()}
		_ = g.Out.Event(ev, ev.human())
	}
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
