//go:build !windows

package desktop

import (
	"golang.org/x/sys/unix"
	"os"
)

func tryDesktopLock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
func releaseDesktopLock(f *os.File)   { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
