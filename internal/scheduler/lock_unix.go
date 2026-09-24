//go:build !windows

package scheduler

import (
	"golang.org/x/sys/unix"
	"os"
)

func tryScheduleLock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
func releaseScheduleLock(f *os.File)   { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
