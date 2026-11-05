package preflight

import (
	"math"
	"strings"
	"testing"

	"github.com/voska/bambu/internal/job"
	"github.com/voska/bambu/internal/testutil"
)

func TestRemainingEstimateGates(t *testing.T) {
	for _, tc := range []struct {
		name           string
		grams          float64
		unknown, empty bool
		want, gate     string
	}{
		{"sufficient", 50, false, false, Warn, "filament_amount"},
		{"insufficient", 19, false, false, Fail, "filament_amount"},
		{"rounded threshold", 19.14, false, false, Fail, "filament_amount"},
		{"at reserve", 12.3*1.15 + 5, false, false, Warn, "filament_amount"},
		{"below reserve", 12.3*1.15 + 5 - 0.001, false, false, Fail, "filament_amount"},
		{"unknown sufficient", 50, true, false, Warn, "filament_amount"},
		{"unknown insufficient", 0, true, false, Fail, "filament_amount"},
		{"empty tray", 50, false, true, Fail, "tray"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, err := job.Inspect(testutil.Write(t, t.TempDir(), "part", testutil.Opts{Filaments: []string{"PLA:GFA01:12.3"}}), 1)
			if err != nil {
				t.Fatal(err)
			}
			s := idleStatus(t)
			if tc.unknown {
				s.AMS[0].RemainG, s.AMS[0].RemainPct = nil, nil
			}
			s.AMS[0].Loaded = !tc.empty
			for _, strict := range []bool{false, true} {
				r := Run(Input{Printer: shop, Job: j, Status: s, TrayIDs: []int{0}, Strict: strict, Remaining: map[int]float64{0: tc.grams}})
				if status(r, tc.gate) != tc.want || (r.Result == Fail) != (tc.want == Fail) {
					t.Fatalf("strict=%v: %+v", strict, r)
				}
				if tc.gate == "filament_amount" && !strings.Contains(gateFor(r, tc.gate, 0).Detail, "= 19.145 g") {
					t.Fatalf("required grams must not round down: %+v", r.Gates)
				}
				if tc.unknown {
					g := gateFor(r, tc.gate, 0)
					if g.OperatorEstimate == nil || g.OperatorEstimate.AMSRemainG != nil || g.OperatorEstimate.AMSRemainPct != nil {
						t.Fatalf("unknown AMS evidence: %+v", g)
					}
				}
			}
		})
	}
}

func TestRemainingEstimateCannotBypassInvalidJobUsage(t *testing.T) {
	j, err := job.Inspect(testutil.Write(t, t.TempDir(), "part", testutil.Opts{Filaments: []string{"PLA:GFA01:NaN"}}), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !math.IsNaN(j.Filaments[0].UsedG) {
		t.Fatal("fixture must reproduce nonfinite job usage")
	}
	for _, remaining := range []map[int]float64{nil, {0: 50}} {
		for _, strict := range []bool{false, true} {
			r := Run(Input{Printer: shop, Job: j, Status: idleStatus(t), TrayIDs: []int{0}, Remaining: remaining, Strict: strict})
			if status(r, "filament_amount") != Fail {
				t.Fatalf("invalid job usage must still FAIL with a checked estimate: %+v", r)
			}
		}
	}
}
