package cmd

import (
	"strings"
	"testing"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/preflight"
	"github.com/voska/bambu/internal/testutil"
)

func TestAMSSetAvailable(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("ams", "set", "--help"); code != 0 {
		t.Fatalf("ams set unavailable: exit %d: %s", code, h.stderr.String())
	}
}

func TestUntypedTrayPreflight(t *testing.T) {
	for _, strict := range []bool{false, true} {
		for _, tc := range []struct {
			name, bits, detail string
		}{
			{"present", "f", "slot A3 is untyped (spool detected; no material set)"},
			{"empty", "b", "slot A3 is empty"},
			{"unknown", "", "slot A3 has no material metadata; filament presence is unknown"},
		} {
			t.Run(tc.name+map[bool]string{false: "", true: " strict"}[strict], func(t *testing.T) {
				h := newHarness(t, func(m map[string]any) {
					ams := m["ams"].(map[string]any)
					ams["tray_exist_bits"] = tc.bits
					unit := ams["ams"].([]any)[0].(map[string]any)
					unit["tray"].([]any)[2] = map[string]any{"id": "2"}
				})
				args := []string{"print", "send", h.job(testutil.Opts{}), "--slot", "A3", "--dry-run", "--json"}
				if strict {
					args = append(args, "--strict")
				}
				if code := h.run(args...); code != errfmt.ExitGate {
					t.Fatalf("exit %d: %s", code, h.stdout.String())
				}
				var out struct {
					Preflight preflight.Result `json:"preflight"`
				}
				h.json(&out)
				found := false
				for _, gate := range out.Preflight.Gates {
					if gate.Gate == "tray" {
						found = true
						if gate.Status != preflight.Fail || gate.Detail != tc.detail {
							t.Fatalf("wrong tray failure: %+v", gate)
						}
						if tc.name != "empty" && !strings.Contains(gate.Fix, "bambu ams set --slot A3") {
							t.Fatalf("missing recovery command: %+v", gate)
						}
					}
				}
				if !found || len(h.conn.Commands) != 0 {
					t.Fatalf("missing gate or dry-run wrote to printer: %+v, %v", out, h.conn.Commands)
				}
			})
		}
	}
}
