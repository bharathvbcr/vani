//go:build !unix

package ledger

import "os"

// FileID identifies a file by device and inode where the platform exposes
// them. Here it does not: every FileID is zero, and a replaced events file is
// caught by its size and its last line instead.
type FileID struct{ Dev, Ino uint64 }

func fileIDOf(os.FileInfo) FileID { return FileID{} }
