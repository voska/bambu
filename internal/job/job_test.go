package job

import (
	"reflect"
	"testing"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/testutil"
)

func TestInspect(t *testing.T) {
	p := testutil.Write(t, t.TempDir(), "part", testutil.Opts{})
	j, err := Inspect(p, 1)
	if err != nil {
		t.Fatal(err)
	}
	if j.GcodeMD5OK == nil || !*j.GcodeMD5OK || len(j.GcodeMD5) != 32 {
		t.Fatalf("md5: %+v", j)
	}
	if j.Layers != 113 || j.TotalTime != "27m 14s" || j.PredictionS != 1634 {
		t.Fatalf("header: %d %q %d", j.Layers, j.TotalTime, j.PredictionS)
	}
	if j.PrinterModel != "Bambu Lab X1 Carbon" || j.NozzleDiameter != "0.4" || j.BedType != "textured_plate" || j.BedTemp != "55" {
		t.Fatalf("settings: %+v", j)
	}
	if len(j.Filaments) != 1 || j.Filaments[0].FilamentID != "GFA00" || j.Filaments[0].UsedG != 8.1 || j.Filaments[0].Index != 0 {
		t.Fatalf("filaments: %+v", j.Filaments)
	}
	if j.NozzleTempRange != [2]string{"190", "240"} || j.NozzleTemp != "220" {
		t.Fatalf("temps: %+v", j)
	}
	if j.Settings["brim_type"] != "no_brim" {
		t.Fatal("settings not surfaced")
	}
}

func TestInspectErrors(t *testing.T) {
	if _, err := Inspect("/nonexistent.gcode.3mf", 1); errfmt.As(err).Code != errfmt.ExitNotFound {
		t.Fatalf("missing: %v", err)
	}
	p := testutil.Write(t, t.TempDir(), "unsliced", testutil.Opts{NoGcode: true})
	if _, err := Inspect(p, 1); errfmt.As(err).Code != errfmt.ExitUsage {
		t.Fatalf("unsliced: %v", err)
	}
	c := testutil.Write(t, t.TempDir(), "corrupt", testutil.Opts{CorruptMD5: true})
	j, err := Inspect(c, 1)
	if err != nil || j.GcodeMD5OK == nil || *j.GcodeMD5OK {
		t.Fatalf("corrupt md5 not detected: %+v %v", j, err)
	}
}

func TestProjectFileVerifiedPayload(t *testing.T) {
	p := testutil.Write(t, t.TempDir(), "part", testutil.Opts{})
	j, _ := Inspect(p, 1)
	got := ProjectFile(j, RemoteName(p), 3, false)
	want := map[string]any{
		"command": "project_file", "param": "Metadata/plate_1.gcode", "url": "file:///sdcard/part.gcode.3mf",
		"subtask_name": "part", "md5": j.GcodeMD5, "project_id": "0", "profile_id": "0", "task_id": "0", "subtask_id": "0",
		"bed_type": "auto", "timelapse": false, "bed_leveling": true, "flow_cali": true, "vibration_cali": true,
		"layer_inspect": true, "use_ams": true, "ams_mapping": []int{3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload drift from the verified field set:\n got %v\nwant %v", got, want)
	}
}

func TestAMSMapping(t *testing.T) {
	j := &Job{FilamentPresets: []string{"a", "b", "c"}, Filaments: []Filament{{Index: 1}}}
	if got := AMSMapping(j, 2); !reflect.DeepEqual(got, []int{-1, 2, -1}) {
		t.Fatal(got)
	}
	if got := AMSMapping(&Job{}, 0); !reflect.DeepEqual(got, []int{0}) {
		t.Fatal(got)
	}
}

func TestRemoteName(t *testing.T) {
	for in, want := range map[string]string{
		"/x/my part.gcode.3mf": "my-part.gcode.3mf",
		"/x/p.3mf":             "p.gcode.3mf",
		"/x/ok_1-2.gcode.3mf":  "ok_1-2.gcode.3mf",
		"/x/ünï.gcode.3mf":     "-n-.gcode.3mf",
	} {
		if got := RemoteName(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}
