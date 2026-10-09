package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/voska/bambu/internal/config"
)

func TestNotifications(t *testing.T) {
	for _, image := range []bool{false, true} {
		var gotMethod, gotBody, gotMessage string
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/base/printer" || r.Header.Get("Authorization") != "Bearer TEST_TOKEN" || r.Header.Get("Priority") != "4" {
				t.Errorf("request: %s %v", r.URL.Path, r.Header)
			}
			b, _ := io.ReadAll(r.Body)
			gotMethod, gotBody, gotMessage = r.Method, string(b), r.Header.Get("Message")
			w.WriteHeader(http.StatusOK)
		}))
		file := ""
		if image {
			file = filepath.Join(t.TempDir(), "frame.jpg")
			_ = os.WriteFile(file, []byte("JPEG"), 0o600)
		}
		err := Send(context.Background(), config.Ntfy{URL: s.URL + "/base", Topic: "printer"}, "TEST_TOKEN", "layer check", "line 1\nline 2", "camera", 4, file)
		s.Close()
		if err != nil {
			t.Fatal(err)
		}
		if image {
			if gotMethod != "PUT" || gotBody != "JPEG" || gotMessage != "line 1 line 2" {
				t.Fatalf("image %s %s %s", gotMethod, gotBody, gotMessage)
			}
		} else if gotMethod != "POST" || gotBody != "line 1\nline 2" {
			t.Fatalf("text %s %s", gotMethod, gotBody)
		}
	}
}

func TestNotificationFailure(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer s.Close()
	if err := Send(context.Background(), config.Ntfy{URL: s.URL, Topic: "printer"}, "", "title", "message", "warning", 4, ""); err == nil {
		t.Fatal("403 is not success")
	}
}
