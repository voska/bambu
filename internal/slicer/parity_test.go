package slicer

import (
	"archive/zip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/voska/bambu/internal/errfmt"
)

func TestRunDoesNotReuseStaleSlice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	_, rq, err := twoFilamentRun(t, func(r *Request) { r.ChangeZs = []float64{3.0} })
	if errfmt.As(err).Code != errfmt.ExitSliceFailed {
		t.Fatalf("first slice must fail verification: %v", err)
	}
	rq.ChangeZs = []float64{4.0}
	bin := filepath.Join(t.TempDir(), "fail.sh")
	_ = os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755)
	_, err = Run(context.Background(), &Studio{Binary: bin}, index(t), rq)
	if err == nil || errfmt.As(err).Code != errfmt.ExitSliceFailed || exists(filepath.Join(rq.OutDir, "two.gcode.3mf")) {
		t.Fatalf("failed process must not publish a stale slice: %v", err)
	}
}

func TestRunRejectsUnexpectedChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	_, rq, _ := twoFilamentRun(t, func(r *Request) { r.Filaments, r.Colors, r.ChangeZs = r.Filaments[:1], nil, nil })
	if exists(filepath.Join(rq.OutDir, "two.gcode.3mf")) {
		t.Fatal("single-filament request must reject G-code with an unexpected T1 change")
	}
}

func TestSliceObjectFootprintAndSlot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	sum, _, err := twoFilamentRun(t, func(r *Request) { r.SuggestedSlot = "A4" })
	if err != nil {
		t.Fatal(err)
	}
	if sum.SuggestedSlot != "A4" || len(sum.Objects) != 1 || !reflect.DeepEqual(sum.Objects[0].Footprint, [3]float64{98.41, 98.41, 5.4}) {
		t.Fatalf("summary %+v", sum)
	}
}

func TestFootprintsMatchFirstPlate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	sum, _, err := twoFilamentRun(t, func(_ *Request) {
		b, _ := os.ReadFile(os.Getenv("FAKE_RESULT"))
		var res map[string]any
		if err := json.Unmarshal(b, &res); err != nil {
			t.Fatal(err)
		}
		plates := res["sliced_plates"].([]any)
		res["sliced_plates"] = append(plates, map[string]any{"objects": []any{map[string]any{"name": "unrelated plate", "bbox": map[string]any{"width": 250, "depth": 250, "height": 100}}}})
		b, _ = json.Marshal(res)
		path := filepath.Join(t.TempDir(), "multi-result.json")
		_ = os.WriteFile(path, b, 0o600)
		t.Setenv("FAKE_RESULT", path)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Objects) != 1 || sum.Objects[0].Name == "unrelated plate" {
		t.Fatalf("wrong plate footprints: %+v", sum.Objects)
	}
}

func TestStrictSliceNeedsPreview(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	_, rq, err := twoFilamentRun(t, func(r *Request) {
		r.RequirePreview = true
		zr, err := zip.OpenReader(os.Getenv("FAKE_3MF"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = zr.Close() }()
		path := filepath.Join(t.TempDir(), "no-preview.gcode.3mf")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(file)
		for _, entry := range zr.File {
			if entry.Name == "Metadata/plate_1.png" {
				continue
			}
			reader, err := entry.Open()
			if err != nil {
				t.Fatal(err)
			}
			writer, err := zw.Create(entry.Name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(writer, reader); err != nil {
				t.Fatal(err)
			}
			_ = reader.Close()
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		t.Setenv("FAKE_3MF", path)
	})
	if err == nil || errfmt.As(err).Code != errfmt.ExitSliceFailed || exists(filepath.Join(rq.OutDir, "two.gcode.3mf")) {
		t.Fatalf("missing preview must not publish strict slice: %v", err)
	}
}

func TestSTEPConversion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	dir := t.TempDir()
	python := filepath.Join(dir, "python")
	_ = os.WriteFile(python, []byte("#!/bin/sh\nprintf mesh > \"$4\"\n"), 0o755)
	mesh, err := prepareSTEP(context.Background(), python, filepath.Join(dir, "part.step"), dir)
	if err != nil || !exists(mesh) {
		t.Fatalf("STEP conversion: %s %v", mesh, err)
	}
	_ = os.WriteFile(python, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	if _, err := prepareSTEP(context.Background(), python, filepath.Join(dir, "part.stp"), dir); err == nil {
		t.Fatal("stale mesh accepted after converter wrote nothing")
	}
}
