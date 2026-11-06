package job

import (
	"strings"
	"testing"
)

func TestFirstLayerClearance(t *testing.T) {
	const prefix = "G90\nM83\nG1 X100 Y100\nG1 X18 Y2 E15\n; layer num/total_layer_count: 1/2\n"
	for _, tt := range []struct {
		name, code string
		conflicts  int
		verified   bool
	}{
		{"clear", "; OBJECT_ID: 1\nG1 X50 Y50\nG1 X60 Y50 E1\n", 0, true},
		{"front strip", "; OBJECT_ID: 1\nG1 X50 Y10\nG1 X60 Y10 E1\n", 1, true},
		{"corner", "; OBJECT_ID: 1\nG1 X10 Y20\nG1 X11 Y20 E1\n", 1, true},
		{"margin", "; OBJECT_ID: 1\nG1 X50 Y12.5\nG1 X60 Y12.5 E1\n", 1, true},
		{"segment crosses with endpoints outside", "; OBJECT_ID: 1\nG1 X10 Y10\nG1 X250 Y10 E1\n", 1, true},
		{"travel and unretraction", "; OBJECT_ID: 1\nG1 X50 Y10\nG1 X60 Y10\nG1 E1\nG1 X70 Y10 E-1\nG1 X70 Y30\nG1 X80 Y30 E1\n", 0, true},
		{"non object ignored", "G1 X50 Y10\nG1 X60 Y10 E1\n; OBJECT_ID: 1\nG1 X50 Y50\nG1 X60 Y50 E1\n; OBJECT_ID: -1\nG1 X50 Y10 E1\n", 0, true},
		{"later layer ignored", "; OBJECT_ID: 1\nG1 X50 Y50\nG1 X60 Y50 E1\n; layer num/total_layer_count: 2/2\nG1 X50 Y10 E1\n", 0, true},
		{"clockwise arc crosses strip", "; OBJECT_ID: 1\nG1 X40 Y20\nG2 X60 Y20 I10 J0 E1\n", 0, true},
		{"counterclockwise arc crosses strip", "; OBJECT_ID: 1\nG1 X40 Y20\nG3 X60 Y20 I10 J0 E1\n", 1, true},
		{"full circle", "; OBJECT_ID: 1\nG1 X40 Y20\nG2 X40 Y20 I10 J0 E1\n", 1, true},
		{"absolute extrusion and reset", "M82\nG92 E10\n; OBJECT_ID: 1\nG1 X50 Y50\nG1 X60 Y50 E11\nG1 X50 Y10 E10\nG92 E0\nG1 X60 Y10 E1\n", 1, true},
		{"relative XY", "; OBJECT_ID: 1\nG1 X50 Y50\nG91\nG1 X10 E1\nG1 Y-40 E1\n", 1, true},
		{"unknown arc", "; OBJECT_ID: 1\nG1 X50 Y50\nG2 X60 Y50 R10 E1\n", 0, false},
		{"no object markers", "G1 X50 Y50 E1\n", 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := FirstLayerClearance([]byte(prefix + tt.code))
			if got.Conflicts != tt.conflicts || got.Verified != tt.verified {
				t.Fatalf("got %+v, want conflicts=%d verified=%v", got, tt.conflicts, tt.verified)
			}
		})
	}
	if got := FirstLayerClearance([]byte(strings.ReplaceAll(prefix, "1/2", "2/2"))); got.Verified {
		t.Fatalf("missing first layer verified: %+v", got)
	}
}

func TestClearanceUnsupportedCoordinates(t *testing.T) {
	for _, code := range []string{
		"G20\nG1 X2 Y2 E1\n",
		"G90.1\nG2 X60 Y50 I55 J50 E1\n",
		"G18\nG2 X60 Y50 I10 K0 E1\n",
		"G1 Xbad Y10 E1\n",
		"G92 X0\nG1 X60 Y50 E1\n",
	} {
		gcode := "G90\nM83\nG1 X50 Y50\n; layer num/total_layer_count: 1/2\n; OBJECT_ID: 1\nG1 X60 Y50 E1\n" + code
		if got := FirstLayerClearance([]byte(gcode)); got.Verified {
			t.Fatalf("unsupported coordinates verified: %s %+v", code, got)
		}
	}
}
