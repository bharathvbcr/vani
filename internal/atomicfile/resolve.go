package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNotRegular means the leaf a program-named write would replace is a
// directory, FIFO, socket or device. Replacing it would silently destroy
// something that is not a data file (or block forever opening it), so the write
// is refused.
var ErrNotRegular = errors.New("atomicfile: not a regular file")

// ResolveLeaf returns the path a write to a program-named file must target:
// the directory part is resolved (Resolve), so a state dir symlinked to iCloud
// or an external volume keeps working, but the leaf is NOT followed. If the
// leaf is a symlink the write replaces the link itself with a regular file, so
// a planted link can never carry a write out of the state directory, whether
// it points at an existing file or dangles. A leaf that exists and is neither
// a regular file nor a symlink fails with ErrNotRegular.
//
// The result is stable: ResolveLeaf of a result is the result. Callers that
// lock and then write resolve once and hand this exact path to both.
func ResolveLeaf(path string) (string, error) {
	dir, err := Resolve(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	leaf := filepath.Join(dir, filepath.Base(path))
	st, err := os.Lstat(leaf)
	switch {
	case err == nil:
		if m := st.Mode(); !m.IsRegular() && m&os.ModeSymlink == 0 {
			return "", fmt.Errorf("%s (%s): %w", leaf, m.Type(), ErrNotRegular)
		}
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("resolve %s: %w", leaf, err)
	}
	return leaf, nil
}

// maxLinkHops bounds how many symlinks Resolve follows while walking a path
// whose final component or ancestors dangle, so a loop is an error, not a hang.
const maxLinkHops = 16

// Resolve returns path with every symlink followed, the leaf included. Only a
// path the operator chose (the config file) may be written this way; files
// whose name the program picks use ResolveLeaf, which leaves the leaf alone.
// Directory components are resolved by both.
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
