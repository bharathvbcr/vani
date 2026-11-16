package main

// Safe persistence for every file rkb rewrites.
//
// writeAtomic: bytes go to a sibling temp file, are fsynced, then replace the
// target with one rename. A crash, a full disk or a killed shell never leaves a
// half-written bank.json or Claims.verify.json behind, and a concurrent reader
// sees either the old file or the new one.
//
// Shared state (bank.json, Claims.verify.json) is also guarded against other
// writers:
//   - lockFile: an advisory lock on "<file>.lock" (flock where the platform has
//     it) serialises rkb processes. The lock file is never deleted, because
//     some mounts (the Claude desktop folder bridge) do not allow deletes.
//   - compare-and-swap: rkb remembers the sha256 of the bytes it read and,
//     just before the rename, checks the file on disk still has them. An edit
//     by any other tool between read and write (an editor, a script, another
//     agent that does not take the lock) is detected and the write refused
//     with errConflict instead of silently overwriting that edit.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var errConflict = errors.New("file changed on disk since rkb read it")

// sha256Bytes is the content hash used for compare-and-swap ("" for absent).
func sha256Bytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// guard remembers what a file held when rkb read it.
type guard struct {
	path   string
	sha    string // sha256 of the bytes read; "" when the file did not exist
	exists bool
}

// readGuarded reads p and remembers its hash. A missing file is not an error
// (exists=false); any other read error is.
func readGuarded(p string) ([]byte, guard, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, guard{path: p}, nil
	}
	if err != nil {
		return nil, guard{path: p}, err
	}
	return b, guard{path: p, sha: sha256Bytes(b), exists: true}, nil
}

// unchanged reports whether the file still holds what was read.
func (g guard) unchanged() (bool, string) {
	b, err := os.ReadFile(g.path)
	if errors.Is(err, os.ErrNotExist) {
		return !g.exists, ""
	}
	if err != nil {
		return false, ""
	}
	cur := sha256Bytes(b)
	return g.exists && cur == g.sha, cur
}

// commit writes b over the guarded file only if nobody changed it since it was
// read, then re-reads it to prove the bytes landed.
func (g guard) commit(b []byte) error {
	if ok, _ := g.unchanged(); !ok {
		return fmt.Errorf("%s: %w", g.path, errConflict)
	}
	if err := writeAtomic(g.path, b); err != nil {
		return err
	}
	got, err := os.ReadFile(g.path)
	if err != nil {
		return fmt.Errorf("%s: re-read after write: %v", g.path, err)
	}
	if !bytes.Equal(got, b) {
		return fmt.Errorf("%s: %w (overwritten during the write)", g.path, errConflict)
	}
	return nil
}

// writeAtomic replaces p with b via temp file + fsync + rename.
func writeAtomic(p string, b []byte) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %v", dir, err)
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(p); err == nil {
		mode = st.Mode().Perm()
	}
	tmp := filepath.Join(dir, "."+filepath.Base(p)+".rkb-tmp-"+strconv.Itoa(os.Getpid())+"-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		// Some mounts refuse new dotfiles or O_EXCL; fall back to a direct write
		// rather than failing the command (still never truncates on error paths
		// before the bytes are ready).
		return os.WriteFile(p, b, mode)
	}
	// On each failure below the write error is what is reported; closing and
	// removing the temp file are best-effort cleanup (p itself was never touched).
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %v", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("fsync %s: %v", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close %s: %v", tmp, err)
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %v", tmp, p, err)
	}
	if d, err := os.Open(dir); err == nil { // persist the rename itself; the file is already in place
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// encodeJSON is the one JSON style rkb writes: two-space indent, trailing
// newline, and & < > kept literal (bullet text is prose, not HTML).
func encodeJSON(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// withLock runs fn holding the advisory lock for p. Waits up to timeout for
// another rkb process to finish.
func withLock(p string, timeout time.Duration, fn func() error) error {
	release, err := lockFile(p+".lock", timeout)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// guardedUpdate is the read-modify-write loop for shared files: lock, read,
// apply, compare-and-swap. If something outside rkb changed the file between
// the read and the write, it reloads and re-applies (apply must be pure with
// respect to the bytes it is given), up to attempts times.
func guardedUpdate(p string, attempts int, apply func(old []byte, exists bool) ([]byte, error)) error {
	return withLock(p, 2*time.Minute, func() error {
		var last error
		for i := 0; i < attempts; i++ {
			old, g, err := readGuarded(p)
			if err != nil {
				return err
			}
			nb, err := apply(old, g.exists)
			if err != nil {
				// A file another program is in the middle of writing in place can
				// read as truncated JSON. If it changes within a moment, that was
				// a write in progress: read it again instead of failing.
				time.Sleep(time.Duration(100*(i+1)) * time.Millisecond)
				if same, _ := g.unchanged(); !same && i+1 < attempts {
					last = err
					continue
				}
				return err
			}
			if nb == nil { // nothing to write
				return nil
			}
			err = g.commit(nb)
			if err == nil {
				return nil
			}
			if !errors.Is(err, errConflict) {
				return err
			}
			last = err
			time.Sleep(time.Duration(50*(i+1)) * time.Millisecond)
		}
		return fmt.Errorf("%v; gave up after %d attempts — another process keeps writing it", last, attempts)
	})
}

// ---------------------------------------------------------------- duplicate keys

// dupKeys lists object keys that appear twice in the same JSON object. Go's
// and Python's decoders silently keep the last one, which would make one of
// two bullets with the same id vanish.
func dupKeys(b []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	type frame struct {
		obj    bool
		keys   map[string]bool
		path   string
		expKey bool
		index  int
	}
	var stack []*frame
	var dups []string
	cur := func() *frame {
		if len(stack) == 0 {
			return nil
		}
		return stack[len(stack)-1]
	}
	var lastKey string
	childPath := func() string {
		f := cur()
		if f == nil {
			return "$"
		}
		if f.obj {
			return f.path + "." + lastKey
		}
		return f.path + "[" + strconv.Itoa(f.index) + "]"
	}
	valueDone := func() {
		if f := cur(); f != nil {
			if f.obj {
				f.expKey = true
			} else {
				f.index++
			}
		}
	}
	tops := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return dups, err
		}
		if len(stack) == 0 {
			if tops++; tops > 1 {
				return dups, fmt.Errorf("more than one JSON value in the file")
			}
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				p := childPath()
				stack = append(stack, &frame{obj: t == '{', keys: map[string]bool{}, path: p, expKey: true})
			case '}', ']':
				stack = stack[:len(stack)-1]
				valueDone()
			}
		case string:
			f := cur()
			if f != nil && f.obj && f.expKey {
				if f.keys[t] {
					dups = append(dups, f.path+"."+t)
				}
				f.keys[t] = true
				f.expKey = false
				lastKey = t
				continue
			}
			valueDone()
		default:
			valueDone()
		}
	}
	if len(stack) > 0 {
		return dups, fmt.Errorf("unexpected end of JSON (file truncated?)")
	}
	if tops == 0 {
		return dups, fmt.Errorf("empty file")
	}
	sort.Strings(dups)
	return dups, nil
}

// ---------------------------------------------------------------- dropped fields

// droppedFields compares the key sets of every object in old and new (paths
// matched by object keys, arrays element by element) and lists fields present in old but
// missing in new. rkb's structs only know the fields they model; this stops a
// rewrite from silently deleting a field someone added by hand.
func droppedFields(oldB, newB []byte) []string {
	var o, n interface{}
	if json.Unmarshal(oldB, &o) != nil || json.Unmarshal(newB, &n) != nil {
		return nil
	}
	var out []string
	var walk func(path string, a, b interface{})
	walk = func(path string, a, b interface{}) {
		if aa, ok := a.([]interface{}); ok {
			// arrays: element-wise (append-only lists keep their prefix; a
			// filtered list compares like-shaped objects, so no false alarm
			// unless a field really disappears)
			if ba, ok := b.([]interface{}); ok {
				for i := 0; i < len(aa) && i < len(ba); i++ {
					walk(path+"["+strconv.Itoa(i)+"]", aa[i], ba[i])
				}
			}
			return
		}
		am, ok1 := a.(map[string]interface{})
		bm, ok2 := b.(map[string]interface{})
		if !ok1 || !ok2 {
			return
		}
		for k, av := range am {
			bv, ok := bm[k]
			if !ok {
				if isEmptyJSON(av) { // omitempty legitimately drops empty values
					continue
				}
				out = append(out, path+"."+k)
				continue
			}
			walk(path+"."+k, av, bv)
		}
	}
	walk("$", o, n)
	sort.Strings(out)
	return out
}

func isEmptyJSON(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return !t
	case float64:
		return t == 0
	case []interface{}:
		return len(t) == 0
	case map[string]interface{}:
		return len(t) == 0
	}
	return false
}

// humanList joins at most n items for an error message.
func humanList(xs []string, n int) string {
	if len(xs) <= n {
		return strings.Join(xs, ", ")
	}
	return strings.Join(xs[:n], ", ") + fmt.Sprintf(" (+%d more)", len(xs)-n)
}
