package camera

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/voska/bambu/internal/errfmt"
)

func TestNoFFmpeg(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := Snapshot(context.Background(), "192.0.2.1", "12345678", filepath.Join(t.TempDir(), "x.jpg"))
	if errfmt.As(err).Code != errfmt.ExitConfig {
		t.Fatalf("want config error, got %v", err)
	}
}

func TestErrorRedactsCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	dir := t.TempDir()
	// fake ffmpeg that echoes its input URL (which contains the access code) and fails
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in rtsps*) echo \"$a: Input/output error\" >&2;; esac; done\nexit 1\n"
	_ = os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte(script), 0o755)
	t.Setenv("PATH", dir)
	err := Snapshot(context.Background(), "192.0.2.1", "SECRET99", filepath.Join(dir, "x.jpg"))
	if err == nil || strings.Contains(err.Error(), "SECRET99") || !strings.Contains(err.Error(), "***") {
		t.Fatalf("code must be redacted: %v", err)
	}
}

func TestSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nfor a; do last=\"$a\"; done\nprintf 'JPEG' > \"$last\"\n"
	_ = os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte(script), 0o755)
	t.Setenv("PATH", dir)
	out := filepath.Join(dir, "sub", "snap.jpg")
	if err := Snapshot(context.Background(), "192.0.2.1", "12345678", out); err != nil {
		t.Fatal(err)
	}
}
