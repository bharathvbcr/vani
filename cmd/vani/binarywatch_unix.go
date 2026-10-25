//go:build !windows

package main

import (
	"io/fs"
	"syscall"
)

// devIno is the device and inode of fi, or zeros when the platform's stat does
// not carry them (the size and mtime still tell files apart).
func devIno(fi fs.FileInfo) (dev, ino uint64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), uint64(st.Ino) //nolint:unconvert // Dev is int32 on darwin, uint64 on linux
	}
	return 0, 0
}
