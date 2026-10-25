//go:build windows

package main

import "io/fs"

// devIno has no equivalent in Windows' stat; size and mtime tell files apart.
func devIno(fs.FileInfo) (dev, ino uint64) { return 0, 0 }
