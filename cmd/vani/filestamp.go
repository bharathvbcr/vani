package main

import (
	"errors"
	"io/fs"
	"os"
)

// fileStamp is what stat says of a file, enough to tell that it changed:
// written in place (size, modification time, and the change time, which a
// write sets and a caller cannot set back), replaced by a rename (device and
// inode), deleted (missing), or its mode changed (change time). Equal stamps
// taken before two reads mean the second read would find what the first
// found, so a caller stats before it reads and reuses the read while the
// stamp is unchanged.
type fileStamp struct {
	missing      bool
	dev, ino     uint64
	size         int64
	mode         fs.FileMode
	mtime, ctime int64
}

// stampOf is path's stamp. ok is false when the stamp cannot tell a change:
// stat failed other than for a missing file, or the platform's stat has no
// change time and inode (statChange). A caller then reads again and keeps
// nothing.
func stampOf(path string) (st fileStamp, ok bool) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileStamp{missing: true}, true
	}
	if err != nil {
		return fileStamp{}, false
	}
	ctime, dev, ino, ok := statChange(fi)
	if !ok {
		return fileStamp{}, false
	}
	return fileStamp{dev: dev, ino: ino, size: fi.Size(), mode: fi.Mode(), mtime: fi.ModTime().UnixNano(), ctime: ctime}, true
}
