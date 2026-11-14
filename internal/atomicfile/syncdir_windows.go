//go:build windows

package atomicfile

// syncDirReal is a no-op on Windows: the read-only handle os.Open gives a
// directory cannot be flushed (FlushFileBuffers needs write access), and NTFS
// journals the rename's metadata itself.
func syncDirReal(string) error { return nil }
