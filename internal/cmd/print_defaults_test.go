package cmd

import (
	"os"
	"testing"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/testutil"
)

func TestSendTimelapseDefaults(t *testing.T) {
	for _, tt := range []struct {
		name, config string
		flags        []string
		want         bool
		code         int
	}{
		{name: "default", want: true},
		{name: "opt out", flags: []string{"--no-timelapse"}},
		{name: "explicit false", flags: []string{"--timelapse=false"}},
		{name: "configured off", config: "[print]\ntimelapse = false\n"},
		{name: "configured on", config: "[print]\ntimelapse = true\n", want: true},
		{name: "explicit on", config: "[print]\ntimelapse = false\n", flags: []string{"--timelapse"}, want: true},
		{name: "explicit off", config: "[print]\ntimelapse = true\n", flags: []string{"--no-timelapse"}},
		{name: "conflicting flags", flags: []string{"--timelapse", "--no-timelapse"}, code: errfmt.ExitUsage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil)
			f := h.job(testutil.Opts{Filaments: []string{"PLA:GFL99:13.5"}})
			cfg, err := os.OpenFile(os.Getenv("BAMBU_CONFIG"), os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = cfg.WriteString(tt.config)
			if err != nil {
				t.Fatal(err)
			}
			_ = cfg.Close()
			args := append([]string{"print", "send", f, "--slot", "A4", "--dry-run", "--json"}, tt.flags...)
			if code := h.run(args...); code != tt.code {
				t.Fatalf("exit=%d want=%d: %s %s", code, tt.code, h.stdout.String(), h.stderr.String())
			}
			if tt.code != 0 {
				return
			}
			var r sendResult
			h.json(&r)
			if got := r.Plan.MQTT.Payload["print"].(map[string]any)["timelapse"]; got != tt.want {
				t.Fatalf("timelapse=%v want=%v", got, tt.want)
			}
			if len(h.conn.Commands) != 0 {
				t.Fatal("dry run published")
			}
			if _, ok := h.ftp.File("/part.gcode.3mf"); ok {
				t.Fatal("dry run uploaded")
			}
		})
	}
}

func TestSendClearanceBlocksWrites(t *testing.T) {
	for _, gcode := range []string{
		"G90\nM83\n; layer num/total_layer_count: 1/2\n; OBJECT_ID: 1\nG1 X50 Y10\nG1 X60 Y10 E1\n",
		"G28\n",
	} {
		for _, flags := range [][]string{{"--dry-run"}, {"--dry-run", "--strict"}, {"--confirm", "--strict"}} {
			h := newHarness(t, nil)
			f := h.job(testutil.Opts{Gcode: gcode, Filaments: []string{"PLA:GFL99:13.5"}})
			args := append([]string{"print", "send", f, "--slot", "A4", "--json"}, flags...)
			if code := h.run(args...); code != errfmt.ExitGate {
				t.Fatalf("exit %d: %s %s", code, h.stdout.String(), h.stderr.String())
			}
			if len(h.conn.Commands) != 0 {
				t.Fatal("rejected send published")
			}
			if _, ok := h.ftp.File("/part.gcode.3mf"); ok {
				t.Fatal("rejected send uploaded")
			}
		}
	}
}
