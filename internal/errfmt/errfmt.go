// Package errfmt defines bambu's exit codes and typed errors that carry a recovery hint.
package errfmt

import (
	"errors"
	"fmt"
)

// Exit codes. 0-10 follow the voska CLI convention; 11+ are bambu-specific.
const (
	ExitOK          = 0
	ExitError       = 1
	ExitUsage       = 2
	ExitEmpty       = 3
	ExitAuth        = 4
	ExitNotFound    = 5
	ExitForbidden   = 6
	ExitRetryable   = 8
	ExitGate        = 9
	ExitConfig      = 10
	ExitSliceFailed = 11
	ExitPrintFailed = 12
	ExitPrintPaused = 13
	ExitTimeout     = 14
)

// Error is an error with an exit code, an optional recovery hint and optional structured data.
type Error struct {
	Code    int            `json:"code"`
	Name    string         `json:"name"`
	Message string         `json:"message"`
	Hint    string         `json:"hint,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
	// Silent errors only set the exit code: the command already printed its result (e.g. preflight FAIL).
	Silent bool `json:"-"`
	cause  error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return e.Message + ": " + e.cause.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.cause }

// WithHint returns e with a recovery hint.
func (e *Error) WithHint(format string, args ...any) *Error {
	e.Hint = fmt.Sprintf(format, args...)
	return e
}

// WithData attaches structured data that is included in --json error output.
func (e *Error) WithData(k string, v any) *Error {
	if e.Data == nil {
		e.Data = map[string]any{}
	}
	e.Data[k] = v
	return e
}

// New creates an Error with the given code.
func New(code int, format string, args ...any) *Error {
	return &Error{Code: code, Name: Name(code), Message: fmt.Sprintf(format, args...)}
}

// Wrap creates an Error that wraps cause.
func Wrap(code int, cause error, format string, args ...any) *Error {
	return &Error{Code: code, Name: Name(code), Message: fmt.Sprintf(format, args...), cause: cause}
}

// Exit returns a silent error that only carries an exit code.
func Exit(code int) *Error {
	return &Error{Code: code, Name: Name(code), Message: Name(code), Silent: true}
}

// As extracts an *Error from err, or wraps it as a generic error.
func As(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		if e.Message == "" {
			e.Message = e.Error()
		}
		return e
	}
	return &Error{Code: ExitError, Name: Name(ExitError), Message: err.Error()}
}

// Entry describes one exit code.
type Entry struct {
	Code        int    `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

var table = []Entry{
	{ExitOK, "success", "Operation completed successfully"},
	{ExitError, "error", "Unexpected error"},
	{ExitUsage, "usage", "Invalid usage, arguments or setting keys"},
	{ExitEmpty, "empty", "Nothing found (not an error for agents)"},
	{ExitAuth, "auth_required", "No access code, or the printer refused it"},
	{ExitNotFound, "not_found", "File, printer, preset or recipe not found"},
	{ExitForbidden, "forbidden", "Printer rejected the command (Developer Mode off)"},
	{ExitRetryable, "retryable", "Printer unreachable or timed out; safe to retry"},
	{ExitGate, "gate_failed", "Safety gate failed: preflight FAIL, missing --confirm, printer busy"},
	{ExitConfig, "config_error", "Configuration error or slicer not found"},
	{ExitSliceFailed, "slice_failed", "Bambu Studio failed to slice"},
	{ExitPrintFailed, "print_failed", "Monitored print ended FAILED"},
	{ExitPrintPaused, "print_paused", "Monitored print is PAUSED and needs a human"},
	{ExitTimeout, "timeout", "Monitor timeout reached while the job is still active"},
}

// Table returns all exit codes.
func Table() []Entry { return append([]Entry(nil), table...) }

// Name returns the symbolic name of an exit code.
func Name(code int) string {
	for _, e := range table {
		if e.Code == code {
			return e.Name
		}
	}
	return "error"
}
