package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/voska/bambu/internal/auth"
	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/printer"
	"github.com/voska/bambu/internal/testutil"
)

const fixture = "../printer/testdata/pushall.json"

type memKeyring map[string]string

func (m memKeyring) Get(s, u string) (string, error) {
	if v, ok := m[s+"/"+u]; ok {
		return v, nil
	}
	return "", auth.ErrNotFound
}
func (m memKeyring) Set(s, u, p string) error { m[s+"/"+u] = p; return nil }
func (m memKeyring) Delete(s, u string) error { delete(m, s+"/"+u); return nil }

type harness struct {
	t      *testing.T
	dir    string
	conn   *testutil.FakeConn
	ftp    *testutil.FTPSServer
	kr     memKeyring
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func newHarness(t *testing.T, mut func(m map[string]any)) *harness {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(cfg, []byte("default_printer = \"shop\"\n[printers.shop]\nhost = \"127.0.0.1\"\nserial = \"00M00A000000001\"\nmodel = \"X1C\"\nnozzle = 0.4\nplate = \"textured_plate\"\n"), 0o600)
	t.Setenv("BAMBU_CONFIG", cfg)
	t.Setenv("BAMBU_ACCESS_CODE", "")
	t.Setenv("BAMBU_STUDIO_CONF", filepath.Join(dir, "none.conf"))
	t.Setenv("BAMBU_PRINTER", "")
	t.Setenv("BAMBU_JSON", "")
	h := &harness{t: t, dir: dir, ftp: testutil.NewFTPSServer(t), kr: memKeyring{auth.Service + "/00M00A000000001": "12345678"}}
	h.conn = testutil.NewFakeConn(t, fixture, func(m map[string]any) {
		m["gcode_state"], m["hms"] = "FINISH", []any{}
		if mut != nil {
			mut(m)
		}
	})
	return h
}

func (h *harness) run(args ...string) int {
	h.stdout.Reset()
	h.stderr.Reset()
	return Execute(args, BuildInfo{Version: "test"}, func(g *Globals) {
		g.Keyring = h.kr
		g.FTPSPort = h.ftp.Port()
		g.Dial = func(context.Context, string, string, string) (printer.Conn, error) { return h.conn, nil }
		g.Out.Stdout, g.Out.Stderr = &h.stdout, &h.stderr
	})
}

func (h *harness) json(v any) {
	h.t.Helper()
	if err := json.Unmarshal(h.stdout.Bytes(), v); err != nil {
		h.t.Fatalf("stdout is not JSON: %v\n%s", err, h.stdout.String())
	}
}

func (h *harness) job(opts testutil.Opts) string {
	return testutil.Write(h.t, h.dir, "part", opts)
}

func TestStatusJSON(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("status", "--json"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	var v statusView
	h.json(&v)
	if v.Printer != "shop" || v.Status.State != "FINISH" || len(v.Status.AMS) != 4 {
		t.Fatalf("%+v", v)
	}
	if h.run("status", "-q"); strings.TrimSpace(h.stdout.String()) != "FINISH" {
		t.Fatalf("quiet: %q", h.stdout.String())
	}
}

func TestSendRequiresConfirm(t *testing.T) {
	h := newHarness(t, nil)
	f := h.job(testutil.Opts{Filaments: []string{"PLA:GFL99:13.5"}})
	if code := h.run("print", "send", f, "--slot", "4", "--json"); code != errfmt.ExitGate {
		t.Fatalf("want gate exit, got %d", code)
	}
	var e struct{ Error errfmt.Error }
	h.json(&e)
	if e.Error.Name != "gate_failed" || e.Error.Hint == "" {
		t.Fatalf("%+v", e)
	}
	if len(h.conn.Commands) != 0 {
		t.Fatal("nothing may be published without --confirm")
	}
}

func TestSendDryRun(t *testing.T) {
	h := newHarness(t, nil)
	f := h.job(testutil.Opts{Filaments: []string{"PLA:GFL99:13.5"}})
	if code := h.run("print", "send", f, "--slot", "A4", "--dry-run", "--json"); code != 0 {
		t.Fatalf("exit %d: %s %s", code, h.stdout.String(), h.stderr.String())
	}
	var r sendResult
	h.json(&r)
	p := r.Plan.MQTT.Payload["print"].(map[string]any)
	if p["url"] != "file:///sdcard/part.gcode.3mf" || p["bed_type"] != "auto" || !reflect.DeepEqual(p["ams_mapping"], []any{float64(3)}) {
		t.Fatalf("payload: %v", p)
	}
	if r.Sent || !r.DryRun || r.Preflight.Result != "PASS" {
		t.Fatalf("%+v", r)
	}
	if len(h.conn.Commands) != 0 {
		t.Fatal("dry run published")
	}
	if _, ok := h.ftp.File("/part.gcode.3mf"); ok {
		t.Fatal("dry run uploaded")
	}
}

func TestSendConfirmed(t *testing.T) {
	h := newHarness(t, nil)
	h.conn.OnCommand = func(f *testutil.FakeConn, section string, body map[string]any) map[string]any {
		if body["command"] == "project_file" {
			f.Set("gcode_state", "PREPARE")
			return map[string]any{"command": "project_file", "result": "SUCCESS"}
		}
		return nil
	}
	f := h.job(testutil.Opts{Filaments: []string{"PLA:GFL99:13.5"}})
	if code := h.run("print", "send", f, "--slot", "4", "--confirm", "--json"); code != 0 {
		t.Fatalf("exit %d: %s %s", code, h.stdout.String(), h.stderr.String())
	}
	local, _ := os.ReadFile(f)
	remote, ok := h.ftp.File("/part.gcode.3mf")
	if !ok || !bytes.Equal(local, remote) {
		t.Fatal("upload missing or corrupt")
	}
	if len(h.conn.Commands) != 1 {
		t.Fatalf("commands: %v", h.conn.Commands)
	}
	body := h.conn.Commands[0]["print"].(map[string]any)
	if body["command"] != "project_file" || body["md5"] == "" || body["layer_inspect"] != true || !reflect.DeepEqual(body["ams_mapping"], []int{3}) {
		t.Fatalf("body: %v", body)
	}
	var r sendResult
	h.json(&r)
	if !r.Sent || r.State != "PREPARE" {
		t.Fatalf("%+v", r)
	}
}

func TestSendRefusedWhenBusy(t *testing.T) {
	h := newHarness(t, func(m map[string]any) { m["gcode_state"] = "RUNNING" })
	f := h.job(testutil.Opts{Filaments: []string{"PLA:GFL99:13.5"}})
	if code := h.run("print", "send", f, "--slot", "4", "--confirm"); code != errfmt.ExitGate {
		t.Fatalf("want gate, got %d", code)
	}
	if len(h.conn.Commands) != 0 {
		t.Fatal("published while busy")
	}
	if _, ok := h.ftp.File("/part.gcode.3mf"); ok {
		t.Fatal("uploaded while busy")
	}
}

func TestSendRejectedAck(t *testing.T) {
	h := newHarness(t, nil)
	h.conn.OnCommand = func(*testutil.FakeConn, string, map[string]any) map[string]any {
		return map[string]any{"command": "project_file", "result": "FAIL", "reason": "mqtt message verify failed"}
	}
	f := h.job(testutil.Opts{Filaments: []string{"PLA:GFL99:13.5"}})
	if code := h.run("print", "send", f, "--slot", "4", "--confirm"); code != errfmt.ExitForbidden {
		t.Fatalf("want forbidden, got %d: %s", code, h.stderr.String())
	}
}

func TestPreflightExitCode(t *testing.T) {
	h := newHarness(t, nil)
	f := h.job(testutil.Opts{})
	if code := h.run("preflight", f, "--slot", "2", "-q"); code != errfmt.ExitGate || strings.TrimSpace(h.stdout.String()) != "FAIL" {
		t.Fatalf("empty slot: %d %q", code, h.stdout.String())
	}
	if code := h.run("preflight", f, "--slot", "1", "-q"); code != 0 || strings.TrimSpace(h.stdout.String()) != "PASS" {
		t.Fatalf("A1: %d %q %s", code, h.stdout.String(), h.stderr.String())
	}
	if code := h.run("preflight", f, "--slot", "9"); code != errfmt.ExitUsage {
		t.Fatalf("bad slot: %d", code)
	}
}

func TestControl(t *testing.T) {
	h := newHarness(t, func(m map[string]any) { m["gcode_state"] = "RUNNING" })
	if code := h.run("print", "pause"); code != errfmt.ExitGate || len(h.conn.Commands) != 0 {
		t.Fatalf("pause without --confirm: %d", code)
	}
	if code := h.run("print", "resume", "--confirm"); code != errfmt.ExitGate {
		t.Fatalf("resume while RUNNING must be refused: %d", code)
	}
	h.conn.OnCommand = func(f *testutil.FakeConn, _ string, body map[string]any) map[string]any {
		f.Set("gcode_state", "PAUSE")
		return map[string]any{"command": body["command"], "result": "success"}
	}
	if code := h.run("print", "pause", "--confirm", "--json"); code != 0 {
		t.Fatalf("pause: %d %s", code, h.stderr.String())
	}
	if len(h.conn.Commands) != 1 || h.conn.Commands[0]["print"].(map[string]any)["command"] != "pause" {
		t.Fatalf("%v", h.conn.Commands)
	}
}

func TestControlNeedsDevMode(t *testing.T) {
	h := newHarness(t, func(m map[string]any) { m["gcode_state"], m["fun"] = "RUNNING", "20011A30F9CFB" })
	if code := h.run("print", "stop", "--confirm"); code != errfmt.ExitForbidden {
		t.Fatalf("want forbidden, got %d", code)
	}
}

func TestMonitorOutcomes(t *testing.T) {
	for final, want := range map[string]int{"FINISH": 0, "FAILED": errfmt.ExitPrintFailed, "PAUSE": errfmt.ExitPrintPaused} {
		h := newHarness(t, func(m map[string]any) { m["gcode_state"] = "RUNNING" })
		go func() {
			time.Sleep(150 * time.Millisecond)
			h.conn.Set("gcode_state", final)
		}()
		if code := h.run("monitor", "--json", "--timeout", "10s"); code != want {
			t.Errorf("%s: exit %d want %d", final, code, want)
		}
		lines := strings.Split(strings.TrimSpace(h.stdout.String()), "\n")
		var last Event
		_ = json.Unmarshal([]byte(lines[len(lines)-1]), &last)
		if last.Event != "final" || last.State != final {
			t.Errorf("%s: last event %+v", final, last)
		}
	}
}

func TestMonitorTimeout(t *testing.T) {
	h := newHarness(t, func(m map[string]any) { m["gcode_state"] = "RUNNING" })
	if code := h.run("monitor", "--timeout", "300ms"); code != errfmt.ExitTimeout {
		t.Fatalf("want timeout exit, got %d", code)
	}
}

func TestPrinterAddListAuth(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("printer", "add", "garage", "--host", "192.0.2.7", "--serial", "01p00a000000002", "--model", "P1S", "--plate", "cool_plate"); code != 0 {
		t.Fatalf("add: %d %s", code, h.stderr.String())
	}
	if code := h.run("printer", "add", "bad", "--host", "x", "--serial", "01P00A000000003", "--model", "Ender3"); code != errfmt.ExitUsage {
		t.Fatalf("unknown model: %d", code)
	}
	h.run("printer", "list", "--json")
	var l struct {
		Printers []struct {
			Name   string `json:"name"`
			Serial string `json:"serial"`
			Code   string `json:"access_code_source"`
		} `json:"printers"`
	}
	h.json(&l)
	if len(l.Printers) != 2 || l.Printers[0].Name != "garage" || l.Printers[0].Serial != "01P00A000000002" || l.Printers[0].Code != "none" || l.Printers[1].Code != "keychain" {
		t.Fatalf("%+v", l)
	}
	if code := h.run("printer", "remove", "garage"); code != errfmt.ExitGate {
		t.Fatalf("remove without confirm: %d", code)
	}
	if code := h.run("printer", "remove", "garage", "--confirm"); code != 0 {
		t.Fatalf("remove: %d", code)
	}
	if code := h.run("auth", "status", "--check", "--json"); code != 0 {
		t.Fatalf("auth status: %d %s", code, h.stdout.String())
	}
	if strings.Contains(h.stdout.String()+h.stderr.String(), "12345678") {
		t.Fatal("access code leaked")
	}
}

func TestSchemaAndExitCodes(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("schema", "print", "send"); code != 0 {
		t.Fatalf("schema: %d %s", code, h.stderr.String())
	}
	var n schemaNode
	h.json(&n)
	if n.Path != "bambu print send" || len(n.Args) != 1 {
		t.Fatalf("%+v", n)
	}
	var flags []string
	for _, f := range n.Flags {
		flags = append(flags, f.Name)
	}
	for _, want := range []string{"--slot", "--confirm", "--dry-run"} {
		if !strings.Contains(strings.Join(flags, " "), want) {
			t.Errorf("missing %s in %v", want, flags)
		}
	}
	h.run("exit-codes", "--json")
	var codes []errfmt.Entry
	h.json(&codes)
	if len(codes) != len(errfmt.Table()) {
		t.Fatal("exit codes")
	}
}

func TestUsageErrorJSON(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("--json", "bogus"); code != errfmt.ExitUsage {
		t.Fatalf("exit %d", code)
	}
	var e struct{ Error errfmt.Error }
	h.json(&e)
	if e.Error.Code != errfmt.ExitUsage {
		t.Fatalf("%+v", e)
	}
}

func TestRecipesAndVersion(t *testing.T) {
	h := newHarness(t, nil)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if code := h.run("recipe", "list", "-q"); code != 0 {
		t.Fatal(code)
	}
	names := strings.Fields(h.stdout.String())
	if strings.Join(names, ",") != "functional-petg,functional-pla,prototype-pla,solid-pla" {
		t.Fatalf("%v", names)
	}
	if code := h.run("recipe", "show", "nope"); code != errfmt.ExitNotFound {
		t.Fatal(code)
	}
	h.run("version", "--json")
	var v map[string]any
	h.json(&v)
	if v["version"] != "test" {
		t.Fatal(v)
	}
}

func TestSliceRejectsBadInputEarly(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("slice", "part.stl", "--recipe", "nope"); code != errfmt.ExitNotFound {
		t.Fatalf("unknown recipe: %d", code)
	}
}
