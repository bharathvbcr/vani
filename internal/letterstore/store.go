// Package letterstore keeps one posting's cover-letter versions beside the
// resume store: <state>/letters/<sha256(key)>/. Build writes a version. Use
// points at the version the extension may fetch. Files are mode 0600.
package letterstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/filelock"
)

const (
	dirName = "letters"
	// MaxPDF matches the resume cap: a larger file cannot cross a native reply.
	MaxPDF = 512 << 10
	// MaxTextBytes caps the body kept beside the PDF.
	MaxTextBytes = 32 << 10
	// MaxVersions is how many versions one posting keeps.
	MaxVersions = 20
	// MaxJobs is how many postings the store holds.
	MaxJobs = 200
	maxKey  = 2048
	// maxPointer and maxMeta bound the small files Save and Room read back.
	maxPointer = 32
	maxMeta    = 64 << 10
	lockWait   = 5 * time.Second
)

// Codes for a store failure.
const (
	CodeBadRequest = "bad_request"
	CodeTooLarge   = "too_large"
	CodeNotFound   = "not_found"
	CodeTimeout    = "timeout"
	CodeFull       = "full"
	CodeDamaged    = "damaged"
	CodeIO         = "io"
)

// Error is a store failure with a code. The message does not include letter text.
type Error struct {
	Code string
	Err  error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "letterstore: " + e.Code
}

func (e *Error) Unwrap() error { return e.Err }

func fail(code string, err error) error { return &Error{Code: code, Err: err} }

// CodeOf is the Code of an *Error in err's chain, or "".
func CodeOf(err error) string {
	var le *Error
	if errors.As(err, &le) {
		return le.Code
	}
	return ""
}

// Meta is one saved version. It holds no letter text and no letterhead.
type Meta struct {
	Version      int    `json:"version"`
	Created      string `json:"created"`
	Words        int    `json:"words"`
	CompanyCheck string `json:"company_check,omitempty"`
	TextSHA256   string `json:"text_sha256"`
	PDFSHA256    string `json:"pdf_sha256,omitempty"`
	PDFBytes     int    `json:"pdf_bytes,omitempty"`
}

// Store is the letters under one state directory.
type Store struct {
	root string
	Now  func() time.Time
}

// Dir is the letters directory for a state directory.
func Dir(stateDir string) string { return filepath.Join(stateDir, dirName) }

// Open returns the store. It creates nothing.
func Open(stateDir string) (*Store, error) {
	if stateDir == "" || !filepath.IsAbs(stateDir) {
		return nil, fail(CodeBadRequest, errors.New("letterstore: the state directory must be an absolute path"))
	}
	return &Store{root: Dir(stateDir)}, nil
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func checkKey(key string) error {
	switch {
	case strings.TrimSpace(key) == "":
		return fail(CodeBadRequest, errors.New("letterstore: the posting key is empty"))
	case len(key) > maxKey:
		return fail(CodeBadRequest, fmt.Errorf("letterstore: the posting key is %d bytes, over %d", len(key), maxKey))
	case !utf8.ValidString(key):
		return fail(CodeBadRequest, errors.New("letterstore: the posting key is not UTF-8"))
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return fail(CodeBadRequest, errors.New("letterstore: the posting key holds a control character"))
		}
	}
	return nil
}

func folder(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// Folder is the directory name for key, a hash, so a key cannot name a path.
func Folder(key string) (string, error) {
	if err := checkKey(key); err != nil {
		return "", err
	}
	return folder(key), nil
}

func (s *Store) dir(key string) string { return filepath.Join(s.root, folder(key)) }

// Save writes the next version. It does not mark it used. An empty body, a
// body over MaxTextBytes, a PDF over MaxPDF, or a PDF that is not a PDF is
// refused and nothing is written. A body identical to the current version is
// not written again. pdf may be nil: the text is kept and no PDF is recorded.
func (s *Store) Save(ctx context.Context, key, body, companyCheck string, pdf []byte) (Meta, error) {
	if err := checkKey(key); err != nil {
		return Meta{}, err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return Meta{}, fail(CodeBadRequest, errors.New("letterstore: the letter has no text"))
	}
	if len(body) > MaxTextBytes {
		return Meta{}, fail(CodeTooLarge, fmt.Errorf("letterstore: the letter is %d bytes, over %d", len(body), MaxTextBytes))
	}
	if pdf != nil {
		if len(pdf) == 0 || !bytesHasPDF(pdf) {
			return Meta{}, fail(CodeBadRequest, errors.New("letterstore: the file is not a PDF"))
		}
		if len(pdf) > MaxPDF {
			return Meta{}, fail(CodeTooLarge, fmt.Errorf("letterstore: the PDF is %d bytes, over %d", len(pdf), MaxPDF))
		}
	}
	if err := ctx.Err(); err != nil {
		return Meta{}, fail(CodeTimeout, fmt.Errorf("letterstore: %w", err))
	}
	adm, err := s.admit(ctx, key, opSave)
	if err != nil {
		return Meta{}, err
	}
	defer adm.release()
	dir := adm.dir
	textSum := sha256.Sum256([]byte(body))
	textHex := hex.EncodeToString(textSum[:])
	var pdfHex string
	if pdf != nil {
		sum := sha256.Sum256(pdf)
		pdfHex = hex.EncodeToString(sum[:])
	}
	if cur, err := readPointer(filepath.Join(dir, "current")); err == nil {
		if meta, merr := readMeta(dir, cur); merr == nil && meta.TextSHA256 == textHex && meta.PDFSHA256 == pdfHex {
			return meta, nil
		}
	}
	if adm.full != nil {
		return Meta{}, adm.full
	}
	next := adm.next
	meta := Meta{
		Version:      next,
		Created:      s.now().Format(time.RFC3339),
		Words:        len(strings.Fields(body)),
		CompanyCheck: companyCheck,
		TextSHA256:   textHex,
		PDFSHA256:    pdfHex,
		PDFBytes:     len(pdf),
	}
	if err := atomicfile.Write(filepath.Join(dir, fmt.Sprintf("v%d.txt", next)), []byte(body)); err != nil {
		return Meta{}, fail(CodeIO, fmt.Errorf("letterstore: write text: %w", err))
	}
	if pdf != nil {
		if err := atomicfile.Write(filepath.Join(dir, fmt.Sprintf("v%d.pdf", next)), pdf); err != nil {
			return Meta{}, fail(CodeIO, fmt.Errorf("letterstore: write pdf: %w", err))
		}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return Meta{}, fail(CodeIO, err)
	}
	if err := atomicfile.Write(filepath.Join(dir, fmt.Sprintf("v%d.json", next)), raw); err != nil {
		return Meta{}, fail(CodeIO, fmt.Errorf("letterstore: write meta: %w", err))
	}
	if err := atomicfile.Write(filepath.Join(dir, "current"), []byte(strconv.Itoa(next)+"\n")); err != nil {
		return Meta{}, fail(CodeIO, fmt.Errorf("letterstore: write current: %w", err))
	}
	if err := atomicfile.Write(filepath.Join(dir, "key"), []byte(key)); err != nil {
		return Meta{}, fail(CodeIO, fmt.Errorf("letterstore: write key: %w", err))
	}
	return meta, nil
}

// Room reports whether Save could write one more version for key right now,
// without writing anything: the CodeFull error Save would return when key is at
// MaxVersions, or when key is new and the store already holds MaxJobs postings.
// A caller that pays for a letter asks first, so a full store refuses before the
// money is spent. It is a probe, not a reservation: a writer that fills the
// store between Room and Save still makes Save report full.
func (s *Store) Room(ctx context.Context, key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fail(CodeTimeout, fmt.Errorf("letterstore: %w", err))
	}
	adm, err := s.admit(ctx, key, opRoom)
	if err != nil {
		return err
	}
	defer adm.release()
	return adm.full
}

// Use points the used letter at version n. It does not copy the text into the reply.
func (s *Store) Use(ctx context.Context, key string, n int) (Meta, error) {
	if err := checkKey(key); err != nil {
		return Meta{}, err
	}
	if n < 1 || n > MaxVersions {
		return Meta{}, fail(CodeBadRequest, fmt.Errorf("letterstore: version %d is not in 1..%d", n, MaxVersions))
	}
	adm, err := s.admit(ctx, key, opUse)
	if err != nil {
		return Meta{}, err
	}
	defer adm.release()
	dir := adm.dir
	if err := verifyKey(dir, key); err != nil {
		return Meta{}, err
	}
	meta, err := readMeta(dir, n)
	if err != nil {
		return Meta{}, err
	}
	if _, err := readChecked(filepath.Join(dir, fmt.Sprintf("v%d.txt", n)), meta.TextSHA256, MaxTextBytes); err != nil {
		return Meta{}, err
	}
	if err := atomicfile.Write(filepath.Join(dir, "used"), []byte(strconv.Itoa(n)+"\n")); err != nil {
		return Meta{}, fail(CodeIO, fmt.Errorf("letterstore: write used: %w", err))
	}
	return meta, nil
}

// Current is the version Build saved last.
func (s *Store) Current(key string) (Meta, error) {
	return s.pointed(key, "current")
}

// Used is the version Use selected.
func (s *Store) Used(key string) (Meta, error) {
	return s.pointed(key, "used")
}

func (s *Store) pointed(key, name string) (Meta, error) {
	if err := checkKey(key); err != nil {
		return Meta{}, err
	}
	dir := s.dir(key)
	if err := verifyKey(dir, key); err != nil {
		return Meta{}, err
	}
	n, err := readPointer(filepath.Join(dir, name))
	if err != nil {
		return Meta{}, err
	}
	return readMeta(dir, n)
}

// Text returns the body of version n. PDF returns the PDF, or a not-found
// error when that version has no PDF.
func (s *Store) Text(key string, n int) (string, error) {
	meta, err := s.meta(key, n)
	if err != nil {
		return "", err
	}
	b, err := readChecked(filepath.Join(s.dir(key), fmt.Sprintf("v%d.txt", n)), meta.TextSHA256, MaxTextBytes)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// PDF returns the PDF of version n.
func (s *Store) PDF(key string, n int) ([]byte, error) {
	meta, err := s.meta(key, n)
	if err != nil {
		return nil, err
	}
	if meta.PDFSHA256 == "" {
		return nil, fail(CodeNotFound, errors.New("letterstore: this version has no PDF"))
	}
	return readChecked(filepath.Join(s.dir(key), fmt.Sprintf("v%d.pdf", n)), meta.PDFSHA256, MaxPDF)
}

func (s *Store) meta(key string, n int) (Meta, error) {
	if err := checkKey(key); err != nil {
		return Meta{}, err
	}
	if err := verifyKey(s.dir(key), key); err != nil {
		return Meta{}, err
	}
	if n < 1 || n > MaxVersions {
		return Meta{}, fail(CodeBadRequest, fmt.Errorf("letterstore: version %d is not in 1..%d", n, MaxVersions))
	}
	return readMeta(s.dir(key), n)
}

// admitOp is what the caller of admit is about to do with the posting.
type admitOp int

const (
	opUse  admitOp = iota // read an existing posting; never create
	opSave                // write the next version; create the posting if new
	opRoom                // ask whether opSave would succeed; write nothing durable
)

// admission is what admit decided. release drops the locks it took. full is the
// CodeFull error opSave and opRoom owe when the posting is at MaxVersions; Save
// reads it after its identical-letter check, which still returns a full
// posting's current version.
type admission struct {
	dir     string
	next    int
	full    error
	release func()
}

// admit is the one step Save and Room both run: take the store lock, resolve the
// posting folder, take its own lock, decide capacity (MaxJobs for a new posting,
// MaxVersions for an existing one) and prove the folder can take a write. Room
// stops there, so what it says cannot drift from what Save then does.
//
// Not bounded: the fsync and rename inside atomicfile.Write on a hung
// filesystem. They are not abandoned on a timeout either, because a write that
// landed after the caller was told "not saved" would make that reply dishonest.
func (s *Store) admit(ctx context.Context, key string, op admitOp) (admission, error) {
	none := admission{release: func() {}}
	ctx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()
	if op == opSave {
		if err := os.MkdirAll(s.root, 0o700); err != nil {
			return none, fail(CodeIO, fmt.Errorf("letterstore: %w", err))
		}
	} else if _, err := os.Lstat(s.root); err != nil {
		if op == opRoom && errors.Is(err, os.ErrNotExist) {
			return none, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return none, fail(CodeNotFound, errors.New("letterstore: no letter for this posting"))
		}
		return none, fail(CodeIO, err)
	}
	lk, err := filelock.Acquire(ctx, filepath.Join(s.root, ".lock"))
	if err != nil {
		return none, lockFailure(ctx, err)
	}
	defer lk.Release()
	dir := s.dir(key)
	st, serr := os.Lstat(dir)
	switch {
	case errors.Is(serr, os.ErrNotExist):
		if op == opUse {
			return none, fail(CodeNotFound, errors.New("letterstore: no letter for this posting"))
		}
		n, err := jobCount(s.root)
		if err != nil {
			return none, err
		}
		if n >= MaxJobs {
			return none, fail(CodeFull, fmt.Errorf("letterstore: %d postings already have a letter", MaxJobs))
		}
		if op == opRoom {
			return none, checkWritable(s.root)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return none, fail(CodeIO, fmt.Errorf("letterstore: %w", err))
		}
	case serr != nil:
		return none, fail(CodeIO, serr)
	case !st.IsDir():
		return none, fail(CodeDamaged, errors.New("letterstore: the posting folder is not a directory"))
	}
	jl, err := filelock.Acquire(ctx, filepath.Join(dir, ".lock"))
	if err != nil {
		return none, lockFailure(ctx, err)
	}
	adm := admission{dir: dir, next: 1, release: func() { _ = jl.Release() }}
	if op == opUse {
		return adm, nil
	}
	next, err := nextVersion(dir)
	if err != nil {
		adm.release()
		return none, err
	}
	adm.next = next
	if next > MaxVersions {
		adm.full = fail(CodeFull, fmt.Errorf("letterstore: %d versions already saved", MaxVersions))
	}
	if err := checkSaveTargets(dir); err != nil {
		adm.release()
		return none, err
	}
	if err := checkWritable(dir); err != nil {
		adm.release()
		return none, err
	}
	return adm, nil
}

// checkSaveTargets refuses a posting folder where a file Save rewrites is a
// link, directory or FIFO. Save would find that only at its last writes, after
// the model was paid and with the new version half in place.
func checkSaveTargets(dir string) error {
	for _, name := range []string{"current", "key"} {
		fi, err := os.Lstat(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fail(CodeIO, err)
		}
		if !fi.Mode().IsRegular() {
			return fail(CodeDamaged, fmt.Errorf("letterstore: %s is not a regular file", name))
		}
	}
	return nil
}

func lockFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fail(CodeTimeout, fmt.Errorf("letterstore: lock: %w", ctx.Err()))
	}
	return fail(CodeIO, fmt.Errorf("letterstore: lock: %w", err))
}

// checkWritable proves dir takes a new file the way every Save write does, then
// removes it.
func checkWritable(dir string) error {
	if err := atomicfile.CheckWritable(dir); err != nil {
		return fail(CodeIO, fmt.Errorf("letterstore: %w", err))
	}
	return nil
}

func jobCount(root string) (int, error) {
	ents, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fail(CodeIO, err)
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() && len(e.Name()) == 64 {
			n++
		}
	}
	return n, nil
}

func nextVersion(dir string) (int, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0, fail(CodeIO, err)
	}
	max := 0
	for _, e := range ents {
		name := e.Name()
		if !strings.HasPrefix(name, "v") || !strings.HasSuffix(name, ".json") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "v"), ".json"))
		if err != nil || n < 1 {
			continue
		}
		if n > max {
			max = n
		}
	}
	return max + 1, nil
}

func verifyKey(dir, key string) error {
	b, err := readBounded(filepath.Join(dir, "key"), maxKey)
	if errors.Is(err, os.ErrNotExist) {
		if _, serr := os.Lstat(dir); errors.Is(serr, os.ErrNotExist) {
			return fail(CodeNotFound, errors.New("letterstore: no letter for this posting"))
		}
		return fail(CodeDamaged, errors.New("letterstore: the stored posting key does not match"))
	}
	if err != nil {
		return ioOr(err)
	}
	if string(b) != key {
		return fail(CodeDamaged, errors.New("letterstore: the stored posting key does not match"))
	}
	return nil
}

func readPointer(path string) (int, error) {
	b, err := readBounded(path, maxPointer)
	if errors.Is(err, os.ErrNotExist) {
		return 0, fail(CodeNotFound, errors.New("letterstore: no letter is selected"))
	}
	if err != nil {
		return 0, ioOr(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 1 {
		return 0, fail(CodeDamaged, errors.New("letterstore: the version pointer is damaged"))
	}
	return n, nil
}

func readMeta(dir string, n int) (Meta, error) {
	b, err := readBounded(filepath.Join(dir, fmt.Sprintf("v%d.json", n)), maxMeta)
	if errors.Is(err, os.ErrNotExist) {
		return Meta{}, fail(CodeNotFound, fmt.Errorf("letterstore: no v%d", n))
	}
	if err != nil {
		return Meta{}, ioOr(err)
	}
	var meta Meta
	if json.Unmarshal(b, &meta) != nil || meta.Version != n || meta.TextSHA256 == "" {
		return Meta{}, fail(CodeDamaged, fmt.Errorf("letterstore: v%d is damaged", n))
	}
	return meta, nil
}

func readChecked(path, want string, max int) ([]byte, error) {
	b, err := readBounded(path, max)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fail(CodeNotFound, errors.New("letterstore: the file is missing"))
	}
	if err != nil {
		return nil, ioOr(err)
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != want {
		return nil, fail(CodeDamaged, errors.New("letterstore: the file does not match the stored hash"))
	}
	return b, nil
}

// readBounded reads a regular file of at most max bytes. A symbolic link, FIFO,
// device or directory in its place is CodeDamaged, never followed or waited on:
// the file is opened non-blocking and re-checked after the open, so a swap
// between the Lstat and the open cannot park the reader on a FIFO. A missing
// file comes back as an error satisfying os.ErrNotExist.
func readBounded(path string, max int) ([]byte, error) {
	notRegular := fail(CodeDamaged, errors.New("letterstore: the file is not a regular file of the stored size"))
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > int64(max) {
		return nil, notRegular
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || !os.SameFile(fi, st) {
		return nil, notRegular
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > max {
		return nil, notRegular
	}
	return b, nil
}

// ioOr keeps a store error as it is and codes any other failure CodeIO.
func ioOr(err error) error {
	if CodeOf(err) != "" {
		return err
	}
	return fail(CodeIO, err)
}

func bytesHasPDF(b []byte) bool {
	return len(b) >= 5 && string(b[:5]) == "%PDF-"
}
