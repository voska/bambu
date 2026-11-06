package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/ftps"
)

func TestTimelapseList(t *testing.T) {
	h := newHarness(t, nil)
	h.ftp.Files["/timelapse/old.avi"] = []byte("old")
	h.ftp.Files["/timelapse/new video.mp4"] = []byte("new video")
	h.ftp.ModTimes["/timelapse/new video.mp4"] = time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	h.ftp.Files["/timelapse/preview.jpg"] = []byte("preview")
	h.ftp.Files["/other/video.mp4"] = []byte("elsewhere")
	if code := h.run("timelapse", "list", "--json"); code != 0 {
		t.Fatalf("list exit %d: %s", code, h.stdout.String())
	}
	var result struct {
		Printer string `json:"printer"`
		Rows    []struct {
			Name  string    `json:"name"`
			Size  int64     `json:"bytes"`
			MTime time.Time `json:"mtime"`
		} `json:"timelapses"`
	}
	h.json(&result)
	rows := result.Rows
	if result.Printer != "shop" || len(rows) != 2 || rows[0].Name != "new video.mp4" || rows[0].Size != 9 || rows[0].MTime.IsZero() {
		t.Fatalf("files: %+v", rows)
	}
	if code := h.run("timelapse", "list", "--plain"); code != 0 || !strings.Contains(h.stdout.String(), "name\tbytes\tmtime\n") {
		t.Fatalf("plain exit %d: %s", code, h.stdout.String())
	}
	if code := h.run("timelapse", "list", "-q"); code != 0 || h.stdout.String() != "new video.mp4\nold.avi\n" {
		t.Fatalf("quiet exit %d: %q", code, h.stdout.String())
	}
	if len(h.conn.Commands) != 0 {
		t.Fatal("listing published MQTT")
	}
}

func TestTimelapseEmpty(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("timelapse", "list", "--json"); code != errfmt.ExitEmpty || !strings.Contains(h.stdout.String(), `"timelapses": []`) {
		t.Fatalf("empty exit %d: %s", code, h.stdout.String())
	}
	if code := h.run("timelapse", "pull", "--latest", "-o", filepath.Join(h.dir, "video.mp4"), "--json"); code != errfmt.ExitEmpty {
		t.Fatalf("latest empty exit %d: %s", code, h.stdout.String())
	}
}

func TestTimelapsePull(t *testing.T) {
	for _, latest := range []bool{false, true} {
		t.Run(map[bool]string{false: "named", true: "latest"}[latest], func(t *testing.T) {
			h := newHarness(t, nil)
			payload := bytes.Repeat([]byte("video"), 1000)
			h.ftp.Files["/timelapse/video.mp4"] = payload
			out := filepath.Join(h.dir, "video.mp4")
			args := []string{"timelapse", "pull", "video.mp4"}
			if latest {
				args = []string{"timelapse", "pull", "--latest"}
			}
			args = append(args, "-o", out, "--json")
			if code := h.run(args...); code != 0 {
				t.Fatalf("pull exit %d: %s", code, h.stdout.String())
			}
			got, err := os.ReadFile(out)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("download mismatch: %v", err)
			}
			var result struct {
				Name, Path string
				Bytes      int64
			}
			h.json(&result)
			if result.Path != out || result.Name != "video.mp4" || result.Bytes != int64(len(payload)) {
				t.Fatalf("result: %+v", result)
			}
			if code := h.run(args...); code != errfmt.ExitError {
				t.Fatalf("existing destination must fail: %d %s", code, h.stdout.String())
			}
			got, _ = os.ReadFile(out)
			if !bytes.Equal(got, payload) || len(h.conn.Commands) != 0 {
				t.Fatal("retry changed the destination or published MQTT")
			}
		})
	}
}

func TestTimelapseInvalidSelection(t *testing.T) {
	for _, selection := range [][]string{
		{}, {"clip.mp4", "--latest"}, {"../clip.mp4"}, {"/clip.mp4"}, {"dir/clip.mp4"}, {`dir\clip.mp4`}, {"clip\r\nDELE x.mp4"}, {"preview.jpg"},
	} {
		h := newHarness(t, nil)
		args := append([]string{"timelapse", "pull"}, selection...)
		if code := h.run(append(args, "--json")...); code != errfmt.ExitUsage {
			t.Errorf("%q exit %d: %s", selection, code, h.stdout.String())
		}
	}
	h := newHarness(t, nil)
	if code := h.run("timelapse", "pull", "missing.mp4", "-o", filepath.Join(h.dir, "out.mp4"), "--json"); code != errfmt.ExitNotFound {
		t.Fatalf("missing file exit %d: %s", code, h.stdout.String())
	}
}

func TestTimelapseFailedDownloadLeavesNoFile(t *testing.T) {
	for _, reply := range []string{"213 100", "213 -1", "213 invalid"} {
		t.Run(reply, func(t *testing.T) {
			h := newHarness(t, nil)
			h.ftp.Files["/timelapse/video.mp4"] = []byte("short")
			h.ftp.Replies["SIZE /timelapse/video.mp4"] = reply
			out := filepath.Join(h.dir, "download.mp4")
			if code := h.run("timelapse", "pull", "--latest", "-o", out, "--json"); code != errfmt.ExitRetryable {
				t.Fatalf("incomplete download exit %d: %s", code, h.stdout.String())
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("failed download published a destination")
			}
			parts, _ := filepath.Glob(filepath.Join(h.dir, ".*.part"))
			if len(parts) != 0 {
				t.Fatalf("temporary files left behind: %v", parts)
			}
		})
	}
}

func TestTimelapseMetadataFailureIsNotEmpty(t *testing.T) {
	for _, command := range []string{"NLST /timelapse/", "MDTM /timelapse/video.mp4"} {
		h := newHarness(t, nil)
		h.ftp.Files["/timelapse/video.mp4"] = []byte("video")
		h.ftp.Replies[command] = "550 unavailable"
		if code := h.run("timelapse", "list", "--json"); code != errfmt.ExitRetryable {
			t.Fatalf("%s failure exit %d: %s", command, code, h.stdout.String())
		}
	}
}

func TestTimelapseOutputPaths(t *testing.T) {
	h := newHarness(t, nil)
	t.Chdir(h.dir)
	h.ftp.Files["/timelapse/a.mp4"] = []byte("older")
	h.ftp.Files["/timelapse/z.MP4"] = []byte("latest")
	if code := h.run("timelapse", "pull", "--latest", "-q"); code != 0 || strings.TrimSpace(h.stdout.String()) != filepath.Join(h.dir, "z.MP4") || h.stderr.Len() != 0 {
		t.Fatalf("default output exit %d: %s %s", code, h.stdout.String(), h.stderr.String())
	}
	if code := h.run("timelapse", "pull", "a.mp4", "-o", h.dir, "--plain"); code != 0 || !strings.Contains(h.stdout.String(), filepath.Join(h.dir, "a.mp4")) {
		t.Fatalf("directory output exit %d: %s %s", code, h.stdout.String(), h.stderr.String())
	}
	if code := h.run("timelapse", "pull", "a.mp4", "-o", "-", "--json"); code != errfmt.ExitUsage {
		t.Fatalf("stdout output exit %d: %s", code, h.stdout.String())
	}
}

func TestTimelapseAuthAndSchema(t *testing.T) {
	h := newHarness(t, nil)
	h.kr = memKeyring{}
	if code := h.run("timelapse", "list", "--json"); code != errfmt.ExitAuth {
		t.Fatalf("missing access code exit %d: %s", code, h.stdout.String())
	}
	if code := h.run("schema", "timelapse", "pull"); code != 0 || !strings.Contains(h.stdout.String(), `"name": "--latest"`) {
		t.Fatalf("schema exit %d: %s", code, h.stdout.String())
	}
}

func TestTimelapsePullRequiresSuccessfulTransferReply(t *testing.T) {
	h := newHarness(t, nil)
	h.ftp.Files["/timelapse/video.mp4"] = []byte("video")
	h.ftp.RetrieveReply = "426 transfer aborted"
	out := filepath.Join(h.dir, "video.mp4")
	if code := h.run("timelapse", "pull", "--latest", "-o", out, "--json"); code != errfmt.ExitRetryable {
		t.Fatalf("rejected transfer exit %d: %s", code, h.stdout.String())
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("rejected transfer published a file")
	}
}

func TestTimelapsePullRejectsChangedMetadata(t *testing.T) {
	h := newHarness(t, nil)
	h.ftp.Files["/timelapse/video.mp4"] = []byte("video")
	g := NewGlobals(context.Background(), &CLI{}, BuildInfo{})
	g.Out.Stdout, g.Out.Stderr = &h.stdout, &h.stderr
	f, err := ftps.Dial(g.Ctx, "127.0.0.1", h.ftp.Port(), "bblp", "12345678")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	before := timelapseFile{Name: "video.mp4", Bytes: 5, MTime: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)}
	out := filepath.Join(h.dir, "video.mp4")
	if err := pullTimelapse(g, f, before, out); err == nil || errfmt.As(err).Code != errfmt.ExitRetryable {
		t.Fatalf("changed mtime: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("changed source published a file")
	}
}

func TestTimelapseEmptyVideo(t *testing.T) {
	h := newHarness(t, nil)
	h.ftp.Files["/timelapse/z.mp4"] = nil
	out := filepath.Join(h.dir, "out.mp4")
	if code := h.run("timelapse", "pull", "z.mp4", "-o", out, "--json"); code != errfmt.ExitError {
		t.Fatalf("empty named video exit %d: %s", code, h.stdout.String())
	}
	if code := h.run("timelapse", "pull", "--latest", "-o", out, "--json"); code != errfmt.ExitEmpty {
		t.Fatalf("only empty videos exit %d: %s", code, h.stdout.String())
	}
	h.ftp.Files["/timelapse/a.mp4"] = []byte("valid")
	if code := h.run("timelapse", "pull", "--latest", "-o", out, "--json"); code != 0 {
		t.Fatalf("latest nonempty exit %d: %s", code, h.stdout.String())
	}
	got, _ := os.ReadFile(out)
	if string(got) != "valid" {
		t.Fatalf("downloaded %q", got)
	}
}

func TestTimelapseNamedPullIgnoresOtherMetadata(t *testing.T) {
	h := newHarness(t, nil)
	h.ftp.Files["/timelapse/broken.mp4"] = []byte("other")
	h.ftp.Files["/timelapse/video.mp4"] = []byte("video")
	h.ftp.Replies["MDTM /timelapse/broken.mp4"] = "550 unavailable"
	if code := h.run("timelapse", "pull", "video.mp4", "-o", filepath.Join(h.dir, "video.mp4"), "--json"); code != 0 {
		t.Fatalf("unrelated metadata blocked named pull: %d %s", code, h.stdout.String())
	}
}

func TestTimelapsePullRefusesMissingOutputDirectory(t *testing.T) {
	h := newHarness(t, nil)
	h.ftp.Files["/timelapse/video.mp4"] = []byte("video")
	if code := h.run("timelapse", "pull", "--latest", "-o", filepath.Join(h.dir, "missing")+string(os.PathSeparator), "--json"); code != errfmt.ExitUsage {
		t.Fatalf("missing output directory exit %d: %s", code, h.stdout.String())
	}
}

func TestTimelapsePullPreservesExistingFile(t *testing.T) {
	h := newHarness(t, nil)
	h.ftp.Files["/timelapse/video.mp4"] = []byte("video")
	out := filepath.Join(h.dir, "video.mp4")
	if err := os.WriteFile(out, []byte("keep this"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("timelapse", "pull", "--latest", "-o", out, "--json"); code != errfmt.ExitError {
		t.Fatalf("existing output exit %d: %s", code, h.stdout.String())
	}
	got, _ := os.ReadFile(out)
	if string(got) != "keep this" {
		t.Fatalf("overwrote existing file: %q", got)
	}
}
