package resumestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/filelock"
)

// Exists reports whether key's job folder is there, whatever it holds: a folder
// with only an attempts log counts, since Rekey (and a Save) would meet it.
func (s *Store) Exists(key string) (bool, error) {
	if err := CheckKey(key); err != nil {
		return false, err
	}
	_, err := os.Lstat(s.dir(key))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	}
	return false, fmt.Errorf("resumestore: %w", err)
}

// Rekey moves oldKey's job to newKey, so a posting that was re-keyed keeps its
// resume versions, current and used pointers and attempts log. The folder is
// named by the key's hash, so the move is a rename of the folder, then the key
// file and each version record are rewritten to name newKey.
//
// What a reader sees at each step is never a quiet wrong answer. The rename is
// the only atomic step and comes first, so a crash after it leaves the folder
// under newKey's hash with files that still name oldKey: openJob(newKey) says
// ErrDamaged ("names a different posting"), which is what the check is for, and
// oldKey is ErrNotFound. After the key file, a crash between version records
// leaves List(newKey) returning the rewritten versions beside an ErrDamaged for
// the ones not yet rewritten. Rekey run again with the same arguments finishes
// the move: it recognises the half-moved folder because a folder under newKey's
// hash whose key file or records name oldKey can only be this move.
//
// It refuses to merge: when newKey already has a job folder it returns
// ErrExists and changes nothing (also when the folder under newKey's hash names
// some third key). It returns ErrNotFound when there is nothing to move (no
// folder under either key) and nil when the job is already at newKey. A job
// whose key file or records are not what the store wrote is ErrDamaged and is
// not moved.
//
// The store lock is held through the rename, so no Save can create newKey's
// folder between the check and the move, and oldKey's job lock is taken first,
// so a Save in flight finishes before the folder moves. Version records keep
// their modification times: pruning orders jobs by them, and a re-key must not
// make a job look newly saved.
//
// Not supported on Windows, which cannot rename a folder holding its open lock
// file (internal/rekey, the caller, refuses there too).
func (s *Store) Rekey(ctx context.Context, oldKey, newKey string) error {
	if err := CheckKey(oldKey); err != nil {
		return err
	}
	if err := CheckKey(newKey); err != nil {
		return err
	}
	if oldKey == newKey {
		return fmt.Errorf("%w: the old and new keys are the same", ErrInvalid)
	}
	if _, err := os.Lstat(s.root); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	storeLock, err := filelock.Acquire(ctx, filepath.Join(s.root, lockFile))
	if err != nil {
		return busy(err)
	}
	storeHeld := true
	defer func() {
		if storeHeld {
			storeLock.Release()
		}
	}()

	oldDir, newDir := s.dir(oldKey), s.dir(newKey)
	oldThere, err := folderState(oldDir)
	if err != nil {
		return err
	}
	newThere, err := folderState(newDir)
	if err != nil {
		return err
	}
	var dir string // the job folder, under newKey's hash, once it is there
	switch {
	case !oldThere && !newThere:
		return ErrNotFound
	case oldThere && newThere:
		return fmt.Errorf("%w: %s already has a resume folder", ErrExists, jobFolder(newKey)[:12])
	case oldThere:
		jl, err := filelock.Acquire(ctx, filepath.Join(oldDir, lockFile))
		if err != nil {
			return busy(err)
		}
		defer func() { _ = jl.Release() }()
		_, l, err := s.openJob(oldKey)
		if err != nil {
			return err
		}
		// Every record is checked before anything moves, so a refusal changes
		// nothing.
		if len(l.jsons) > 2*MaxVersions {
			return fmt.Errorf("%w: %d versions, over %d", ErrDamaged, len(l.jsons), 2*MaxVersions)
		}
		for _, n := range l.jsons {
			if _, _, err := loadRecord(versionFile(oldDir, n, "json"), n, oldKey, newKey); err != nil {
				return err
			}
		}
		if err := os.Rename(oldDir, newDir); err != nil {
			return fmt.Errorf("resumestore: rekey: %w", err)
		}
		dir = newDir
		storeLock.Release()
		storeHeld = false
		if err := afterStage("rename"); err != nil {
			return err
		}
	default: // only newKey's folder: done, or a move that stopped part way
		got, err := readRegular(filepath.Join(newDir, keyFile), MaxKeyBytes)
		switch {
		case errors.Is(err, os.ErrNotExist):
			return fmt.Errorf("%w: %s has no key file", ErrNotFound, jobFolder(newKey)[:12])
		case err != nil:
			return fmt.Errorf("%w: key file: %w", ErrDamaged, err)
		case string(got) != oldKey && string(got) != newKey:
			return fmt.Errorf("%w: %s belongs to another posting", ErrExists, jobFolder(newKey)[:12])
		}
		jl, err := filelock.Acquire(ctx, filepath.Join(newDir, lockFile))
		if err != nil {
			return busy(err)
		}
		defer func() { _ = jl.Release() }()
		dir = newDir
		storeLock.Release()
		storeHeld = false
	}
	return s.rewriteKeys(dir, oldKey, newKey)
}

// folderState says whether dir is there; something that is not a folder is
// damage.
func folderState(dir string) (bool, error) {
	st, err := os.Lstat(dir)
	switch {
	case err == nil && !st.IsDir():
		return false, fmt.Errorf("%w: %s is not a folder", ErrDamaged, filepath.Base(dir)[:12])
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	}
	return false, fmt.Errorf("resumestore: %w", err)
}

// rewriteKeys makes the key file and every version record of the folder at dir
// name newKey. A file that already does is left alone; one that names neither
// key is damage. The caller holds the job lock.
func (s *Store) rewriteKeys(dir, oldKey, newKey string) error {
	got, err := readRegular(filepath.Join(dir, keyFile), MaxKeyBytes)
	switch {
	case err != nil:
		return fmt.Errorf("%w: key file: %w", ErrDamaged, err)
	case string(got) == oldKey:
		if err := atomicfile.Write(filepath.Join(dir, keyFile), []byte(newKey)); err != nil {
			return fmt.Errorf("resumestore: %w", err)
		}
	case string(got) != newKey:
		return fmt.Errorf("%w: key file names another posting", ErrDamaged)
	}
	if err := afterStage("key"); err != nil {
		return err
	}
	l, err := scan(dir, s.now())
	if err != nil {
		return fmt.Errorf("resumestore: %w", err)
	}
	if len(l.jsons) > 2*MaxVersions {
		return fmt.Errorf("%w: %d versions, over %d", ErrDamaged, len(l.jsons), 2*MaxVersions)
	}
	for _, n := range l.jsons {
		path := versionFile(dir, n, "json")
		body, at, err := loadRecord(path, n, oldKey, newKey)
		if err == nil && body != nil {
			err = writeRecord(path, body, at)
		}
		if err != nil {
			return err
		}
		if err := afterStage("v" + strconv.Itoa(n)); err != nil {
			return err
		}
	}
	return nil
}

// loadRecord reads one vN.json for a rekey. It returns the bytes the record
// takes under newKey and its modification time, or nil bytes when the record
// already names newKey. A record that names neither key, or is not what the
// store wrote, is damage.
func loadRecord(path string, n int, oldKey, newKey string) ([]byte, time.Time, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("v%d: %w", n, damaged(err))
	}
	b, err := readRegular(path, MaxMetaBytes)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("v%d: %w", n, damaged(err))
	}
	var probe struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return nil, time.Time{}, fmt.Errorf("%w: v%d does not read: %v", ErrDamaged, n, err)
	}
	if probe.Schema != Schema {
		return nil, time.Time{}, fmt.Errorf("%w: v%d has schema %d, this reads %d", ErrSchema, n, probe.Schema, Schema)
	}
	var m Meta
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil || dec.More() {
		return nil, time.Time{}, fmt.Errorf("%w: v%d does not read", ErrDamaged, n)
	}
	switch m.Key {
	case newKey:
		return nil, st.ModTime(), nil
	case oldKey:
	default:
		return nil, time.Time{}, fmt.Errorf("%w: v%d names a different posting", ErrDamaged, n)
	}
	m.Key = newKey
	body, err := json.Marshal(m)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("resumestore: marshal: %w", err)
	}
	if len(body) > MaxMetaBytes {
		return nil, time.Time{}, fmt.Errorf("%w: v%d would be %d bytes under the new key, over %d", ErrInvalid, n, len(body), MaxMetaBytes)
	}
	return body, st.ModTime(), nil
}

// writeRecord replaces a record atomically and gives it back its modification
// time.
func writeRecord(path string, body []byte, at time.Time) error {
	if err := atomicfile.Write(path, body); err != nil {
		return fmt.Errorf("resumestore: %w", err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		return fmt.Errorf("resumestore: %w", err)
	}
	return nil
}
