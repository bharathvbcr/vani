// Package resumestore is the one owner of a job's resume versions on disk:
// `<state>/resumes/<sha256(key) hex>/` holds
//
//	key             the posting key; checked on every open (a mismatch is damage)
//	vN.json         one version's Meta (the commit point of a Save)
//	vN.pdf          its PDF, optional; vN.json holds the PDF's sha256
//	current         the version the Studio shows, "N\n"
//	used            the version bound for applying, "N\n"
//	attempts.jsonl  builds that were paid for and saved nothing
//	.lock           the per-job lock
//
// The folder is named by the key's hash, so no key can name a path. Names that
// start with "." beside the job folders are not the store's: `.tmp` belongs to
// rkbresume's build folders and is never read, swept or counted here, and
// `.lock` is the store lock.
//
// A Save writes the PDF, then the JSON, then the pointers. Each file replaces
// atomically (internal/atomicfile: 0600 files, 0700 folders), so a crash leaves
// one of: nothing new (an orphan PDF, swept by the next Save), the version but
// no pointer move, or everything. Readers take no lock; they verify instead (a
// PDF is served only when its bytes match the hash in the JSON).
//
// Writers take a per-job lock for the few milliseconds the files take, through
// internal/filelock, so two Saves from any number of processes interleave
// safely. Taking it is guarded by a short store lock: a writer holds the store
// lock only while it makes sure the folder exists (pruning the oldest job
// folder when the store is full) and takes the job lock, so a prune never
// deletes a folder a writer is about to use.
package resumestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/filelock"
)

const (
	dirName  = "resumes"
	lockWait = 10 * time.Second

	keyFile      = "key"
	currentFile  = "current"
	usedFile     = "used"
	attemptsFile = "attempts.jsonl"
	lockFile     = ".lock"

	// maxAttemptsBytes bounds attempts.jsonl; maxAttemptLines bounds its lines.
	// The oldest go first, so the log is a recent window, never a failure.
	maxAttemptsBytes = 32 << 10
	maxAttemptLines  = 100
	// staleTmp is how old an atomicfile temp file in a job folder must be
	// before a Save sweeps it (a crash left it).
	staleTmp = time.Hour
)

var (
	fileRe   = regexp.MustCompile(`^v([1-9][0-9]{0,3})\.(json|pdf)$`)
	folderRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
	pointer  = regexp.MustCompile(`^([1-9][0-9]{0,3})\n?$`)
)

// afterStage is called after each durable step of a Save ("pdf", "json",
// "current"); a non-nil return aborts the Save there, as a crash would. Tests
// set it; nothing else does.
var afterStage = func(string) error { return nil }

// Store is the resume versions under one state directory. It holds no open
// files, so any number of Stores (in any number of processes) may share a
// directory.
type Store struct {
	root string
	// Now stamps Created; nil means time.Now.
	Now func() time.Time
}

// Dir is where the store lives for a state directory.
func Dir(stateDir string) string { return filepath.Join(stateDir, dirName) }

// Open returns the store for stateDir. It creates nothing: the first Save does.
func Open(stateDir string) (*Store, error) {
	if stateDir == "" {
		return nil, fmt.Errorf("%w: no state directory", ErrInvalid)
	}
	return &Store{root: Dir(stateDir)}, nil
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func jobFolder(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func (s *Store) dir(key string) string { return filepath.Join(s.root, jobFolder(key)) }

func versionFile(dir string, n int, ext string) string {
	return filepath.Join(dir, "v"+strconv.Itoa(n)+"."+ext)
}

// --- locking -----------------------------------------------------------

// lockJob takes the job's lock and returns the folder. create says a missing
// folder is made (pruning the least recently saved job when MaxJobs is
// reached); without it a missing folder is ErrNotFound and nothing is created.
func (s *Store) lockJob(ctx context.Context, key string, create bool) (dir string, release func(), err error) {
	if !create {
		// Nothing is made on a read-side call: a store never written is not found.
		if _, err := os.Lstat(s.root); errors.Is(err, os.ErrNotExist) {
			return "", nil, ErrNotFound
		}
	}
	ctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	storeLock, err := filelock.Acquire(ctx, filepath.Join(s.root, lockFile))
	if err != nil {
		return "", nil, busy(err)
	}
	defer storeLock.Release()
	dir = s.dir(key)
	st, serr := os.Lstat(dir)
	switch {
	case serr == nil && !st.IsDir():
		return "", nil, fmt.Errorf("%w: %s is not a folder", ErrDamaged, jobFolder(key)[:12])
	case errors.Is(serr, os.ErrNotExist):
		if !create {
			return "", nil, ErrNotFound
		}
		if err := s.makeRoom(); err != nil {
			return "", nil, err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", nil, fmt.Errorf("resumestore: create folder: %w", err)
		}
	case serr != nil:
		return "", nil, fmt.Errorf("resumestore: %w", serr)
	}
	jl, err := filelock.Acquire(ctx, filepath.Join(dir, lockFile))
	if err != nil {
		return "", nil, busy(err)
	}
	return dir, func() { _ = jl.Release() }, nil
}

func busy(err error) error {
	if errors.Is(err, filelock.ErrWouldBlock) {
		return fmt.Errorf("%w: %w", ErrBusy, err)
	}
	return fmt.Errorf("resumestore: %w", err)
}

// --- folder bound ------------------------------------------------------

type folder struct {
	name  string
	saved time.Time
}

// jobFolders lists the job folders: real directories named by a 64-hex hash.
// Everything else under the root (`.tmp`, `.lock`, strays) is not the store's.
func (s *Store) jobFolders() ([]folder, error) {
	ents, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resumestore: %w", err)
	}
	var out []folder
	for _, e := range ents {
		if !folderRe.MatchString(e.Name()) || !e.IsDir() {
			continue
		}
		out = append(out, folder{name: e.Name()})
	}
	return out, nil
}

// lastSaved is the newest vN.json modification time in dir (the folder's own
// when it holds none), so reading or moving a pointer does not make a job look
// recently saved.
func lastSaved(dir string) time.Time {
	var newest time.Time
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			m := fileRe.FindStringSubmatch(e.Name())
			if m == nil || m[2] != "json" {
				continue
			}
			if info, err := e.Info(); err == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
	}
	if newest.IsZero() {
		if st, err := os.Stat(dir); err == nil {
			return st.ModTime()
		}
	}
	return newest
}

// makeRoom deletes least-recently-saved job folders that have no used version
// until there is room for one more, or returns ErrFull. A folder whose job
// lock is held is skipped. The caller holds the store lock.
func (s *Store) makeRoom() error {
	fs, err := s.jobFolders()
	if err != nil {
		return err
	}
	if len(fs) < MaxJobs {
		return nil
	}
	var cands []folder
	for _, f := range fs {
		dir := filepath.Join(s.root, f.name)
		if _, err := os.Lstat(filepath.Join(dir, usedFile)); err == nil {
			continue // a used version is what the owner applied with; never deleted
		}
		f.saved = lastSaved(dir)
		cands = append(cands, f)
	}
	sort.Slice(cands, func(i, j int) bool {
		if !cands[i].saved.Equal(cands[j].saved) {
			return cands[i].saved.Before(cands[j].saved)
		}
		return cands[i].name < cands[j].name
	})
	have := len(fs)
	for _, c := range cands {
		if have < MaxJobs {
			break
		}
		dir := filepath.Join(s.root, c.name)
		l, err := filelock.TryAcquire(filepath.Join(dir, lockFile))
		if err != nil {
			continue
		}
		rerr := os.RemoveAll(dir)
		_ = l.Release()
		if rerr != nil {
			return fmt.Errorf("resumestore: prune job folder: %w", rerr)
		}
		have--
	}
	if have >= MaxJobs {
		return fmt.Errorf("%w: %d jobs hold a used or busy resume and none can be removed", ErrFull, have)
	}
	return nil
}

// --- reading files -----------------------------------------------------

// readRegular reads a regular file of at most max bytes; a link, a folder or a
// longer file is damage.
func readRegular(path string, max int64) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrDamaged, filepath.Base(path))
	}
	if st.Size() > max {
		return nil, fmt.Errorf("%w: %s is %d bytes, over %d", ErrDamaged, filepath.Base(path), st.Size(), max)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%w: %s grew past %d bytes", ErrDamaged, filepath.Base(path), max)
	}
	return b, nil
}

type listing struct {
	jsons []int // versions with a vN.json, ascending
	pdfs  map[int]bool
	temps []string // stale atomicfile temp files
}

func scan(dir string, now time.Time) (listing, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return listing{}, err
	}
	l := listing{pdfs: map[int]bool{}}
	for _, e := range ents {
		name := e.Name()
		if m := fileRe.FindStringSubmatch(name); m != nil {
			n, _ := strconv.Atoi(m[1])
			if m[2] == "json" {
				l.jsons = append(l.jsons, n)
			} else {
				l.pdfs[n] = true
			}
			continue
		}
		if strings.HasPrefix(name, ".") && strings.Contains(name, ".tmp-") {
			if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > staleTmp {
				l.temps = append(l.temps, name)
			}
		}
	}
	slices.Sort(l.jsons)
	return l, nil
}

// openJob checks that dir holds the job for key: its key file reads and names
// key. A folder with no key file and no versions is an unfinished create and
// reads as not found; one with versions but no key file is damaged.
func (s *Store) openJob(key string) (dir string, l listing, err error) {
	dir = s.dir(key)
	l, err = scan(dir, s.now())
	if errors.Is(err, os.ErrNotExist) {
		return "", listing{}, ErrNotFound
	}
	if err != nil {
		if st, serr := os.Lstat(dir); serr == nil && !st.IsDir() {
			return "", listing{}, fmt.Errorf("%w: %s is not a folder", ErrDamaged, jobFolder(key)[:12])
		}
		return "", listing{}, fmt.Errorf("resumestore: %w", err)
	}
	got, err := readRegular(filepath.Join(dir, keyFile), MaxKeyBytes)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if len(l.jsons) == 0 {
			return "", listing{}, ErrNotFound
		}
		return "", listing{}, fmt.Errorf("%w: %s has versions but no key file", ErrDamaged, jobFolder(key)[:12])
	case err != nil:
		return "", listing{}, fmt.Errorf("%w: key file: %w", ErrDamaged, err)
	case string(got) != key:
		return "", listing{}, fmt.Errorf("%w: %s names a different posting", ErrDamaged, jobFolder(key)[:12])
	}
	return dir, l, nil
}

// readMeta reads and validates version n of the job in dir. A record that is
// not what this store wrote (other schema, other key or number, unknown or
// invalid fields, a PDF hash with no PDF) is an error; a PDF's bytes are not
// read here (Get does).
func readMeta(dir, key string, n int) (Meta, error) {
	b, err := readRegular(versionFile(dir, n, "json"), MaxMetaBytes)
	if errors.Is(err, os.ErrNotExist) {
		return Meta{}, fmt.Errorf("%w: v%d", ErrNotFound, n)
	}
	if err != nil {
		return Meta{}, fmt.Errorf("v%d: %w", n, damaged(err))
	}
	schema, err := probeSchema(b, n)
	if err != nil {
		return Meta{}, err
	}
	var m Meta
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Meta{}, fmt.Errorf("%w: v%d does not read: %v", ErrDamaged, n, err)
	}
	if dec.More() {
		return Meta{}, fmt.Errorf("%w: v%d holds more than one value", ErrDamaged, n)
	}
	switch {
	case m.Key != key:
		return Meta{}, fmt.Errorf("%w: v%d names a different posting", ErrDamaged, n)
	case m.Version != n:
		return Meta{}, fmt.Errorf("%w: v%d records version %d", ErrDamaged, n, m.Version)
	case m.Parent >= n:
		return Meta{}, fmt.Errorf("%w: v%d has parent %d", ErrDamaged, n, m.Parent)
	case schema < 2 && m.Instruction != "":
		return Meta{}, fmt.Errorf("%w: v%d is schema %d, which has no instruction", ErrDamaged, n, schema)
	case schema < 2 && (m.Scope != "" || len(m.Lock) > 0):
		return Meta{}, fmt.Errorf("%w: v%d is schema %d, which has no scope or lock", ErrDamaged, n, schema)
	}
	if err := checkMeta(m); err != nil {
		return Meta{}, fmt.Errorf("%w: v%d: %v", ErrDamaged, n, err)
	}
	if m.PDFSHA256 != "" {
		st, err := os.Lstat(versionFile(dir, n, "pdf"))
		if err != nil || !st.Mode().IsRegular() {
			return Meta{}, fmt.Errorf("%w: v%d has a PDF hash but no PDF", ErrDamaged, n)
		}
	}
	return m, nil
}

// probeSchema reads a record's schema number and refuses one outside
// MinSchema..Schema (ErrSchema, so a newer record is "unsupported", never
// damage). Every reader of a vN.json (readMeta, Rekey) goes through it.
func probeSchema(b []byte, n int) (int, error) {
	var probe struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return 0, fmt.Errorf("%w: v%d does not read: %v", ErrDamaged, n, err)
	}
	if probe.Schema < MinSchema || probe.Schema > Schema {
		return 0, fmt.Errorf("%w: v%d has schema %d, this reads %d to %d", ErrSchema, n, probe.Schema, MinSchema, Schema)
	}
	return probe.Schema, nil
}

func damaged(err error) error {
	if errors.Is(err, ErrDamaged) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrDamaged, err)
}

// readPointer reads current or used: 0 and nil when it is not set; a pointer
// that does not parse, or names a version that is not there, is damage.
func readPointer(dir, name string, l listing) (int, error) {
	b, err := readRegular(filepath.Join(dir, name), 16)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, damaged(err))
	}
	m := pointer.FindSubmatch(b)
	if m == nil {
		return 0, fmt.Errorf("%w: %s does not hold a version number", ErrDamaged, name)
	}
	n, _ := strconv.Atoi(string(m[1]))
	if _, ok := slices.BinarySearch(l.jsons, n); !ok {
		return 0, fmt.Errorf("%w: %s names v%d, which is not there", ErrDamaged, name, n)
	}
	return n, nil
}

func writePointer(dir, name string, n int) error {
	if err := atomicfile.Write(filepath.Join(dir, name), []byte(strconv.Itoa(n)+"\n")); err != nil {
		return fmt.Errorf("resumestore: %w", err)
	}
	return nil
}

// ensureKey writes the key file of a job that has none (a fresh folder); one
// that names another key, or is missing from a folder that holds versions, is
// damage and nothing is written. The caller holds the job lock.
func ensureKey(dir, key string, l listing) error {
	got, err := readRegular(filepath.Join(dir, keyFile), MaxKeyBytes)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if len(l.jsons) > 0 {
			return fmt.Errorf("%w: %s has versions but no key file", ErrDamaged, jobFolder(key)[:12])
		}
		if err := atomicfile.Write(filepath.Join(dir, keyFile), []byte(key)); err != nil {
			return fmt.Errorf("resumestore: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("key file: %w", damaged(err))
	case string(got) != key:
		return fmt.Errorf("%w: %s names a different posting", ErrDamaged, jobFolder(key)[:12])
	}
	return nil
}

// --- Save --------------------------------------------------------------

// Save stores m (and pdf, which may be nil) as the next version of key's resume
// and returns the record as written: Version is max+1 over the job's versions,
// Schema, Key and Created are set by the store, and PDFSHA256 is the hash of
// pdf ("" when pdf is nil). m.Created, m.Version, m.Schema, m.Key and
// m.PDFSHA256 as passed are ignored.
//
// current is true when the new version is now the job's current one: it moves
// only if m.Parent is the current version at the moment of the save, or the job
// has no current. Otherwise the version is saved and the pointer is left where
// it was (a build that began from an older current must not displace one made
// since), and current is false.
//
// Order: the PDF, then the JSON (the commit point), then the pointer. On error,
// Meta.Version != 0 says the JSON was committed before the failure; the error
// is then about the pointer. Anything else committed nothing. A job already at
// MaxVersions loses its oldest version that is neither current nor used first;
// a store at MaxJobs loses the least recently saved job that has no used
// version, or the Save fails with ErrFull.
//
// pdf is the owner's resume and holds their header: it is written to a 0600
// file and is never inlined into JSON or a log.
func (s *Store) Save(ctx context.Context, key string, m Meta, pdf []byte) (saved Meta, current bool, err error) {
	if err := CheckKey(key); err != nil {
		return Meta{}, false, err
	}
	if pdf != nil {
		if len(pdf) == 0 || len(pdf) > MaxPDFBytes {
			return Meta{}, false, fmt.Errorf("%w: the PDF is %d bytes (1..%d)", ErrInvalid, len(pdf), MaxPDFBytes)
		}
		if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
			return Meta{}, false, fmt.Errorf("%w: the PDF does not start with %%PDF-", ErrInvalid)
		}
		sum := sha256.Sum256(pdf)
		m.PDFSHA256 = hex.EncodeToString(sum[:])
	} else {
		m.PDFSHA256 = ""
	}
	m.Schema, m.Key = Schema, key
	// Validated before any lock or file: a bad record never touches the disk.
	if err := checkMeta(m); err != nil {
		return Meta{}, false, err
	}
	dir, release, err := s.lockJob(ctx, key, true)
	if err != nil {
		return Meta{}, false, err
	}
	defer release()
	l, err := scan(dir, s.now())
	if err != nil {
		return Meta{}, false, fmt.Errorf("resumestore: %w", err)
	}
	if err := ensureKey(dir, key, l); err != nil {
		return Meta{}, false, err
	}
	cur, err := readPointer(dir, currentFile, l)
	if err != nil {
		return Meta{}, false, err
	}
	used, err := readPointer(dir, usedFile, l)
	if err != nil {
		return Meta{}, false, err
	}
	sweep(dir, l)
	// Room first: if the sweep or the prune fails nothing has been committed.
	keep := l.jsons
	for len(keep) >= MaxVersions {
		i := slices.IndexFunc(keep, func(n int) bool { return n != cur && n != used })
		if i < 0 {
			return Meta{}, false, fmt.Errorf("%w: %d versions are all current or used", ErrFull, len(keep))
		}
		if err := removeVersion(dir, keep[i]); err != nil {
			return Meta{}, false, err
		}
		keep = slices.Delete(slices.Clone(keep), i, i+1)
	}
	n := 1
	if len(l.jsons) > 0 {
		n = l.jsons[len(l.jsons)-1] + 1
	}
	if n > MaxVersion {
		return Meta{}, false, fmt.Errorf("%w: version numbers are used up (v%d)", ErrFull, MaxVersion)
	}
	if m.Parent >= n {
		return Meta{}, false, fmt.Errorf("%w: parent v%d is not older than v%d", ErrInvalid, m.Parent, n)
	}
	m.Version, m.Created = n, s.now()
	body, err := json.Marshal(m)
	if err != nil {
		return Meta{}, false, fmt.Errorf("resumestore: marshal: %w", err)
	}
	if len(body) > MaxMetaBytes {
		return Meta{}, false, fmt.Errorf("%w: the record is %d bytes, over %d", ErrInvalid, len(body), MaxMetaBytes)
	}
	if pdf != nil {
		if err := atomicfile.Write(versionFile(dir, n, "pdf"), pdf); err != nil {
			return Meta{}, false, fmt.Errorf("resumestore: %w", err)
		}
		if err := afterStage("pdf"); err != nil {
			return Meta{}, false, err
		}
	}
	if err := atomicfile.Write(versionFile(dir, n, "json"), body); err != nil {
		return Meta{}, false, fmt.Errorf("resumestore: %w", err)
	}
	if err := afterStage("json"); err != nil {
		return m, false, err
	}
	if cur == 0 || m.Parent == cur {
		if err := writePointer(dir, currentFile, n); err != nil {
			return m, false, err
		}
		current = true
	}
	if err := afterStage("current"); err != nil {
		return m, current, err
	}
	return m, current, nil
}

// sweep removes what a crashed Save left: a vN.pdf with no vN.json, and stale
// atomicfile temp files. Failures are not errors: it only makes room, and the
// next Save tries again.
func sweep(dir string, l listing) {
	for n := range l.pdfs {
		if _, ok := slices.BinarySearch(l.jsons, n); !ok {
			_ = os.Remove(versionFile(dir, n, "pdf"))
		}
	}
	for _, t := range l.temps {
		_ = os.Remove(filepath.Join(dir, t))
	}
}

// removeVersion deletes a version: the JSON first (so the version stops
// existing at once), then the PDF.
func removeVersion(dir string, n int) error {
	if err := os.Remove(versionFile(dir, n, "json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("resumestore: prune v%d: %w", n, err)
	}
	if err := os.Remove(versionFile(dir, n, "pdf")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("resumestore: prune v%d pdf: %w", n, err)
	}
	return nil
}

// --- reading -----------------------------------------------------------

// List returns key's versions, oldest first. A version that does not read is
// left out and reported: the error then wraps ErrDamaged (or ErrSchema) and the
// slice holds the versions that did read, so a caller can show both and never
// takes the slice for the whole job. A job with no versions is ErrNotFound.
func (s *Store) List(key string) ([]Meta, error) {
	if err := CheckKey(key); err != nil {
		return nil, err
	}
	dir, l, err := s.openJob(key)
	if err != nil {
		return nil, err
	}
	if len(l.jsons) == 0 {
		return nil, ErrNotFound
	}
	if len(l.jsons) > 2*MaxVersions {
		return nil, fmt.Errorf("%w: %d versions, over %d", ErrDamaged, len(l.jsons), 2*MaxVersions)
	}
	var out []Meta
	var bad []error
	for _, n := range l.jsons {
		m, err := readMeta(dir, key, n)
		if err != nil {
			bad = append(bad, err)
			continue
		}
		out = append(out, m)
	}
	return out, errors.Join(bad...)
}

// Get returns version n and its PDF (nil when the version has none). A PDF
// whose bytes do not match the hash in the record is ErrDamaged and is never
// returned.
func (s *Store) Get(key string, n int) (Meta, []byte, error) {
	if err := CheckKey(key); err != nil {
		return Meta{}, nil, err
	}
	if err := checkVersion(n); err != nil {
		return Meta{}, nil, err
	}
	dir, _, err := s.openJob(key)
	if err != nil {
		return Meta{}, nil, err
	}
	m, err := readMeta(dir, key, n)
	if err != nil {
		return Meta{}, nil, err
	}
	if m.PDFSHA256 == "" {
		return m, nil, nil
	}
	pdf, err := readRegular(versionFile(dir, n, "pdf"), MaxPDFBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// A prune between the two reads removes the JSON first: say gone, not damaged.
			if _, jerr := os.Lstat(versionFile(dir, n, "json")); errors.Is(jerr, os.ErrNotExist) {
				return Meta{}, nil, fmt.Errorf("%w: v%d", ErrNotFound, n)
			}
		}
		return Meta{}, nil, fmt.Errorf("v%d pdf: %w", n, damaged(err))
	}
	sum := sha256.Sum256(pdf)
	if hex.EncodeToString(sum[:]) != m.PDFSHA256 {
		return Meta{}, nil, fmt.Errorf("%w: v%d's PDF does not match its hash", ErrDamaged, n)
	}
	return m, pdf, nil
}

// pointed reads the record a pointer names; ErrNotFound when it is not set.
func (s *Store) pointed(key, name string) (Meta, error) {
	if err := CheckKey(key); err != nil {
		return Meta{}, err
	}
	dir, l, err := s.openJob(key)
	if err != nil {
		return Meta{}, err
	}
	n, err := readPointer(dir, name, l)
	if err != nil {
		return Meta{}, err
	}
	if n == 0 {
		return Meta{}, ErrNotFound
	}
	return readMeta(dir, key, n)
}

// Current is the version the Studio shows; ErrNotFound when none is set.
func (s *Store) Current(key string) (Meta, error) { return s.pointed(key, currentFile) }

// Used is the version bound for applying; ErrNotFound when none is set.
func (s *Store) Used(key string) (Meta, error) { return s.pointed(key, usedFile) }

// Summary is what a list needs about key's job: the version count and the
// current and used records (Selection dropped). A job with no folder or no
// versions is the zero Summary and no error; anything unreadable is an error,
// never an empty Summary, so a damaged store does not look like "no resume".
func (s *Store) Summary(key string) (Summary, error) {
	if err := CheckKey(key); err != nil {
		return Summary{}, err
	}
	dir, l, err := s.openJob(key)
	if errors.Is(err, ErrNotFound) {
		return Summary{}, nil
	}
	if err != nil {
		return Summary{}, err
	}
	return summarize(dir, key, l)
}

// summarize is the Summary of the job in dir, whose key file already names key
// and whose listing is l: Summary and Summaries share it.
func summarize(dir, key string, l listing) (Summary, error) {
	sum := Summary{Versions: len(l.jsons)}
	for _, p := range []struct {
		name string
		into **Meta
	}{{currentFile, &sum.Current}, {usedFile, &sum.Used}} {
		n, err := readPointer(dir, p.name, l)
		if err != nil {
			return Summary{}, err
		}
		if n == 0 {
			continue
		}
		m, err := readMeta(dir, key, n)
		if err != nil {
			return Summary{}, err
		}
		m.Selection = nil
		*p.into = &m
	}
	return sum, nil
}

// --- pointers ----------------------------------------------------------

// SetCurrent makes version n the job's current one. It needs the version to
// read; it does not read the old pointer, so it also repairs a damaged one.
func (s *Store) SetCurrent(ctx context.Context, key string, n int) error {
	_, err := s.bind(ctx, key, n, currentFile, nil)
	return err
}

// Use binds version n for applying (the version `used` names, the one a mark
// records as sent). The version must have passed its fit (Checks.Fit passed or
// soft), had current eligibility when it was built (Checks.Eligibility) and
// have a PDF whose bytes match its hash; otherwise the error wraps ErrNotUsable
// (or ErrDamaged for a PDF that does not match) and nothing changes. The
// eligibility is the one recorded at build time: a caller that must know the
// bank has not moved since revalidates first.
func (s *Store) Use(ctx context.Context, key string, n int) (Meta, error) {
	return s.bind(ctx, key, n, usedFile, func(dir string, m Meta) error {
		switch {
		case m.Checks.Fit != "passed" && m.Checks.Fit != "soft":
			return fmt.Errorf("%w: v%d's fit is %s", ErrNotUsable, n, m.Checks.Fit)
		case m.Checks.Eligibility != "current":
			return fmt.Errorf("%w: v%d's eligibility is %s", ErrNotUsable, n, m.Checks.Eligibility)
		case m.PDFSHA256 == "":
			return fmt.Errorf("%w: v%d has no PDF", ErrNotUsable, n)
		}
		pdf, err := readRegular(versionFile(dir, n, "pdf"), MaxPDFBytes)
		if err != nil {
			return fmt.Errorf("v%d pdf: %w", n, damaged(err))
		}
		sum := sha256.Sum256(pdf)
		if hex.EncodeToString(sum[:]) != m.PDFSHA256 {
			return fmt.Errorf("%w: v%d's PDF does not match its hash", ErrDamaged, n)
		}
		return nil
	})
}

func (s *Store) bind(ctx context.Context, key string, n int, name string, ok func(dir string, m Meta) error) (Meta, error) {
	if err := CheckKey(key); err != nil {
		return Meta{}, err
	}
	if err := checkVersion(n); err != nil {
		return Meta{}, err
	}
	dir, release, err := s.lockJob(ctx, key, false)
	if err != nil {
		return Meta{}, err
	}
	defer release()
	if _, _, err := s.openJob(key); err != nil {
		return Meta{}, err
	}
	m, err := readMeta(dir, key, n)
	if err != nil {
		return Meta{}, err
	}
	if ok != nil {
		if err := ok(dir, m); err != nil {
			return Meta{}, err
		}
	}
	if err := writePointer(dir, name, n); err != nil {
		return Meta{}, err
	}
	return m, nil
}

// --- attempts ----------------------------------------------------------

// AppendAttempt records a build that was paid for and saved nothing. The file
// is a window: past maxAttemptsBytes or maxAttemptLines its oldest lines go, so
// logging never fails for being full. A zero Created is stamped now.
func (s *Store) AppendAttempt(ctx context.Context, key string, a Attempt) error {
	if err := CheckKey(key); err != nil {
		return err
	}
	if a.Created.IsZero() {
		a.Created = s.now()
	}
	a.Created = a.Created.UTC()
	if err := checkAttempt(a); err != nil {
		return err
	}
	line, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("resumestore: marshal: %w", err)
	}
	dir, release, err := s.lockJob(ctx, key, true)
	if err != nil {
		return err
	}
	defer release()
	l, err := scan(dir, s.now())
	if err != nil {
		return fmt.Errorf("resumestore: %w", err)
	}
	if err := ensureKey(dir, key, l); err != nil {
		return err
	}
	var lines [][]byte
	// An unreadable or oversized log is replaced: it is a diagnostic window and
	// must never stop the next paid attempt from being recorded.
	if old, err := readRegular(filepath.Join(dir, attemptsFile), 4*maxAttemptsBytes); err == nil {
		for _, ln := range bytes.Split(old, []byte("\n")) {
			if len(ln) > 0 {
				lines = append(lines, ln)
			}
		}
	}
	lines = append(lines, line)
	if len(lines) > maxAttemptLines {
		lines = lines[len(lines)-maxAttemptLines:]
	}
	size := func() int {
		t := 0
		for _, ln := range lines {
			t += len(ln) + 1
		}
		return t
	}
	for len(lines) > 1 && size() > maxAttemptsBytes {
		lines = lines[1:]
	}
	out := append(bytes.Join(lines, []byte("\n")), '\n')
	if err := atomicfile.Write(filepath.Join(dir, attemptsFile), out); err != nil {
		return fmt.Errorf("resumestore: %w", err)
	}
	return nil
}

// Attempts returns key's recorded attempts, oldest first. Lines that do not
// read are left out and reported as ErrDamaged beside the ones that did.
func (s *Store) Attempts(key string) ([]Attempt, error) {
	if err := CheckKey(key); err != nil {
		return nil, err
	}
	dir, _, err := s.openJob(key)
	if err != nil {
		return nil, err
	}
	raw, err := readRegular(filepath.Join(dir, attemptsFile), 4*maxAttemptsBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("attempts: %w", damaged(err))
	}
	var out []Attempt
	var bad []error
	for i, ln := range bytes.Split(raw, []byte("\n")) {
		if len(ln) == 0 {
			continue
		}
		var a Attempt
		dec := json.NewDecoder(bytes.NewReader(ln))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&a); err != nil || checkAttempt(a) != nil {
			bad = append(bad, fmt.Errorf("%w: attempts line %d", ErrDamaged, i+1))
			continue
		}
		out = append(out, a)
	}
	return out, errors.Join(bad...)
}
