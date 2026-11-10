//go:build !darwin && !linux

package server

import "os"

func terminalSize(*os.File) (int, int) { return 0, 0 }
func IsTerminal(*os.File) bool         { return false }
