// Package notify sends optional printer event notifications through ntfy.
package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/voska/bambu/internal/config"
)

// Send posts a text event, or uploads a snapshot as an attachment. Callers decide how to report failures.
func Send(ctx context.Context, cfg config.Ntfy, token, title, message, tags string, priority int, snapshot string) error {
	if err := Validate(cfg); err != nil {
		return err
	}
	endpoint := strings.TrimRight(cfg.URL, "/") + "/" + url.PathEscape(cfg.Topic)
	method, body := http.MethodPost, io.Reader(strings.NewReader(message))
	if snapshot != "" {
		f, err := os.Open(snapshot) //nolint:gosec // the camera output chosen by the user
		if err != nil {
			return fmt.Errorf("open ntfy attachment: %w", err)
		}
		defer func() { _ = f.Close() }()
		method, body = http.MethodPut, f
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("create ntfy request")
	}
	req.Header.Set("Title", title)
	req.Header.Set("Tags", tags)
	req.Header.Set("Priority", strconv.Itoa(priority))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if snapshot != "" {
		req.Header.Set("Filename", filepath.Base(snapshot))
		req.Header.Set("Message", strings.NewReplacer("\n", " ", "\r", " ").Replace(message))
	}
	client := &http.Client{Timeout: 15 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy request failed (network, timeout or invalid headers)")
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned HTTP %d", res.StatusCode)
	}
	return nil
}

// Validate checks notification configuration without making a network request.
func Validate(cfg config.Ntfy) error {
	u, err := url.Parse(strings.TrimRight(cfg.URL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("ntfy url must be an http(s) base URL without credentials, query or fragment")
	}
	if cfg.Topic == "" || strings.ContainsAny(cfg.Topic, "/\r\n?#") {
		return fmt.Errorf("invalid ntfy topic")
	}
	return nil
}
