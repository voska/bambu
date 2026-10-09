package cmd

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/output"
	"github.com/voska/bambu/internal/printer"
)

// FilamentCmd groups AMS filament commands.
type FilamentCmd struct {
	Load FilamentLoadCmd `cmd:"" help:"Unload the toolhead and load an AMS slot: a remote colour change at a pause (requires --confirm)."`
}

// FilamentLoadCmd swaps the filament in the toolhead.
type FilamentLoadCmd struct {
	Slot    string        `short:"s" required:"" help:"AMS slot to load: 1-4 (first AMS) or A1-D4."`
	Confirm bool          `help:"Required: the printer cuts the filament in the toolhead, loads the slot and purges. Only as part of an approved job."`
	Timeout time.Duration `default:"5m" help:"How long to wait for the printer to report the slot loaded (exit 14 if it doesn't)."`
}

type loadResult struct {
	Printer   string         `json:"printer"`
	Slot      string         `json:"slot"`
	TrayID    int            `json:"tray_id"`
	Before    string         `json:"toolhead_before"`
	After     string         `json:"toolhead_after"`
	Changed   bool           `json:"changed"`
	AMSStatus string         `json:"ams_status"`
	State     string         `json:"state"`
	Ack       map[string]any `json:"ack,omitempty"`
}

// Run executes the command.
func (c *FilamentLoadCmd) Run(g *Globals) error {
	if !c.Confirm {
		return errfmt.New(errfmt.ExitGate, "filament load needs --confirm").
			WithHint("it cuts the filament in the toolhead and loads another slot; only as part of an approved job (e.g. the colour change at a pause)")
	}
	tray, err := printer.ParseSlot(c.Slot)
	if err != nil {
		return errfmt.Wrap(errfmt.ExitUsage, err, "bad --slot")
	}
	label := printer.TrayLabel(tray)
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
	target, active := findTray(before, tray), findTray(before, -1)
	if err := loadGates(before, label, target, active); err != nil {
		return err
	}
	res := loadResult{
		Printer: p.Name, Slot: label, TrayID: tray, Before: printer.ToolheadSlot(before.ActiveTray),
		After: printer.ToolheadSlot(before.ActiveTray), AMSStatus: before.AMSStatus, State: before.State,
	}
	if before.ActiveTray != strconv.Itoa(tray) {
		ctx, cancel := context.WithTimeout(g.Ctx, 10*time.Second)
		ack, err := conn.Command(ctx, "print", printer.AMSChangeFilament(tray, printer.LoadTemp(active), printer.LoadTemp(target)))
		cancel()
		if err != nil {
			return err
		}
		if err := printer.CheckAck(ack, "load "+label); err != nil {
			return err
		}
		after, err := waitLoaded(g.Ctx, conn, before, tray, c.Timeout)
		if err != nil {
			return err
		}
		res.Changed, res.Ack, res.After, res.AMSStatus, res.State = true, ack, label, after.AMSStatus, after.State
	}
	return g.Out.Print(output.View{
		Data: res,
		Human: func(h *output.Human) {
			if !res.Changed {
				h.Line("%s is already in the toolhead; nothing sent", label)
			} else {
				h.Line("%s: toolhead %s -> %s  ams_status=%s  state=%s", h.Good("loaded"), orDash(res.Before), res.After, res.AMSStatus, h.Status(res.State))
			}
			if res.State == "PAUSE" {
				h.Line("%s", h.Dim("next: check the camera (bambu camera snapshot), then bambu print resume --confirm"))
			}
		},
		Plain: [][]string{{"slot", "toolhead_before", "toolhead_after", "changed", "state"}, {label, res.Before, res.After, strconv.FormatBool(res.Changed), res.State}},
		Quiet: res.After,
	})
}

// findTray returns the AMS tray with id, or the one in the toolhead for id -1 (nil if none).
func findTray(s printer.Status, id int) *printer.Tray {
	if id < 0 {
		n, err := strconv.Atoi(s.ActiveTray)
		if err != nil {
			return nil
		}
		id = n
	}
	for i := range s.AMS {
		if s.AMS[i].TrayID == id {
			return &s.AMS[i]
		}
	}
	return nil
}

// loadGates refuses a filament load before anything is sent.
func loadGates(s printer.Status, label string, target, active *printer.Tray) error {
	switch {
	case !printer.IdleStates[s.State] && s.State != "PAUSE":
		return errfmt.New(errfmt.ExitGate, "can't load filament: printer is %q", s.State).
			WithHint("only while paused or idle: bambu print pause --confirm first, or wait for the job to end")
	case s.DevMode == nil || !*s.DevMode:
		return errfmt.New(errfmt.ExitForbidden, "filament load needs Developer Mode (the printer requires signed commands)").
			WithHint("enable Developer Mode on the printer, or use the printer screen")
	case printer.FilamentChanging(s):
		return errfmt.New(errfmt.ExitGate, "a filament change is already in progress (ams_status=%s stage=%s)", s.AMSStatus, s.Stage).
			WithHint("wait for it to finish (`bambu status`)")
	}
	if codes := blockingHMS(s, nil); len(codes) > 0 {
		return errfmt.New(errfmt.ExitGate, "printer has HMS errors %v", codes).
			WithHint("fix the cause and clear them on the printer first (see the wiki links in `bambu status`)")
	}
	if target == nil || !target.Loaded {
		return errfmt.New(errfmt.ExitGate, "slot %s is empty", label).WithHint("load filament into AMS slot %s first", label)
	}
	switch {
	case s.State != "PAUSE":
	case active == nil:
		return errfmt.New(errfmt.ExitGate, "can't check the paused job's material: the toolhead holds no AMS filament (tray_now=%s)", orDash(s.ActiveTray)).
			WithHint("look at the camera and use the printer screen")
	case !strings.EqualFold(target.Type, active.Type):
		return errfmt.New(errfmt.ExitGate, "slot %s has %s but the paused job is printing %s from %s", label, target.Type, active.Type, active.Slot).
			WithHint("pick a slot with the same material (`bambu status`)")
	}
	return nil
}

// blockingHMS lists non-informational HMS codes that are not in base.
func blockingHMS(s printer.Status, base []string) []string {
	var out []string
	for _, h := range s.Errors.HMS {
		if !h.Informational && !slices.Contains(base, h.Code) {
			out = append(out, h.Code)
		}
	}
	return out
}

// waitLoaded succeeds only once the printer reports tray in the toolhead with the change over. A new HMS code or
// print_error during the change fails it, as does the timeout.
func waitLoaded(ctx context.Context, conn printer.Conn, before printer.Status, tray int, timeout time.Duration) (printer.Status, error) {
	label, want := printer.TrayLabel(tray), strconv.Itoa(tray)
	var base []string
	for _, h := range before.Errors.HMS {
		base = append(base, h.Code)
	}
	var fault []string
	s, err := waitState(ctx, conn, timeout, func(s printer.Status) bool {
		fault = blockingHMS(s, base)
		if pe := s.Errors.PrintError; pe != "" && pe != before.Errors.PrintError {
			fault = append(fault, "print_error "+pe)
		}
		return len(fault) > 0 || (s.ActiveTray == want && !printer.FilamentChanging(s))
	})
	switch {
	case len(fault) > 0:
		return s, errfmt.New(errfmt.ExitError, "printer reported %v while loading %s", fault, label).
			WithHint("check `bambu status` (HMS wiki links) and the camera; don't resume until the toolhead holds the right filament").
			WithData("status", s)
	case err != nil:
		return s, errfmt.New(errfmt.ExitTimeout, "%s not confirmed loaded %s after the command: toolhead=%s ams_status=%s stage=%s",
			label, timeout, orDash(printer.ToolheadSlot(s.ActiveTray)), s.AMSStatus, s.Stage).
			WithHint("look at the camera / printer screen before anything else; `bambu status`").WithData("status", s)
	}
	return s, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
