//go:build !windows

package atomicfile

import "os"

// SyncDir fsyncs dir, making a rename, link or create inside it durable: a
// file's own fsync does not cover the directory entry that names it.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	serr := d.Sync()
	cerr := d.Close()
	if serr != nil {
		return serr
	}
	return cerr
}
