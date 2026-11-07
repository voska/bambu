package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/voska/bambu/internal/printer"
)

type monitorSequenceConn struct {
	printer.Conn
	reports []map[string]any
	index   int
	updates chan struct{}
}

func (c *monitorSequenceConn) State() map[string]any {
	s := c.reports[c.index]
	if c.index < len(c.reports)-1 {
		c.index++
	}
	return s
}

func (c *monitorSequenceConn) Updates() <-chan struct{} { return c.updates }

func TestMonitorSnapshotLayer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake ffmpeg")
	}
	report := func(state string, stage, layer, total int) map[string]any {
		return map[string]any{
			"gcode_state": state, "stg_cur": stage, "subtask_name": "part",
			"layer_num": layer, "total_layer_num": total,
		}
	}
	finish := report("FINISH", 255, 75, 75)
	for _, tc := range []struct {
		name      string
		reports   []map[string]any
		wantLayer int
	}{
		{
			name: "previous job layer during startup",
			reports: []map[string]any{
				report("PREPARE", 255, 319, 319),
				report("RUNNING", 255, 319, 75),
				report("RUNNING", 255, 0, 75),
				report("RUNNING", 16, 0, 75),
				report("RUNNING", 0, 1, 75),
				report("RUNNING", 0, 2, 75),
				report("RUNNING", 0, 3, 75),
				finish,
			},
			wantLayer: 2,
		},
		{
			name: "join before layer reset",
			reports: []map[string]any{
				report("RUNNING", 255, 319, 75),
				report("RUNNING", 0, 2, 75), finish,
			},
			wantLayer: 2,
		},
		{
			name: "total not yet reported",
			reports: []map[string]any{
				report("RUNNING", 255, 319, 0),
				report("RUNNING", 0, 2, 75), finish,
			},
			wantLayer: 2,
		},
		{
			name: "join active print after target",
			reports: []map[string]any{
				report("RUNNING", 0, 10, 75), finish,
			},
			wantLayer: 10,
		},
		{
			name: "last layer is valid",
			reports: []map[string]any{
				report("RUNNING", 0, 75, 75), finish,
			},
			wantLayer: 75,
		},
		{
			name: "never reaches a valid layer",
			reports: []map[string]any{
				report("RUNNING", 255, 319, 75), finish,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			ffmpeg := "#!/bin/sh\nfor a; do last=\"$a\"; done\nprintf JPEG > \"$last\"\n"
			if err := os.WriteFile(filepath.Join(h.dir, "ffmpeg"), []byte(ffmpeg), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", h.dir)
			conn := &monitorSequenceConn{Conn: h.conn, reports: tc.reports, updates: make(chan struct{})}
			close(conn.updates)
			code := Execute([]string{"monitor", "--snapshot-at-layer", "2", "--snapshot-dir", h.dir, "--timeout", "5s", "--json"}, BuildInfo{}, func(g *Globals) {
				g.Keyring = h.kr
				g.Dial = func(context.Context, string, string, string) (printer.Conn, error) { return conn, nil }
				g.Out.Stdout, g.Out.Stderr = &h.stdout, &h.stderr
			})
			if code != 0 {
				t.Fatalf("exit %d: %s", code, h.stderr.String())
			}
			var snapshots []Event
			var final Event
			for _, line := range strings.Split(strings.TrimSpace(h.stdout.String()), "\n") {
				var ev Event
				if err := json.Unmarshal([]byte(line), &ev); err != nil {
					t.Fatal(err)
				}
				if ev.Event == "snapshot" {
					snapshots = append(snapshots, ev)
				}
				final = ev
			}
			if tc.wantLayer == 0 {
				if len(snapshots) != 0 || final.Snapshot != "" {
					t.Fatalf("unexpected snapshot: %+v, final %+v", snapshots, final)
				}
				return
			}
			if len(snapshots) != 1 || snapshots[0].Layer != tc.wantLayer {
				t.Fatalf("want one snapshot at layer %d, got %+v", tc.wantLayer, snapshots)
			}
			if final.Event != "final" || final.Snapshot != snapshots[0].Snapshot {
				t.Fatalf("final lost snapshot: %+v", final)
			}
			if b, err := os.ReadFile(final.Snapshot); err != nil || string(b) != "JPEG" {
				t.Fatalf("snapshot not written: %q, %v", b, err)
			}
		})
	}
}
