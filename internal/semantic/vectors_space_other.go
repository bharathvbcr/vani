//go:build !(darwin || linux || freebsd)

package semantic

import "errors"

// freeBytes cannot check here; the caller records that and does not treat it as
// enough space.
func freeBytes(string) (uint64, error) {
	return 0, errors.New("free disk space cannot be checked on this platform")
}
