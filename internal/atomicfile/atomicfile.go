// Package atomicfile replaces files so that a reader, or a crash, sees the old
// contents or the new ones and never part of either. It is the one owner of
// Vani's write-temp-then-rename step; the state, config, index and cache
// writers all go through it.
package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ErrLinkedLeaf means the leaf an append would open is a symbolic link. Whole
// file rewrites (Write) replace such a link, since they supply the full content;
// an append cannot, so it refuses rather than discard the history behind it.
var ErrLinkedLeaf = errors.New("atomicfile: leaf is a symbolic link")

// syncDir is SyncDir; a variable only so tests can see that Write reached it.
var syncDir = SyncDir

// Write replaces path with data, mode 0600, creating the parent directory
// (0700) if it is missing. The temp file is fsynced before the rename and the
// directory after it, so once Write returns nil the new contents survive a
// crash. An error after the rename says the file was replaced but its
// durability is unknown; writing the same data again is safe.
//
// path is first resolved with ResolveLeaf: the directory part follows symlinks
// (a state dir linked to iCloud keeps working), the leaf does not. A symlink at
// the leaf is replaced by the regular file, never written through, so a planted
// link cannot move the write outside the directory. A directory, FIFO or device
// at the leaf is refused with ErrNotRegular. The operator-chosen config path,
// which must be written through its link, resolves once with Resolve and passes
// that result here.
func Write(path string, data []byte) (err error) {
	name := filepath.Base(path)
	path, err = ResolveLeaf(path)
	if err != nil {
		return fmt.Errorf("atomic write %s: %w", name, err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("atomic write %s: create directory: %w", name, err)
	}
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("atomic write %s: create temp file: %w", name, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("atomic write %s: chmod temp file: %w", name, err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("atomic write %s: write temp file: %w", name, err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("atomic write %s: sync temp file: %w", name, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("atomic write %s: close temp file: %w", name, err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("atomic write %s: replace file: %w", name, err)
	}
	if serr := syncDir(dir); serr != nil {
		return fmt.Errorf("atomic write %s: replaced, but syncing the directory failed: %w", name, serr)
	}
	return nil
}

// OpenAppend opens path read-write, mode 0600, creating it when absent. created
// reports that this call made the file. Creation goes through an O_EXCL probe
// first, so of any number of racing openers exactly one is told it created the
// file and the rest open the file that won; the creator owes a SyncDir of the
// parent (see SyncNew), because a file's own fsync does not make its directory
// entry durable. path is resolved with ResolveLeaf: directories follow links,
// the leaf does not. A symlink at the leaf (live or dangling) is refused with
// ErrLinkedLeaf and nothing is created or removed, because replacing it would
// silently start an append-only log empty; a leaf that is not a regular file is
// refused with ErrNotRegular. extra is OR-ed into the flags
// (os.O_APPEND for an append-only log).
func OpenAppend(path string, extra int) (f *os.File, created bool, err error) {
	path, err = ResolveLeaf(path)
	if err != nil {
		return nil, false, err
	}
	flags := os.O_RDWR | extra
	for range 4 {
		f, err = os.OpenFile(path, flags|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if st, serr := f.Stat(); serr == nil {
				newFiles.Store(path, st)
			}
			return f, true, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, false, err
		}
		st, lerr := os.Lstat(path)
		if lerr != nil {
			if errors.Is(lerr, os.ErrNotExist) {
				continue // removed since the probe: probe again
			}
			return nil, false, lerr
		}
		if st.Mode()&os.ModeSymlink != 0 {
			// Nothing is created or removed: replacing the link would start an
			// append-only history empty.
			return nil, false, fmt.Errorf("%s is a symbolic link; Vani will not append through it - replace it with the file itself or link its directory instead: %w", path, ErrLinkedLeaf)
		}
		if !st.Mode().IsRegular() {
			return nil, false, fmt.Errorf("%s (%s): %w", path, st.Mode().Type(), ErrNotRegular)
		}
		f, err = os.OpenFile(path, flags, 0o600)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, false, err
		}
		// The open follows links, so prove it opened the leaf itself: a link
		// planted between the Lstat and the open names a different file.
		fst, ferr := f.Stat()
		lst, lerr := os.Lstat(path)
		if ferr == nil && lerr == nil && os.SameFile(fst, lst) {
			return f, false, nil
		}
		_ = f.Close()
	}
	return nil, false, fmt.Errorf("open %s: the file kept changing underneath the open", filepath.Base(path))
}

// newFiles remembers, per path, the file OpenAppend just created, so SyncNew can
// tell whether the path still names that very file. SyncNew consumes the entry.
var newFiles sync.Map // path -> os.FileInfo

// SyncNew makes the directory entry of a file OpenAppend just created durable.
// If that fails the new file is removed again, so the next open sees it as new
// and syncs again rather than trusting an entry that may not survive a crash;
// the caller closes f. The removal is conservative: the file is deleted only if
// the path still refers to the file OpenAppend created (os.SameFile) and it is
// still empty. If another opener has appended to it, or the path was replaced,
// the file is left alone and the sync error is returned. That narrows the race
// but does not close it (check and remove are two steps), so callers must still
// hold their lock across OpenAppend..SyncNew. syncFn is the caller's seam
// (SyncDir in production).
func SyncNew(path string, syncFn func(dir string) error) error {
	name := filepath.Base(path)
	path, err := ResolveLeaf(path)
	if err != nil {
		return fmt.Errorf("syncing the directory of new %s: %w", name, err)
	}
	want, _ := newFiles.LoadAndDelete(path)
	err = syncFn(filepath.Dir(path))
	if err == nil {
		return nil
	}
	err = fmt.Errorf("syncing the directory of new %s: %w", filepath.Base(path), err)
	st, serr := os.Lstat(path)
	if serr != nil || !st.Mode().IsRegular() || st.Size() != 0 {
		return err
	}
	if orig, ok := want.(os.FileInfo); ok && !os.SameFile(orig, st) {
		return err
	}
	_ = os.Remove(path)
	return err
}

// TornTail reports whether f is non-empty and its last byte is not a newline:
// a crash mid-append left a partial row, and a row appended now would be glued
// onto it and lost with it. The caller writes a newline first. An error means
// the check could not run; it is never reported as "not torn".
func TornTail(f *os.File) (bool, error) {
	st, err := f.Stat()
	if err != nil {
		return false, fmt.Errorf("torn-tail check: %w", err)
	}
	if st.Size() == 0 {
		return false, nil
	}
	var last [1]byte
	if _, err := f.ReadAt(last[:], st.Size()-1); err != nil {
		return false, fmt.Errorf("torn-tail check: %w", err)
	}
	return last[0] != '\n', nil
}
