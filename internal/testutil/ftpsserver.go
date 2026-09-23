package testutil

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// FTPSServer is a tiny implicit-FTPS server that, like the printer, refuses data connections
// that do not resume the control connection's TLS session.
type FTPSServer struct {
	t     *testing.T
	ln    net.Listener
	cfg   *tls.Config
	mu    sync.Mutex
	Files map[string][]byte
	Code  string
}

// NewFTPSServer starts a fake printer FTPS server on 127.0.0.1 (password 12345678).
func NewFTPSServer(t *testing.T) *FTPSServer {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "printer"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := &FTPSServer{t: t, ln: ln, cfg: cfg, Files: map[string][]byte{"/existing.gcode.3mf": []byte("hello")}, Code: "12345678"}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

// Port returns the listening port.
func (s *FTPSServer) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *FTPSServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *FTPSServer) handle(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(format string, a ...any) { fmt.Fprintf(c, format+"\r\n", a...) }
	say("220 fake printer")
	var dataLn net.Listener
	accept := func() (*tls.Conn, bool) {
		dc, err := dataLn.Accept()
		_ = dataLn.Close()
		if err != nil {
			return nil, false
		}
		tc := dc.(*tls.Conn)
		if err := tc.Handshake(); err != nil || !tc.ConnectionState().DidResume {
			_ = tc.Close()
			say("425 data connection must reuse the TLS session")
			return nil, false
		}
		return tc, true
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch cmd {
		case "USER":
			say("331 password please")
		case "PASS":
			if arg != s.Code {
				say("530 login incorrect")
				continue
			}
			say("230 ok")
		case "PBSZ", "PROT", "TYPE":
			say("200 ok")
		case "PASV":
			dataLn, _ = tls.Listen("tcp", "127.0.0.1:0", s.cfg)
			p := dataLn.Addr().(*net.TCPAddr).Port
			say("227 Entering Passive Mode (10,9,8,7,%d,%d)", p/256, p%256) // bogus IP: client must use its host
		case "STOR":
			say("150 ok")
			if dc, ok := accept(); ok {
				b, _ := io.ReadAll(dc)
				_ = dc.Close()
				s.mu.Lock()
				s.Files[arg] = b
				s.mu.Unlock()
				say("226 done")
			}
		case "RETR":
			s.mu.Lock()
			b, ok := s.Files[arg]
			s.mu.Unlock()
			if !ok {
				say("550 not found")
				continue
			}
			say("150 ok")
			if dc, ok := accept(); ok {
				_, _ = dc.Write(b)
				_ = dc.Close()
				say("226 done")
			}
		case "NLST":
			say("150 ok")
			if dc, ok := accept(); ok {
				s.mu.Lock()
				for n := range s.Files {
					fmt.Fprintf(dc, "%s\r\n", n)
				}
				s.mu.Unlock()
				_ = dc.Close()
				say("226 done")
			}
		case "SIZE":
			s.mu.Lock()
			b, ok := s.Files[arg]
			s.mu.Unlock()
			if !ok {
				say("550 not found")
				continue
			}
			say("213 %d", len(b))
		case "QUIT":
			say("221 bye")
			return
		default:
			say("502 not implemented")
		}
	}
}

// File returns a stored file.
func (s *FTPSServer) File(name string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.Files[name]
	return b, ok
}
