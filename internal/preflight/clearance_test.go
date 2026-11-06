package preflight

import (
	"testing"

	"github.com/voska/bambu/internal/job"
	"github.com/voska/bambu/internal/testutil"
)

func TestStartLineClearanceGate(t *testing.T) {
	for _, tt := range []struct {
		name, model, code, want string
		strict                  bool
	}{
		{"strict collision", "X1C", "G90\nM83\n; layer num/total_layer_count: 1/2\n; OBJECT_ID: 1\nG1 X50 Y10\nG1 X60 Y10 E1\n", Fail, true},
		{"default collision", "X1C", "G90\nM83\n; layer num/total_layer_count: 1/2\n; OBJECT_ID: 1\nG1 X50 Y10\nG1 X60 Y10 E1\n", Fail, false},
		{"strict clear", "X1C", "", Pass, true},
		{"strict unknown", "X1C", "G28\n", Fail, true},
		{"default unknown", "X1C", "G28\n", Warn, false},
		{"other printer", "P1S", "G28\n", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			j, err := job.Inspect(testutil.Write(t, t.TempDir(), "part", testutil.Opts{Gcode: tt.code}), 1)
			if err != nil {
				t.Fatal(err)
			}
			p := *shop
			p.Model = tt.model
			r := Run(Input{Printer: &p, Job: j, Status: idleStatus(t), TrayIDs: []int{0}, Strict: tt.strict})
			if got := status(r, "start_line_clearance"); got != tt.want {
				t.Fatalf("gate=%s want=%s; %+v", got, tt.want, r)
			}
		})
	}
}
