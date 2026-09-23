package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/voska/bambu/internal/auth"
	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/output"
	"github.com/voska/bambu/internal/printer"
)

// AuthCmd groups access-code commands.
type AuthCmd struct {
	Set    AuthSetCmd    `cmd:"" help:"Store a printer's LAN access code in the OS keychain (read from stdin)."`
	Status AuthStatusCmd `cmd:"" help:"Show where each printer's access code comes from; --check connects (read-only)."`
	Remove AuthRemoveCmd `cmd:"" help:"Delete a printer's access code from the keychain (requires --confirm)."`
}

// AuthSetCmd stores an access code.
type AuthSetCmd struct {
	Name    string `arg:"" help:"Printer name."`
	NoCheck bool   `name:"no-check" help:"Don't verify the code against the printer."`
}

// Run executes the command.
func (c *AuthSetCmd) Run(g *Globals) error {
	cfg, err := g.ConfigFile()
	if err != nil {
		return err
	}
	p, err := cfg.Select(c.Name)
	if err != nil {
		return err
	}
	var code string
	if term.IsTerminal(int(os.Stdin.Fd())) { //nolint:gosec // fd fits in int
		if g.CLI.NoInput {
			return errfmt.New(errfmt.ExitUsage, "no access code on stdin and --no-input set").WithHint("echo CODE | bambu auth set %s", p.Name)
		}
		fmt.Fprintf(os.Stderr, "LAN access code for %s (printer: Settings > LAN Only): ", p.Name)
		b, err := term.ReadPassword(int(os.Stdin.Fd())) //nolint:gosec // fd fits in int
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return errfmt.Wrap(errfmt.ExitUsage, err, "read access code")
		}
		code = string(b)
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return errfmt.New(errfmt.ExitUsage, "no access code on stdin").WithHint("echo CODE | bambu auth set %s", p.Name)
		}
		code = line
	}
	code = strings.TrimSpace(code)
	if err := auth.Store(g.Keyring, p.Serial, code); err != nil {
		return err
	}
	res := map[string]any{"printer": p.Name, "stored": true, "keychain_service": auth.Service, "keychain_account": p.Serial}
	if !c.NoCheck {
		conn, err := g.Dial(g.Ctx, p.Host, p.Serial, code)
		if err != nil {
			return errfmt.As(err).WithData("stored", true)
		}
		raw, err := conn.Pushall(g.Ctx)
		conn.Close()
		if err != nil {
			return err
		}
		s := printer.Summarize(raw)
		res["verified"] = true
		res["dev_mode"] = s.DevMode
		res["state"] = s.State
	}
	return g.Out.Print(output.View{
		Data: res,
		Human: func(h *output.Human) {
			h.Line("stored access code for %s in the OS keychain (service %s, account %s)", p.Name, auth.Service, p.Serial)
			if res["verified"] == true {
				h.Line("%s printer accepted it (state %v, dev_mode %s)", h.Good("verified:"), res["state"], ptrBool(res["dev_mode"].(*bool)))
			}
		},
		Plain: [][]string{{"printer", "stored"}, {p.Name, "true"}},
		Quiet: "ok",
	})
}

// AuthStatusCmd reports access-code sources.
type AuthStatusCmd struct {
	Check bool `help:"Connect to each printer (read-only) to verify the code."`
}

// Run executes the command.
func (c *AuthStatusCmd) Run(g *Globals) error {
	cfg, err := g.ConfigFile()
	if err != nil {
		return err
	}
	names := cfg.Names()
	if g.CLI.Printer != "" {
		names = []string{g.CLI.Printer}
	}
	type entry struct {
		Printer string `json:"printer"`
		Source  string `json:"source"`
		Checked bool   `json:"checked"`
		OK      *bool  `json:"ok,omitempty"`
		DevMode *bool  `json:"dev_mode,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	var out []entry
	plain := [][]string{{"printer", "source", "ok", "dev_mode"}}
	allOK := true
	for _, n := range names {
		p, err := cfg.Select(n)
		if err != nil {
			return err
		}
		e := entry{Printer: n, Source: "none"}
		code, src, rerr := auth.Resolve(g.Keyring, p.Serial)
		if rerr == nil {
			e.Source = src
		}
		if c.Check {
			e.Checked = true
			ok := false
			if rerr != nil {
				e.Error = errfmt.As(rerr).Message
			} else if conn, err := g.Dial(g.Ctx, p.Host, p.Serial, code); err != nil {
				e.Error = errfmt.As(err).Error()
			} else {
				raw, err := conn.Pushall(g.Ctx)
				conn.Close()
				if err != nil {
					e.Error = errfmt.As(err).Error()
				} else {
					ok = true
					e.DevMode = printer.Summarize(raw).DevMode
				}
			}
			e.OK = &ok
			allOK = allOK && ok
		}
		out = append(out, e)
		okS := ""
		if e.OK != nil {
			okS = fmt.Sprint(*e.OK)
		}
		plain = append(plain, []string{n, e.Source, okS, ptrBool(e.DevMode)})
	}
	if err := g.Out.Print(output.View{
		Data: map[string]any{"printers": out, "keychain_service": auth.Service},
		Human: func(h *output.Human) {
			for _, e := range out {
				line := fmt.Sprintf("%-12s code from %s", e.Printer, e.Source)
				if e.Source == auth.SourceBambuStudio {
					line += h.Dim(" (fallback; run `bambu auth set` to store it in the keychain)")
				}
				if e.Checked {
					if *e.OK {
						line += "  " + h.Good("ok") + " dev_mode=" + ptrBool(e.DevMode)
					} else {
						line += "  " + h.Bad("failed: "+e.Error)
					}
				}
				h.Line("%s", line)
			}
		},
		Plain: plain,
	}); err != nil {
		return err
	}
	if !allOK {
		return errfmt.Exit(errfmt.ExitAuth)
	}
	return nil
}

// AuthRemoveCmd deletes an access code.
type AuthRemoveCmd struct {
	Name    string `arg:"" help:"Printer name."`
	Confirm bool   `help:"Required."`
}

// Run executes the command.
func (c *AuthRemoveCmd) Run(g *Globals) error {
	cfg, err := g.ConfigFile()
	if err != nil {
		return err
	}
	p, err := cfg.Select(c.Name)
	if err != nil {
		return err
	}
	if !c.Confirm {
		return errfmt.New(errfmt.ExitGate, "auth remove needs --confirm")
	}
	if err := auth.Remove(g.Keyring, p.Serial); err != nil {
		return err
	}
	return g.Out.Print(output.View{
		Data:  map[string]any{"printer": p.Name, "removed": true},
		Human: func(h *output.Human) { h.Line("removed the access code for %s from the keychain", p.Name) },
		Plain: [][]string{{"printer", "removed"}, {p.Name, "true"}},
		Quiet: "ok",
	})
}
