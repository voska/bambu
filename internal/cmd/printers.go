package cmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/voska/bambu/internal/auth"
	"github.com/voska/bambu/internal/config"
	"github.com/voska/bambu/internal/discover"
	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/output"
	"github.com/voska/bambu/internal/printer"
)

// PrinterCmd groups printer management commands.
type PrinterCmd struct {
	Add      PrinterAddCmd      `cmd:"" help:"Add or update a printer (idempotent)."`
	List     PrinterListCmd     `cmd:"" help:"List configured printers."`
	Remove   PrinterRemoveCmd   `cmd:"" help:"Remove a printer from the config (requires --confirm)."`
	Default  PrinterDefaultCmd  `cmd:"" help:"Set the default printer."`
	Discover PrinterDiscoverCmd `cmd:"" help:"Listen for printers announcing themselves on the LAN (SSDP, UDP 2021)."`
}

// PrinterAddCmd adds or updates a printer.
type PrinterAddCmd struct {
	Name    string  `arg:"" help:"Printer name (letters, digits, - _)."`
	Host    string  `required:"" help:"IP address or hostname."`
	Serial  string  `required:"" help:"Serial number (printer Settings > Device, or bambu printer discover)."`
	Model   string  `default:"X1C" help:"Model: ${models}."`
	Nozzle  float64 `default:"0.4" help:"Installed nozzle diameter (mm)."`
	Plate   string  `default:"textured_plate" enum:"textured_plate,cool_plate,eng_plate,hot_plate,supertack_plate" help:"Installed build plate."`
	Default bool    `help:"Make this the default printer."`
}

// Run executes the command.
func (c *PrinterAddCmd) Run(g *Globals) error {
	cfg, err := g.ConfigFile()
	if err != nil {
		return err
	}
	m, ok := printer.LookupModel(c.Model)
	if !ok {
		return errfmt.New(errfmt.ExitUsage, "unknown model %q", c.Model).WithHint("one of: %s", strings.Join(printer.Aliases(), ", "))
	}
	p := &config.Printer{Name: c.Name, Host: c.Host, Serial: strings.ToUpper(c.Serial), Model: m.Alias, Nozzle: c.Nozzle, Plate: c.Plate}
	if err := p.Validate(); err != nil {
		return err
	}
	_, existed := cfg.Printers[c.Name]
	cfg.Printers[c.Name] = p
	if c.Default || len(cfg.Printers) == 1 {
		cfg.DefaultPrinter = c.Name
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	action := "added"
	if existed {
		action = "updated"
	}
	_, src, codeErr := auth.Resolve(g.Keyring, p.Serial)
	return g.Out.Print(output.View{
		Data: map[string]any{"action": action, "printer": p, "default": cfg.DefaultPrinter == c.Name, "config": cfg.Path(), "access_code_source": src},
		Human: func(h *output.Human) {
			h.Line("%s printer %s (%s, %s, %s mm, %s) in %s", action, h.Bold(p.Name), p.Host, m.PrinterModel, p.NozzleString(), p.Plate, cfg.Path())
			if codeErr != nil {
				h.Line("%s", h.Dim("next: bambu auth set "+p.Name+"   (access code from the printer: Settings > LAN Only)"))
			}
		},
		Plain: [][]string{{"action", "name"}, {action, p.Name}},
		Quiet: p.Name,
	})
}

// PrinterListCmd lists printers.
type PrinterListCmd struct{}

// Run executes the command. Exit 3 when none are configured.
func (c *PrinterListCmd) Run(g *Globals) error {
	cfg, err := g.ConfigFile()
	if err != nil {
		return err
	}
	type row struct {
		*config.Printer
		Default bool   `json:"default"`
		Code    string `json:"access_code_source"`
	}
	rows := []row{}
	plain := [][]string{{"name", "host", "serial", "model", "nozzle", "plate", "default", "access_code_source"}}
	for _, n := range cfg.Names() {
		p := cfg.Printers[n]
		_, src, err := auth.Resolve(g.Keyring, p.Serial)
		if err != nil {
			src = "none"
		}
		r := row{Printer: p, Default: cfg.DefaultPrinter == n, Code: src}
		rows = append(rows, r)
		plain = append(plain, []string{n, p.Host, p.Serial, p.Model, p.NozzleString(), p.Plate, strconv.FormatBool(r.Default), src})
	}
	if err := g.Out.Print(output.View{
		Data: map[string]any{"printers": rows, "config": cfg.Path()},
		Human: func(h *output.Human) {
			if len(rows) == 0 {
				h.Line("no printers configured (%s)", cfg.Path())
				h.Line("%s", h.Dim("add one: bambu printer add <name> --host <ip> --serial <serial> --model X1C"))
				return
			}
			t := [][]string{{"", "NAME", "HOST", "MODEL", "NOZZLE", "PLATE", "ACCESS CODE"}}
			for _, r := range rows {
				mark := ""
				if r.Default {
					mark = "*"
				}
				t = append(t, []string{mark, r.Name, r.Host, r.Model, r.NozzleString(), r.Plate, r.Code})
			}
			h.Table(t)
		},
		Plain: plain,
		Quiet: strings.Join(cfg.Names(), "\n"),
	}); err != nil {
		return err
	}
	if len(rows) == 0 {
		return errfmt.Exit(errfmt.ExitEmpty)
	}
	return nil
}

// PrinterRemoveCmd removes a printer.
type PrinterRemoveCmd struct {
	Name    string `arg:"" help:"Printer name."`
	Confirm bool   `help:"Required."`
}

// Run executes the command.
func (c *PrinterRemoveCmd) Run(g *Globals) error {
	cfg, err := g.ConfigFile()
	if err != nil {
		return err
	}
	if _, ok := cfg.Printers[c.Name]; !ok {
		return errfmt.New(errfmt.ExitNotFound, "printer %q not configured", c.Name)
	}
	if !c.Confirm {
		return errfmt.New(errfmt.ExitGate, "printer remove needs --confirm")
	}
	delete(cfg.Printers, c.Name)
	if cfg.DefaultPrinter == c.Name {
		cfg.DefaultPrinter = ""
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	return g.Out.Print(output.View{
		Data: map[string]any{"removed": c.Name},
		Human: func(h *output.Human) {
			h.Line("removed %s (the access code stays in the keychain: bambu auth remove)", c.Name)
		},
		Plain: [][]string{{"removed"}, {c.Name}},
		Quiet: c.Name,
	})
}

// PrinterDefaultCmd sets the default printer.
type PrinterDefaultCmd struct {
	Name string `arg:"" help:"Printer name."`
}

// Run executes the command.
func (c *PrinterDefaultCmd) Run(g *Globals) error {
	cfg, err := g.ConfigFile()
	if err != nil {
		return err
	}
	if _, err := cfg.Select(c.Name); err != nil {
		return err
	}
	cfg.DefaultPrinter = c.Name
	if err := cfg.Save(); err != nil {
		return err
	}
	return g.Out.Print(output.View{
		Data:  map[string]any{"default_printer": c.Name},
		Human: func(h *output.Human) { h.Line("default printer: %s", c.Name) },
		Plain: [][]string{{"default_printer"}, {c.Name}},
		Quiet: c.Name,
	})
}

// PrinterDiscoverCmd listens for SSDP announcements.
type PrinterDiscoverCmd struct {
	Timeout time.Duration `default:"10s" help:"How long to listen (printers announce every few seconds)."`
}

// Run executes the command. Exit 3 when nothing was found.
func (c *PrinterDiscoverCmd) Run(g *Globals) error {
	g.Out.Hint("listening on UDP %d for %s…", discover.Port, c.Timeout)
	ctx, cancel := context.WithTimeout(g.Ctx, c.Timeout)
	defer cancel()
	devs, err := discover.Listen(ctx, discover.Port)
	if err != nil {
		return err
	}
	type found struct {
		discover.Device
		Model string `json:"model"`
	}
	out := []found{}
	plain := [][]string{{"serial", "host", "model", "name", "connect"}}
	for _, d := range devs {
		m, _ := printer.LookupModel(d.ModelID)
		out = append(out, found{d, m.Alias})
		plain = append(plain, []string{d.Serial, d.Host, m.Alias, d.Name, d.Connect})
	}
	if err := g.Out.Print(output.View{
		Data: map[string]any{"printers": out},
		Human: func(h *output.Human) {
			if len(out) == 0 {
				h.Line("no printers heard in %s (discovery only works on the same network segment/VLAN)", c.Timeout)
				return
			}
			for _, f := range out {
				h.Line("%s  %s  %s %s  %s", f.Serial, f.Host, f.Model, h.Dim(f.Name), h.Dim(f.Connect))
				h.Line("  %s", h.Dim(fmt.Sprintf("bambu printer add <name> --host %s --serial %s --model %s", f.Host, f.Serial, orDefault(f.Model, "X1C"))))
			}
		},
		Plain: plain,
		Quiet: strings.TrimSpace(func() string {
			var b strings.Builder
			for _, f := range out {
				b.WriteString(f.Serial + "\n")
			}
			return b.String()
		}()),
	}); err != nil {
		return err
	}
	if len(out) == 0 {
		return errfmt.Exit(errfmt.ExitEmpty)
	}
	return nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
