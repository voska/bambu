// Package preflight runs the safety gates that must pass before a job is sent. FAIL blocks; WARN informs.
package preflight

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/voska/bambu/internal/config"
	"github.com/voska/bambu/internal/job"
	"github.com/voska/bambu/internal/printer"
)

// Gate statuses.
const (
	Pass = "PASS"
	Warn = "WARN"
	Fail = "FAIL"
)

// Gate is one check.
type Gate struct {
	Gate   string `json:"gate"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// Result is the preflight outcome.
type Result struct {
	Result   string   `json:"result"`
	Failed   []string `json:"failed"`
	Warnings []string `json:"warnings"`
	Slot     string   `json:"slot"`
	TrayID   int      `json:"tray_id"`
	Gates    []Gate   `json:"gates"`
}

// Input bundles everything the gates look at.
type Input struct {
	Printer *config.Printer
	Job     *job.Job
	Status  printer.Status
	TrayID  int
	// FTPSCheck, if set, performs a read-only FTPS login + listing and returns its error.
	FTPSCheck func() error
}

type gates []Gate

func (g *gates) add(name string, ok bool, failStatus, detail, fix string) {
	st := Pass
	if !ok {
		st = failStatus
	}
	gt := Gate{Gate: name, Status: st, Detail: detail}
	if !ok {
		gt.Fix = fix
	}
	*g = append(*g, gt)
}

// Run evaluates all gates.
func Run(in Input) Result {
	var g gates
	s, j, p := in.Status, in.Job, in.Printer
	label := printer.TrayLabel(in.TrayID)

	g.add("printer_idle", printer.IdleStates[s.State], Fail, fmt.Sprintf("state=%s stage=%s", s.State, s.Stage),
		"wait for the current job to finish (bambu monitor)")
	var codes []string
	for _, h := range s.Errors.HMS {
		c := h.Code
		if h.Informational {
			c += " (info)"
		}
		codes = append(codes, c)
	}
	g.add("no_errors", !s.Errors.Blocking, Fail, fmt.Sprintf("hms=%v print_error=%s", codes, orNone(s.Errors.PrintError)),
		"fix the cause, clear the error on the printer screen, then retry (see the HMS wiki link in `bambu status`)")
	g.add("developer_mode", s.DevMode != nil && *s.DevMode, Fail, "dev_mode="+boolStr(s.DevMode),
		"on the printer: Settings > LAN Only > enable LAN Only and Developer Mode; then `bambu auth set` if the code changed")
	g.add("sdcard", s.SDCard, Fail, fmt.Sprintf("sdcard=%v", s.SDCard), "insert the printer's microSD card")

	g.add("file_integrity", j.GcodeMD5OK == nil || *j.GcodeMD5OK, Fail, fmt.Sprintf("%s md5_ok=%s", j.GcodeParam, boolStr(j.GcodeMD5OK)),
		"re-slice; the 3MF is corrupt")
	model, known := printer.LookupModel(p.Model)
	want := model.PrinterModel
	if !known {
		want = p.Model
	}
	g.add("sliced_for_printer", j.PrinterModel == want, Fail, fmt.Sprintf("file=%q printer=%q", j.PrinterModel, want),
		"re-slice for this printer (bambu slice --printer "+p.Name+")")
	used := len(j.Filaments)
	g.add("single_filament", used == 1, Fail, fmt.Sprintf("%d filaments used", used),
		"multi-material jobs are not supported yet; use Bambu Studio")

	pd, _ := strconv.ParseFloat(s.Nozzle.Diameter, 64)
	jd, _ := strconv.ParseFloat(j.NozzleDiameter, 64)
	g.add("nozzle_diameter", pd > 0 && math.Abs(pd-jd) < 1e-3, Fail, fmt.Sprintf("file=%s printer=%s", j.NozzleDiameter, s.Nozzle.Diameter),
		"swap the nozzle or re-slice for the installed one")
	g.add("nozzle_type", s.Nozzle.Material == "" || j.NozzleType == "" || s.Nozzle.Material == j.NozzleType, Warn,
		fmt.Sprintf("file=%s printer=%s", orNone(j.NozzleType), orNone(s.Nozzle.Material)),
		"fine for PLA/PETG; abrasive filaments need a hardened nozzle")
	g.add("bed_type", j.BedType == p.Plate, Fail, fmt.Sprintf("file=%s installed=%s", j.BedType, p.Plate),
		"re-slice (the plate comes from the printer config) or swap the plate and update it: bambu printer add "+p.Name+" … --plate")

	var tray *printer.Tray
	for i := range s.AMS {
		if s.AMS[i].TrayID == in.TrayID {
			tray = &s.AMS[i]
		}
	}
	var need job.Filament
	if len(j.Filaments) > 0 {
		need = j.Filaments[0]
	}
	switch {
	case tray == nil:
		g.add("tray", false, Fail, "slot "+label+" not reported by the AMS", "check the AMS is connected, or pick another slot")
	case !tray.Loaded:
		g.add("tray", false, Fail, "slot "+label+" is empty", fmt.Sprintf("load %s into AMS slot %s", need.Type, label))
	default:
		exact := strings.EqualFold(tray.Type, need.Type)
		sameFamily := family(tray.Type) == family(need.Type)
		st := Pass
		switch {
		case !exact && sameFamily:
			st = Warn
		case !exact:
			st = Fail
		}
		g = append(g, Gate{
			Gate: "filament_type", Status: st, Detail: fmt.Sprintf("slot %s=%s file=%s", label, tray.Type, need.Type),
			Fix: map[string]string{
				Pass: "", Warn: "same family, different variant; OK only if intended",
				Fail: fmt.Sprintf("load %s into slot %s, or choose the slot that has it (--slot)", need.Type, label),
			}[st],
		})
		g.add("filament_profile", tray.FilamentID == need.FilamentID, Warn,
			fmt.Sprintf("slot %s filament_id=%s (%s) file=%s", label, orNone(tray.FilamentID), orNone(tray.Name), orNone(need.FilamentID)),
			"a different product than sliced for; re-slice with --filament to match, or accept")
		if tray.RemainG == nil {
			g.add("filament_amount", false, Warn, fmt.Sprintf("slot %s remaining unknown; job needs %.1f g", label, need.UsedG),
				"confirm there is enough filament on the spool")
		} else {
			ok := float64(*tray.RemainG) >= need.UsedG*1.15+5
			g.add("filament_amount", ok, Fail, fmt.Sprintf("slot %s ~%d g (%d%%); job needs %.1f g (+15%%, +5 g)", label, *tray.RemainG, *tray.RemainPct, need.UsedG),
				"load a fuller spool into slot "+label)
		}
		lo, _ := strconv.ParseFloat(j.NozzleTempRange[0], 64)
		hi, _ := strconv.ParseFloat(j.NozzleTempRange[1], 64)
		t, _ := strconv.ParseFloat(j.NozzleTemp, 64)
		g.add("nozzle_temp", t > 0 && (lo == 0 || t >= lo) && (hi == 0 || t <= hi), Fail,
			fmt.Sprintf("nozzle=%s range=%s-%s", j.NozzleTemp, j.NozzleTempRange[0], j.NozzleTempRange[1]), "fix the recipe temperatures")
	}

	pr := s.Protections
	g.add("ai_protections", isTrue(pr.FirstLayerInspector) && isTrue(pr.SpaghettiDetector) && isTrue(pr.PrintHalt), Warn,
		fmt.Sprintf("first_layer_inspector=%s spaghetti_detector=%s print_halt=%s", boolStr(pr.FirstLayerInspector), boolStr(pr.SpaghettiDetector), boolStr(pr.PrintHalt)),
		"enable them on the printer (bambu never changes printer settings)")
	if in.FTPSCheck != nil {
		err := in.FTPSCheck()
		detail := "implicit FTPS login + listing OK"
		if err != nil {
			detail = err.Error()
		}
		g.add("ftps_login", err == nil, Fail, detail, "check the access code (bambu auth status --check) and Developer Mode")
	}

	r := Result{Result: Pass, Slot: label, TrayID: in.TrayID, Gates: g, Failed: []string{}, Warnings: []string{}}
	for _, x := range g {
		switch x.Status {
		case Fail:
			r.Failed = append(r.Failed, x.Gate)
			r.Result = Fail
		case Warn:
			r.Warnings = append(r.Warnings, x.Gate)
		}
	}
	return r
}

func family(t string) string {
	t = strings.ToUpper(strings.TrimSpace(t))
	if i := strings.IndexAny(t, "- "); i > 0 {
		return t[:i]
	}
	return t
}

func isTrue(b *bool) bool { return b != nil && *b }

func boolStr(b *bool) string {
	if b == nil {
		return "unknown"
	}
	return strconv.FormatBool(*b)
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
