package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/testutil"
)

// pauseFixture is a recorded report from a G-code pause before a colour change: PAUSE at layer 21, A4 in the toolhead,
// print_error 0300_8013 (paused), ams_status "assist". Trays: A1 PLA Matte, A2 PETG HF, A3 PLA Basic, A4 Generic PLA.
const pauseFixture = "../printer/testdata/pause.json"

func pausedHarness(t *testing.T, mut func(m map[string]any)) *harness {
	t.Helper()
	h := newHarness(t, nil)
	h.conn = testutil.NewFakeConn(t, pauseFixture, mut)
	return h
}

// setToolhead changes ams.tray_now (and optionally ams_status) the way the printer reports a finished load.
func setToolhead(f *testutil.FakeConn, trayNow string, amsStatus int) {
	ams := f.State()["ams"].(map[string]any)
	ams["tray_now"] = trayNow
	f.Set("ams", ams)
	f.Set("ams_status", amsStatus)
}

type loadOut struct {
	Printer   string         `json:"printer"`
	Slot      string         `json:"slot"`
	Before    string         `json:"toolhead_before"`
	After     string         `json:"toolhead_after"`
	Changed   bool           `json:"changed"`
	AMSStatus string         `json:"ams_status"`
	State     string         `json:"state"`
	Ack       map[string]any `json:"ack"`
}

func TestFilamentLoadAtPause(t *testing.T) {
	h := pausedHarness(t, nil)
	h.conn.OnCommand = func(f *testutil.FakeConn, _ string, body map[string]any) map[string]any {
		f.Set("ams_status", 0x0100) // filament_change: unloading A4, then loading A1
		go func() {
			time.Sleep(100 * time.Millisecond)
			setToolhead(f, "0", 0x0100) // A1 in the toolhead, still purging: not done yet
			time.Sleep(300 * time.Millisecond)
			setToolhead(f, "0", 768)
		}()
		return map[string]any{"command": body["command"], "result": "success"}
	}
	if code := h.run("filament", "load", "--slot", "A1", "--confirm", "--json"); code != 0 {
		t.Fatalf("exit %d: %s %s", code, h.stdout.String(), h.stderr.String())
	}
	want := map[string]any{"command": "ams_change_filament", "ams_id": 0, "slot_id": 0, "target": 0, "curr_temp": 215, "tar_temp": 210}
	if len(h.conn.Commands) != 1 || !reflect.DeepEqual(h.conn.Commands[0]["print"], want) {
		t.Fatalf("commands: %v", h.conn.Commands)
	}
	var r loadOut
	h.json(&r)
	if !r.Changed || r.Before != "A4" || r.After != "A1" || r.AMSStatus != "assist" || r.State != "PAUSE" || r.Slot != "A1" {
		t.Fatalf("success only once A1 is in and the change is over: %+v", r)
	}
}

func TestFilamentLoadAlreadyLoaded(t *testing.T) {
	h := pausedHarness(t, nil)
	if code := h.run("filament", "load", "--slot", "A4", "--confirm", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	var r loadOut
	h.json(&r)
	if r.Changed || r.After != "A4" || len(h.conn.Commands) != 0 {
		t.Fatalf("nothing to send: %+v %v", r, h.conn.Commands)
	}
}

func TestFilamentLoadRefusals(t *testing.T) {
	cases := []struct {
		name string
		args []string
		mut  func(m map[string]any)
		want int
	}{
		{"no --confirm", []string{"--slot", "A1"}, nil, errfmt.ExitGate},
		{"bad slot", []string{"--slot", "Z9", "--confirm"}, nil, errfmt.ExitUsage},
		{"printing", []string{"--slot", "A1", "--confirm"}, func(m map[string]any) { m["gcode_state"] = "RUNNING" }, errfmt.ExitGate},
		{"preparing", []string{"--slot", "A1", "--confirm"}, func(m map[string]any) { m["gcode_state"] = "PREPARE" }, errfmt.ExitGate},
		{"dev mode off", []string{"--slot", "A1", "--confirm"}, func(m map[string]any) { m["fun"] = "20011A30F9CFB" }, errfmt.ExitForbidden},
		{"change in progress", []string{"--slot", "A1", "--confirm"}, func(m map[string]any) { m["ams_status"] = 0x0100 }, errfmt.ExitGate},
		{"loading stage", []string{"--slot", "A1", "--confirm"}, func(m map[string]any) { m["stg_cur"] = 24 }, errfmt.ExitGate},
		{"blocking hms", []string{"--slot", "A1", "--confirm"}, func(m map[string]any) {
			m["hms"] = []any{map[string]any{"attr": 0x07000200, "code": 0x00020001}}
		}, errfmt.ExitGate},
		{"other material than the paused job", []string{"--slot", "A2", "--confirm"}, nil, errfmt.ExitGate}, // A2 PETG, job PLA
		{"empty slot", []string{"--slot", "B1", "--confirm"}, nil, errfmt.ExitGate},
		{"paused, toolhead empty: job material unknown", []string{"--slot", "A1", "--confirm"}, func(m map[string]any) {
			m["ams"].(map[string]any)["tray_now"] = "255"
		}, errfmt.ExitGate},
		{"paused on the external spool: job material unknown", []string{"--slot", "A1", "--confirm"}, func(m map[string]any) {
			m["ams"].(map[string]any)["tray_now"] = "254"
		}, errfmt.ExitGate},
		{"unknown state", []string{"--slot", "A1", "--confirm"}, func(m map[string]any) { m["gcode_state"] = "INIT" }, errfmt.ExitGate},
		{"no state reported", []string{"--slot", "A1", "--confirm"}, func(m map[string]any) { delete(m, "gcode_state") }, errfmt.ExitGate},
	}
	for _, c := range cases {
		h := pausedHarness(t, c.mut)
		if code := h.run(append([]string{"filament", "load", "--json", "--timeout", "1s"}, c.args...)...); code != c.want {
			t.Errorf("%s: exit %d want %d: %s", c.name, code, c.want, h.stdout.String())
		}
		if len(h.conn.Commands) != 0 {
			t.Errorf("%s: published %v", c.name, h.conn.Commands)
		}
	}
}

func TestFilamentLoadIdleAllowsOtherMaterial(t *testing.T) {
	h := pausedHarness(t, func(m map[string]any) { m["gcode_state"] = "FINISH" })
	h.conn.OnCommand = func(f *testutil.FakeConn, _ string, body map[string]any) map[string]any {
		setToolhead(f, "1", 768)
		return map[string]any{"command": body["command"], "result": "success"}
	}
	if code := h.run("filament", "load", "--slot", "A2", "--confirm", "--json"); code != 0 {
		t.Fatalf("idle: no job to match, exit %d: %s", code, h.stderr.String())
	}
}

func TestFilamentLoadNotConfirmed(t *testing.T) {
	cases := []struct {
		name  string
		react func(f *testutil.FakeConn)
		want  int
	}{
		{"never loads", func(*testutil.FakeConn) {}, errfmt.ExitTimeout},
		{"stuck in the change", func(f *testutil.FakeConn) { setToolhead(f, "0", 0x0100) }, errfmt.ExitTimeout},
		{"new hms during the change", func(f *testutil.FakeConn) {
			f.Set("hms", []any{map[string]any{"attr": 0x07000200, "code": 0x00020001}})
		}, errfmt.ExitError},
		{"new print_error during the change", func(f *testutil.FakeConn) { f.Set("print_error", 0x07008011) }, errfmt.ExitError},
	}
	for _, c := range cases {
		h := pausedHarness(t, nil)
		h.conn.OnCommand = func(f *testutil.FakeConn, _ string, body map[string]any) map[string]any {
			c.react(f)
			return map[string]any{"command": body["command"], "result": "success"}
		}
		if code := h.run("filament", "load", "--slot", "A1", "--confirm", "--timeout", "1s", "--json"); code != c.want {
			t.Errorf("%s: exit %d want %d: %s", c.name, code, c.want, h.stdout.String())
		}
	}
	h := pausedHarness(t, nil)
	h.conn.OnCommand = func(_ *testutil.FakeConn, _ string, body map[string]any) map[string]any {
		return map[string]any{"command": body["command"], "result": "fail", "reason": "not allowed"}
	}
	if code := h.run("filament", "load", "--slot", "A1", "--confirm", "--timeout", "1s"); code != errfmt.ExitError {
		t.Errorf("rejected: exit %d", code)
	}
}

func TestResumeConfirmsRunning(t *testing.T) {
	h := pausedHarness(t, nil)
	h.conn.OnCommand = func(_ *testutil.FakeConn, _ string, body map[string]any) map[string]any {
		return map[string]any{"command": body["command"], "result": "success"} // acked, but the printer stays paused
	}
	if code := h.run("print", "resume", "--confirm", "--wait", "1s", "--json"); code != errfmt.ExitTimeout {
		t.Fatalf("resume must not report success while still PAUSE: exit %d %s", code, h.stdout.String())
	}
	h = pausedHarness(t, nil)
	h.conn.OnCommand = func(f *testutil.FakeConn, _ string, body map[string]any) map[string]any {
		f.Set("gcode_state", "RUNNING")
		return map[string]any{"command": body["command"], "result": "success"}
	}
	if code := h.run("print", "resume", "--confirm", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	var r map[string]any
	h.json(&r)
	if r["state_before"] != "PAUSE" || r["state_after"] != "RUNNING" {
		t.Fatalf("%v", r)
	}
}

func TestResumeAlwaysVerifies(t *testing.T) {
	for _, wait := range []string{"0s", "-1s"} {
		h := pausedHarness(t, nil)
		h.conn.OnCommand = func(_ *testutil.FakeConn, _ string, body map[string]any) map[string]any {
			return map[string]any{"command": body["command"], "result": "success"}
		}
		if code := h.run("print", "resume", "--confirm", "--wait", wait); code != errfmt.ExitUsage || len(h.conn.Commands) != 0 {
			t.Errorf("--wait %s: exit %d, commands %v", wait, code, h.conn.Commands)
		}
	}
}

func TestResumePollsUntilRunning(t *testing.T) {
	h := pausedHarness(t, nil)
	h.conn.OnCommand = func(f *testutil.FakeConn, _ string, body map[string]any) map[string]any {
		go func() {
			time.Sleep(700 * time.Millisecond)
			f.Set("gcode_state", "RUNNING")
		}()
		return map[string]any{"command": body["command"], "result": "success"}
	}
	if code := h.run("print", "resume", "--confirm", "--wait", "5s", "-q"); code != 0 || strings.TrimSpace(h.stdout.String()) != "RUNNING" {
		t.Fatalf("exit %d %q", code, h.stdout.String())
	}
}

func TestResumeEmptyToolheadOnlyGatedWhereVerified(t *testing.T) {
	h := pausedHarness(t, func(m map[string]any) { m["ams"].(map[string]any)["tray_now"] = "255" })
	cfg := filepath.Join(h.dir, "config.toml")
	_ = os.WriteFile(cfg, []byte("default_printer = \"shop\"\n[printers.shop]\nhost = \"127.0.0.1\"\nserial = \"00M00A000000001\"\nmodel = \"H2D\"\nnozzle = 0.4\nplate = \"textured_plate\"\n"), 0o600)
	h.conn.OnCommand = func(f *testutil.FakeConn, _ string, body map[string]any) map[string]any {
		f.Set("gcode_state", "RUNNING")
		return map[string]any{"command": body["command"], "result": "success"}
	}
	if code := h.run("print", "resume", "--confirm"); code != 0 {
		t.Fatalf("H2D: tray_now 255 isn't known to mean an empty toolhead there; exit %d: %s", code, h.stderr.String())
	}
}

func TestResumeRefusedMidChange(t *testing.T) {
	for name, mut := range map[string]func(m map[string]any){
		"filament change in progress": func(m map[string]any) { m["ams_status"] = 0x0100 },
		"empty toolhead":              func(m map[string]any) { m["ams"].(map[string]any)["tray_now"] = "255" },
	} {
		h := pausedHarness(t, mut)
		if code := h.run("print", "resume", "--confirm"); code != errfmt.ExitGate || len(h.conn.Commands) != 0 {
			t.Errorf("%s: exit %d, commands %v", name, code, h.conn.Commands)
		}
	}
}

func TestTwoFilamentSend(t *testing.T) {
	h := newHarness(t, nil)
	f := testutil.FromDir(t, "../job/testdata/two_filament", h.dir, "two")
	if code := h.run("print", "send", f, "--slot", "A4", "--slot", "A1", "--dry-run", "--json"); code != 0 {
		t.Fatalf("exit %d: %s %s", code, h.stdout.String(), h.stderr.String())
	}
	var r sendResult
	h.json(&r)
	p := r.Plan.MQTT.Payload["print"].(map[string]any)
	if !reflect.DeepEqual(p["ams_mapping"], []any{float64(3), float64(0)}) || r.Preflight.Result != "PASS" {
		t.Fatalf("payload %v preflight %+v", p["ams_mapping"], r.Preflight)
	}
	if !reflect.DeepEqual(r.Preflight.Slots, []string{"A4", "A1"}) || r.Preflight.Slot != "A4" {
		t.Fatalf("%+v", r.Preflight)
	}
	if code := h.run("preflight", f, "--slot", "A4", "-q"); code != errfmt.ExitGate {
		t.Fatalf("two filaments, one slot: exit %d", code)
	}
	if code := h.run("preflight", f, "--slot", "A4", "--slot", "4"); code != errfmt.ExitUsage {
		t.Fatalf("same slot twice: exit %d", code)
	}
}
