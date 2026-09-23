//go:build windows

package discover

import "syscall"

func reuse(_, _ string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	})
	if err != nil {
		return err //nolint:wrapcheck // surfaced by Listen
	}
	return serr //nolint:wrapcheck // surfaced by Listen
}
