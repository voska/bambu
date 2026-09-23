// Package camera grabs a single frame from a printer camera.
package camera

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/voska/bambu/internal/errfmt"
)

// Snapshot writes one JPEG frame from rtsps://host:322/streaming/live/1 (X1/H2/P2S series) using ffmpeg.
// ffmpeg >= 8 verifies TLS by default and the printer certificate is self-signed, hence -tls_verify 0.
// The access code is passed in the URL (visible to local `ps` for the ~3 s ffmpeg runs) and redacted from errors.
func Snapshot(ctx context.Context, host, code, out string) error {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return errfmt.New(errfmt.ExitConfig, "ffmpeg not found").WithHint("install ffmpeg (brew install ffmpeg / apt install ffmpeg)")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return errfmt.Wrap(errfmt.ExitConfig, err, "create output dir")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	url := "rtsps://bblp:" + code + "@" + host + ":322/streaming/live/1"
	cmd := exec.CommandContext(ctx, ffmpeg, "-y", "-loglevel", "error", "-rtsp_transport", "tcp", "-tls_verify", "0", //nolint:gosec // fixed args
		"-i", url, "-frames:v", "1", "-q:v", "2", out)
	b, runErr := cmd.CombinedOutput()
	msg := strings.TrimSpace(strings.ReplaceAll(string(b), code, "***"))
	if ctx.Err() != nil {
		return errfmt.New(errfmt.ExitRetryable, "camera snapshot timed out").
			WithHint("enable LAN Mode Liveview on the printer (Settings > LAN Only / General)")
	}
	if runErr != nil {
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		return errfmt.New(errfmt.ExitRetryable, "camera snapshot failed: %s", msg).
			WithHint("check `bambu status` camera_lan_liveview; enable LAN Mode Liveview on the printer")
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		return errfmt.New(errfmt.ExitRetryable, "camera snapshot produced no image").WithHint("retry")
	}
	return nil
}
