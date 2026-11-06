package ftps

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/testutil"
)

func TestRoundTrip(t *testing.T) {
	s := testutil.NewFTPSServer(t)
	ctx := context.Background()
	c, err := Dial(ctx, "127.0.0.1", s.Port(), "bblp", "12345678")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	names, err := c.List(ctx, "/")
	if err != nil || len(names) != 1 || names[0] != "/existing.gcode.3mf" {
		t.Fatalf("list: %v %v", names, err)
	}
	payload := bytes.Repeat([]byte("gcode"), 100000)
	if err := c.Store(ctx, "/job.gcode.3mf", bytes.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	sum, err := c.MD5(ctx, "/job.gcode.3mf")
	want := md5.Sum(payload)
	if err != nil || sum != hex.EncodeToString(want[:]) {
		t.Fatalf("md5 %s %v", sum, err)
	}
	n, err := c.Size("/job.gcode.3mf")
	if err != nil || n != int64(len(payload)) {
		t.Fatalf("size %d %v", n, err)
	}
	if _, err := c.MD5(ctx, "/missing"); err == nil {
		t.Fatal("expected error for missing file")
	}
	if err := c.Store(ctx, "/bad\x01name", bytes.NewReader(nil)); err == nil {
		t.Fatal("expected control-char rejection")
	}
}

func TestBadPassword(t *testing.T) {
	s := testutil.NewFTPSServer(t)
	_, err := Dial(context.Background(), "127.0.0.1", s.Port(), "bblp", "wrong")
	if err == nil || !strings.Contains(err.Error(), "login refused") {
		t.Fatalf("want login refused, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatal("password leaked in error")
	}
}

func TestParsePASV(t *testing.T) {
	p, err := parsePASV("Entering Passive Mode (192,168,1,5,195,80).")
	if err != nil || p != 195*256+80 {
		t.Fatal(p, err)
	}
	for _, bad := range []string{"nope", "(1,2,3)", "(1,2,3,4,999,1)"} {
		if _, err := parsePASV(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestRetrieveAndModTime(t *testing.T) {
	s := testutil.NewFTPSServer(t)
	s.ModTimes["/existing.gcode.3mf"] = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c, err := Dial(context.Background(), "127.0.0.1", s.Port(), "bblp", "12345678")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var buf bytes.Buffer
	n, err := c.Retrieve(context.Background(), "/existing.gcode.3mf", &buf)
	if err != nil || n != 5 || buf.String() != "hello" {
		t.Fatalf("retrieve: %d %q %v", n, buf.String(), err)
	}
	mtime, err := c.ModTime("/existing.gcode.3mf")
	if err != nil || !mtime.Equal(s.ModTimes["/existing.gcode.3mf"]) {
		t.Fatalf("mtime: %v %v", mtime, err)
	}
	if _, err := c.Retrieve(context.Background(), "/bad\r\nname", &buf); err == nil {
		t.Fatal("accepted command injection")
	}
	if _, err := c.ModTime("/bad\nname"); err == nil {
		t.Fatal("accepted command injection")
	}
}

func TestModTimeRejectsMalformedReply(t *testing.T) {
	s := testutil.NewFTPSServer(t)
	s.Replies["MDTM /existing.gcode.3mf"] = "213 yesterday"
	c, err := Dial(context.Background(), "127.0.0.1", s.Port(), "bblp", "12345678")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ModTime("/existing.gcode.3mf"); err == nil {
		t.Fatal("accepted malformed MDTM")
	}
}

func TestSessionCancellation(t *testing.T) {
	s := testutil.NewFTPSServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := Dial(ctx, "127.0.0.1", s.Port(), "bblp", "12345678")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cancel()
	// The context callback closes the control socket, interrupting blocked I/O.
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := c.ModTime("/existing.gcode.3mf"); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled session still usable")
		}
	}
}

type failedDownloadWriter struct{ short bool }

func (w failedDownloadWriter) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, io.ErrClosedPipe
}

func TestRetrieveLocalWriteFailure(t *testing.T) {
	for _, short := range []bool{false, true} {
		s := testutil.NewFTPSServer(t)
		c, err := Dial(context.Background(), "127.0.0.1", s.Port(), "bblp", "12345678")
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Retrieve(context.Background(), "/existing.gcode.3mf", failedDownloadWriter{short: short})
		var typed *errfmt.Error
		if !errors.As(err, &typed) || typed.Code != errfmt.ExitError {
			t.Errorf("local write failure must be distinguishable from network failure: %v", err)
		}
		c.Close()
	}
}

func TestRetrieveReadIdleTimeout(t *testing.T) {
	s := testutil.NewFTPSServer(t)
	s.RetrieveDelay = 500 * time.Millisecond
	c, err := Dial(context.Background(), "127.0.0.1", s.Port(), "bblp", "12345678")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.timeout = 100 * time.Millisecond
	var buf bytes.Buffer
	_, err = c.Retrieve(context.Background(), "/existing.gcode.3mf", &buf)
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() || !strings.Contains(err.Error(), "read download") {
		t.Fatalf("stalled read must time out: %v", err)
	}
}
