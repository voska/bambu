package cmd

import (
	"slices"
	"strings"
	"testing"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/preflight"
	"github.com/voska/bambu/internal/testutil"
)

func zeroRemaining(m map[string]any) {
	ams := m["ams"].(map[string]any)["ams"].([]any)[0].(map[string]any)
	tray := ams["tray"].([]any)[0].(map[string]any)
	tray["remain"] = float64(0)
}

func TestFilamentRemainingEstimate(t *testing.T) {
	for _, command := range [][]string{{"preflight"}, {"print", "send"}} {
		t.Run(strings.Join(command, "_"), func(t *testing.T) {
			h := newHarness(t, zeroRemaining)
			f := h.job(testutil.Opts{Filaments: []string{"PLA:GFA01:12.3"}})
			args := append(slices.Clone(command), f, "--slot", "A1", "--strict", "--json")
			if len(command) > 1 {
				args = append(args, "--dry-run")
			}
			if code := h.run(args...); code != errfmt.ExitGate {
				t.Fatalf("zero AMS reading must fail: %d %s", code, h.stdout.String())
			}
			if code := h.run(append(args, "--filament-remaining", "1=50g")...); code != 0 {
				t.Fatalf("checked amount must allow strict preflight: %d %s %s", code, h.stdout.String(), h.stderr.String())
			}
			var r struct{ Preflight preflight.Result }
			h.json(&r)
			if r.Preflight.Result != preflight.Pass || !r.Preflight.Strict || !slices.Contains(r.Preflight.Warnings, "filament_amount") {
				t.Fatalf("estimate must remain visible as WARN: %+v", r.Preflight)
			}
			for _, gate := range r.Preflight.Gates {
				if gate.Gate == "filament_amount" && (gate.Status != preflight.Warn || !strings.Contains(gate.Detail, "operator estimate 50 g") || !strings.Contains(gate.Detail, "AMS ~0 g (0%)")) {
					t.Fatalf("missing estimate/telemetry provenance: %+v", gate)
				}
				if gate.Gate == "filament_amount" && (gate.OperatorEstimate == nil || gate.OperatorEstimate.StatedG != 50 || gate.OperatorEstimate.AMSRemainG == nil || *gate.OperatorEstimate.AMSRemainG != 0 || gate.OperatorEstimate.RequiredG < 19.14 || gate.OperatorEstimate.RequiredG > 19.15) {
					t.Fatalf("missing structured amount evidence: %+v", gate)
				}
			}
			// Estimates apply to this invocation only.
			if code := h.run(args...); code != errfmt.ExitGate {
				t.Fatalf("estimate leaked into next invocation: %d", code)
			}
			if len(h.conn.Commands) != 0 {
				t.Fatal("read-only check published a command")
			}
			if _, ok := h.ftp.File("/part.gcode.3mf"); ok {
				t.Fatal("read-only check uploaded a file")
			}
		})
	}
}

func TestFilamentRemainingInsufficientAndOtherGates(t *testing.T) {
	for _, tc := range []struct {
		name, amount, state, material, failed string
	}{
		{"insufficient reserve", "19g", "FINISH", "PLA", "filament_amount"},
		{"zero", "0g", "FINISH", "PLA", "filament_amount"},
		{"busy", "50g", "RUNNING", "PLA", "printer_idle"},
		{"wrong material", "50g", "FINISH", "PETG", "filament_type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(m map[string]any) { zeroRemaining(m); m["gcode_state"] = tc.state })
			f := h.job(testutil.Opts{Filaments: []string{tc.material + ":GFA01:12.3"}})
			if code := h.run("print", "send", f, "--slot", "A1", "--strict", "--confirm", "--filament-remaining", "A1="+tc.amount, "--json"); code != errfmt.ExitGate {
				t.Fatalf("want gate failure, got %d: %s %s", code, h.stdout.String(), h.stderr.String())
			}
			if !strings.Contains(h.stdout.String(), tc.failed) || len(h.conn.Commands) != 0 {
				t.Fatalf("gate %s must still block: %s", tc.failed, h.stdout.String())
			}
			if _, ok := h.ftp.File("/part.gcode.3mf"); ok {
				t.Fatal("failed gate allowed upload")
			}
		})
	}
}

func TestFilamentRemainingValidation(t *testing.T) {
	for _, values := range [][]string{
		{"A1=50"},
		{"A1=50kg"},
		{"A1=50%"},
		{"A1=-1g"},
		{"A1=NaNg"},
		{"A1=Infg"},
		{"A1=1e999g"},
		{"A1=" + strings.Repeat("9", 400) + "g"},
		{"A1=g"},
		{"A1=50g=60g"},
		{"=50g"},
		{"E1=50g"},
		{"A2=50g"},
		{"A1=50g", "1=60g"},
	} {
		t.Run(strings.Join(values, ","), func(t *testing.T) {
			h := newHarness(t, nil)
			f := h.job(testutil.Opts{})
			args := []string{"preflight", f, "--slot", "A1", "--json"}
			for _, value := range values {
				args = append(args, "--filament-remaining", value)
			}
			if code := h.run(args...); code != errfmt.ExitUsage {
				t.Fatalf("invalid estimate %v: exit %d %s %s", values, code, h.stdout.String(), h.stderr.String())
			}
		})
	}
}

func TestFilamentRemainingMultipleSlots(t *testing.T) {
	h := newHarness(t, zeroRemaining)
	f := h.job(testutil.Opts{Filaments: []string{"PLA:GFA01:12.3", "PLA:GFL99:12.3"}})
	args := []string{"print", "send", f, "--slot", "A4", "--slot", "A1", "--dry-run", "--strict", "--json", "--filament-remaining", "A4=30.5g"}
	if code := h.run(args...); code != errfmt.ExitGate {
		t.Fatalf("unoverridden zero slot must still fail: %d", code)
	}
	if code := h.run(append(args, "--filament-remaining", "A1=50g")...); code != 0 {
		t.Fatalf("both amounts should allow dry run: %d %s %s", code, h.stdout.String(), h.stderr.String())
	}
	var r sendResult
	h.json(&r)
	amounts := map[int]float64{1: 30.5, 2: 50}
	for _, gate := range r.Preflight.Gates {
		if gate.Gate == "filament_amount" {
			if gate.Status != preflight.Warn || gate.OperatorEstimate == nil || gate.OperatorEstimate.StatedG != amounts[gate.Filament] {
				t.Fatalf("estimate must apply to its own filament: %+v", gate)
			}
			delete(amounts, gate.Filament)
		}
	}
	if len(amounts) != 0 {
		t.Fatalf("missing amount gates: %v", amounts)
	}
}

func TestFilamentRemainingConfirmedSend(t *testing.T) {
	h := newHarness(t, zeroRemaining)
	f := h.job(testutil.Opts{Filaments: []string{"PLA:GFA01:12.3"}})
	args := []string{"print", "send", f, "--slot", "A1", "--strict", "--filament-remaining", "A1=50g"}
	if code := h.run(args...); code != errfmt.ExitGate || len(h.conn.Commands) != 0 {
		t.Fatalf("estimate must not replace confirmation: %d", code)
	}
	h.conn.OnCommand = func(f *testutil.FakeConn, _ string, body map[string]any) map[string]any {
		f.Set("gcode_state", "PREPARE")
		return map[string]any{"command": body["command"], "result": "SUCCESS"}
	}
	if code := h.run(append(args, "--confirm")...); code != 0 {
		t.Fatalf("confirmed send: %d %s %s", code, h.stdout.String(), h.stderr.String())
	}
	if len(h.conn.Commands) != 1 {
		t.Fatalf("expected one start: %v", h.conn.Commands)
	}
	if _, ok := h.ftp.File("/part.gcode.3mf"); !ok {
		t.Fatal("confirmed send did not upload")
	}
	if !strings.Contains(h.stderr.String(), "operator estimate 50 g") || !strings.Contains(h.stderr.String(), "AMS ~0 g (0%)") {
		t.Fatalf("confirmed send must show estimate warning: %s", h.stderr.String())
	}
}
