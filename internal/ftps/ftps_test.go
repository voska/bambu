package ftps

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"strings"
	"testing"

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
