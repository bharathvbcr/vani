package resumestore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxVersions bounds the versions kept per job; Save prunes the oldest that
	// is neither current nor used.
	MaxVersions = 20
	// MaxJobs bounds the job folders; Save prunes the least recently saved that
	// has no used version, and refuses (ErrFull) when every one has.
	MaxJobs = 200
	// MaxMetaBytes bounds one vN.json, written and read.
	MaxMetaBytes = 256 << 10
	// MaxPDFBytes bounds one vN.pdf; it is rkbresume.DefaultMaxPDF.
	MaxPDFBytes = 512 << 10
	// MaxSelectionBytes bounds the stored choice.
	MaxSelectionBytes = 64 << 10
	// MaxKeyBytes bounds a posting key (marks.MaxKeyBytes).
	MaxKeyBytes = 512
	// MaxVersion is the highest version number: names are v1..v9999.
	MaxVersion = 9999

	maxListEntries = 1000 // per slice or map in a Meta
	maxItemBytes   = 512  // per string in a Meta slice
	maxReasonBytes = 2000
	maxPages       = 20
	maxTurns       = 1000
)

var (
	versionName = regexp.MustCompile(`^v[1-9][0-9]{0,3}$`)
	hex64       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hex12       = regexp.MustCompile(`^[0-9a-f]{12}$`)
	codeRe      = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
)

// CheckKey validates a posting key: non-empty, at most MaxKeyBytes, valid
// UTF-8, no control characters (NUL included). Any other character is fine:
// the job's folder is named by the key's hash, so a key can never name a path.
func CheckKey(key string) error {
	switch {
	case key == "":
		return fmt.Errorf("%w: empty key", ErrInvalid)
	case len(key) > MaxKeyBytes:
		return fmt.Errorf("%w: key is %d bytes, over %d", ErrInvalid, len(key), MaxKeyBytes)
	case !utf8.ValidString(key):
		return fmt.Errorf("%w: key is not UTF-8", ErrInvalid)
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: key holds a control character", ErrInvalid)
		}
	}
	return nil
}

func checkVersion(n int) error {
	if n < 1 || n > MaxVersion {
		return fmt.Errorf("%w: version %d is not in 1..%d", ErrInvalid, n, MaxVersion)
	}
	return nil
}

func oneOf(field, v string, allowed ...string) error {
	if !slices.Contains(allowed, v) {
		return fmt.Errorf("%w: %s %q is not one of %v", ErrInvalid, field, truncate(v, 40), allowed)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// text checks a free-text field: bounded, valid UTF-8, no control characters
// (newline and tab only where allowNL).
func text(field, s string, max int, allowNL bool) error {
	if len(s) > max {
		return fmt.Errorf("%w: %s is %d bytes, over %d", ErrInvalid, field, len(s), max)
	}
	if !utf8.ValidString(s) {
		return fmt.Errorf("%w: %s is not UTF-8", ErrInvalid, field)
	}
	for _, r := range s {
		if allowNL && (r == '\n' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %s holds a control character", ErrInvalid, field)
		}
	}
	return nil
}

// checkInstruction holds a record's instruction to what the builder produces:
// at most MaxInstructionRunes runes of valid, printable text on one line (no
// control, format or invisible character), with no space around it. Empty is no
// instruction. What reads as an email address or a phone number is the builder's
// to refuse (rkbresume); the store bounds and cleans what it keeps.
func checkInstruction(s string) error {
	switch {
	case s == "":
		return nil
	case !utf8.ValidString(s):
		return fmt.Errorf("%w: instruction is not UTF-8", ErrInvalid)
	case utf8.RuneCountInString(s) > MaxInstructionRunes:
		return fmt.Errorf("%w: instruction is %d characters, over %d", ErrInvalid, utf8.RuneCountInString(s), MaxInstructionRunes)
	case strings.TrimSpace(s) != s:
		return fmt.Errorf("%w: instruction has space around it", ErrInvalid)
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("%w: instruction holds a control, formatting or invisible character", ErrInvalid)
		}
	}
	return nil
}

func texts(field string, ss []string, allowNL bool) error {
	if len(ss) > maxListEntries {
		return fmt.Errorf("%w: %s holds %d entries, over %d", ErrInvalid, field, len(ss), maxListEntries)
	}
	for _, s := range ss {
		if err := text(field, s, maxItemBytes, allowNL); err != nil {
			return err
		}
	}
	return nil
}

func printHex(field, v string, allowEmpty bool) error {
	if v == "" && allowEmpty {
		return nil
	}
	if !hex64.MatchString(v) {
		return fmt.Errorf("%w: %s is not a sha256 hex", ErrInvalid, field)
	}
	return nil
}

func checkCost(c Cost) error {
	if math.IsNaN(c.USD) || math.IsInf(c.USD, 0) || c.USD < 0 {
		return fmt.Errorf("%w: cost is %v", ErrInvalid, c.USD)
	}
	if c.Turns < 0 || c.Turns > maxTurns {
		return fmt.Errorf("%w: %d turns", ErrInvalid, c.Turns)
	}
	return nil
}

// forbiddenSelectionKeys are the top-level keys a stored choice never carries:
// the bank, rkb's meta and header (personal data) are not part of a choice.
var forbiddenSelectionKeys = []string{"bank", "meta", "header", "basics"}

func checkSelection(sel json.RawMessage) error {
	if len(sel) == 0 {
		return fmt.Errorf("%w: no selection", ErrInvalid)
	}
	if len(sel) > MaxSelectionBytes {
		return fmt.Errorf("%w: selection is %d bytes, over %d", ErrInvalid, len(sel), MaxSelectionBytes)
	}
	if t := bytes.TrimSpace(sel); len(t) == 0 || t[0] != '{' {
		return fmt.Errorf("%w: selection is not a JSON object", ErrInvalid)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(sel, &top); err != nil {
		return fmt.Errorf("%w: selection is not valid JSON", ErrInvalid)
	}
	for _, k := range forbiddenSelectionKeys {
		if _, ok := top[k]; ok {
			return fmt.Errorf("%w: selection carries %q", ErrInvalid, k)
		}
	}
	return nil
}

// checkMeta validates everything in m except what only the store knows (its
// version number, key and schema, checked by the caller against the file it
// came from). It is the one validation Save and every read share.
func checkMeta(m Meta) error {
	if err := oneOf("source", m.Source, "tailored", "base", "edited", "revalidated"); err != nil {
		return err
	}
	if m.Parent < 0 || m.Parent > MaxVersion {
		return fmt.Errorf("%w: parent %d", ErrInvalid, m.Parent)
	}
	if err := text("reason", m.Reason, maxReasonBytes, true); err != nil {
		return err
	}
	if err := text("model", m.Model, 128, false); err != nil {
		return err
	}
	if err := checkInstruction(m.Instruction); err != nil {
		return err
	}
	if err := checkCost(m.Cost); err != nil {
		return err
	}
	c := m.Checks
	for _, e := range []error{
		oneOf("checks.eligibility", c.Eligibility, "current", "stale", "unknown"),
		oneOf("checks.lint", c.Lint, "passed", "failed", "not_run"),
		oneOf("checks.fit", c.Fit, "passed", "soft", "failed", "not_run", "could_not_run"),
		oneOf("checks.figures", c.Figures, "verified", "dropped", "not_run"),
	} {
		if e != nil {
			return e
		}
	}
	if m.Pages < 0 || m.Pages > maxPages {
		return fmt.Errorf("%w: %d pages", ErrInvalid, m.Pages)
	}
	if err := texts("notes", m.Notes, true); err != nil {
		return err
	}
	if err := texts("unverified_dropped", m.Dropped, false); err != nil {
		return err
	}
	if err := texts("jd_keywords", m.Keywords, false); err != nil {
		return err
	}
	if err := checkSelection(m.Selection); err != nil {
		return err
	}
	if len(m.Bullets) > maxListEntries {
		return fmt.Errorf("%w: %d bullets", ErrInvalid, len(m.Bullets))
	}
	for _, b := range m.Bullets {
		if err := text("bullet id", b.ID, 128, false); err != nil {
			return err
		}
		if b.ID == "" || !hex12.MatchString(b.TextHash) {
			return fmt.Errorf("%w: bullet %q has no id or a text_hash that is not 12 hex", ErrInvalid, truncate(b.ID, 40))
		}
	}
	if len(m.FitLines) > maxListEntries {
		return fmt.Errorf("%w: %d fit lines", ErrInvalid, len(m.FitLines))
	}
	for id, n := range m.FitLines {
		if err := text("fit line id", id, 128, false); err != nil {
			return err
		}
		if n < 0 || n > 100 {
			return fmt.Errorf("%w: fit line count %d", ErrInvalid, n)
		}
	}
	p := m.Prints
	for _, f := range []struct {
		name, v string
		empty   bool
	}{
		{"fingerprints.bank", p.Bank, true},
		{"fingerprints.base_selection", p.BaseSelection, true},
		{"fingerprints.selections", p.Selections, true},
		{"fingerprints.vm_report", p.VMReport, true},
		{"fingerprints.claims", p.Claims, true},
		{"fingerprints.rkb", p.RKB, true},
		{"pdf_sha256", m.PDFSHA256, true},
	} {
		if err := printHex(f.name, f.v, f.empty); err != nil {
			return err
		}
	}
	// A fit that passed without a PDF would be a pass nobody can see.
	if (c.Fit == "passed" || c.Fit == "soft") && m.PDFSHA256 == "" {
		return fmt.Errorf("%w: fit %s but there is no PDF", ErrInvalid, c.Fit)
	}
	return nil
}

func checkAttempt(a Attempt) error {
	if !codeRe.MatchString(a.Code) {
		return fmt.Errorf("%w: attempt code %q", ErrInvalid, truncate(a.Code, 40))
	}
	return checkCost(a.Cost)
}
