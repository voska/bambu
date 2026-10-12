package cmd

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/preflight"
	"github.com/voska/bambu/internal/testutil"
)

func TestAllowLowFilament(t *testing.T) {
	cases := []struct {
		name   string
		remain int
		allow  bool
		opts   testutil.Opts
		mut    func(map[string]any)
		failed string
		amount string
	}{
		{name: "zero blocks by default", remain: 0, failed: "filament_amount", amount: preflight.Fail},
		{name: "low blocks by default", remain: 1, failed: "filament_amount", amount: preflight.Fail},
		{name: "zero overridden", remain: 0, allow: true, amount: preflight.Warn},
		{name: "low overridden", remain: 1, allow: true, amount: preflight.Warn},
		{name: "enough unchanged", remain: 33, allow: true, amount: preflight.Pass},
		{name: "unknown unchanged", remain: -1, allow: true, amount: preflight.Warn},
		{name: "empty still blocks", allow: true, mut: func(m map[string]any) { firstTray(m)["tray_type"] = "" }, failed: "tray"},
		{name: "material still blocks", allow: true, opts: testutil.Opts{Filaments: []string{"PETG:GFG00:8"}}, failed: "filament_type", amount: preflight.Warn},
		{name: "temperature still blocks", allow: true, opts: testutil.Opts{NozzleTemp: "300"}, failed: "nozzle_temp", amount: preflight.Warn},
		{name: "integrity still blocks", allow: true, opts: testutil.Opts{CorruptMD5: true}, failed: "file_integrity", amount: preflight.Warn},
		{name: "busy still blocks", allow: true, mut: func(m map[string]any) { m["gcode_state"] = "RUNNING" }, failed: "printer_idle", amount: preflight.Warn},
	}
	for _, c := range cases {
		for _, command := range []string{"preflight", "send"} {
			for _, strict := range []bool{false, true} {
				t.Run(c.name+"/"+command+map[bool]string{false: "", true: "/strict"}[strict], func(t *testing.T) {
					h := newHarness(t, func(m map[string]any) {
						firstTray(m)["remain"] = c.remain
						if c.mut != nil {
							c.mut(m)
						}
					})
					f := h.job(c.opts)
					args := []string{"preflight", f, "--slot", "A1", "--json"}
					if command == "send" {
						args = []string{"print", "send", f, "--slot", "A1", "--dry-run", "--json"}
					}
					if c.allow {
						args = append(args, "--allow-low-filament")
					}
					if strict {
						args = append(args, "--strict")
					}
					want := 0
					if c.failed != "" {
						want = errfmt.ExitGate
					}
					if code := h.run(args...); code != want {
						t.Fatalf("exit %d, want %d: %s %s", code, want, h.stdout.String(), h.stderr.String())
					}
					var r gatesData
					h.json(&r)
					if c.failed != "" && !slices.Contains(r.Preflight.Failed, c.failed) {
						t.Fatalf("missing failure %s: %+v", c.failed, r.Preflight)
					}
					amount := ""
					for _, g := range r.Preflight.Gates {
						if g.Gate == "filament_amount" {
							amount = g.Status
							if c.allow && c.remain >= 0 && amount == preflight.Warn && (!strings.Contains(g.Fix, "--allow-low-filament") || !strings.Contains(g.Fix, "spool")) {
								t.Fatalf("override must be explicit with a spool-swap warning: %+v", g)
							}
						}
					}
					if amount != c.amount {
						t.Fatalf("amount %q, want %q", amount, c.amount)
					}
					if r.Preflight.AllowLowFilament != c.allow {
						t.Fatal("preflight must record whether the override was requested")
					}
					if len(h.conn.Commands) != 0 {
						t.Fatal("read-only command published")
					}
					if _, ok := h.ftp.File("/part.gcode.3mf"); ok {
						t.Fatal("read-only command uploaded")
					}
				})
			}
		}
	}
}

func firstTray(m map[string]any) map[string]any {
	return m["ams"].(map[string]any)["ams"].([]any)[0].(map[string]any)["tray"].([]any)[0].(map[string]any)
}

func TestLowFilamentSendConfirmed(t *testing.T) {
	h := newHarness(t, func(m map[string]any) { firstTray(m)["remain"] = 0 })
	f := h.job(testutil.Opts{})
	args := []string{"print", "send", f, "--slot", "A1", "--allow-low-filament", "--strict", "--json"}
	if code := h.run(args...); code != errfmt.ExitGate || len(h.conn.Commands) != 0 {
		t.Fatalf("override must still require --confirm: exit %d", code)
	}
	h.conn.OnCommand = func(f *testutil.FakeConn, _ string, body map[string]any) map[string]any {
		f.Set("gcode_state", "PREPARE")
		return map[string]any{"command": body["command"], "result": "SUCCESS"}
	}
	if code := h.run(append(args, "--confirm")...); code != 0 {
		t.Fatalf("exit %d: %s %s", code, h.stdout.String(), h.stderr.String())
	}
	var r sendResult
	h.json(&r)
	if !r.Sent || r.State != "PREPARE" || !slices.Contains(r.Preflight.Warnings, "filament_amount") {
		t.Fatalf("send not verified with amount warning: %+v", r)
	}
	if !strings.Contains(h.stderr.String(), "--allow-low-filament") || !strings.Contains(h.stderr.String(), "spool") {
		t.Fatalf("missing visible override warning: %s", h.stderr.String())
	}
	local, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	remote, ok := h.ftp.File("/part.gcode.3mf")
	if !ok || !bytes.Equal(local, remote) || len(h.conn.Commands) != 1 {
		t.Fatal("confirmed send must upload intact file and publish once")
	}
}

func TestAllowLowFilamentTwoSlots(t *testing.T) {
	h := newHarness(t, func(m map[string]any) {
		trays := m["ams"].(map[string]any)["ams"].([]any)[0].(map[string]any)["tray"].([]any)
		trays[0].(map[string]any)["remain"] = 0
		trays[2].(map[string]any)["remain"] = 0
	})
	f := h.job(testutil.Opts{Filaments: []string{"PLA:GFA01:8", "PLA:GFA00:8"}})
	if code := h.run("preflight", f, "--slot", "A1", "--slot", "A3", "--allow-low-filament", "--strict", "--json"); code != 0 {
		t.Fatalf("exit %d: %s %s", code, h.stdout.String(), h.stderr.String())
	}
	var r gatesData
	h.json(&r)
	var filaments []int
	for _, g := range r.Preflight.Gates {
		if g.Gate == "filament_amount" && g.Status == preflight.Warn {
			filaments = append(filaments, g.Filament)
		}
	}
	if !slices.Equal(filaments, []int{1, 2}) {
		t.Fatalf("both filament amounts must warn with attribution: %+v", r.Preflight)
	}
}

func TestLowFilamentSendQuietWarning(t *testing.T) {
	h := newHarness(t, func(m map[string]any) { firstTray(m)["remain"] = 0 })
	h.conn.OnCommand = func(f *testutil.FakeConn, _ string, body map[string]any) map[string]any {
		f.Set("gcode_state", "PREPARE")
		return map[string]any{"command": body["command"], "result": "SUCCESS"}
	}
	f := h.job(testutil.Opts{})
	if code := h.run("print", "send", f, "--slot", "A1", "--allow-low-filament", "--confirm", "--quiet"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if strings.TrimSpace(h.stdout.String()) != "PREPARE" {
		t.Fatalf("quiet stdout: %s", h.stdout.String())
	}
	if !strings.Contains(h.stderr.String(), "--allow-low-filament") || !strings.Contains(h.stderr.String(), "spool") {
		t.Fatalf("quiet must not suppress the override warning: %s", h.stderr.String())
	}
}
