package preflight

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/voska/bambu/internal/config"
	"github.com/voska/bambu/internal/job"
	"github.com/voska/bambu/internal/printer"
	"github.com/voska/bambu/internal/testutil"
)

func idleStatus(t *testing.T) printer.Status {
	t.Helper()
	b, err := os.ReadFile("../printer/testdata/pushall.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	m["gcode_state"] = "FINISH"
	m["hms"] = []any{}
	return printer.Summarize(m)
}

var shop = &config.Printer{Name: "shop", Model: "X1C", Nozzle: 0.4, Plate: "textured_plate"}

func run(t *testing.T, o testutil.Opts, slot int, mut func(*printer.Status)) Result {
	t.Helper()
	j, err := job.Inspect(testutil.Write(t, t.TempDir(), "part", o), 1)
	if err != nil {
		t.Fatal(err)
	}
	s := idleStatus(t)
	if mut != nil {
		mut(&s)
	}
	return Run(Input{Printer: shop, Job: j, Status: s, TrayIDs: []int{slot}, FTPSCheck: func() error { return nil }})
}

func status(r Result, gate string) string {
	for _, g := range r.Gates {
		if g.Gate == gate {
			return g.Status
		}
	}
	return ""
}

func TestHappyPathWithWarnings(t *testing.T) {
	// slot A1 = PLA Matte (GFA01), 330 g; job sliced for PLA Basic (GFA00)
	r := run(t, testutil.Opts{}, 0, nil)
	if r.Result != Pass || len(r.Failed) != 0 {
		t.Fatalf("want PASS, got %+v", r)
	}
	if status(r, "filament_profile") != Warn || !slices.Contains(r.Warnings, "filament_profile") {
		t.Fatalf("profile mismatch should WARN: %+v", r.Gates)
	}
}

func TestGenericPLAInGFL99Slot(t *testing.T) {
	r := run(t, testutil.Opts{Filaments: []string{"PLA:GFL99:13.5"}}, 3, nil)
	if status(r, "filament_type") != Pass || status(r, "filament_profile") != Pass {
		t.Fatalf("GFL99 tray must match a Generic PLA slice: %+v", r.Gates)
	}
	if status(r, "filament_amount") != Warn || r.Result != Pass {
		t.Fatalf("unknown remaining should WARN only: %+v", r)
	}
}

func TestStrictTemperatureBounds(t *testing.T) {
	for _, bounds := range [][]string{nil, {"bad", "240"}, {"190", ""}, {"NaN", "240"}, {"190", "+Inf"}, {"250", "190"}} {
		j, err := job.Inspect(testutil.Write(t, t.TempDir(), "p", testutil.Opts{}), 1)
		if err != nil {
			t.Fatal(err)
		}
		j.Filaments[0].NozzleTempRange = bounds
		r := Run(Input{Printer: shop, Job: j, Status: idleStatus(t), TrayIDs: []int{0}, Strict: true})
		if status(r, "nozzle_temp") != Fail {
			t.Fatalf("strict temp accepted %v: %+v", bounds, r)
		}
	}
}

func TestFailures(t *testing.T) {
	cases := []struct {
		name string
		opts testutil.Opts
		slot int
		mut  func(*printer.Status)
		gate string
	}{
		{"busy", testutil.Opts{}, 0, func(s *printer.Status) { s.State = "RUNNING" }, "printer_idle"},
		{"blocking hms", testutil.Opts{}, 0, func(s *printer.Status) {
			s.Errors = printer.Errors{HMS: []printer.HMS{printer.DecodeHMS(0x05000500, 0x00010007)}, Blocking: true}
		}, "no_errors"},
		{"dev mode off", testutil.Opts{}, 0, func(s *printer.Status) { f := false; s.DevMode = &f }, "developer_mode"},
		{"dev mode unknown", testutil.Opts{}, 0, func(s *printer.Status) { s.DevMode = nil }, "developer_mode"},
		{"no sd", testutil.Opts{}, 0, func(s *printer.Status) { s.SDCard = false }, "sdcard"},
		{"corrupt", testutil.Opts{CorruptMD5: true}, 0, nil, "file_integrity"},
		{"other printer", testutil.Opts{PrinterModel: "Bambu Lab P1S"}, 0, nil, "sliced_for_printer"},
		{"multi", testutil.Opts{Filaments: []string{"PLA:GFA00:4", "PLA:GFA01:4"}}, 0, nil, "single_filament"},
		{"nozzle", testutil.Opts{Nozzle: "0.6"}, 0, nil, "nozzle_diameter"},
		{"plate", testutil.Opts{BedType: "cool_plate"}, 0, nil, "bed_type"},
		{"empty slot", testutil.Opts{}, 1, nil, "tray"},
		{"wrong type", testutil.Opts{Filaments: []string{"PETG:GFG00:8"}}, 0, nil, "filament_type"},
		{"not enough", testutil.Opts{}, 2, nil, "filament_amount"}, // A3: ~13 g, needs 8.1*1.15+5
		{"temp", testutil.Opts{NozzleTemp: "300"}, 0, nil, "nozzle_temp"},
	}
	for _, c := range cases {
		r := run(t, c.opts, c.slot, c.mut)
		if r.Result != Fail || !slices.Contains(r.Failed, c.gate) {
			t.Errorf("%s: want FAIL on %s, got %+v", c.name, c.gate, r.Failed)
		}
	}
}

func TestInfoHMSDoesNotBlock(t *testing.T) {
	r := run(t, testutil.Opts{}, 0, func(s *printer.Status) {
		s.Errors = printer.Errors{HMS: []printer.HMS{printer.DecodeHMS(0x0C000300, 0x0003000B)}}
	})
	if status(r, "no_errors") != Pass {
		t.Fatalf("informational HMS must not block: %+v", r.Gates)
	}
}

func TestFamilyWarn(t *testing.T) {
	r := run(t, testutil.Opts{Filaments: []string{"PLA-CF:GFA50:8"}}, 0, nil)
	if status(r, "filament_type") != Warn {
		t.Fatalf("PLA vs PLA-CF should WARN: %+v", r.Gates)
	}
}

func TestFTPSFailure(t *testing.T) {
	j, _ := job.Inspect(testutil.Write(t, t.TempDir(), "p", testutil.Opts{}), 1)
	r := Run(Input{Printer: shop, Job: j, Status: idleStatus(t), TrayIDs: []int{0}, FTPSCheck: func() error { return errors.New("login refused") }})
	if !slices.Contains(r.Failed, "ftps_login") {
		t.Fatal("ftps failure must FAIL")
	}
}

func runTwo(t *testing.T, trays []int, mut func(*printer.Status)) Result {
	t.Helper()
	// recorded two-filament slice: Generic PLA (GFL99) 16.98 g, then PLA Matte (GFA01) 3.12 g from layer 21
	j, err := job.Inspect(testutil.FromDir(t, "../job/testdata/two_filament", t.TempDir(), "two"), 1)
	if err != nil {
		t.Fatal(err)
	}
	s := idleStatus(t)
	if mut != nil {
		mut(&s)
	}
	return Run(Input{Printer: shop, Job: j, Status: s, TrayIDs: trays, FTPSCheck: func() error { return nil }})
}

func gateFor(r Result, gate string, filament int) *Gate {
	for i := range r.Gates {
		if r.Gates[i].Gate == gate && r.Gates[i].Filament == filament {
			return &r.Gates[i]
		}
	}
	return nil
}

func TestTwoFilaments(t *testing.T) {
	// A4 = Generic PLA GFL99 (remaining unknown), A1 = PLA Matte GFA01 ~330 g
	r := runTwo(t, []int{3, 0}, nil)
	if r.Result != Pass || !slices.Equal(r.Slots, []string{"A4", "A1"}) || !slices.Equal(r.TrayIDs, []int{3, 0}) || r.Slot != "A4" || r.TrayID != 3 {
		t.Fatalf("want PASS on A4+A1: %+v", r)
	}
	if !slices.Equal(r.Warnings, []string{"filament_amount"}) || gateFor(r, "filament_amount", 1).Status != Warn {
		t.Fatalf("only A4's unknown amount should WARN: %+v", r.Gates)
	}
	for _, g := range []string{"filament_type", "filament_profile", "filament_amount", "nozzle_temp"} {
		if gateFor(r, g, 1) == nil || gateFor(r, g, 2) == nil {
			t.Errorf("%s must run once per filament: %+v", g, r.Gates)
		}
	}
	if d := gateFor(r, "filament_amount", 2).Detail; d != "slot A1 ~330 g (33%); job needs 3.1 g (+15%, +5 g)" {
		t.Errorf("filament 2 is checked against A1: %q", d)
	}
}

func TestTwoFilamentFailures(t *testing.T) {
	cases := []struct {
		name     string
		trays    []int
		mut      func(*printer.Status)
		gate     string
		filament int
	}{
		{"one slot for two filaments", []int{3}, nil, "single_filament", 0},
		{"second slot empty", []int{3, 1}, nil, "tray", 2},
		{"second slot too low", []int{3, 2}, func(s *printer.Status) { g := 2; s.AMS[2].RemainG = &g }, "filament_amount", 2},
		{"second slot wrong material", []int{3, 0}, func(s *printer.Status) { s.AMS[0].Type = "PETG" }, "filament_type", 2},
	}
	for _, c := range cases {
		r := runTwo(t, c.trays, c.mut)
		if g := gateFor(r, c.gate, c.filament); r.Result != Fail || g == nil || g.Status != Fail {
			t.Errorf("%s: want FAIL on %s (filament %d), got %+v", c.name, c.gate, c.filament, r.Gates)
		}
	}
	if d := gateFor(runTwo(t, []int{3}, nil), "single_filament", 0).Detail; d != "2 filaments in plate, 1 slot given" {
		t.Errorf("detail: %q", d)
	}
}

func TestSingleFilamentGatesUnchanged(t *testing.T) {
	r := run(t, testutil.Opts{}, 0, nil)
	for _, g := range r.Gates {
		if g.Filament != 0 {
			t.Fatalf("single-filament gates carry no filament number: %+v", g)
		}
	}
	if !slices.Equal(r.Slots, []string{"A1"}) || !slices.Equal(r.TrayIDs, []int{0}) || r.Slot != "A1" || r.TrayID != 0 {
		t.Fatalf("%+v", r)
	}
}
