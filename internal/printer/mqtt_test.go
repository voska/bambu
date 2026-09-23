package printer

import (
	"testing"

	"github.com/voska/bambu/internal/errfmt"
)

func TestCheckAck(t *testing.T) {
	if err := CheckAck(nil, "x"); err != nil {
		t.Fatal("no ack is not an error")
	}
	if err := CheckAck(map[string]any{"result": "SUCCESS"}, "x"); err != nil {
		t.Fatal(err)
	}
	err := CheckAck(map[string]any{"result": "FAIL", "reason": "mqtt message verify failed"}, "project_file")
	if errfmt.As(err).Code != errfmt.ExitForbidden {
		t.Fatalf("verification failure should be forbidden: %v", err)
	}
	err = CheckAck(map[string]any{"result": "failed", "reason": "sd card error"}, "project_file")
	if errfmt.As(err).Code != errfmt.ExitError {
		t.Fatalf("%v", err)
	}
}

func TestDeepMerge(t *testing.T) {
	dst := map[string]any{"a": 1.0, "ams": map[string]any{"tray_now": "1", "x": "keep"}}
	DeepMerge(dst, map[string]any{"b": 2.0, "ams": map[string]any{"tray_now": "3"}})
	ams := dst["ams"].(map[string]any)
	if dst["a"] != 1.0 || dst["b"] != 2.0 || ams["tray_now"] != "3" || ams["x"] != "keep" {
		t.Fatalf("%v", dst)
	}
}
