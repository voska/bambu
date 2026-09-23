// Package cmd wires bambu's commands (kong structs with Run(*Globals) methods).
package cmd

import (
	"context"
	"os"
	"path/filepath"

	"github.com/voska/bambu/internal/auth"
	"github.com/voska/bambu/internal/config"
	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/output"
	"github.com/voska/bambu/internal/printer"
	"github.com/voska/bambu/internal/slicer"
)

// CLI is the root command.
type CLI struct {
	JSON    bool   `short:"j" name:"json" help:"JSON output on stdout (NDJSON for streams). Also: BAMBU_JSON=1." aliases:"machine"`
	Plain   bool   `short:"p" help:"Tab-separated output, no color." aliases:"tsv"`
	Quiet   bool   `short:"q" help:"Print only the primary value (state, path, PASS/FAIL)."`
	NoColor bool   `name:"no-color" help:"Disable colors (also: NO_COLOR env)."`
	NoInput bool   `name:"no-input" help:"Never prompt; fail instead."`
	Printer string `short:"P" env:"BAMBU_PRINTER" help:"Printer name from the config (default: default_printer, or the only one)."`
	Config  string `env:"BAMBU_CONFIG" help:"Config file (default ~/.config/bambu/config.toml)." type:"path"`

	Status    StatusCmd    `cmd:"" help:"Printer status (read-only). --watch streams changes."`
	Slice     SliceCmd     `cmd:"" help:"Slice a model headlessly with Bambu Studio and a recipe."`
	Preflight PreflightCmd `cmd:"" help:"Run the safety gates for a sliced file (read-only)."`
	Print     PrintCmd     `cmd:"" help:"Send and control print jobs."`
	Monitor   MonitorCmd   `cmd:"" help:"Follow the current job until it ends; exit code reflects the outcome."`
	Camera    CameraCmd    `cmd:"" help:"Printer camera."`
	Printers  PrinterCmd   `cmd:"" name:"printer" help:"Manage configured printers."`
	Auth      AuthCmd      `cmd:"" help:"Access codes (OS keychain)."`
	Recipe    RecipeCmd    `cmd:"" help:"Slicing recipes."`
	Slicer    SlicerCmd    `cmd:"" help:"Bambu Studio discovery."`
	Schema    SchemaCmd    `cmd:"" help:"Dump the command tree as JSON for agents."`
	ExitCodes ExitCodesCmd `cmd:"" name:"exit-codes" help:"Print the exit code table."`
	Version   VersionCmd   `cmd:"" help:"Print version information."`
}

// BuildInfo is set from main via ldflags.
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Date    string `json:"date,omitempty"`
}

// Globals is shared state passed to every command.
type Globals struct {
	Ctx     context.Context
	Out     *output.Out
	CLI     *CLI
	Build   BuildInfo
	Keyring auth.Keyring
	// Dial connects to a printer; swappable in tests.
	Dial func(ctx context.Context, host, serial, code string) (printer.Conn, error)
	// FTPSPort is the printer's implicit-FTPS port (990; swappable in tests).
	FTPSPort int

	cfg *config.Config
}

// NewGlobals builds Globals from parsed flags.
func NewGlobals(ctx context.Context, cli *CLI, build BuildInfo) *Globals {
	return &Globals{
		Ctx: ctx, CLI: cli, Build: build,
		Out:      output.New(cli.JSON, cli.Plain, cli.Quiet, cli.NoColor),
		Keyring:  auth.System{},
		Dial:     printer.Dial,
		FTPSPort: 990,
	}
}

func newOut(cli *CLI) *output.Out { return output.New(cli.JSON, cli.Plain, cli.Quiet, cli.NoColor) }

// ConfigFile loads the config once.
func (g *Globals) ConfigFile() (*config.Config, error) {
	if g.cfg == nil {
		c, err := config.Load(g.CLI.Config)
		if err != nil {
			return nil, err
		}
		g.cfg = c
	}
	return g.cfg, nil
}

// Target returns the selected printer.
func (g *Globals) Target() (*config.Printer, error) {
	c, err := g.ConfigFile()
	if err != nil {
		return nil, err
	}
	return c.Select(g.CLI.Printer)
}

// Code resolves the access code for p.
func (g *Globals) Code(p *config.Printer) (string, string, error) {
	return auth.Resolve(g.Keyring, p.Serial)
}

// Connect opens an MQTT session to p.
func (g *Globals) Connect(p *config.Printer) (printer.Conn, error) {
	code, _, err := g.Code(p)
	if err != nil {
		return nil, err
	}
	return g.Dial(g.Ctx, p.Host, p.Serial, code)
}

// Studio discovers Bambu Studio and loads its profile index.
func (g *Globals) Studio() (*slicer.Studio, *slicer.Index, error) {
	c, err := g.ConfigFile()
	if err != nil {
		return nil, nil, err
	}
	st, err := slicer.Discover(c.Slicer.Path, c.Slicer.Resources)
	if err != nil {
		return nil, nil, err
	}
	ix, err := slicer.LoadIndex(st.Resources)
	if err != nil {
		return nil, nil, err
	}
	return st, ix, nil
}

// RecipesDir returns the user recipes directory.
func (g *Globals) RecipesDir() string {
	if c, err := g.ConfigFile(); err == nil && c.RecipesDir != "" {
		return expand(c.RecipesDir)
	}
	return filepath.Join(config.Dir(), "recipes")
}

func expand(p string) string {
	if len(p) > 1 && p[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

// model looks up the configured model of p.
func model(p *config.Printer) (printer.Model, error) {
	m, ok := printer.LookupModel(p.Model)
	if !ok {
		return m, errfmt.New(errfmt.ExitConfig, "printer %s has unknown model %q", p.Name, p.Model).
			WithHint("set --model to one of %v: bambu printer add %s --model X1C …", printer.Aliases(), p.Name)
	}
	return m, nil
}
