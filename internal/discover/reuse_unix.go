//go:build !windows

package discover

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func reuse(_, _ string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1) //nolint:gosec // fd fits in int
		if serr == nil {
			serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1) //nolint:gosec // fd fits in int
		}
	})
	if err != nil {
		return err //nolint:wrapcheck // surfaced by Listen
	}
	return serr //nolint:wrapcheck // surfaced by Listen
}
