package resumestore

import (
	"encoding/json"
	"errors"
	"time"
)

// Schema is the only version record layout this package reads or writes.
const Schema = 1

// Meta is one version of a job's resume: vN.json, beside vN.pdf. The store does
// not import rkbresume; Selection is the choice as raw JSON in the owner-edit
// shape. No field holds personal data (header, name, contact), and Selection
// never carries bank, meta, header or a path (Save refuses those top-level
// keys).
type Meta struct {
	Schema    int             `json:"schema"`           // = Schema
	Key       string          `json:"key"`              // posting.Key the job is under
	Version   int             `json:"version"`          // N of vN, assigned by the store on Save
	Parent    int             `json:"parent,omitempty"` // the version this was made from; 0 = none
	Source    string          `json:"source"`           // "tailored" | "base" | "edited" | "revalidated"
	Reason    string          `json:"reason,omitempty"` // why base / what revalidation dropped
	Model     string          `json:"model,omitempty"`  // model id when a model turn ran
	Cost      Cost            `json:"cost"`
	Checks    Checks          `json:"checks"`
	Pages     int             `json:"pages,omitempty"` // 0 = unknown (not fitted)
	Notes     []string        `json:"notes,omitempty"` // rkb layout notes (scrubbed)
	Dropped   []string        `json:"unverified_dropped,omitempty"`
	Keywords  []string        `json:"jd_keywords,omitempty"`
	Selection json.RawMessage `json:"selection"`           // the choice in the owner-edit shape (rkbresume decodeEdit accepts it)
	Bullets   []BulletRef     `json:"bullets"`             // every bullet on the page, page order
	FitLines  map[string]int  `json:"fit_lines,omitempty"` // bullet id -> rendered lines (from rkb fit report)
	Prints    Fingerprints    `json:"fingerprints"`
	PDFSHA256 string          `json:"pdf_sha256,omitempty"` // "" when there is no PDF (render without fit)
	Created   time.Time       `json:"created"`
}

// Cost is what a build's model turns cost.
type Cost struct {
	USD      float64 `json:"usd"`      // sum of billed turns
	Complete bool    `json:"complete"` // false when any turn's cost is unknown (then USD is a lower bound)
	Turns    int     `json:"turns"`    // model turns sent
}

// Checks holds each check's state. Each is one of the listed strings; "" is
// never written, so a check that did not run never reads as one that passed.
type Checks struct {
	Eligibility string `json:"eligibility"` // "current" | "stale" | "unknown"
	Lint        string `json:"lint"`        // "passed" | "failed" | "not_run"
	Fit         string `json:"fit"`         // "passed" | "soft" | "failed" | "not_run" | "could_not_run"
	Figures     string `json:"figures"`     // "verified" | "dropped" | "not_run"
}

// BulletRef names one bullet on the page and the text it was chosen for.
type BulletRef struct {
	ID       string `json:"id"`
	TextHash string `json:"text_hash"` // rkb's 12-hex sha1 of the resolved plain text
}

// Fingerprints are what a build read, so a later reader can tell a version
// made from inputs that have since changed.
type Fingerprints struct {
	Bank          string `json:"bank"`           // sha256 hex of the bank bytes the build read (the snapshot)
	BaseSelection string `json:"base_selection"` // sha256 hex of the base selection file
	Selections    string `json:"selections"`     // sha256 hex of the sorted kb.selections paths + each file's sha256
	VMReport      string `json:"vm_report"`      // sha256 hex of the vm report file, "" if none
	Claims        string `json:"claims"`         // sha256 hex of the sorted claim id=value pairs read at load
	RKB           string `json:"rkb"`            // rkb binary sha256 hex
}

// Attempt is a paid build that saved nothing (cancelled, timed out, or failed
// after a model turn): the money is spent and no version records it, so it is
// appended to the job's attempts.jsonl.
type Attempt struct {
	Created time.Time `json:"created"`
	Cost    Cost      `json:"cost"`
	Code    string    `json:"code"` // the build's error code, e.g. "canceled"
}

// Summary is what a job list needs about a job's resume: how many versions
// there are and the current and used ones. Current and Used are nil when the
// job has none. Their Selection is dropped (a list never needs the choice).
type Summary struct {
	Versions int
	Current  *Meta
	Used     *Meta
}

var (
	// ErrInvalid means a key, version number or record broke the store's rules.
	ErrInvalid = errors.New("resumestore: invalid")
	// ErrNotFound means there is no such job, version or pointer.
	ErrNotFound = errors.New("resumestore: not found")
	// ErrDamaged means something on disk is not what the store wrote: a key
	// file that names another key, a record that does not read, a PDF that does
	// not match its hash, a pointer to a missing version. A damaged version is
	// never served.
	ErrDamaged = errors.New("resumestore: damaged")
	// ErrSchema means a record was written by a different schema.
	ErrSchema = errors.New("resumestore: unknown schema")
	// ErrBusy means a lock could not be had in time.
	ErrBusy = errors.New("resumestore: busy")
	// ErrFull means a bound was reached and nothing may be deleted to make room:
	// every job folder holds a used version, or the version numbers ran out.
	ErrFull = errors.New("resumestore: full")
	// ErrExists means a Rekey would land on a job folder that is already there:
	// two jobs are never merged.
	ErrExists = errors.New("resumestore: already exists")
	// ErrNotUsable means a version cannot be bound for applying: its fit did not
	// pass (or soft-pass), its eligibility is not current, or it has no PDF.
	ErrNotUsable = errors.New("resumestore: not usable")
)
