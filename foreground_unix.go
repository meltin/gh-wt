//go:build unix

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// foreground reports whether gh wt owns the terminal on stdout. A background
// job that changes the terminal's settings is stopped by the kernel (SIGTTOU).
func foreground() bool {
	pgrp, err := unix.IoctlGetInt(int(os.Stdout.Fd()), unix.TIOCGPGRP)
	return err == nil && pgrp == unix.Getpgrp()
}
