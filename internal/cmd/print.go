package cmd

import (
	"context"
	"crypto/md5" //nolint:gosec // integrity check of the uploaded file
	"encoding/hex"
	"io"
	"os"
	"time"

	"github.com/voska/bambu/internal/config"
	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/ftps"
	"github.com/voska/bambu/internal/job"
	"github.com/voska/bambu/internal/output"
	"github.com/voska/bambu/internal/preflight"
	"github.com/voska/bambu/internal/printer"
)

// PreflightCmd runs the safety gates.
type PreflightCmd struct {
	File  string `arg:"" help:"Sliced .gcode.3mf." type:"path"`
	Slot  string `short:"s" required:"" help:"AMS slot: 1-4 (first AMS) or A1-D4."`
	NoFTP bool   `name:"no-ftp" help:"Skip the FTPS login check."`
}

// Run executes the command.
func (c *PreflightCmd) Run(g *Globals) error {
	pc, err := runPreflight(g, c.File, c.Slot, !c.NoFTP)
	if err != nil {
		return err
	}
	pc.conn.Close()
	if err := g.Out.Print(gatesView(pc)); err != nil {
		return err
	}
	if pc.result.Result != preflight.Pass {
		return errfmt.Exit(errfmt.ExitGate)
	}
	return nil
}

type preflightCtx struct {
	p      *config.Printer
	job    *job.Job
	status printer.Status
	result preflight.Result
	conn   printer.Conn
	code   string
}

func runPreflight(g *Globals, file, slot string, checkFTP bool) (*preflightCtx, error) {
	p, err := g.Target()
	if err != nil {
		return nil, err
	}
	tray, err := printer.ParseSlot(slot)
	if err != nil {
		return nil, errfmt.Wrap(errfmt.ExitUsage, err, "bad --slot")
	}
	j, err := job.Inspect(file, 1)
	if err != nil {
		return nil, err
	}
	code, _, err := g.Code(p)
	if err != nil {
		return nil, err
	}
	conn, err := g.Dial(g.Ctx, p.Host, p.Serial, code)
	if err != nil {
		return nil, err
	}
	raw, err := conn.Pushall(g.Ctx)
	if err != nil {
		conn.Close()
		return nil, err
	}
	in := preflight.Input{Printer: p, Job: j, Status: printer.Summarize(raw), TrayID: tray}
	if checkFTP {
		in.FTPSCheck = func() error {
			ctx, cancel := context.WithTimeout(g.Ctx, 30*time.Second)
			defer cancel()
			f, err := ftps.Dial(ctx, p.Host, g.FTPSPort, "bblp", code)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			_, err = f.List(ctx, "/")
			return err
		}
	}
	return &preflightCtx{p: p, job: j, status: in.Status, result: preflight.Run(in), conn: conn, code: code}, nil
}

type gatesData struct {
	Printer   string           `json:"printer"`
	Preflight preflight.Result `json:"preflight"`
	Job       *job.Job         `json:"job"`
}

func gatesView(pc *preflightCtx) output.View {
	rows := [][]string{{"gate", "status", "detail"}}
	for _, gt := range pc.result.Gates {
		rows = append(rows, []string{gt.Gate, gt.Status, gt.Detail})
	}
	return output.View{
		Data:  gatesData{Printer: pc.p.Name, Preflight: pc.result, Job: pc.job},
		Human: func(h *output.Human) { humanGates(h, pc.p.Name, pc.result) },
		Plain: rows,
		Quiet: pc.result.Result,
	}
}

func humanGates(h *output.Human, name string, r preflight.Result) {
	h.Line("preflight %s  (%s, slot %s)", h.Status(r.Result), name, r.Slot)
	for _, gt := range r.Gates {
		h.Line("  [%s] %-18s %s", h.Status(gt.Status), gt.Gate, gt.Detail)
		if gt.Fix != "" {
			h.Line("         %s %s", h.Dim("→"), h.Dim(gt.Fix))
		}
	}
}

// PrintCmd groups job commands.
type PrintCmd struct {
	Send   SendCmd   `cmd:"" help:"Upload a sliced file and start it (requires --confirm and a passing preflight)."`
	Pause  PauseCmd  `cmd:"" help:"Pause the running job (requires --confirm)."`
	Resume ResumeCmd `cmd:"" help:"Resume a paused job (requires --confirm)."`
	Stop   StopCmd   `cmd:"" help:"Abort the running job, irreversibly (requires --confirm)."`
}

// SendCmd uploads and starts a job.
type SendCmd struct {
	File      string        `arg:"" help:"Sliced .gcode.3mf." type:"path"`
	Slot      string        `short:"s" required:"" help:"AMS slot: 1-4 (first AMS) or A1-D4."`
	Confirm   bool          `help:"Required to actually print. Only pass it after a human approved this exact file and confirmed the plate is clear."`
	DryRun    bool          `short:"n" name:"dry-run" help:"Run preflight and show the exact upload path and MQTT payload; send nothing."`
	Timelapse bool          `help:"Record a timelapse."`
	Wait      time.Duration `default:"180s" help:"How long to wait for the job to start."`
}

type sendPlan struct {
	FTPS struct {
		Host       string `json:"host"`
		Port       int    `json:"port"`
		User       string `json:"user"`
		RemotePath string `json:"remote_path"`
		Local      string `json:"local"`
		LocalMD5   string `json:"local_md5"`
		Bytes      int64  `json:"bytes"`
	} `json:"ftps"`
	MQTT struct {
		Topic   string         `json:"topic"`
		QoS     int            `json:"qos"`
		Payload map[string]any `json:"payload"`
	} `json:"mqtt"`
}

type sendResult struct {
	Printer   string           `json:"printer"`
	DryRun    bool             `json:"dry_run"`
	Sent      bool             `json:"sent"`
	State     string           `json:"state,omitempty"`
	Ack       map[string]any   `json:"ack,omitempty"`
	Preflight preflight.Result `json:"preflight"`
	Plan      sendPlan         `json:"plan"`
}

// Run executes the command.
func (c *SendCmd) Run(g *Globals) error {
	if !c.DryRun && !c.Confirm {
		return errfmt.New(errfmt.ExitGate, "refusing to print without --confirm").
			WithHint("preview with --dry-run; pass --confirm only after a human approved this file and confirmed the plate is clear")
	}
	pc, err := runPreflight(g, c.File, c.Slot, true)
	if err != nil {
		return err
	}
	defer pc.conn.Close()
	remote := job.RemoteName(pc.job.File)
	var plan sendPlan
	plan.FTPS.Host, plan.FTPS.Port, plan.FTPS.User, plan.FTPS.RemotePath, plan.FTPS.Local = pc.p.Host, 990, "bblp", "/"+remote, pc.job.File
	plan.FTPS.LocalMD5, plan.FTPS.Bytes, err = fileMD5(pc.job.File)
	if err != nil {
		return errfmt.Wrap(errfmt.ExitError, err, "read %s", pc.job.File)
	}
	body := job.ProjectFile(pc.job, remote, pc.result.TrayID, c.Timelapse)
	plan.MQTT.Topic, plan.MQTT.QoS = "device/"+pc.p.Serial+"/request", 1
	plan.MQTT.Payload = map[string]any{"print": withSeq(body)}
	res := sendResult{Printer: pc.p.Name, DryRun: c.DryRun, Preflight: pc.result, Plan: plan}

	if c.DryRun {
		if err := g.Out.Print(output.View{
			Data: res,
			Human: func(h *output.Human) {
				humanGates(h, pc.p.Name, pc.result)
				h.Line("")
				h.Line("%s nothing sent", h.Warn("DRY RUN"))
				h.Line("  FTPS  %s -> ftps://%s:990%s (%d bytes, md5 %s)", plan.FTPS.Local, plan.FTPS.Host, plan.FTPS.RemotePath, plan.FTPS.Bytes, plan.FTPS.LocalMD5)
				h.Line("  MQTT  %s (qos 1):", plan.MQTT.Topic)
				_ = g.Out.JSON(plan.MQTT.Payload)
			},
			Plain: [][]string{{"result", "remote_path", "topic"}, {pc.result.Result, plan.FTPS.RemotePath, plan.MQTT.Topic}},
			Quiet: pc.result.Result,
		}); err != nil {
			return err
		}
		if pc.result.Result != preflight.Pass {
			return errfmt.Exit(errfmt.ExitGate)
		}
		return nil
	}
	if pc.result.Result != preflight.Pass {
		_ = g.Out.Print(gatesView(pc))
		return errfmt.New(errfmt.ExitGate, "preflight failed: %v", pc.result.Failed).WithHint("fix the failed gates (bambu preflight) and retry")
	}

	g.Out.Hint("uploading %s to %s…", remote, pc.p.Name)
	if err := upload(g.Ctx, pc, plan, g.FTPSPort); err != nil {
		return err
	}
	g.Out.Hint("starting job…")
	ctx, cancel := context.WithTimeout(g.Ctx, 15*time.Second)
	ack, err := pc.conn.Command(ctx, "print", body)
	cancel()
	if err != nil {
		return err
	}
	if err := printer.CheckAck(ack, "project_file"); err != nil {
		return err
	}
	state, err := waitStarted(g.Ctx, pc.conn, c.Wait)
	if err != nil {
		return err
	}
	res.Sent, res.State, res.Ack = true, state, ack
	return g.Out.Print(output.View{
		Data: res,
		Human: func(h *output.Human) {
			h.Line("%s %s on %s: %s", h.Good("sent"), remote, pc.p.Name, h.Status(state))
			h.Line("%s", h.Dim("next: bambu monitor --snapshot-at-layer 2"))
		},
		Plain: [][]string{{"sent", "state", "remote_path"}, {"true", state, plan.FTPS.RemotePath}},
		Quiet: state,
	})
}

func withSeq(body map[string]any) map[string]any {
	out := map[string]any{"sequence_id": "0"}
	for k, v := range body {
		out[k] = v
	}
	return out
}

func fileMD5(path string) (string, int64, error) {
	f, err := os.Open(path) //nolint:gosec // user-selected job file
	if err != nil {
		return "", 0, err //nolint:wrapcheck // wrapped by caller
	}
	defer func() { _ = f.Close() }()
	h := md5.New() //nolint:gosec // integrity check
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err //nolint:wrapcheck // wrapped by caller
}

func upload(ctx context.Context, pc *preflightCtx, plan sendPlan, port int) error {
	f, err := ftps.Dial(ctx, pc.p.Host, port, "bblp", pc.code)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	in, err := os.Open(plan.FTPS.Local)
	if err != nil {
		return errfmt.Wrap(errfmt.ExitError, err, "open %s", plan.FTPS.Local)
	}
	defer func() { _ = in.Close() }()
	if err := f.Store(ctx, plan.FTPS.RemotePath, in); err != nil {
		return errfmt.Wrap(errfmt.ExitRetryable, err, "FTPS upload failed").WithHint("retry; check the SD card has free space")
	}
	sum, err := f.MD5(ctx, plan.FTPS.RemotePath)
	if err != nil {
		return errfmt.Wrap(errfmt.ExitRetryable, err, "read-back of the uploaded file failed").WithHint("retry the send")
	}
	if sum != plan.FTPS.LocalMD5 {
		return errfmt.New(errfmt.ExitRetryable, "uploaded file MD5 mismatch (%s != %s)", sum, plan.FTPS.LocalMD5).
			WithHint("retry the send; check SD card health")
	}
	return nil
}

func waitStarted(ctx context.Context, conn printer.Conn, wait time.Duration) (string, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	state := ""
	for {
		s := printer.Summarize(conn.State())
		state = s.State
		if s.Errors.Blocking {
			return state, errfmt.New(errfmt.ExitError, "printer reported an error after send: %+v", s.Errors).
				WithHint("check the printer screen and `bambu status`")
		}
		if state == "PREPARE" || state == "RUNNING" { // preflight required an idle printer, so this is our job
			return state, nil
		}
		select {
		case <-ctx.Done():
			return state, errfmt.New(errfmt.ExitError, "interrupted while waiting for the job to start")
		case <-deadline.C:
			return state, errfmt.New(errfmt.ExitRetryable, "job not started after %s (state=%s)", wait, state).
				WithHint("check the printer screen (a prompt may be waiting) and `bambu status`")
		case <-conn.Updates():
		case <-tick.C:
		}
	}
}

// PauseCmd pauses the job.
type PauseCmd struct {
	Confirm bool `help:"Required: this acts on a real print."`
}

// Run executes the command.
func (c *PauseCmd) Run(g *Globals) error { return control(g, "pause", c.Confirm) }

// ResumeCmd resumes a paused job.
type ResumeCmd struct {
	Confirm bool `help:"Required: only resume once the cause of the pause is fixed."`
}

// Run executes the command.
func (c *ResumeCmd) Run(g *Globals) error { return control(g, "resume", c.Confirm) }

// StopCmd aborts the job.
type StopCmd struct {
	Confirm bool `help:"Required: stopping is irreversible."`
}

// Run executes the command.
func (c *StopCmd) Run(g *Globals) error { return control(g, "stop", c.Confirm) }

func control(g *Globals, verb string, confirm bool) error {
	if !confirm {
		return errfmt.New(errfmt.ExitGate, "print %s needs --confirm", verb).WithHint("this acts on a real print")
	}
	p, err := g.Target()
	if err != nil {
		return err
	}
	conn, err := g.Connect(p)
	if err != nil {
		return err
	}
	defer conn.Close()
	raw, err := conn.Pushall(g.Ctx)
	if err != nil {
		return err
	}
	before := printer.Summarize(raw)
	allowed := map[string]map[string]bool{
		"pause": {"RUNNING": true, "PREPARE": true}, "resume": {"PAUSE": true}, "stop": printer.ActiveStates,
	}[verb]
	if !allowed[before.State] {
		return errfmt.New(errfmt.ExitGate, "can't %s: printer is %s", verb, before.State).WithHint("check `bambu status`")
	}
	if before.DevMode == nil || !*before.DevMode {
		return errfmt.New(errfmt.ExitForbidden, "%s needs Developer Mode (the printer requires signed commands)", verb).
			WithHint("enable Developer Mode on the printer, or use the printer screen")
	}
	ctx, cancel := context.WithTimeout(g.Ctx, 10*time.Second)
	ack, err := conn.Command(ctx, "print", map[string]any{"command": verb, "param": ""})
	cancel()
	if err != nil {
		return err
	}
	if err := printer.CheckAck(ack, verb); err != nil {
		return err
	}
	select {
	case <-time.After(3 * time.Second):
	case <-g.Ctx.Done():
	}
	after := printer.Summarize(conn.State())
	data := map[string]any{"printer": p.Name, "command": verb, "acked": ack != nil, "state_before": before.State, "state_after": after.State}
	return g.Out.Print(output.View{
		Data:  data,
		Human: func(h *output.Human) { h.Line("%s: %s -> %s", verb, before.State, h.Status(after.State)) },
		Plain: [][]string{{"command", "state_before", "state_after"}, {verb, before.State, after.State}},
		Quiet: after.State,
	})
}
