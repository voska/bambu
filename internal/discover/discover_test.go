package discover

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/voska/bambu/internal/errfmt"
)

const pkt = "NOTIFY * HTTP/1.1\r\nHOST: 239.255.255.250:1990\r\nServer: UPnP/1.0\r\nLocation: 192.0.2.50\r\n" +
	"NT: urn:bambulab-com:device:3dprinter:1\r\nUSN: 00M00A000000001\r\nDevModel.bambu.com: BL-P001\r\n" +
	"DevName.bambu.com: Workshop X1C\r\nDevConnect.bambu.com: lan\r\nDevBind.bambu.com: free\r\nDevVersion.bambu.com: 01.12.00.00\r\n\r\n"

func TestParse(t *testing.T) {
	d, ok := Parse([]byte(pkt), "192.0.2.99")
	if !ok || d.Host != "192.0.2.50" || d.Serial != "00M00A000000001" || d.ModelID != "BL-P001" || d.Name != "Workshop X1C" || d.Connect != "lan" {
		t.Fatalf("%+v", d)
	}
	if _, ok := Parse([]byte("M-SEARCH * HTTP/1.1\r\nST: ssdp:all\r\n"), "x"); ok {
		t.Fatal("non-Bambu packet accepted")
	}
}

func freePort(t *testing.T) int {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := pc.LocalAddr().(*net.UDPAddr).Port
	_ = pc.Close()
	return p
}

func TestListen(t *testing.T) {
	port := freePort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	go func() {
		time.Sleep(150 * time.Millisecond)
		c, err := net.Dial("udp4", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			return
		}
		defer c.Close()
		for i := 0; i < 3; i++ {
			_, _ = c.Write([]byte(pkt))
		}
	}()
	devs, err := Listen(ctx, port)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 || devs[0].Serial != "00M00A000000001" {
		t.Fatalf("%+v", devs)
	}
}

func TestPortInUse(t *testing.T) {
	// An exclusive holder (no SO_REUSEPORT), like Bambu Studio.
	pc, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	port := pc.LocalAddr().(*net.UDPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = Listen(ctx, port)
	if err == nil {
		t.Skip("OS allowed a shared bind; nothing to assert")
	}
	if errfmt.As(err).Code != errfmt.ExitRetryable {
		t.Fatalf("want retryable with hint, got %v", err)
	}
}
