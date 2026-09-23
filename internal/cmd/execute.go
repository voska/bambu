package cmd

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/alecthomas/kong"

	"github.com/voska/bambu/internal/errfmt"
	"github.com/voska/bambu/internal/printer"
)

// Execute parses args, runs the command and returns the process exit code.
// setup, if non-nil, can replace dependencies (keyring, dialer, output streams) before the command runs.
func Execute(args []string, build BuildInfo, setup func(*Globals)) int {
	var cli CLI
	exited, exitCode := false, 0
	parser, err := kong.New(&cli,
		kong.Name("bambu"),
		kong.Description("Slice, check, send and monitor Bambu Lab prints over LAN — for humans and AI agents.\n\n"+
			"Printers must be in LAN mode with Developer Mode enabled. Data goes to stdout (--json for agents); hints go to stderr."),
		kong.UsageOnError(),
		kong.Vars{"models": strings.Join(printer.Aliases(), ", ")},
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
		kong.Exit(func(code int) { exited, exitCode = true, code }), // --help: kong prints usage, we return

	)
	if err != nil {
		panic(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	g := NewGlobals(ctx, &cli, build)
	if setup != nil {
		setup(g)
	}
	kctx, err := parser.Parse(args)
	if exited {
		return exitCode
	}
	if envTrue("BAMBU_JSON") {
		cli.JSON = true
	}
	if err != nil {
		return report(g, errfmt.Wrap(errfmt.ExitUsage, err, "invalid usage").WithHint("see: bambu --help"), cli.JSON || containsJSON(args))
	}
	// Re-derive output settings from the parsed flags (setup may have replaced the streams).
	stdout, stderr := g.Out.Stdout, g.Out.Stderr
	g.Out = newOut(&cli)
	g.Out.Stdout, g.Out.Stderr = stdout, stderr
	kctx.Bind(g, parser)
	if err := kctx.Run(g); err != nil {
		return report(g, errfmt.As(err), cli.JSON)
	}
	return 0
}

func envTrue(k string) bool {
	switch strings.ToLower(os.Getenv(k)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// containsJSON detects --json when parsing failed before flags were bound.
func containsJSON(args []string) bool {
	for _, a := range args {
		if a == "--json" || a == "-j" || a == "--machine" {
			return true
		}
	}
	return false
}

func report(g *Globals, e *errfmt.Error, jsonMode bool) int {
	if e.Silent {
		return e.Code
	}
	full := e.Error()
	if jsonMode {
		enc := json.NewEncoder(g.Out.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		out := *e
		out.Message = full
		_ = enc.Encode(map[string]any{"error": out})
	}
	g.Out.Error(full, e.Hint)
	return e.Code
}
