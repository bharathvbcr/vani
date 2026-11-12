//go:build darwin

package main

import (
	"io/fs"
	"syscall"
)

// statChange is fi's change time in nanoseconds, device and inode.
func statChange(fi fs.FileInfo) (ctime int64, dev, ino uint64, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0, false
	}
	return st.Ctimespec.Nano(), uint64(st.Dev), st.Ino, true //nolint:unconvert // Dev is int32 on darwin
}
