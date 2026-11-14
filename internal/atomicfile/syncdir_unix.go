//go:build !windows

package atomicfile

import "os"

// syncDirReal is SyncDir without the test hook.
func syncDirReal(dir string) error {
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
