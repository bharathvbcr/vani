//go:build !darwin && !linux

package main

import "io/fs"

// statChange has no change time to give here, so no stamp is trusted and
// nothing is reused on its strength.
func statChange(fs.FileInfo) (ctime int64, dev, ino uint64, ok bool) { return 0, 0, 0, false }
