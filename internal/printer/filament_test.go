package printer

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// pause.json is a recorded report from a G-code pause before a colour change: A4 in the toolhead, layer 21.
func loadPause(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile("testdata/pause.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSummarizePause(t *testing.T) {
	s := Summarize(loadPause(t))
	if s.State != "PAUSE" || s.AMSStatus != "assist" || s.ActiveTray != "3" || ToolheadSlot(s.ActiveTray) != "A4" {
		t.Fatalf("%+v", s)
	}
	if s.Errors.PrintError != "0300_8013" || FilamentChanging(s) {
		t.Fatalf("pause: %+v", s.Errors)
	}
	if Summarize(load(t)).AMSStatus != "" {
		t.Fatal("ams_status must stay empty when not reported")
	}
}

func TestAMSStatusName(t *testing.T) {
	for v, want := range map[int]string{0: "idle", 0x0100: "filament_change", 0x0103: "filament_change", 768: "assist", 0x4000: "ams_status_0x40"} {
		if got := AMSStatusName(v); got != want {
			t.Errorf("%#x: %s want %s", v, got, want)
		}
	}
}

func TestFilamentChanging(t *testing.T) {
	cases := []struct {
		s    Status
		want bool
	}{
		{Status{AMSStatus: "assist", Stage: "printing"}, false},
		{Status{AMSStatus: "filament_change"}, true},
		{Status{AMSStatus: "assist", Stage: "filament_loading"}, true},
		{Status{Stage: "filament_unloading"}, true},
		{Status{Stage: "changing_filament"}, true},
	}
	for _, c := range cases {
		if got := FilamentChanging(c.s); got != c.want {
			t.Errorf("%+v: %v", c.s, got)
		}
	}
}

func TestToolheadSlot(t *testing.T) {
	for in, want := range map[string]string{"0": "A1", "3": "A4", "5": "B2", "254": "external", "255": "", "": ""} {
		if got := ToolheadSlot(in); got != want {
			t.Errorf("%q: %q want %q", in, got, want)
		}
	}
}

func TestAMSChangeFilamentVerifiedPayload(t *testing.T) {
	s := Summarize(loadPause(t))
	a1, a4 := &s.AMS[0], &s.AMS[3]
	got := AMSChangeFilament(a1.TrayID, LoadTemp(a4), LoadTemp(a1))
	want := map[string]any{"command": "ams_change_filament", "ams_id": 0, "slot_id": 0, "target": 0, "curr_temp": 215, "tar_temp": 210}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload drift from the one verified on the printer:\n got %v\nwant %v", got, want)
	}
	if b := AMSChangeFilament(6, 210, 240); b["ams_id"] != 1 || b["slot_id"] != 2 || b["target"] != 6 {
		t.Fatalf("B3: %v", b)
	}
	if LoadTemp(nil) != 210 || LoadTemp(&Tray{}) != 210 {
		t.Fatal("no range: Studio's default 210")
	}
}
