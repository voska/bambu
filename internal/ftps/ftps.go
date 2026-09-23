// Package ftps is a minimal implicit-TLS FTP client for Bambu printers (port 990, user bblp).
//
// The printer requires implicit TLS, PROT P, and TLS session reuse on data connections (like vsftpd's
// require_ssl_reuse). Session reuse works because the control and data connections share one tls.Config
// with a ClientSessionCache and an explicit ServerName (the cache key).
package ftps

import (
	"context"
	"crypto/md5" //nolint:gosec // MD5 is the printer's integrity checksum, not a security primitive
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/voska/bambu/internal/errfmt"
)

// Client is an authenticated FTPS session.
type Client struct {
	host    string
	cfg     *tls.Config
	ctrl    *textproto.Conn
	raw     net.Conn
	timeout time.Duration
}

// Dial connects with implicit TLS, logs in and enables protected binary transfers.
func Dial(ctx context.Context, host string, port int, user, pass string) (*Client, error) {
	cfg := &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // printers use self-signed certs
		ServerName:         host, // session-cache key shared by control and data connections
		ClientSessionCache: tls.NewLRUClientSessionCache(8),
		MinVersion:         tls.VersionTLS12,
	}
	d := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 15 * time.Second}, Config: cfg}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, errfmt.Wrap(errfmt.ExitRetryable, err, "FTPS connect to %s:%d failed", host, port).
			WithHint("check the printer is reachable and in LAN mode")
	}
	c := &Client{host: host, cfg: cfg, raw: conn, ctrl: textproto.NewConn(conn), timeout: 60 * time.Second}
	steps := []struct {
		cmd  string
		code int
	}{{"", 220}, {"USER " + user, 331}, {"PASS " + pass, 230}, {"PBSZ 0", 200}, {"PROT P", 200}, {"TYPE I", 200}}
	for _, s := range steps {
		var err error
		if s.cmd == "" {
			_, err = c.cmd(s.code, "")
		} else {
			_, err = c.cmd(s.code, "%s", s.cmd)
		}
		if err != nil {
			_ = conn.Close()
			if s.code == 230 {
				return nil, errfmt.Wrap(errfmt.ExitAuth, err, "FTPS login refused").
					WithHint("the access code is wrong or stale: bambu auth set <printer>")
			}
			return nil, errfmt.Wrap(errfmt.ExitRetryable, err, "FTPS handshake failed at %q", redact(s.cmd))
		}
	}
	return c, nil
}

func redact(cmd string) string {
	if strings.HasPrefix(cmd, "PASS ") {
		return "PASS ***"
	}
	return cmd
}

// cmd sends a command (empty = just read) and expects a reply code (prefix match: 2 accepts any 2xx).
func (c *Client) cmd(expect int, format string, args ...any) (string, error) {
	_ = c.raw.SetDeadline(time.Now().Add(c.timeout))
	if format != "" {
		if err := c.ctrl.PrintfLine(format, args...); err != nil {
			return "", fmt.Errorf("send: %w", err)
		}
	}
	code, msg, err := c.ctrl.ReadResponse(expect)
	if err != nil {
		return msg, fmt.Errorf("%d %s: %w", code, msg, err)
	}
	return msg, nil
}

// pasv opens a protected data connection that resumes the control connection's TLS session.
// It dials the configured host (not the PASV address) so NAT/Docker setups work.
func (c *Client) pasv(ctx context.Context) (net.Conn, error) {
	msg, err := c.cmd(227, "PASV")
	if err != nil {
		return nil, err
	}
	port, err := parsePASV(msg)
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(c.host, strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("data connection: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Minute))
	// The TLS handshake happens lazily, after the transfer command is sent (servers accept the
	// data connection's TLS only once they have the command).
	return tls.Client(conn, c.cfg), nil
}

func parsePASV(msg string) (int, error) {
	start, end := strings.Index(msg, "("), strings.Index(msg, ")")
	if start < 0 || end < start {
		return 0, fmt.Errorf("bad PASV reply %q", msg)
	}
	parts := strings.Split(msg[start+1:end], ",")
	if len(parts) != 6 {
		return 0, fmt.Errorf("bad PASV reply %q", msg)
	}
	hi, err1 := strconv.Atoi(strings.TrimSpace(parts[4]))
	lo, err2 := strconv.Atoi(strings.TrimSpace(parts[5]))
	if err1 != nil || err2 != nil || hi < 0 || hi > 255 || lo < 0 || lo > 255 {
		return 0, fmt.Errorf("bad PASV port in %q", msg)
	}
	return hi*256 + lo, nil
}

// transfer runs a data command: fn reads from or writes to the data connection.
func (c *Client) transfer(ctx context.Context, command string, fn func(net.Conn) error) error {
	data, err := c.pasv(ctx)
	if err != nil {
		return err
	}
	if _, err := c.cmd(1, "%s", command); err != nil { // 125/150
		_ = data.Close()
		return err
	}
	if tc, ok := data.(*tls.Conn); ok {
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = data.Close()
			_, _ = c.cmd(0, "") // consume the server's error reply
			return fmt.Errorf("data connection TLS: %w", err)
		}
	}
	ferr := fn(data)
	cerr := data.Close() // sends TLS close_notify; the printer only replies 226 after it
	if _, err := c.cmd(226, ""); err != nil && ferr == nil {
		ferr = err
	}
	if ferr == nil && cerr != nil && !strings.Contains(cerr.Error(), "closed") {
		ferr = cerr
	}
	return ferr
}

// Store uploads r to path.
func (c *Client) Store(ctx context.Context, path string, r io.Reader) error {
	if err := checkPath(path); err != nil {
		return err
	}
	return c.transfer(ctx, "STOR "+path, func(conn net.Conn) error {
		_, err := io.Copy(conn, r)
		return err //nolint:wrapcheck // surfaced with context by caller
	})
}

// MD5 downloads path and returns its hex MD5.
func (c *Client) MD5(ctx context.Context, path string) (string, error) {
	if err := checkPath(path); err != nil {
		return "", err
	}
	h := md5.New() //nolint:gosec // integrity check against the printer copy
	err := c.transfer(ctx, "RETR "+path, func(conn net.Conn) error {
		_, err := io.Copy(h, conn)
		return err //nolint:wrapcheck // surfaced with context by caller
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

// List returns the names in dir (NLST).
func (c *Client) List(ctx context.Context, dir string) ([]string, error) {
	if err := checkPath(dir); err != nil {
		return nil, err
	}
	var buf strings.Builder
	err := c.transfer(ctx, "NLST "+dir, func(conn net.Conn) error {
		_, err := io.Copy(&buf, conn)
		return err //nolint:wrapcheck // surfaced with context by caller
	})
	var names []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names, err
}

// Size returns the size of path in bytes.
func (c *Client) Size(path string) (int64, error) {
	if err := checkPath(path); err != nil {
		return 0, err
	}
	msg, err := c.cmd(213, "SIZE %s", path)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(msg), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad SIZE reply %q: %w", msg, err)
	}
	return n, nil
}

// Close ends the session.
func (c *Client) Close() error {
	_, _ = c.cmd(221, "QUIT")
	return c.raw.Close() //nolint:wrapcheck // best-effort close
}

func checkPath(p string) error {
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid control character in path %q", p)
		}
	}
	return nil
}
