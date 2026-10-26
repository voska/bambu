package cmd

import (
	"context"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/output"
	"github.com/voska/bambu/internal/printer"
)

// AMSCmd groups AMS tray metadata commands.
type AMSCmd struct {
	Set AMSSetCmd `cmd:"" help:"Set non-RFID spool metadata while idle; verify by fresh read-back. Does not load filament, heat, slice, or select flow calibration."`
}

// AMSSetCmd records the material a human has physically loaded into a tray.
type AMSSetCmd struct {
	Slot          string        `short:"s" required:"" help:"AMS slot: 1-4 (first AMS) or A1-D4."`
	Type          string        `required:"" help:"Material type, e.g. PLA or PETG. PLA+ uses PLA."`
	FilamentID    string        `required:"" help:"Matching filament profile ID, e.g. GFL99 for Generic PLA. Common Generic IDs are type-checked; the caller must match other IDs to --type."`
	Color         string        `required:"" aliases:"colour" help:"Opaque preview color: RRGGBB or #RRGGBB."`
	NozzleTempMin int           `required:"" help:"Spool's minimum nozzle temperature in Celsius (150-350)."`
	NozzleTempMax int           `required:"" help:"Spool's maximum nozzle temperature in Celsius (150-350)."`
	DryRun        bool          `help:"Read state and show the exact payload; send nothing."`
	Confirm       bool          `help:"Required for writes: a human identified the physical spool. Never relabel a spool to bypass preflight. Temperature bounds affect later filament loading."`
	Wait          time.Duration `default:"10s" help:"How long to wait for fresh tray read-back (exit 14 if unverified)."`
}

type amsSetResult struct {
	Printer           string                    `json:"printer"`
	Slot              string                    `json:"slot"`
	DryRun            bool                      `json:"dry_run"`
	Sent              bool                      `json:"sent"`
	Verified          bool                      `json:"verified"`
	Before            printer.Tray              `json:"before"`
	After             printer.Tray              `json:"after"`
	CalibrationBefore map[string]any            `json:"calibration_before,omitempty"`
	CalibrationAfter  map[string]any            `json:"calibration_after,omitempty"`
	Payload           map[string]map[string]any `json:"payload"`
	Ack               map[string]any            `json:"ack,omitempty"`
}

var (
	materialPattern = regexp.MustCompile(`^[A-Z][A-Z0-9-]{0,31}$`)
	filamentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	// Bambu Studio profiles/BBL/filament/Generic <material> @base.json.
	genericMaterials = map[string]string{
		"GFL99": "PLA", "GFG99": "PETG", "GFB99": "ABS", "GFB98": "ASA",
		"GFN99": "PA", "GFC99": "PC", "GFU99": "TPU",
	}
)

func (c *AMSSetCmd) validate() (int, string, error) {
	id, err := printer.ParseSlot(c.Slot)
	if err != nil {
		return 0, "", errfmt.Wrap(errfmt.ExitUsage, err, "bad --slot")
	}
	color := strings.ToUpper(strings.TrimPrefix(c.Color, "#"))
	_, colorErr := hex.DecodeString(color)
	switch {
	case !materialPattern.MatchString(c.Type):
		return 0, "", errfmt.New(errfmt.ExitUsage, "--type must be a material code such as PLA or PETG")
	case !filamentPattern.MatchString(c.FilamentID):
		return 0, "", errfmt.New(errfmt.ExitUsage, "--filament-id must be a profile ID such as GFL99")
	case genericMaterials[c.FilamentID] != "" && genericMaterials[c.FilamentID] != c.Type:
		return 0, "", errfmt.New(errfmt.ExitUsage, "--filament-id %s is %s, not --type %s", c.FilamentID, genericMaterials[c.FilamentID], c.Type)
	case len(color) != 6 || colorErr != nil:
		return 0, "", errfmt.New(errfmt.ExitUsage, "--color must be RRGGBB or #RRGGBB")
	case c.NozzleTempMin < 150 || c.NozzleTempMax > 350 || c.NozzleTempMin > c.NozzleTempMax:
		return 0, "", errfmt.New(errfmt.ExitUsage, "nozzle temperature range must satisfy 150 <= min <= max <= 350")
	case c.Wait <= 0:
		return 0, "", errfmt.New(errfmt.ExitUsage, "--wait must be positive")
	case c.DryRun && c.Confirm:
		return 0, "", errfmt.New(errfmt.ExitUsage, "use either --dry-run or --confirm")
	case !c.DryRun && !c.Confirm:
		return 0, "", errfmt.New(errfmt.ExitGate, "ams set needs --confirm or --dry-run").WithHint("only record the material physically loaded into the slot, after a human identifies it")
	}
	return id, color, nil
}

// Run executes the command.
func (c *AMSSetCmd) Run(g *Globals) error {
	id, color, err := c.validate()
	if err != nil {
		return err
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
	if err := amsSetGates(raw, before, id); err != nil {
		return err
	}
	body := printer.AMSFilamentSetting(id, c.Type, c.FilamentID, color, c.NozzleTempMin, c.NozzleTempMax)
	r := amsSetResult{
		Printer: p.Name, Slot: printer.TrayLabel(id), DryRun: c.DryRun,
		Before: *findTray(before, id), After: *findTray(before, id),
		Payload:           map[string]map[string]any{"print": withSeq(body)},
		CalibrationBefore: trayCalibration(printer.RawAMSTray(raw, id)),
	}
	r.CalibrationAfter = r.CalibrationBefore
	if r.Before.Type != c.Type && len(r.CalibrationBefore) > 0 {
		g.Out.Hint("AMS metadata does not change the slot's flow calibration; check its calibration before printing %s", c.Type)
	}
	if !c.DryRun {
		r.Verified = printer.AMSSettingsMatch(raw, id, body)
		if !r.Verified {
			ackCtx, cancel := context.WithTimeout(g.Ctx, min(c.Wait, 3*time.Second))
			r.Ack, err = conn.Command(ackCtx, "print", body)
			cancel()
			if err != nil {
				return err
			}
			r.Sent = true
			if err := printer.CheckAck(r.Ack, "set "+r.Slot); err != nil {
				return err
			}
			if err := verifyAMSSet(g.Ctx, conn, id, body, c.Wait, &r); err != nil {
				return err
			}
		}
	}
	return g.Out.Print(output.View{
		Data: r,
		Human: func(h *output.Human) {
			if r.DryRun {
				h.Line("dry-run: %s -> %s (%s), #%s, %d-%d C; nothing sent", r.Slot, c.Type, c.FilamentID, color, c.NozzleTempMin, c.NozzleTempMax)
				return
			}
			h.Line("%s: %s (%s), %s, %d-%d C; verified (sent=%t)", r.Slot, r.After.Type, r.After.FilamentID, r.After.Color, r.After.TempMin, r.After.TempMax, r.Sent)
		},
		Plain: [][]string{
			{"slot", "type", "filament_id", "color", "nozzle_temp_min", "nozzle_temp_max", "dry_run", "sent", "verified"},
			{r.Slot, c.Type, c.FilamentID, "#" + color, strconv.Itoa(c.NozzleTempMin), strconv.Itoa(c.NozzleTempMax), strconv.FormatBool(r.DryRun), strconv.FormatBool(r.Sent), strconv.FormatBool(r.Verified)},
		},
		Quiet: r.Slot,
	})
}

func amsSetGates(raw map[string]any, s printer.Status, id int) error {
	label := printer.TrayLabel(id)
	switch {
	case !printer.IdleStates[s.State]:
		return errfmt.New(errfmt.ExitGate, "can't set AMS metadata: printer is %q", s.State).WithHint("wait until IDLE, FINISH, or FAILED; paused jobs also retain their material mapping")
	case s.DevMode == nil || !*s.DevMode:
		return errfmt.New(errfmt.ExitForbidden, "ams set needs Developer Mode").WithHint("enable Developer Mode on the printer")
	case printer.FilamentChanging(s) || (s.AMSStatus != "" && s.AMSStatus != "idle" && s.AMSStatus != "assist"):
		return errfmt.New(errfmt.ExitGate, "AMS is busy (ams_status=%s stage=%s)", s.AMSStatus, s.Stage)
	case s.Errors.Blocking:
		return errfmt.New(errfmt.ExitGate, "printer has blocking errors").WithHint("check `bambu status` and clear the cause first")
	case findTray(s, id) == nil:
		return errfmt.New(errfmt.ExitGate, "AMS slot %s was not reported", label)
	}
	present := printer.TrayPresent(raw, id)
	if present == nil || !*present {
		return errfmt.New(errfmt.ExitGate, "filament presence in %s is empty or unknown", label).WithHint("insert the spool; the AMS must report tray_exist_bits before metadata can be set")
	}
	t := printer.RawAMSTray(raw, id)
	for _, key := range []string{"tag_uid", "tray_uuid"} {
		if v, ok := t[key].(string); ok && strings.Trim(v, "0") != "" {
			return errfmt.New(errfmt.ExitGate, "%s has RFID metadata; ams set only edits non-RFID spools", label)
		}
	}
	return nil
}

func trayCalibration(t map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"k", "n", "cali_idx"} {
		if v, ok := t[k]; ok {
			out[k] = v
		}
	}
	return out
}

func verifyAMSSet(ctx context.Context, conn printer.Conn, id int, body map[string]any, wait time.Duration, r *amsSetResult) error {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		raw, err := conn.Pushall(ctx)
		if err != nil {
			return errfmt.Wrap(errfmt.ExitTimeout, err, "%s metadata submitted, unverified: read-back failed", r.Slot).
				WithData("ams", r).WithHint("read `bambu status --json` before retrying")
		}
		if after := findTray(printer.Summarize(raw), id); after != nil {
			r.After = *after
		}
		r.CalibrationAfter = trayCalibration(printer.RawAMSTray(raw, id))
		if printer.AMSSettingsMatch(raw, id, body) {
			r.Verified = true
			return nil
		}
		select {
		case <-ctx.Done():
			return errfmt.New(errfmt.ExitTimeout, "%s metadata submitted, unverified after %s", r.Slot, wait).
				WithData("ams", r).WithHint("read `bambu status --json` before retrying")
		case <-tick.C:
		}
	}
}
