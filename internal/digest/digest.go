// Package digest renders the daily Markdown digest from ledger records.
//
// Every field that came from a job board (title, company, location,
// explanation, URL) is untrusted: it is escaped so it cannot add links,
// headings, HTML or extra lines to the digest.
package digest

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Annotation keys come from the ledger package, the one place the pipeline
// (writer) and the digest (reader) agree on them.
const (
	maxFieldRunes = 300
	maxExplainLen = 2000
	maxSameDay    = 50
)

// TriageSlots are the triage annotations shown for every role, in order.
var TriageSlots = []string{"role_fit", "sponsorship", "work_mode", "seniority"}

// Counts is the footer input: what happened in today's run.
type Counts struct {
	New     int
	Dropped int
	Dead    int
}

// Select returns the records that belong in a digest, in digest order:
// Explained by score descending (missing score last), then Unsure by the same
// order. Digested and every other status are excluded.
func Select(recs []ledger.Record) []ledger.Record {
	var out []ledger.Record
	for _, r := range recs {
		if r.Status == posting.Explained || r.Status == posting.Unsure {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if IsUnsure(a) != IsUnsure(b) {
			return !IsUnsure(a)
		}
		sa, oka := Rank(a)
		sb, okb := Rank(b)
		if oka != okb {
			return oka
		}
		if oka && sa != sb {
			return sa > sb
		}
		return a.Key < b.Key
	})
	return out
}

// IsUnsure reports whether triage could not decide this posting. The pipeline
// keeps scoring unsure postings, so after scoring their status is Scored or
// Explained and the flag lives on as the "triage.unsure" annotation.
func IsUnsure(r ledger.Record) bool {
	return r.Status == posting.Unsure || strings.TrimSpace(r.Annotations[ledger.AnnTriagePrefix+"unsure"]) != ""
}

// Rank is the digest's sort key from the "rank" annotation; false when the
// posting is not scored.
func Rank(r ledger.Record) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(r.Annotations[ledger.AnnRank]), 64)
	if err != nil || v != v {
		return 0, false
	}
	return v, true
}

// Coverage is the must-have coverage shown to the reader. The rank may carry
// an employer-preference boost on top, so it is read from its own key;
// records written before the keys diverged have only the rank.
func Coverage(r ledger.Record) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(r.Annotations[ledger.AnnCoverage]), 64)
	if err != nil || v != v {
		return Rank(r)
	}
	return v, true
}

// Render builds the Markdown for date and returns the keys it includes, in
// order, so the caller can mark them Digested after the file is written.
func Render(recs []ledger.Record, date time.Time, c Counts) (string, []posting.Key) {
	sel := Select(recs)
	var b strings.Builder
	fmt.Fprintf(&b, "# Vani digest %s\n\n", date.Format("2006-01-02"))

	var explained, unsure []ledger.Record
	for _, r := range sel {
		if !IsUnsure(r) {
			explained = append(explained, r)
		} else {
			unsure = append(unsure, r)
		}
	}
	keys := make([]posting.Key, 0, len(sel))

	if len(sel) == 0 {
		b.WriteString("No new roles today.\n\n")
	}
	if len(explained) > 0 {
		fmt.Fprintf(&b, "## Ranked roles (%d)\n\n", len(explained))
		for _, r := range explained {
			writeRole(&b, r)
			keys = append(keys, r.Key)
		}
	}
	if len(unsure) > 0 {
		fmt.Fprintf(&b, "## Unsure (%d)\n\n", len(unsure))
		b.WriteString("Triage could not decide these; they are kept for you to judge.\n\n")
		for _, r := range unsure {
			writeRole(&b, r)
			keys = append(keys, r.Key)
		}
	}

	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "New today: %d | Dropped today: %d | Dead today: %d\n", c.New, c.Dropped, c.Dead)
	return b.String(), keys
}

func writeRole(b *strings.Builder, r ledger.Record) {
	p := r.Posting
	fmt.Fprintf(b, "### %s at %s\n\n", Escape(p.Title, maxFieldRunes), Escape(p.Company, maxFieldRunes))

	where := Escape(p.Location, maxFieldRunes)
	switch {
	case p.Remote && where != "":
		where += " (remote)"
	case p.Remote:
		where = "Remote"
	case where == "":
		where = "location not listed"
	}
	fmt.Fprintf(b, "- Where: %s\n", where)
	fmt.Fprintf(b, "- Link: %s\n", linkLine(p.URL))

	if cov, ok := Coverage(r); ok {
		fmt.Fprintf(b, "- Must-have coverage: %s%%\n", strconv.FormatFloat(cov, 'f', 0, 64))
	} else {
		b.WriteString("- Must-have coverage: not scored\n")
	}
	for _, slot := range TriageSlots {
		if v := strings.TrimSpace(r.Annotations["triage."+slot]); v != "" {
			fmt.Fprintf(b, "- %s: %s\n", slot, Escape(v, 80))
		}
	}
	if v := strings.TrimSpace(r.Annotations[ledger.AnnLCA]); v != "" {
		fmt.Fprintf(b, "- Sponsorship history: %s\n", Escape(v, 120))
	}
	if v := strings.TrimSpace(r.Annotations[ledger.AnnEmployer]); v != "" {
		line := Escape(v, 200)
		if rank, ok := Rank(r); ok {
			if cov, ok := Coverage(r); ok && rank > cov {
				line += " (ranked up: the active track prefers this kind of employer)"
			}
		}
		fmt.Fprintf(b, "- Employer: %s\n", line)
	}
	if IsUnsure(r) {
		fmt.Fprintf(b, "- Why unsure: %s\n", WhyUnsure(r.Annotations))
	}
	if ex := strings.TrimSpace(r.Annotations[ledger.AnnExplanation]); ex != "" {
		fmt.Fprintf(b, "\n%s\n", Escape(ex, maxExplainLen))
	}
	b.WriteString("\n")
}

// WhyUnsure names the triage slots that could not decide, from a record's
// annotations.
func WhyUnsure(ann map[string]string) string {
	var parts []string
	var slots []string
	for k := range ann {
		if s, ok := strings.CutPrefix(k, "triage."); ok {
			if s, ok := strings.CutSuffix(s, ".reading"); ok && s != "" {
				slots = append(slots, s)
			}
		}
	}
	sort.Strings(slots)
	for _, s := range slots {
		parts = append(parts, Escape(s, 40)+" read as "+Escape(ann["triage."+s+".reading"], 80))
	}
	if len(parts) == 0 {
		return "no triage reading recorded"
	}
	return strings.Join(parts, "; ")
}

// linkLine renders an http(s) URL as an autolink and refuses anything else, so
// a javascript: or data: URL from a board never becomes clickable.
func linkLine(raw string) string {
	u, ok := HTTPURL(raw)
	if !ok {
		return "(no valid link)"
	}
	return "<" + u + ">"
}

// HTTPURL returns raw trimmed when it is an absolute http(s) URL with a host
// and no whitespace, control characters or angle brackets; anything else (a
// javascript: or data: URL from a board) is refused. Every surface that makes
// a posting's link clickable goes through it.
func HTTPURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		strings.ContainsFunc(raw, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == '<' || r == '>' }) {
		return "", false
	}
	return raw, true
}

// Escape makes untrusted text inert in Markdown: it flattens whitespace and
// control characters to single spaces, truncates to maxRunes, and
// backslash-escapes every Markdown and HTML metacharacter.
func Escape(s string, maxRunes int) string {
	var b strings.Builder
	n := 0
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			space = b.Len() > 0
			continue
		}
		if n >= maxRunes {
			b.WriteString("…")
			return b.String()
		}
		if space {
			b.WriteByte(' ')
			n++
			space = false
		}
		if strings.ContainsRune("\\`*_{}[]()<>#+-.!|~&:=\"'$%^", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// Write renders the digest and writes stateDir/digests/YYYY-MM-DD.md
// atomically. It returns the path and the keys the file includes.
func Write(stateDir string, date time.Time, recs []ledger.Record, c Counts) (string, []posting.Key, error) {
	if stateDir == "" {
		return "", nil, errors.New("digest: empty state dir")
	}
	md, keys := Render(recs, date, c)
	dir := filepath.Join(stateDir, "digests")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("digest: %w", err)
	}
	stem := date.Format("2006-01-02")
	path := filepath.Join(dir, stem+".md")
	if _, err := os.Lstat(path); err == nil && len(keys) == 0 {
		// Nothing new, and today's digest already exists: leave it alone.
		return path, nil, nil
	}
	tmp, err := os.CreateTemp(dir, ".digest-*.tmp")
	if err != nil {
		return "", nil, fmt.Errorf("digest: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(md); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return "", nil, fmt.Errorf("digest: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return "", nil, fmt.Errorf("digest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return "", nil, fmt.Errorf("digest: %w", err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		_ = os.Remove(name)
		return "", nil, fmt.Errorf("digest: %w", err)
	}
	// Link (not rename) so an existing digest is never overwritten: a second
	// run on the same day, whose earlier roles are already Digested, gets
	// YYYY-MM-DD-2.md instead of erasing the first file.
	defer os.Remove(name)
	for n := 1; n <= maxSameDay; n++ {
		target := path
		if n > 1 {
			target = filepath.Join(dir, fmt.Sprintf("%s-%d.md", stem, n))
		}
		err := os.Link(name, target)
		if err == nil {
			return target, keys, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, fmt.Errorf("digest: %w", err)
		}
	}
	return "", nil, fmt.Errorf("digest: more than %d digests for %s", maxSameDay, stem)
}
