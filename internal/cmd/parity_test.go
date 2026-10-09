package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voska/bambu/internal/auth"
	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/printer"
	"github.com/voska/bambu/internal/testutil"
)

type noAckLightConn struct{ *testutil.FakeConn }

func (c noAckLightConn) Command(ctx context.Context, section string, body map[string]any) (map[string]any, error) {
	_, _ = c.FakeConn.Command(ctx, section, body)
	<-ctx.Done()
	return nil, nil
}

func (c noAckLightConn) Pushall(ctx context.Context) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.FakeConn.Pushall(ctx)
}

func TestLightMissingAckCanVerifyState(t *testing.T) {
	h := newHarness(t, func(m map[string]any) { m["lights_report"] = lightReport("off") })
	h.conn.OnCommand = func(f *testutil.FakeConn, _ string, body map[string]any) map[string]any {
		f.Set("lights_report", lightReport(body["led_mode"].(string)))
		return nil
	}
	var conn printer.Conn = noAckLightConn{h.conn}
	for _, mode := range []string{"on", "off"} {
		if _, err := setLight(context.Background(), conn, mode, 10*time.Millisecond); err != nil {
			t.Fatalf("no ack, confirmed %s: %v", mode, err)
		}
	}
}

func lightReport(mode string) []any {
	return []any{map[string]any{"node": "chamber_light", "mode": mode}}
}

func TestLightReadback(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		h := newHarness(t, func(m map[string]any) { m["lights_report"] = lightReport("off") })
		h.conn.OnCommand = func(f *testutil.FakeConn, section string, body map[string]any) map[string]any {
			if section != "system" || body["command"] != "ledctrl" || body["led_node"] != "chamber_light" || body["led_on_time"] != 500 {
				t.Fatalf("unexpected light payload: %s %v", section, body)
			}
			if confirmed {
				f.Set("lights_report", lightReport("on"))
			}
			return map[string]any{"result": "success"}
		}
		want := errfmt.ExitTimeout
		if confirmed {
			want = 0
		}
		if code := h.run("light", "on", "--wait", "10ms", "--json"); code != want {
			t.Fatalf("confirmed=%v: exit %d want %d: %s", confirmed, code, want, h.stdout.String())
		}
	}
}

func TestLightStatusAndMissingTelemetry(t *testing.T) {
	h := newHarness(t, func(m map[string]any) { m["lights_report"] = lightReport("flashing") })
	if code := h.run("light", "status", "--json"); code != 0 || !strings.Contains(h.stdout.String(), "flashing") || len(h.conn.Commands) != 0 {
		t.Fatalf("read-only light status: %d %s", code, h.stdout.String())
	}
	h.conn.Set("lights_report", nil)
	if code := h.run("light", "on", "--json"); code != errfmt.ExitGate || len(h.conn.Commands) != 0 {
		t.Fatalf("missing light telemetry must refuse: %d %s", code, h.stdout.String())
	}
}

func TestSnapshotRestoresLight(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake ffmpeg")
	}
	for _, mode := range []string{"on", "off", "flashing"} {
		for _, captureOK := range []bool{false, true} {
			h := newHarness(t, func(m map[string]any) { m["lights_report"] = lightReport(mode) })
			script := "#!/bin/sh\nexit 1\n"
			if captureOK {
				script = "#!/bin/sh\nfor a; do last=\"$a\"; done\nprintf JPEG > \"$last\"\n"
			}
			_ = os.WriteFile(filepath.Join(h.dir, "ffmpeg"), []byte(script), 0o755)
			t.Setenv("PATH", h.dir)
			h.conn.OnCommand = func(f *testutil.FakeConn, _ string, body map[string]any) map[string]any {
				f.Set("lights_report", lightReport(body["led_mode"].(string)))
				return map[string]any{"result": "success"}
			}
			code := h.run("camera", "snapshot", "--light", "--output", filepath.Join(h.dir, "snap.jpg"), "--json")
			if (code == 0) != captureOK {
				t.Fatalf("%s capture=%v: exit %d %s", mode, captureOK, code, h.stdout.String())
			}
			wantCommands := 2
			if mode == "on" {
				wantCommands = 0
			}
			if len(h.conn.Commands) != wantCommands || h.conn.State()["lights_report"].([]any)[0].(map[string]any)["mode"] != mode {
				t.Fatalf("light %s not restored: %v", mode, h.conn.Commands)
			}
		}
	}
}

func TestPauseStopNeedReadback(t *testing.T) {
	for _, verb := range []string{"pause", "stop"} {
		h := newHarness(t, func(m map[string]any) { m["gcode_state"] = "RUNNING" })
		h.conn.OnCommand = func(_ *testutil.FakeConn, _ string, _ map[string]any) map[string]any {
			return map[string]any{"result": "success"}
		}
		if code := h.run("print", verb, "--confirm", "--wait", "10ms", "--json"); code != errfmt.ExitTimeout {
			t.Fatalf("%s accepted unchanged RUNNING: exit %d %s", verb, code, h.stdout.String())
		}
	}
}

func TestMonitorParityFlags(t *testing.T) {
	h := newHarness(t, func(m map[string]any) { m["gcode_state"] = "PAUSE" })
	start := time.Now()
	if code := h.run("monitor", "--watch", "--interval", "1ms", "--pushall-every", "5ms", "--timeout", "20ms", "--json"); code != errfmt.ExitPrintPaused {
		t.Fatalf("continuous monitoring should wait across pause: exit %d %s", code, h.stdout.String())
	}
	if time.Since(start) < 10*time.Millisecond {
		t.Fatal("--watch exited immediately on PAUSE")
	}
	if code := h.run("monitor", "--interval", "0s", "--json"); code != errfmt.ExitUsage {
		t.Fatalf("zero interval: %d", code)
	}
}

func TestMonitorWatchFailedExit(t *testing.T) {
	h := newHarness(t, func(m map[string]any) { m["gcode_state"] = "FAILED" })
	if code := h.run("monitor", "--watch", "--timeout", "10ms", "--json"); code != errfmt.ExitPrintFailed {
		t.Fatalf("FAILED watch reported success: %d %s", code, h.stdout.String())
	}
}

func TestInterruptedMonitorHasFinal(t *testing.T) {
	h := newHarness(t, func(m map[string]any) { m["gcode_state"] = "RUNNING" })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code := Execute([]string{"monitor", "--watch", "--json"}, BuildInfo{}, func(g *Globals) {
		g.Ctx, g.Keyring = ctx, h.kr
		g.Dial = func(context.Context, string, string, string) (printer.Conn, error) { return h.conn, nil }
		g.Out.Stdout, g.Out.Stderr = &h.stdout, &h.stderr
	})
	if code != 0 || !strings.Contains(h.stdout.String(), `"event":"final"`) || !strings.Contains(h.stdout.String(), `"final":`) {
		t.Fatalf("interruption lost final state: %d %s", code, h.stdout.String())
	}
}

func TestStrictPreflightType(t *testing.T) {
	h := newHarness(t, nil)
	f := h.job(testutil.Opts{Filaments: []string{"PLA-CF:GFA00:8"}})
	if code := h.run("preflight", f, "--slot", "A1", "--no-ftp", "--json"); code != 0 {
		t.Fatalf("legacy same-family warning changed: %d %s", code, h.stdout.String())
	}
	if code := h.run("preflight", f, "--slot", "A1", "--no-ftp", "--strict", "--json"); code != errfmt.ExitGate || !strings.Contains(h.stdout.String(), "filament_type") {
		t.Fatalf("strict material mismatch must fail: %d %s", code, h.stdout.String())
	}
}

func TestMonitorNotificationsAndFinalData(t *testing.T) {
	h := newHarness(t, func(m map[string]any) {
		m["gcode_state"] = "RUNNING"
		m["hms"] = []any{map[string]any{"attr": 0x05000500, "code": 0x00010007}}
	})
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer TEST_TOKEN" {
			t.Error("missing ntfy token")
		}
		switch calls.Add(1) {
		case 1:
			h.conn.Set("print_error", 123)
		case 2:
			h.conn.Set("gcode_state", "FINISH")
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer s.Close()
	f, _ := os.OpenFile(filepath.Join(h.dir, "config.toml"), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = fmt.Fprintf(f, "\n[ntfy]\nurl = %q\ntopic = \"printer\"\n", s.URL)
	_ = f.Close()
	h.kr[auth.NtfyService+"/token"] = "TEST_TOKEN"
	if code := h.run("monitor", "--notify", "--json", "--timeout", "5s"); code != 0 {
		t.Fatalf("ntfy failure must not break monitor: %d %s", code, h.stdout.String())
	}
	if calls.Load() != 3 || !strings.Contains(h.stderr.String(), "ntfy failed") {
		t.Fatalf("notifications %d, %s", calls.Load(), h.stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(h.stdout.String()), "\n")
	var final Event
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &final); err != nil {
		t.Fatal(err)
	}
	if final.Status == nil || final.Status.State != "FINISH" || final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("final %+v", final)
	}
}

func TestSnapshotRestoreFailureIsLoud(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	h := newHarness(t, func(m map[string]any) { m["lights_report"] = lightReport("off") })
	_ = os.WriteFile(filepath.Join(h.dir, "ffmpeg"), []byte("#!/bin/sh\nexit 1\n"), 0o755)
	t.Setenv("PATH", h.dir)
	h.conn.OnCommand = func(f *testutil.FakeConn, _ string, body map[string]any) map[string]any {
		if body["led_mode"] == "on" {
			f.Set("lights_report", lightReport("on"))
		} else {
			f.Set("lights_report", nil)
		}
		return map[string]any{"result": "success"}
	}
	if code := h.run("camera", "snapshot", "--light", "--json"); code == 0 || !strings.Contains(h.stdout.String(), "could not restore") {
		t.Fatalf("restoration failure hidden: %d %s", code, h.stdout.String())
	}
}
