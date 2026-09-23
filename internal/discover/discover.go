// Package discover listens for Bambu printers' SSDP NOTIFY broadcasts (UDP 2021, sent every few seconds).
package discover

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/voska/bambu/internal/errfmt"
)

// Port is the UDP port Bambu printers broadcast NOTIFY messages to.
const Port = 2021

// Device is one discovered printer.
type Device struct {
	Host     string `json:"host"`
	Serial   string `json:"serial"`
	ModelID  string `json:"model_id"`
	Name     string `json:"name"`
	Connect  string `json:"connect,omitempty"` // "lan" or "cloud"
	Bind     string `json:"bind,omitempty"`
	Firmware string `json:"firmware,omitempty"`
	Signal   string `json:"signal,omitempty"`
}

// Parse parses one NOTIFY packet; ok is false for non-Bambu packets.
func Parse(pkt []byte, from string) (Device, bool) {
	h := map[string]string{}
	for _, line := range strings.Split(string(pkt), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok {
			h[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	if !strings.Contains(strings.ToLower(h["nt"]), "bambulab") && h["devmodel.bambu.com"] == "" {
		return Device{}, false
	}
	d := Device{
		Host: h["location"], Serial: h["usn"], ModelID: h["devmodel.bambu.com"], Name: h["devname.bambu.com"],
		Connect: h["devconnect.bambu.com"], Bind: h["devbind.bambu.com"], Firmware: h["devversion.bambu.com"], Signal: h["devsignal.bambu.com"],
	}
	if d.Host == "" {
		d.Host = from
	}
	return d, d.Serial != ""
}

// Listen collects devices until ctx is done. The socket is opened with SO_REUSEADDR/SO_REUSEPORT where
// available; if another app (usually Bambu Studio) holds the port exclusively, it returns a clear error.
func Listen(ctx context.Context, port int) ([]Device, error) {
	lc := net.ListenConfig{Control: reuse}
	pc, err := lc.ListenPacket(ctx, "udp4", net.JoinHostPort("", strconv.Itoa(port)))
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) || strings.Contains(err.Error(), "address already in use") {
			return nil, errfmt.Wrap(errfmt.ExitRetryable, err, "UDP port %d is in use (usually by Bambu Studio)", port).
				WithHint("quit Bambu Studio and retry, or add the printer manually: bambu printer add <name> --host <ip> --serial <serial>")
		}
		return nil, errfmt.Wrap(errfmt.ExitError, err, "listen on UDP %d", port)
	}
	defer func() { _ = pc.Close() }()
	go func() { <-ctx.Done(); _ = pc.SetReadDeadline(time.Now()) }()
	seen := map[string]Device{}
	var order []string
	buf := make([]byte, 4096)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			break
		}
		from := ""
		if ua, ok := addr.(*net.UDPAddr); ok {
			from = ua.IP.String()
		}
		if d, ok := Parse(buf[:n], from); ok {
			if _, dup := seen[d.Serial]; !dup {
				order = append(order, d.Serial)
			}
			seen[d.Serial] = d
		}
	}
	out := make([]Device, 0, len(order))
	for _, s := range order {
		out = append(out, seen[s])
	}
	return out, nil
}
