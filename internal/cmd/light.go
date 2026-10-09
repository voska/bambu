package cmd

import (
	"context"
	"time"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/output"
	"github.com/voska/bambu/internal/printer"
)

const chamberLight = "chamber_light"

// LightCmd reads or switches the chamber light, verifying the reported mode.
type LightCmd struct {
	Action string        `arg:"" enum:"status,on,off" help:"Chamber light: status (read-only), on, or off."`
	Wait   time.Duration `default:"10s" help:"How long to wait for the light's reported mode."`
}

type lightResult struct {
	Node   string         `json:"node"`
	Before string         `json:"before"`
	After  string         `json:"after"`
	Ack    map[string]any `json:"ack,omitempty"`
}

// Run executes the command.
func (c *LightCmd) Run(g *Globals) error {
	if c.Wait <= 0 {
		return errfmt.New(errfmt.ExitUsage, "--wait must be positive")
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
	if c.Action == "status" {
		raw, err := conn.Pushall(g.Ctx)
		if err != nil {
			return err
		}
		lights := printer.Summarize(raw).Lights
		return g.Out.Print(output.View{
			Data:  map[string]any{"printer": p.Name, "lights": lights},
			Human: func(h *output.Human) { h.Line("chamber_light=%s", lights[chamberLight]) },
			Plain: [][]string{{"node", "mode"}, {chamberLight, lights[chamberLight]}}, Quiet: lights[chamberLight],
		})
	}
	r, err := setLight(g.Ctx, conn, c.Action, c.Wait)
	if err != nil {
		return err
	}
	return g.Out.Print(output.View{
		Data:  map[string]any{"printer": p.Name, "light": r},
		Human: func(h *output.Human) { h.Line("%s: %s -> %s", r.Node, r.Before, r.After) },
		Plain: [][]string{{"node", "before", "after"}, {r.Node, r.Before, r.After}}, Quiet: r.After,
	})
}

func readLight(ctx context.Context, conn printer.Conn) (string, error) {
	raw, err := conn.Pushall(ctx)
	if err != nil {
		return "", err
	}
	mode := printer.Summarize(raw).Lights[chamberLight]
	switch mode {
	case "on", "off", "flashing":
		return mode, nil
	default:
		return "", errfmt.New(errfmt.ExitGate, "printer did not report a known chamber light mode").WithHint("check `bambu status --raw --json`")
	}
}

func setLight(ctx context.Context, conn printer.Conn, mode string, wait time.Duration) (lightResult, error) {
	ackCtx, ackCancel := context.WithTimeout(ctx, wait)
	before, err := readLight(ackCtx, conn)
	r := lightResult{Node: chamberLight, Before: before, After: before}
	if err != nil || before == mode {
		ackCancel()
		return r, err
	}
	body := map[string]any{
		"command": "ledctrl", "led_node": chamberLight, "led_mode": mode,
		"led_on_time": 500, "led_off_time": 500, "loop_times": 0, "interval_time": 0,
	}
	r.Ack, err = conn.Command(ackCtx, "system", body)
	ackCancel()
	if err != nil {
		return r, err
	}
	if err := printer.CheckAck(r.Ack, "chamber light "+mode); err != nil {
		return r, err
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		r.After, err = readLight(ctx, conn)
		if err != nil {
			return r, err
		}
		if r.After == mode {
			return r, nil
		}
		select {
		case <-ctx.Done():
			return r, errfmt.New(errfmt.ExitTimeout, "chamber light %s not confirmed (reported %s)", mode, r.After).
				WithData("light", r).WithHint("check the printer screen and `bambu light status`")
		case <-tick.C:
		}
	}
}
