// Package inbox is the spool between `vani jobs ingest` and the ledger's
// owner (docs/EXTENSION_INGEST.md). The ledger takes one writer at a time, and
// a daemon holds it for its whole life, so the CLI cannot write jobs the
// extension scraped into it. It validates them and spools a batch file; the
// process that owns the ledger drains the spool and folds each batch.
//
// This package is the one owner of the contract's bounds: Request.Validate
// judges what arrives on stdin, Batch.Validate re-judges what a drain reads
// back, and both use the same job rules. Nothing here talks to the network,
// the ledger or the config.
package inbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The contract's bounds. Anything over one is refused, never cut.
const (
	Version = 1

	MaxStdinBytes = 1 << 20
	MinJobs       = 1
	MaxJobs       = 200
	MaxTitle      = 300
	MaxCompany    = 200
	MaxLocation   = 200
	MaxURL        = 2048
	MaxSnippet    = 4000
	MaxPosted     = 64
	MaxPageURL    = 2048
	MaxPageTitle  = 300

	// MaxTokenLen bounds a batch's board token.
	MaxTokenLen = 80
	// MaxBatchBytes bounds one spooled file. A batch is at most the stdin it
	// came from plus its canonicalised URLs and envelope.
	MaxBatchBytes = 2 << 20
	// MaxDrainFiles is how many files one drain takes (the contract's bound).
	MaxDrainFiles = 50

	// Dir is the spool's directory under the state dir.
	Dir = "inbox"
	// BadDir holds the files a drain could not parse. Nothing retries them.
	BadDir = "bad"

	lockName = ".drain.lock"
)

// ErrMalformed marks a request that is not a well-formed payload at all, as
// opposed to a job that breaks a rule (which is rejected alone).
var ErrMalformed = errors.New("malformed request")

// ErrBusy means another drain of the same directory is running.
var ErrBusy = errors.New("inbox: another drain is running")

// Page is the listing page the jobs were scraped from.
type Page struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

// Job is one scraped job. Snippet is untrusted page text.
type Job struct {
	Title    string `json:"title"`
	Company  string `json:"company,omitempty"`
	Location string `json:"location,omitempty"`
	URL      string `json:"url"`
	Snippet  string `json:"snippet,omitempty"`
	Posted   string `json:"posted,omitempty"`
}

// Request is the JSON `vani jobs ingest --json` reads on stdin.
type Request struct {
	Page       Page  `json:"page"`
	SaveSearch bool  `json:"save_search"`
	Jobs       []Job `json:"jobs"`
}

// Rejection says why one job was refused. Reasons never quote the job.
type Rejection struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// ParseRequest decodes stdin's bytes and checks everything that is not about a
// single job: the size, the shape, the job count and the page. A failure wraps
// ErrMalformed. Unknown fields are ignored so a newer extension still ingests.
func ParseRequest(data []byte) (Request, error) {
	if len(data) > MaxStdinBytes {
		return Request{}, fmt.Errorf("%w: input exceeds %d bytes", ErrMalformed, MaxStdinBytes)
	}
	var r Request
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&r); err != nil {
		return Request{}, fmt.Errorf("%w: %v", ErrMalformed, jsonProblem(err))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Request{}, fmt.Errorf("%w: data after the JSON value", ErrMalformed)
	}
	if n := len(r.Jobs); n < MinJobs || n > MaxJobs {
		return Request{}, fmt.Errorf("%w: %d jobs, want %d..%d", ErrMalformed, n, MinJobs, MaxJobs)
	}
	if err := r.Page.check(r.SaveSearch); err != nil {
		return Request{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return r, nil
}

// jsonProblem drops the decoder's text where it could carry input.
func jsonProblem(err error) string {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		return fmt.Sprintf("invalid JSON at byte %d", syn.Offset)
	case errors.As(err, &typ):
		return fmt.Sprintf("field %q has the wrong type", typ.Field)
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "empty or truncated JSON"
	}
	return "invalid JSON"
}

func (p Page) check(saveSearch bool) error {
	if len(p.URL) > MaxPageURL {
		return fmt.Errorf("page.url exceeds %d bytes", MaxPageURL)
	}
	if len(p.Title) > MaxPageTitle {
		return fmt.Errorf("page.title exceeds %d bytes", MaxPageTitle)
	}
	if saveSearch {
		u, err := url.Parse(strings.TrimSpace(p.URL))
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			return errors.New("page.url must be https when save_search is set")
		}
	}
	return nil
}

// Validate judges every job on its own. Valid jobs come back trimmed, with
// their canonical URL, in input order; a job whose canonical URL an earlier
// valid one has counts once, as a duplicate. Rejections carry the input index.
func (r Request) Validate() (jobs []Job, duplicates int, rejected []Rejection) {
	seen := map[string]bool{}
	for i, j := range r.Jobs {
		v, reason := checkJob(j)
		if reason != "" {
			rejected = append(rejected, Rejection{Index: i, Reason: reason})
			continue
		}
		if seen[v.URL] {
			duplicates++
			continue
		}
		seen[v.URL] = true
		jobs = append(jobs, v)
	}
	return jobs, duplicates, rejected
}

// checkJob returns the job as it is spooled (trimmed, canonical URL) or the
// reason it is refused.
func checkJob(j Job) (Job, string) {
	j.Title, j.Company, j.Location = strings.TrimSpace(j.Title), strings.TrimSpace(j.Company), strings.TrimSpace(j.Location)
	j.URL, j.Posted = strings.TrimSpace(j.URL), strings.TrimSpace(j.Posted)
	switch {
	case j.Title == "":
		return Job{}, "title is required"
	case len(j.Title) > MaxTitle:
		return Job{}, fmt.Sprintf("title exceeds %d bytes", MaxTitle)
	case len(j.Company) > MaxCompany:
		return Job{}, fmt.Sprintf("company exceeds %d bytes", MaxCompany)
	case len(j.Location) > MaxLocation:
		return Job{}, fmt.Sprintf("location exceeds %d bytes", MaxLocation)
	case j.URL == "":
		return Job{}, "url is required"
	case len(j.URL) > MaxURL:
		return Job{}, fmt.Sprintf("url exceeds %d bytes", MaxURL)
	case len(j.Snippet) > MaxSnippet:
		return Job{}, fmt.Sprintf("snippet exceeds %d bytes", MaxSnippet)
	case len(j.Posted) > MaxPosted:
		return Job{}, fmt.Sprintf("posted exceeds %d bytes", MaxPosted)
	}
	canon, err := posting.CanonicalURL(j.URL)
	if err != nil {
		return Job{}, "url is not http(s)"
	}
	if len(canon) > MaxURL {
		return Job{}, fmt.Sprintf("url exceeds %d bytes", MaxURL)
	}
	j.URL = canon
	return j, ""
}

// Batch is one spooled file: the request's validated jobs, when they were
// received, and the board they fold under. Token is the pg board token
// (a lower-case slug), Company the fallback for a job with no company.
type Batch struct {
	V          int       `json:"v"`
	ReceivedAt time.Time `json:"received_at"`
	Token      string    `json:"token"`
	Company    string    `json:"company,omitempty"`
	Page       Page      `json:"page"`
	SaveSearch bool      `json:"save_search,omitempty"`
	Jobs       []Job     `json:"jobs"`
}

var tokenRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidToken reports whether s can be a batch's board token: a slug that
// posting.PageKey and config.ValidBoard both accept.
func ValidToken(s string) bool { return len(s) <= MaxTokenLen && tokenRE.MatchString(s) }

// Validate checks a batch as a drain reads it. A job that would be rejected on
// ingest makes the batch invalid: Write only ever spools valid jobs, so one
// here means the file was altered.
func (b Batch) Validate() error {
	switch {
	case b.V != Version:
		return fmt.Errorf("batch version %d, want %d", b.V, Version)
	case !ValidToken(b.Token):
		return errors.New("batch token is not a slug")
	case b.ReceivedAt.IsZero():
		return errors.New("batch has no received_at")
	case len(b.Company) > MaxCompany:
		return errors.New("batch company is too long")
	case len(b.Jobs) < MinJobs || len(b.Jobs) > MaxJobs:
		return fmt.Errorf("batch has %d jobs, want %d..%d", len(b.Jobs), MinJobs, MaxJobs)
	}
	if err := b.Page.check(false); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i, j := range b.Jobs {
		v, reason := checkJob(j)
		if reason != "" {
			return fmt.Errorf("job %d: %s", i, reason)
		}
		if v.URL != j.URL || seen[v.URL] {
			return fmt.Errorf("job %d: url is not canonical or repeats an earlier job", i)
		}
		seen[v.URL] = true
	}
	return nil
}

// Postings turns the batch into ledger postings, each keyed
// posting.PageKey(Token, url) so a repeat of one URL is one record. SeenAt is
// when the batch was received; PostedAt is set only from an RFC 3339 `posted`.
func (b Batch) Postings() []posting.Posting {
	out := make([]posting.Posting, 0, len(b.Jobs))
	for _, j := range b.Jobs {
		key, err := posting.PageKey(b.Token, j.URL)
		if err != nil {
			continue
		}
		p := posting.Posting{
			Key: key, ATS: posting.Page, Company: j.Company, Title: j.Title, Location: j.Location,
			URL: j.URL, Description: j.Snippet, SeenAt: b.ReceivedAt,
		}
		if p.Company == "" {
			p.Company = b.Company
		}
		if t, err := time.Parse(time.RFC3339, j.Posted); err == nil {
			p.PostedAt = t
		}
		out = append(out, p)
	}
	return out
}

// ParseBatch decodes and validates one spooled file's bytes.
func ParseBatch(data []byte) (Batch, error) {
	if len(data) > MaxBatchBytes {
		return Batch{}, fmt.Errorf("batch exceeds %d bytes", MaxBatchBytes)
	}
	var b Batch
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&b); err != nil {
		return Batch{}, errors.New(jsonProblem(err))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Batch{}, errors.New("data after the JSON value")
	}
	if err := b.Validate(); err != nil {
		return Batch{}, err
	}
	return b, nil
}

// Write validates b and spools it as dir/<ns>-<rand>.json, atomically, and
// returns the path. The nanosecond prefix is zero-padded so file names sort in
// arrival order. A batch that fails validation is never written.
func Write(dir string, b Batch) (string, error) {
	if b.V == 0 {
		b.V = Version
	}
	if b.ReceivedAt.IsZero() {
		b.ReceivedAt = time.Now().UTC()
	}
	if err := b.Validate(); err != nil {
		return "", fmt.Errorf("inbox: refusing to spool: %w", err)
	}
	data, err := json.Marshal(b)
	if err != nil {
		return "", fmt.Errorf("inbox: encode: %w", err)
	}
	if len(data) > MaxBatchBytes {
		return "", fmt.Errorf("inbox: batch is %d bytes, over %d", len(data), MaxBatchBytes)
	}
	var rnd [6]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", fmt.Errorf("inbox: random name: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%020d-%s.json", time.Now().UnixNano(), hex.EncodeToString(rnd[:])))
	if err := atomicfile.Write(path, data); err != nil {
		return "", fmt.Errorf("inbox: %w", err)
	}
	return path, nil
}

// BadFile is a spooled file a drain moved aside because it cannot be read as a
// batch. Reason is generic: it never quotes the file.
type BadFile struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// DrainReport says what one Drain did.
type DrainReport struct {
	// Folded is how many files fn accepted (and were removed); Jobs the jobs in them.
	Folded int
	Jobs   int
	// Bad lists the files moved to dir/bad/.
	Bad []BadFile
	// Unreadable counts files that could not be read this time; they stay for the next drain.
	Unreadable int
	// Remaining counts files left in the spool beyond max.
	Remaining int
}

// Drain folds the spooled batches, oldest first, at most max of them. fn
// receives each batch; the file is removed only after fn returns nil. If fn
// fails, the file stays and Drain stops with the error (the fold is expected to
// be idempotent, so the next drain repeats it). A file that does not parse or
// validate is moved to dir/bad/ and reported, and is never retried. A file
// that vanishes mid-drain is skipped. Only one drain of a directory runs at a
// time: another gets ErrBusy (so a batch is never folded by two drains at once).
func Drain(ctx context.Context, dir string, limit int, fn func(Batch) error) (DrainReport, error) {
	var rep DrainReport
	if limit < 1 {
		return rep, fmt.Errorf("inbox: drain limit %d must be above 0", limit)
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return rep, nil // nothing was ever spooled; a drain creates nothing
	}
	lock, err := os.OpenFile(filepath.Join(dir, lockName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return rep, fmt.Errorf("inbox: open drain lock: %w", err)
	}
	defer lock.Close()
	if err := filelock.TryLock(lock, true); err != nil {
		if errors.Is(err, filelock.ErrWouldBlock) {
			return rep, ErrBusy
		}
		return rep, fmt.Errorf("inbox: drain lock: %w", err)
	}
	defer func() { _ = filelock.Unlock(lock) }()

	names, err := spooled(dir)
	if err != nil {
		return rep, err
	}
	if len(names) > limit {
		rep.Remaining = len(names) - limit
		names = names[:limit]
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		path := filepath.Join(dir, name)
		data, err := readBounded(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case errors.Is(err, errTooBig):
			if err := moveBad(dir, name); err != nil {
				return rep, err
			}
			rep.Bad = append(rep.Bad, BadFile{Name: name, Reason: err.Error()})
			continue
		case err != nil:
			rep.Unreadable++
			continue
		}
		b, perr := ParseBatch(data)
		if perr != nil {
			if err := moveBad(dir, name); err != nil {
				return rep, err
			}
			rep.Bad = append(rep.Bad, BadFile{Name: name, Reason: perr.Error()})
			continue
		}
		if err := fn(b); err != nil {
			return rep, err
		}
		rep.Folded++
		rep.Jobs += len(b.Jobs)
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return rep, fmt.Errorf("inbox: remove drained batch: %w", err)
		}
	}
	return rep, nil
}

// spooled lists the batch files in dir, oldest first. Dotfiles (temp files, the
// drain lock), other extensions and directories (bad/) are not batches.
func spooled(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("inbox: %w", err)
	}
	var names []string
	for _, e := range entries { // ReadDir sorts by name
		n := e.Name()
		if e.Type().IsRegular() && strings.HasSuffix(n, ".json") && !strings.HasPrefix(n, ".") {
			names = append(names, n)
		}
	}
	return names, nil
}

// Pending counts the batch files waiting in dir; a dir that does not exist
// holds none. A poll uses it to skip a drain (and the work around one) when
// there is nothing to fold.
func Pending(dir string) (int, error) {
	names, err := spooled(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	return len(names), err
}

var errTooBig = fmt.Errorf("file exceeds %d bytes", MaxBatchBytes)

func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxBatchBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxBatchBytes {
		return nil, errTooBig
	}
	return data, nil
}

// moveBad moves dir/name to dir/bad/name so it is reported once and never read
// again. If it cannot move it the drain fails: leaving it in place would retry it.
func moveBad(dir, name string) error {
	bad := filepath.Join(dir, BadDir)
	if err := os.MkdirAll(bad, 0o700); err != nil {
		return fmt.Errorf("inbox: create bad dir: %w", err)
	}
	if err := os.Rename(filepath.Join(dir, name), filepath.Join(bad, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inbox: move unparseable batch aside: %w", err)
	}
	return nil
}
