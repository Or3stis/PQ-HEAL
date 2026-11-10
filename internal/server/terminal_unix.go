//go:build darwin || linux

package server

import (
	"golang.org/x/sys/unix"
	"os"
)

func terminalSize(file *os.File) (int, int) {
	size, err := unix.IoctlGetWinsize(int(file.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0
	}
	return int(size.Col), int(size.Row)
}

// IsTerminal reports whether file supports an interactive terminal dashboard.
func IsTerminal(file *os.File) bool {
	_, err := unix.IoctlGetTermios(int(file.Fd()), terminalGetAttr)
	return err == nil && os.Getenv("TERM") != "dumb"
}
