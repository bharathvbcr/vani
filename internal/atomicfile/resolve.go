package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// maxLinkHops bounds how many symlinks Resolve follows while walking a path
// whose final component or ancestors dangle, so a loop is an error, not a hang.
const maxLinkHops = 16

// Resolve returns the path a write to path must target: path with every
// symlink followed. It is the one place Vani decides what a symlinked path
// means, so Write, OpenAppend, SyncNew and every lock path agree on it (a state
// dir moved to iCloud or an external volume and linked back is the realistic
// case).
//
// A path that does not exist yet resolves through its parent directory, so the
// path a later write creates is the one every caller computes. A dangling
// symlink resolves to the path it points at, relative targets taken from the
// link's directory, so a write creates the target and keeps the link. A loop,
// or more than maxLinkHops dangling links in a chain, is an error. A path that
// is already resolved comes back unchanged.
func Resolve(path string) (string, error) {
	return resolve(path, 0)
}

func resolve(path string, hops int) (string, error) {
	for {
		r, err := filepath.EvalSymlinks(path)
		if err == nil {
			return r, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("resolve %s: %w", path, err)
		}
		st, lerr := os.Lstat(path)
		if lerr == nil && st.Mode()&os.ModeSymlink != 0 {
			// Dangling: continue at what the link points to.
			if hops++; hops > maxLinkHops {
				return "", fmt.Errorf("resolve %s: more than %d symbolic links", path, maxLinkHops)
			}
			target, rerr := os.Readlink(path)
			if rerr != nil {
				return "", fmt.Errorf("resolve %s: %w", path, rerr)
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(path), target)
			}
			path = target
			continue
		}
		if lerr != nil && !errors.Is(lerr, os.ErrNotExist) {
			return "", fmt.Errorf("resolve %s: %w", path, lerr)
		}
		// Missing: resolve the parent (which may itself be missing or dangle).
		dir := filepath.Dir(path)
		if dir == path {
			return "", fmt.Errorf("resolve %s: %w", path, err)
		}
		rdir, derr := resolve(dir, hops)
		if derr != nil {
			return "", derr
		}
		return filepath.Join(rdir, filepath.Base(path)), nil
	}
}
