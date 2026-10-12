package cmd

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/printer"
	"github.com/voska/bambu/internal/testutil"
)

var amsSetArgs = []string{"ams", "set", "--slot", "1", "--type", "PLA", "--filament-id", "GFL99", "--color", "#c8a26b", "--nozzle-temp-min", "200", "--nozzle-temp-max", "230"}

func amsHarness(t *testing.T, mut func(map[string]any)) *harness {
	t.Helper()
	return newHarness(t, func(m map[string]any) {
		m["ams"].(map[string]any)["tray_exist_bits"] = "f"
		m["ams_status"] = 0
		if mut != nil {
			mut(m)
		}
	})
}

func rawTray(m map[string]any, unit, slot int) map[string]any {
	for _, a := range m["ams"].(map[string]any)["ams"].([]any) {
		am := a.(map[string]any)
		if am["id"] != strconv.Itoa(unit) {
			continue
		}
		for _, t := range am["tray"].([]any) {
			tr := t.(map[string]any)
			if tr["id"] == strconv.Itoa(slot) {
				return tr
			}
		}
	}
	return nil
}

func applyTraySetting(f *testutil.FakeConn, body map[string]any) {
	m := f.State()
	t := rawTray(m, body["ams_id"].(int), body["slot_id"].(int))
	for _, k := range []string{"tray_type", "tray_info_idx", "tray_color", "nozzle_temp_min", "nozzle_temp_max"} {
		t[k] = body[k]
	}
	f.Set("ams", m["ams"])
}

func TestAMSSetUnconfiguredSpool(t *testing.T) {
	h := amsHarness(t, func(m map[string]any) {
		t := rawTray(m, 0, 0)
		for k := range t {
			if k != "id" {
				delete(t, k)
			}
		}
		m["ams"].(map[string]any)["tray_exist_bits"] = "f"
	})
	before := h.conn.State()
	h.conn.OnCommand = func(f *testutil.FakeConn, section string, body map[string]any) map[string]any {
		if section != "print" {
			t.Fatalf("wrong section: %s", section)
		}
		applyTraySetting(f, body)
		return map[string]any{"result": "success"}
	}
	if code := h.run(append(amsSetArgs, "--confirm", "--json")...); code != 0 {
		t.Fatalf("exit %d: %s %s", code, h.stdout.String(), h.stderr.String())
	}
	want := map[string]any{
		"command": "ams_filament_setting", "ams_id": 0, "slot_id": 0, "tray_id": 0,
		"tray_info_idx": "GFL99", "setting_id": "", "tray_type": "PLA", "tray_color": "C8A26BFF", "nozzle_temp_min": 200, "nozzle_temp_max": 230,
	}
	if len(h.conn.Commands) != 1 || !reflect.DeepEqual(h.conn.Commands[0]["print"], want) {
		t.Fatalf("commands: %v", h.conn.Commands)
	}
	var out struct {
		Slot     string       `json:"slot"`
		Sent     bool         `json:"sent"`
		Verified bool         `json:"verified"`
		After    printer.Tray `json:"after"`
	}
	h.json(&out)
	if !out.Sent || !out.Verified || out.Slot != "A1" || out.After.FilamentID != "GFL99" || out.After.Color != "#C8A26B" || out.After.TempMin != 200 || out.After.TempMax != 230 {
		t.Fatalf("read-back: %+v", out)
	}
	after := h.conn.State()
	for slot := 1; slot < 4; slot++ {
		if !reflect.DeepEqual(rawTray(before, 0, slot), rawTray(after, 0, slot)) {
			t.Errorf("changed another slot: %d", slot)
		}
	}
	if after["ams"].(map[string]any)["tray_now"] != before["ams"].(map[string]any)["tray_now"] {
		t.Fatal("metadata edit changed the toolhead filament")
	}
}

func TestAMSSetDryRunAndIdempotence(t *testing.T) {
	h := amsHarness(t, nil)
	if code := h.run(append(amsSetArgs, "--dry-run", "--json")...); code != 0 || len(h.conn.Commands) != 0 {
		t.Fatalf("dry-run exit %d: %s %v", code, h.stdout.String(), h.conn.Commands)
	}
	var plan struct {
		DryRun   bool                      `json:"dry_run"`
		Verified bool                      `json:"verified"`
		Payload  map[string]map[string]any `json:"payload"`
	}
	h.json(&plan)
	if !plan.DryRun || plan.Verified || plan.Payload["print"]["tray_color"] != "C8A26BFF" {
		t.Fatalf("plan: %+v", plan)
	}
	h = amsHarness(t, func(m map[string]any) {
		t := rawTray(m, 0, 0)
		t["tray_info_idx"], t["tray_color"] = "GFL99", "c8a26bff"
		t["nozzle_temp_min"], t["nozzle_temp_max"] = "200", "230"
	})
	if code := h.run(append(amsSetArgs, "--confirm", "--json")...); code != 0 || len(h.conn.Commands) != 0 {
		t.Fatalf("idempotence exit %d: %s %v", code, h.stdout.String(), h.conn.Commands)
	}
	var result struct{ Sent, Verified bool }
	h.json(&result)
	if result.Sent || !result.Verified {
		t.Fatalf("already configured: %+v", result)
	}
}

func TestAMSSetNeverTrustsAck(t *testing.T) {
	for _, field := range []string{"tray_type", "tray_info_idx", "tray_color", "nozzle_temp_min", "nozzle_temp_max"} {
		t.Run(field, func(t *testing.T) {
			h := amsHarness(t, nil)
			h.conn.OnCommand = func(f *testutil.FakeConn, _ string, body map[string]any) map[string]any {
				applyTraySetting(f, body)
				m := f.State()
				rawTray(m, 0, 0)[field] = "wrong"
				f.Set("ams", m["ams"])
				return map[string]any{"result": "success"}
			}
			if code := h.run(append(amsSetArgs, "--confirm", "--wait", "10ms", "--json")...); code != errfmt.ExitTimeout {
				t.Fatalf("ack cannot confirm %s: exit %d %s", field, code, h.stdout.String())
			}
			if !strings.Contains(h.stdout.String(), "unverified") {
				t.Fatalf("must report submitted, unverified: %s", h.stdout.String())
			}
		})
	}
}

func TestAMSSetMissingAckStillVerifies(t *testing.T) {
	h := amsHarness(t, nil)
	h.conn.OnCommand = func(f *testutil.FakeConn, _ string, body map[string]any) map[string]any {
		applyTraySetting(f, body)
		return nil
	}
	if code := h.run(append(amsSetArgs, "--confirm", "--plain")...); code != 0 || !strings.Contains(h.stdout.String(), "A1") {
		t.Fatalf("exit %d: %s", code, h.stdout.String())
	}
}

func TestAMSSetRefusals(t *testing.T) {
	cases := []struct {
		name string
		args []string
		mut  func(map[string]any)
		want int
	}{
		{"no confirmation", nil, nil, errfmt.ExitGate},
		{"conflicting modes", []string{"--dry-run", "--confirm"}, nil, errfmt.ExitUsage},
		{"printing", []string{"--confirm"}, func(m map[string]any) { m["gcode_state"] = "RUNNING" }, errfmt.ExitGate},
		{"paused", []string{"--confirm"}, func(m map[string]any) { m["gcode_state"] = "PAUSE" }, errfmt.ExitGate},
		{"no state", []string{"--confirm"}, func(m map[string]any) { delete(m, "gcode_state") }, errfmt.ExitGate},
		{"AMS busy", []string{"--confirm"}, func(m map[string]any) { m["ams_status"] = 0x0100 }, errfmt.ExitGate},
		{"AMS RFID scan", []string{"--confirm"}, func(m map[string]any) { m["ams_status"] = 0x0200 }, errfmt.ExitGate},
		{"dev mode off", []string{"--confirm"}, func(m map[string]any) { m["fun"] = "20011A30F9CFB" }, errfmt.ExitForbidden},
		{"blocking error", []string{"--confirm"}, func(m map[string]any) { m["print_error"] = 1 }, errfmt.ExitGate},
		{"missing AMS", []string{"--confirm"}, func(m map[string]any) { m["ams"] = map[string]any{} }, errfmt.ExitGate},
		{"physically empty", []string{"--confirm"}, func(m map[string]any) { m["ams"].(map[string]any)["tray_exist_bits"] = "e" }, errfmt.ExitGate},
		{"unknown presence", []string{"--confirm"}, func(m map[string]any) { delete(m["ams"].(map[string]any), "tray_exist_bits") }, errfmt.ExitGate},
		{"RFID", []string{"--confirm"}, func(m map[string]any) { rawTray(m, 0, 0)["tag_uid"] = "FAKE123" }, errfmt.ExitGate},
		{"spool UUID", []string{"--confirm"}, func(m map[string]any) { rawTray(m, 0, 0)["tray_uuid"] = "FAKE123" }, errfmt.ExitGate},
		{"dry-run busy", []string{"--dry-run"}, func(m map[string]any) { m["gcode_state"] = "RUNNING" }, errfmt.ExitGate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := amsHarness(t, tc.mut)
			if code := h.run(append(amsSetArgs, tc.args...)...); code != tc.want || len(h.conn.Commands) != 0 {
				t.Fatalf("exit %d, want %d: %s %v", code, tc.want, h.stderr.String(), h.conn.Commands)
			}
		})
	}
}

func TestAMSSetSecondUnitAndFullColor(t *testing.T) {
	h := amsHarness(t, func(m map[string]any) {
		ams := m["ams"].(map[string]any)
		ams["tray_exist_bits"] = "ff"
		ams["ams"] = append(ams["ams"].([]any), map[string]any{"id": "1", "tray": []any{
			map[string]any{"id": "1", "tray_type": "PLA", "tray_info_idx": "GFL99", "tray_color": "C8A26B00", "nozzle_temp_min": "200", "nozzle_temp_max": "230", "tag_uid": "0000000000000000", "tray_uuid": "00000000000000000000000000000000"},
		}})
	})
	h.conn.OnCommand = func(f *testutil.FakeConn, _ string, body map[string]any) map[string]any {
		applyTraySetting(f, body)
		return nil
	}
	args := append([]string(nil), amsSetArgs...)
	args[3] = "B2"
	if code := h.run(append(args, "--confirm", "--quiet")...); code != 0 || strings.TrimSpace(h.stdout.String()) != "B2" {
		t.Fatalf("exit %d: %s %s", code, h.stdout.String(), h.stderr.String())
	}
	if len(h.conn.Commands) != 1 {
		t.Fatalf("same RGB but wrong alpha needs a write: %v", h.conn.Commands)
	}
	body := h.conn.Commands[0]["print"].(map[string]any)
	if body["ams_id"] != 1 || body["slot_id"] != 1 || body["tray_id"] != 1 {
		t.Fatalf("second AMS uses local tray_id: %v", body)
	}
}

func TestAMSSetRejected(t *testing.T) {
	h := amsHarness(t, nil)
	h.conn.OnCommand = func(*testutil.FakeConn, string, map[string]any) map[string]any {
		return map[string]any{"result": "fail", "reason": "not allowed"}
	}
	if code := h.run(append(amsSetArgs, "--confirm", "--json")...); code != errfmt.ExitError {
		t.Fatalf("exit %d: %s", code, h.stdout.String())
	}
}

type refreshTrayConn struct {
	*testutil.FakeConn
	reads int
	body  map[string]any
}

func (c *refreshTrayConn) Pushall(ctx context.Context) (map[string]any, error) {
	c.reads++
	if c.reads == 3 {
		applyTraySetting(c.FakeConn, c.body)
	}
	return c.FakeConn.Pushall(ctx)
}

func TestAMSSetFreshReadbackWithoutAck(t *testing.T) {
	h := amsHarness(t, nil)
	conn := &refreshTrayConn{FakeConn: h.conn}
	h.conn.OnCommand = func(_ *testutil.FakeConn, _ string, body map[string]any) map[string]any {
		conn.body = body
		return nil
	}
	args := append(amsSetArgs, "--confirm", "--json")
	code := Execute(args, BuildInfo{Version: "test"}, func(g *Globals) {
		g.Keyring = h.kr
		g.Dial = func(context.Context, string, string, string) (printer.Conn, error) { return conn, nil }
		g.Out.Stdout, g.Out.Stderr = &h.stdout, &h.stderr
	})
	if code != 0 || conn.reads != 3 {
		t.Fatalf("fresh status must be requested even without an ack: exit %d, reads %d: %s", code, conn.reads, h.stdout.String())
	}
}

func TestAMSSetRejectsBadValues(t *testing.T) {
	for flag, values := range map[string][]string{
		"--slot": {"Z1", "external"}, "--color": {"wood", "#12345", "12345678"},
		"--type": {"", "PLA\n", "PETG"}, "--filament-id": {"", "bad/id", "GFG99"},
		"--nozzle-temp-min": {"0", "250"}, "--nozzle-temp-max": {"190", "400"}, "--wait": {"0s", "-1s"},
	} {
		for _, value := range values {
			h := amsHarness(t, nil)
			args := append([]string(nil), amsSetArgs...)
			found := false
			for i, arg := range args {
				if arg == flag {
					args[i+1], found = value, true
				}
			}
			if !found {
				args = append(args, flag, value)
			}
			if code := h.run(append(args, "--confirm")...); code != errfmt.ExitUsage || len(h.conn.Commands) != 0 {
				t.Errorf("%s %q: exit %d: %s %v", flag, value, code, h.stderr.String(), h.conn.Commands)
			}
		}
	}
}
