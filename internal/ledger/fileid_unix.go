//go:build unix

package ledger

import (
	"os"
	"syscall"
)

// FileID identifies a file by device and inode, so a reader can tell the
// events file it folded from one renamed over it.
type FileID struct{ Dev, Ino uint64 }

func fileIDOf(fi os.FileInfo) FileID {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return FileID{}
	}
	return FileID{Dev: uint64(st.Dev), Ino: uint64(st.Ino)}
}
