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
	// Jibe is an iCIMS Jibe careers site (AMD).
	Jibe ATS = "jb"
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
func norm(s string) string { return fold(s, false) }

// normTitle is norm for job titles, which keeps the punctuation that names a
// language: "++" right after a word (C++, G++), "#" right after a one-letter
// word (C#, F#, J#) and a "." that begins a word before a letter (.NET).
// Without it "Senior C++ Engineer" and "Senior C# Engineer" fold to one title
// and dedupe as one role. Every other character folds as in norm: a single "+"
// is a seniority mark ("Staff+") or a joiner ("Data+AI"), and "Sr. Engineer"
// still matches "Sr Engineer". Titles that hold none of these keep the key
// they had before.
func normTitle(s string) string { return fold(s, true) }

func fold(s string, tech bool) string {
	rs := []rune(strings.ToLower(s))
	isAlnum := func(j int) bool { return j >= 0 && j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j])) }
	var b strings.Builder
	space := false
	word := 0 // runes in the word being written
	put := func(r rune) {
		if space && b.Len() > 0 {
			b.WriteByte(' ')
			word = 0
		}
		b.WriteRune(r)
		word++
		space = false
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			put(r)
		case tech && r == '+' && isAlnum(i-1) && i+1 < len(rs) && rs[i+1] == '+':
			b.WriteString("++")
			i++
			for i+1 < len(rs) && rs[i+1] == '+' {
				i++ // "c+++" is "c++"
			}
		case tech && r == '#' && isAlnum(i-1) && word == 1:
			b.WriteByte('#')
		case tech && r == '.' && !isAlnum(i-1) && i+1 < len(rs) && unicode.IsLetter(rs[i+1]):
			put(r)
		default:
			space = true
		}
	}
	return b.String()
}
