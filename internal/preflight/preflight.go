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
	Gate     string `json:"gate"`
	Status   string `json:"status"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix,omitempty"`
	Filament int    `json:"filament,omitempty"` // 1-based job filament, on multi-filament jobs only
}

// Result is the preflight outcome. Slot and TrayID are the first filament's.
type Result struct {
	Strict   bool     `json:"strict"`
	Result   string   `json:"result"`
	Failed   []string `json:"failed"`
	Warnings []string `json:"warnings"`
	Slot     string   `json:"slot"`
	TrayID   int      `json:"tray_id"`
	Slots    []string `json:"slots"`
	TrayIDs  []int    `json:"tray_ids"`
	Gates    []Gate   `json:"gates"`
}

// Input bundles everything the gates look at.
type Input struct {
	Printer *config.Printer
	Job     *job.Job
	Status  printer.Status
	TrayIDs []int // the AMS tray feeding each job filament, in filament order
	Strict  bool  // retain bambu-op's exact material and known temperature requirements
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
	g.add("single_filament", used == len(in.TrayIDs), Fail, fmt.Sprintf("%d filament%s in plate, %d slot%s given", used, plural(used), len(in.TrayIDs), plural(len(in.TrayIDs))),
		"pass one --slot per filament, in filament order (e.g. --slot A4 --slot A1)")

	pd, _ := strconv.ParseFloat(s.Nozzle.Diameter, 64)
	jd, _ := strconv.ParseFloat(j.NozzleDiameter, 64)
	g.add("nozzle_diameter", pd > 0 && math.Abs(pd-jd) < 1e-3, Fail, fmt.Sprintf("file=%s printer=%s", j.NozzleDiameter, s.Nozzle.Diameter),
		"swap the nozzle or re-slice for the installed one")
	nozzleMatch := s.Nozzle.Material == j.NozzleType
	if !in.Strict {
		nozzleMatch = s.Nozzle.Material == "" || j.NozzleType == "" || nozzleMatch
	}
	g.add("nozzle_type", nozzleMatch, Warn,
		fmt.Sprintf("file=%s printer=%s", orNone(j.NozzleType), orNone(s.Nozzle.Material)),
		"fine for PLA/PETG; abrasive filaments need a hardened nozzle")
	g.add("bed_type", j.BedType == p.Plate, Fail, fmt.Sprintf("file=%s installed=%s", j.BedType, p.Plate),
		"re-slice (the plate comes from the printer config) or swap the plate and update it: bambu printer add "+p.Name+" … --plate")

	needs := j.Filaments
	if len(needs) == 0 {
		needs = []job.Filament{{NozzleTemp: j.NozzleTemp, NozzleTempRange: j.NozzleTempRange[:]}}
	}
	for n, need := range needs {
		if n >= len(in.TrayIDs) {
			break
		}
		fg := filamentGates(s, need, in.TrayIDs[n], in.Strict)
		if len(needs) > 1 {
			for i := range fg {
				fg[i].Filament = n + 1
			}
		}
		g = append(g, fg...)
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

	r := Result{Strict: in.Strict, Result: Pass, Gates: g, Failed: []string{}, Warnings: []string{}, TrayIDs: in.TrayIDs, Slots: []string{}}
	for _, t := range in.TrayIDs {
		r.Slots = append(r.Slots, printer.TrayLabel(t))
	}
	if len(in.TrayIDs) > 0 {
		r.Slot, r.TrayID = r.Slots[0], in.TrayIDs[0]
	}
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

// filamentGates checks one job filament against the AMS tray mapped to it.
func filamentGates(s printer.Status, need job.Filament, trayID int, strict bool) gates {
	var g gates
	label := printer.TrayLabel(trayID)
	var tray *printer.Tray
	for i := range s.AMS {
		if s.AMS[i].TrayID == trayID {
			tray = &s.AMS[i]
		}
	}
	switch {
	case tray == nil:
		g.add("tray", false, Fail, "slot "+label+" not reported by the AMS", "check the AMS is connected, or pick another slot")
		return g
	case !tray.Loaded:
		g.add("tray", false, Fail, "slot "+label+" is empty", fmt.Sprintf("load %s into AMS slot %s", need.Type, label))
		return g
	}
	exact := strings.EqualFold(tray.Type, need.Type)
	sameFamily := family(tray.Type) == family(need.Type)
	st := Pass
	switch {
	case !exact && sameFamily && !strict:
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
	var rng [2]string
	copy(rng[:], need.NozzleTempRange)
	lo, loErr := strconv.ParseFloat(rng[0], 64)
	hi, hiErr := strconv.ParseFloat(rng[1], 64)
	t, tErr := strconv.ParseFloat(need.NozzleTemp, 64)
	ok := t > 0 && (lo == 0 || t >= lo) && (hi == 0 || t <= hi)
	if strict {
		ok = loErr == nil && hiErr == nil && tErr == nil && lo > 0 && hi >= lo && t >= lo && t <= hi && !math.IsInf(t, 0) && !math.IsInf(lo, 0) && !math.IsInf(hi, 0)
	}
	g.add("nozzle_temp", ok, Fail,
		fmt.Sprintf("nozzle=%s range=%s-%s", need.NozzleTemp, rng[0], rng[1]), "fix the recipe temperatures")
	return g
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
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
