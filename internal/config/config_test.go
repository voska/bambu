package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/voska/bambu/internal/errfmt"
)

func TestRoundTripAndSelect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Select(""); errfmt.As(err).Code != errfmt.ExitConfig {
		t.Fatalf("empty config: want config error, got %v", err)
	}
	c.Printers["shop"] = &Printer{Name: "shop", Host: "192.0.2.10", Serial: "00M00A000000001", Model: "X1C", Nozzle: 0.4, Plate: "textured_plate"}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v", st.Mode().Perm())
	}
	c2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c2.Select("")
	if err != nil || p.Name != "shop" || p.Host != "192.0.2.10" {
		t.Fatalf("select single: %v %+v", err, p)
	}
	c2.Printers["garage"] = &Printer{Name: "garage", Host: "192.0.2.11", Serial: "01P00A000000002", Model: "P1S", Nozzle: 0.4, Plate: "cool_plate"}
	if _, err := c2.Select(""); errfmt.As(err).Code != errfmt.ExitUsage {
		t.Fatalf("ambiguous: want usage, got %v", err)
	}
	c2.DefaultPrinter = "garage"
	if p, _ := c2.Select(""); p.Name != "garage" {
		t.Fatal("default not honoured")
	}
	if p, _ := c2.Select("shop"); p.Name != "shop" {
		t.Fatal("explicit not honoured")
	}
	if _, err := c2.Select("nope"); errfmt.As(err).Code != errfmt.ExitNotFound {
		t.Fatal("want not found")
	}
}

func TestDefaultsApplied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[printers.a]\nhost=\"192.0.2.1\"\nserial=\"ABCDEFGH1\"\nmodel=\"X1C\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p := c.Printers["a"]
	if p.Nozzle != 0.4 || p.Plate != "textured_plate" || p.Name != "a" {
		t.Fatalf("defaults: %+v", p)
	}
}

func TestValidate(t *testing.T) {
	good := Printer{Name: "x1", Host: "printer.local", Serial: "00M00A000000001", Nozzle: 0.4, Plate: "cool_plate"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mut := range []func(p *Printer){
		func(p *Printer) { p.Name = "../x" },
		func(p *Printer) { p.Host = "http://x" },
		func(p *Printer) { p.Serial = "short" },
		func(p *Printer) { p.Plate = "glass" },
		func(p *Printer) { p.Nozzle = 0 },
	} {
		p := good
		mut(&p)
		if err := p.Validate(); err == nil {
			t.Fatalf("expected error for %+v", p)
		}
	}
}

func TestBadTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	_ = os.WriteFile(path, []byte("[printers\n"), 0o600)
	if _, err := Load(path); errfmt.As(err).Code != errfmt.ExitConfig {
		t.Fatalf("want config error, got %v", err)
	}
}
