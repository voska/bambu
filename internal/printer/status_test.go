package printer

import (
	"encoding/json"
	"os"
	"testing"
)

func load(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile("testdata/pushall.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSummarize(t *testing.T) {
	s := Summarize(load(t))
	if s.State != "RUNNING" || s.Stage != "printing" || s.Job.Layer != 44 || s.Job.TotalLayers != 113 || s.Job.Name != "bracket_v2" {
		t.Fatalf("job/state: %+v", s)
	}
	if s.DevMode == nil || !*s.DevMode {
		t.Fatal("dev mode should be on (bit 0x20000000 clear)")
	}
	if s.Nozzle.Material != "hardened_steel" || s.Nozzle.Diameter != "0.4" {
		t.Fatalf("nozzle: %+v", s.Nozzle)
	}
	if len(s.AMS) != 4 {
		t.Fatalf("ams: %d trays", len(s.AMS))
	}
	a1, a2, a3, a4 := s.AMS[0], s.AMS[1], s.AMS[2], s.AMS[3]
	if a1.Slot != "A1" || !a1.Loaded || a1.FilamentID != "GFA01" || *a1.RemainG != 330 || a1.Color != "#FF6A13" {
		t.Fatalf("A1: %+v", a1)
	}
	if a2.Loaded || a2.Slot != "A2" {
		t.Fatalf("A2 should be empty: %+v", a2)
	}
	if *a3.RemainG != 13 || *a3.RemainPct != 5 {
		t.Fatalf("A3 grams: %+v", a3)
	}
	if a4.RemainPct != nil || a4.RemainG != nil {
		t.Fatalf("A4 remain unknown: %+v", a4)
	}
	if len(s.Errors.HMS) != 1 || !s.Errors.HMS[0].Informational || s.Errors.Blocking {
		t.Fatalf("info HMS must not block: %+v", s.Errors)
	}
	if !s.Liveview || !s.SDCard || s.External != "" {
		t.Fatalf("flags: %+v", s)
	}
	if s.Protections.FirstLayerInspector == nil || !*s.Protections.FirstLayerInspector {
		t.Fatal("protections")
	}
}

func TestDevModeOff(t *testing.T) {
	m := load(t)
	m["fun"] = "20011A30F9CFB"
	if s := Summarize(m); s.DevMode == nil || *s.DevMode {
		t.Fatal("dev mode should be off")
	}
	delete(m, "fun")
	if s := Summarize(m); s.DevMode != nil {
		t.Fatal("dev mode unknown without fun")
	}
}

func TestBlocking(t *testing.T) {
	m := load(t)
	m["hms"] = []any{map[string]any{"attr": float64(0x05000500), "code": float64(0x00010007)}}
	s := Summarize(m)
	if !s.Errors.Blocking || s.Errors.HMS[0].Code != "0500_0500_0001_0007" || s.Errors.HMS[0].Severity != "fatal" || s.Errors.HMS[0].Module != "mainboard" {
		t.Fatalf("%+v", s.Errors)
	}
	m["hms"] = []any{}
	m["print_error"] = float64(0x0500400E)
	s = Summarize(m)
	if !s.Errors.Blocking || s.Errors.PrintError != "0500_400E" {
		t.Fatalf("%+v", s.Errors)
	}
}

func TestDecodeHMSInfoSeverity(t *testing.T) {
	h := DecodeHMS(0x07000200, 0x00040001)
	if !h.Informational || h.Severity != "info" || h.Module != "ams" {
		t.Fatalf("%+v", h)
	}
	if h.URL != "https://wiki.bambulab.com/en/x1/troubleshooting/hmscode/0700_0200_0004_0001" {
		t.Fatal(h.URL)
	}
}

func TestParseSlot(t *testing.T) {
	cases := map[string]int{"1": 0, "4": 3, "a1": 0, "A4": 3, "B1": 4, "D4": 15}
	for in, want := range cases {
		got, err := ParseSlot(in)
		if err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	if TrayLabel(4) != "B1" || TrayLabel(3) != "A4" {
		t.Error("TrayLabel")
	}
	for _, bad := range []string{"0", "5", "E1", "ext", "", "A0"} {
		if _, err := ParseSlot(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestNozzleMaterial(t *testing.T) {
	for in, want := range map[string]string{"HX01": "hardened_steel", "HH01": "high_flow_hardened_steel", "HS00": "stainless_steel", "hardened_steel": "hardened_steel", "": ""} {
		if got := NozzleMaterial(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

func TestLookupModel(t *testing.T) {
	for _, in := range []string{"X1C", "x1c", "Bambu Lab X1 Carbon", "X1 Carbon", "BL-P001"} {
		m, ok := LookupModel(in)
		if !ok || m.Alias != "X1C" {
			t.Errorf("%s -> %+v", in, m)
		}
	}
	if m, _ := LookupModel("A1 mini"); m.Alias != "A1M" {
		t.Error("A1 mini")
	}
	if _, ok := LookupModel("Ender 3"); ok {
		t.Error("unknown model accepted")
	}
	m, _ := LookupModel("X1C")
	if m.MachinePreset("0.4") != "Bambu Lab X1 Carbon 0.4 nozzle" {
		t.Error(m.MachinePreset("0.4"))
	}
}

func TestStageName(t *testing.T) {
	if StageName(34) != "paused_first_layer_error" || StageName(-1) != "idle" || StageName(999) != "stage_999" {
		t.Fatal("stage names")
	}
}
