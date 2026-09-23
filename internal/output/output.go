// Package output renders command results as human text, JSON, TSV or a single quiet value.
// Data goes to stdout; hints, progress and warnings go to stderr.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/muesli/termenv"
	"golang.org/x/term"
)

// Mode selects how results are rendered.
type Mode int

// Output modes.
const (
	ModeHuman Mode = iota
	ModeJSON
	ModePlain
)

// Out renders results according to the selected mode.
type Out struct {
	Mode   Mode
	Quiet  bool
	Stdout io.Writer
	Stderr io.Writer
	errOut *termenv.Output
	outOut *termenv.Output
}

// New creates an Out. Colors are disabled for JSON/plain, when noColor is set, when NO_COLOR is set,
// and when the stream is not a terminal.
func New(jsonMode, plain, quiet, noColor bool) *Out {
	o := &Out{Mode: ModeHuman, Quiet: quiet, Stdout: os.Stdout, Stderr: os.Stderr}
	switch {
	case jsonMode:
		o.Mode = ModeJSON
	case plain:
		o.Mode = ModePlain
	}
	colors := !noColor && os.Getenv("NO_COLOR") == "" && o.Mode == ModeHuman
	o.outOut = termenv.NewOutput(os.Stdout, profileOpt(colors && isTTY(os.Stdout)))
	o.errOut = termenv.NewOutput(os.Stderr, profileOpt(colors && isTTY(os.Stderr)))
	return o
}

func profileOpt(colors bool) termenv.OutputOption {
	if colors {
		return termenv.WithProfile(termenv.ANSI256)
	}
	return termenv.WithProfile(termenv.Ascii)
}

func isTTY(f *os.File) bool { return term.IsTerminal(int(f.Fd())) } //nolint:gosec // fd fits in int

// View is one command result in every representation.
type View struct {
	Data  any            // JSON payload (--json)
	Human func(p *Human) // human rendering (default)
	Plain [][]string     // TSV rows, first row is the header (--plain)
	Quiet string         // primary value (--quiet)
}

// Print renders v.
func (o *Out) Print(v View) error {
	switch {
	case o.Quiet && o.Mode != ModeJSON:
		if v.Quiet != "" {
			_, err := fmt.Fprintln(o.Stdout, v.Quiet)
			return err //nolint:wrapcheck // stdout write
		}
		return nil
	case o.Mode == ModeJSON:
		return o.JSON(v.Data)
	case o.Mode == ModePlain:
		for _, row := range v.Plain {
			if _, err := fmt.Fprintln(o.Stdout, strings.Join(row, "\t")); err != nil {
				return err //nolint:wrapcheck // stdout write
			}
		}
		return nil
	default:
		if v.Human == nil {
			return o.JSON(v.Data)
		}
		h := &Human{w: o.Stdout, t: o.outOut}
		v.Human(h)
		return nil
	}
}

// JSON writes data as indented JSON to stdout.
func (o *Out) JSON(data any) error {
	enc := json.NewEncoder(o.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(data) //nolint:wrapcheck // stdout write
}

// Event writes one streaming event: an NDJSON line in JSON mode, otherwise the human line.
func (o *Out) Event(data any, human string) error {
	if o.Mode == ModeJSON {
		enc := json.NewEncoder(o.Stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(data) //nolint:wrapcheck // stdout write
	}
	_, err := fmt.Fprintln(o.Stdout, human)
	return err //nolint:wrapcheck // stdout write
}

// Hint prints a dim line to stderr (suppressed by --quiet).
func (o *Out) Hint(format string, args ...any) {
	if o.Quiet {
		return
	}
	fmt.Fprintln(o.Stderr, o.errOut.String(fmt.Sprintf(format, args...)).Faint())
}

// Warn prints a warning to stderr.
func (o *Out) Warn(format string, args ...any) {
	fmt.Fprintln(o.Stderr, o.errOut.String("warning: "+fmt.Sprintf(format, args...)).Foreground(o.errOut.Color("3")))
}

// Error prints an error (and optional hint) to stderr.
func (o *Out) Error(msg, hint string) {
	fmt.Fprintln(o.Stderr, o.errOut.String("error: "+msg).Foreground(o.errOut.Color("1")))
	if hint != "" {
		fmt.Fprintln(o.Stderr, o.errOut.String("hint: "+hint).Faint())
	}
}

// Human is a small helper for colored human output.
type Human struct {
	w io.Writer
	t *termenv.Output
}

// Line prints a formatted line.
func (h *Human) Line(format string, args ...any) { fmt.Fprintf(h.w, format+"\n", args...) }

// Bold returns s in bold.
func (h *Human) Bold(s string) string { return h.t.String(s).Bold().String() }

// Dim returns s faint.
func (h *Human) Dim(s string) string { return h.t.String(s).Faint().String() }

// Good returns s in green.
func (h *Human) Good(s string) string { return h.t.String(s).Foreground(h.t.Color("2")).String() }

// Bad returns s in red.
func (h *Human) Bad(s string) string { return h.t.String(s).Foreground(h.t.Color("1")).String() }

// Warn returns s in yellow.
func (h *Human) Warn(s string) string { return h.t.String(s).Foreground(h.t.Color("3")).String() }

// Status colors a PASS/WARN/FAIL-like word.
func (h *Human) Status(s string) string {
	switch s {
	case "PASS", "OK", "RUNNING", "FINISH", "true":
		return h.Good(s)
	case "WARN", "PAUSE", "PREPARE":
		return h.Warn(s)
	case "FAIL", "FAILED", "false":
		return h.Bad(s)
	}
	return s
}

// Table prints aligned columns (first row is the header).
func (h *Human) Table(rows [][]string) {
	if len(rows) == 0 {
		return
	}
	widths := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			if i < len(widths) && len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}
	for n, r := range rows {
		parts := make([]string, len(r))
		for i, c := range r {
			pad := c + strings.Repeat(" ", widths[i]-len(c))
			if n == 0 {
				pad = h.Bold(pad)
			}
			parts[i] = pad
		}
		fmt.Fprintln(h.w, strings.TrimRight(strings.Join(parts, "  "), " "))
	}
}
