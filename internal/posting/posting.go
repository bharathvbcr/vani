// Package posting is the one model every Vani stage agrees on: a job posting,
// its canonical identity, and where it stands in the pipeline.
package posting

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
	"unicode"
)

// ATS names the system a posting came from. The value is part of the
// canonical key, so it never changes once written to a ledger.
type ATS string

const (
	Greenhouse ATS = "gh"
	Lever      ATS = "lv"
	Ashby      ATS = "ab"
	Workday    ATS = "wd"
	// Amazon is amazon.jobs's own search API.
	Amazon ATS = "az"
	// Google is Google's careers site (google.com/about/careers).
	Google ATS = "gg"
	// Apple is jobs.apple.com.
	Apple ATS = "ap"
	// Eightfold is an Eightfold "PCSX" careers site (Microsoft, Qualcomm).
	Eightfold ATS = "ef"
	// Page marks a posting read from a company's own careers page (no ATS
	// API), keyed by board and posting URL.
	Page ATS = "pg"
	// Other marks a posting with no ATS identity; its key is the fallback hash.
	Other ATS = "x"
)

// Posting is one job posting as a source reported it. Description is
// untrusted text: it is shown, hashed and classified, never executed or
// followed.
type Posting struct {
	Key        Key       `json:"key"`
	ATS        ATS       `json:"ats"`
	Company    string    `json:"company"`
	Title      string    `json:"title"`
	Location   string    `json:"location,omitempty"`
	Remote     bool      `json:"remote,omitempty"`
	Department string    `json:"department,omitempty"`
	URL        string    `json:"url"`
	ApplyURL   string    `json:"apply_url,omitempty"`
	PostedAt   time.Time `json:"posted_at,omitzero"`
	UpdatedAt  time.Time `json:"updated_at,omitzero"`
	// Description is plain text (HTML already reduced by the source adapter).
	Description string `json:"description,omitempty"`
	// SeenAt is when Vani observed this version of the posting.
	SeenAt time.Time `json:"seen_at"`
}

// DescriptionHash identifies the content of a posting's description after
// whitespace normalisation, so cosmetic re-renders do not count as a change.
func (p Posting) DescriptionHash() string {
	return hashOf(strings.Join(strings.Fields(p.Description), " "))
}

// Status is a posting's position in the pipeline. The order of the constants
// is the forward order of the happy path; Dead, Duplicate and Dropped are
// terminal side exits.
type Status string

const (
	Discovered Status = "discovered"
	Live       Status = "live"
	Triaged    Status = "triaged"
	Unsure     Status = "unsure" // triage could not decide; kept, flagged in the digest
	Scored     Status = "scored"
	Explained  Status = "explained"
	Digested   Status = "digested"
	Dead       Status = "dead"
	Duplicate  Status = "duplicate"
	Dropped    Status = "dropped" // removed by an engine rule keyed on a triage annotation
)

// Terminal reports whether no further stage runs for this status.
func (s Status) Terminal() bool {
	switch s {
	case Dead, Duplicate, Dropped, Digested:
		return true
	}
	return false
}

// Valid reports whether s is one of the declared statuses.
func (s Status) Valid() bool {
	switch s {
	case Discovered, Live, Triaged, Unsure, Scored, Explained, Digested, Dead, Duplicate, Dropped:
		return true
	}
	return false
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// norm lowercases, folds every run of non-letter/digit characters to one
// space, and trims. It is the normalisation the fallback key uses, so it is
// deliberately simple and stable: changing it re-keys every fallback posting.
func norm(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
			continue
		}
		space = true
	}
	return b.String()
}
