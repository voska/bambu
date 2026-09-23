package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/voska/bambu/internal/config"
	"github.com/voska/bambu/internal/output"
	"github.com/voska/bambu/internal/printer"
)

// StatusCmd prints printer status.
type StatusCmd struct {
	Watch bool `short:"w" help:"Stream changes (NDJSON with --json) until interrupted."`
	Raw   bool `help:"Include the raw merged push_status object (--json only)."`
}

type statusView struct {
	Printer string         `json:"printer"`
	Host    string         `json:"host"`
	Status  printer.Status `json:"status"`
	Raw     map[string]any `json:"raw,omitempty"`
}

// Run executes the command.
func (c *StatusCmd) Run(g *Globals) error {
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
	if c.Watch {
		return watch(g, conn)
	}
	s := printer.Summarize(raw)
	v := statusView{Printer: p.Name, Host: p.Host, Status: s}
	if c.Raw {
		v.Raw = raw
	}
	return g.Out.Print(output.View{
		Data:  v,
		Human: func(h *output.Human) { humanStatus(h, p, s) },
		Plain: [][]string{
			{"printer", "state", "stage", "job", "percent", "layer", "total_layers", "remaining_min", "blocking", "dev_mode"},
			{
				p.Name, s.State, s.Stage, s.Job.Name, strconv.Itoa(s.Job.Percent), strconv.Itoa(s.Job.Layer), strconv.Itoa(s.Job.TotalLayers),
				strconv.Itoa(s.Job.RemainingMin), strconv.FormatBool(s.Errors.Blocking), ptrBool(s.DevMode),
			},
		},
		Quiet: s.State,
	})
}

// Event is one streamed status change.
type Event struct {
	TS           string          `json:"ts"`
	Event        string          `json:"event"`
	State        string          `json:"state,omitempty"`
	Stage        string          `json:"stage,omitempty"`
	Job          string          `json:"job,omitempty"`
	Percent      int             `json:"percent"`
	Layer        int             `json:"layer"`
	TotalLayers  int             `json:"total_layers"`
	RemainingMin int             `json:"remaining_min"`
	Errors       *printer.Errors `json:"errors,omitempty"`
	Snapshot     string          `json:"snapshot,omitempty"`
	Message      string          `json:"message,omitempty"`
}

func newEvent(kind string, s printer.Status) Event {
	e := Event{
		TS: time.Now().Format(time.RFC3339), Event: kind, State: s.State, Stage: s.Stage, Job: s.Job.Name,
		Percent: s.Job.Percent, Layer: s.Job.Layer, TotalLayers: s.Job.TotalLayers, RemainingMin: s.Job.RemainingMin,
	}
	if len(s.Errors.HMS) > 0 || s.Errors.PrintError != "" {
		errs := s.Errors
		e.Errors = &errs
	}
	return e
}

func (e Event) human() string {
	switch e.Event {
	case "snapshot":
		return fmt.Sprintf("%s snapshot at layer %d: %s", e.TS, e.Layer, e.Snapshot)
	case "message":
		return e.TS + " " + e.Message
	}
	line := fmt.Sprintf("%s %-8s %-26s %3d%%  layer %d/%d  %d min", e.TS, e.State, e.Stage, e.Percent, e.Layer, e.TotalLayers, e.RemainingMin)
	if e.Errors != nil {
		var codes []string
		for _, h := range e.Errors.HMS {
			tag := "ERR"
			if h.Informational {
				tag = "info"
			}
			codes = append(codes, tag+" "+h.Code)
		}
		if e.Errors.PrintError != "" {
			codes = append(codes, "print_error "+e.Errors.PrintError)
		}
		line += "  " + strings.Join(codes, ", ")
	}
	return line
}

func changeKey(s printer.Status) string {
	var codes []string
	for _, h := range s.Errors.HMS {
		codes = append(codes, h.Code)
	}
	return fmt.Sprintf("%s|%s|%d|%s|%s", s.State, s.Stage, s.Job.Layer, strings.Join(codes, ","), s.Errors.PrintError)
}

func watch(g *Globals, conn printer.Conn) error {
	last := ""
	refresh := time.NewTicker(5 * time.Minute)
	defer refresh.Stop()
	for {
		s := printer.Summarize(conn.State())
		if k := changeKey(s); k != last {
			last = k
			ev := newEvent("status", s)
			if err := g.Out.Event(ev, ev.human()); err != nil {
				return err
			}
		}
		select {
		case <-g.Ctx.Done():
			return nil
		case <-conn.Updates():
		case <-refresh.C:
			_, _ = conn.Pushall(g.Ctx)
		}
	}
}

func humanStatus(h *output.Human, p *config.Printer, s printer.Status) {
	h.Line("%s %s  %s / %s   dev_mode=%s  sdcard=%v  liveview=%v", h.Bold(p.Name), h.Dim("("+p.Host+")"), h.Status(s.State), s.Stage,
		h.Status(ptrBool(s.DevMode)), s.SDCard, s.Liveview)
	if s.Job.Name != "" {
		h.Line("  job      %s  %d%%  layer %d/%d  %d min left", s.Job.Name, s.Job.Percent, s.Job.Layer, s.Job.TotalLayers, s.Job.RemainingMin)
	}
	h.Line("  temps    nozzle %.0f/%.0f  bed %.0f/%.0f   nozzle %s mm %s", s.Temps.Nozzle, s.Temps.NozzleTarget, s.Temps.Bed, s.Temps.BedTarget,
		s.Nozzle.Diameter, s.Nozzle.Material)
	if len(s.Errors.HMS) == 0 && s.Errors.PrintError == "" {
		h.Line("  errors   %s", h.Good("none"))
	} else {
		h.Line("  errors   blocking=%s print_error=%s", h.Status(strconv.FormatBool(s.Errors.Blocking)), s.Errors.PrintError)
		for _, e := range s.Errors.HMS {
			tag := h.Bad(e.Severity)
			if e.Informational {
				tag = h.Dim("informational")
			}
			h.Line("           %s %s/%s  %s", e.Code, e.Module, tag, h.Dim(e.URL))
		}
	}
	for _, t := range s.AMS {
		if !t.Loaded {
			h.Line("  AMS %s   %s", t.Slot, h.Dim("empty"))
			continue
		}
		rem := h.Dim("remaining unknown")
		if t.RemainPct != nil {
			rem = fmt.Sprintf("%d%%", *t.RemainPct)
			if t.RemainG != nil {
				rem += fmt.Sprintf(" (~%d g)", *t.RemainG)
			}
		}
		name := t.Name
		if name == "" {
			name = t.FilamentID
		}
		h.Line("  AMS %s   %-6s %-12s %s  %s", t.Slot, t.Type, name, t.Color, rem)
	}
	if s.External != "" {
		h.Line("  external %s", s.External)
	}
	pr := s.Protections
	h.Line("  protect  first_layer=%s spaghetti=%s halt=%s (%s)", ptrBool(pr.FirstLayerInspector), ptrBool(pr.SpaghettiDetector),
		ptrBool(pr.PrintHalt), pr.HaltSensitivity)
}

func ptrBool(b *bool) string {
	if b == nil {
		return "unknown"
	}
	return strconv.FormatBool(*b)
}
