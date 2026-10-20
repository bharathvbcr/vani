// Package digest renders the daily Markdown digest from ledger records.
//
// Every field that came from a job board (title, company, location, stated
// pay, explanation, URL) is untrusted: it is escaped so it cannot add links,
// headings, HTML or extra lines to the digest.
//
// The digest also honours the user's Jobs marks (internal/marks), and only the
// marks: it is handed a key-to-mark map, never a note. A hidden role is left
// out and its key is not returned, so it stays pending rather than Digested;
// saved roles are listed again under Saved until the user moves on.
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

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/location"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Annotation keys come from the ledger package, the one place the pipeline
// (writer) and the digest (reader) agree on them.
const (
	maxFieldRunes = 300
	maxExplainLen = 2000
	maxSameDay    = 50
	maxSaved      = 50
)

// TriageSlots are the triage annotations shown for every role, in order.
var TriageSlots = []string{"role_fit", "sponsorship", "work_mode", "seniority"}

// Counts is the footer input: what happened in today's run.
type Counts struct {
	New     int
	Dropped int
	Dead    int
	// AwaitingTriage is every posting read from a board but not triaged yet
	// (status live), whenever it was first seen. They are not in the digest,
	// so the footer says they exist: otherwise "No new roles today" above
	// "New today: 3027" reads as nothing to look at.
	AwaitingTriage int
}

// Select returns the records that belong in a digest, in digest order:
// scored roles first, then unscored ones, each by jobinsight.Score descending
// (the one ranker the Jobs list also sorts by), then Unsure by the same order.
// Digested and every other status are excluded, and so is every role the user
// hid in Jobs. Ages are measured from the present; Render measures them from
// its date.
func Select(recs []ledger.Record, m map[posting.Key]marks.Mark) []ledger.Record {
	return selectAt(recs, m, time.Now())
}

func selectAt(recs []ledger.Record, m map[posting.Key]marks.Mark, now time.Time) []ledger.Record {
	var out []ledger.Record
	for _, r := range recs {
		if (r.Status == posting.Explained || r.Status == posting.Unsure) && m[r.Key] != marks.Hidden {
			out = append(out, r)
		}
	}
	sortDigest(out, recs, m, now)
	return out
}

// Saved returns the roles the user saved in Jobs whose posting is still
// listed (not dead, not a duplicate), whatever their pipeline status, in
// digest order.
func Saved(recs []ledger.Record, m map[posting.Key]marks.Mark) []ledger.Record {
	return savedAt(recs, m, time.Now())
}

func savedAt(recs []ledger.Record, m map[posting.Key]marks.Mark, now time.Time) []ledger.Record {
	var out []ledger.Record
	for _, r := range recs {
		if m[r.Key] == marks.Saved && r.Status != posting.Dead && r.Status != posting.Duplicate {
			out = append(out, r)
		}
	}
	sortDigest(out, recs, m, now)
	return out
}

// sortDigest orders out by jobinsight.Score, built from all (the whole ledger,
// so the first-crawl and company-marks parts see every posting) and the marks.
// The digest has no vocabulary, title signals, drop rules or embeddings, so
// those parts take their prior for every role alike. A role DevPrism did not
// score stays behind the scored ones: Render lists them under "Not ranked".
func sortDigest(out, all []ledger.Record, m map[posting.Key]marks.Mark, now time.Time) {
	c := jobinsight.NewRankContext(all, m, now)
	score := make(map[posting.Key]float64, len(out))
	for _, r := range out {
		score[r.Key] = jobinsight.Score(jobinsight.InputsFor(r, c)).Score
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if IsUnsure(a) != IsUnsure(b) {
			return !IsUnsure(a)
		}
		_, oka := Rank(a)
		_, okb := Rank(b)
		if oka != okb {
			return oka
		}
		if sa, sb := score[a.Key], score[b.Key]; sa != sb {
			return sa > sb
		}
		return a.Key < b.Key
	})
}

// IsUnsure reports whether triage could not decide this posting. The pipeline
// keeps scoring unsure postings, so after scoring their status is Scored or
// Explained and the flag lives on as the "triage.unsure" annotation.
func IsUnsure(r ledger.Record) bool { return jobinsight.IsUnsure(r) }

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

// Render builds the Markdown for date and returns the keys of the new roles it
// includes, in order, so the caller can mark them Digested after the file is
// written. m is the user's Jobs marks: hidden roles are left out and their
// keys not returned; saved roles are listed again under Saved, and that list
// adds no keys (a saved role is Digested only when it is listed as new).
func Render(recs []ledger.Record, m map[posting.Key]marks.Mark, date time.Time, c Counts) (string, []posting.Key) {
	sel := selectAt(recs, m, date)
	var b strings.Builder
	fmt.Fprintf(&b, "# Vani digest %s\n\n", date.Format("2006-01-02"))

	// Select puts scored roles before unscored ones, so splitting keeps order.
	var ranked, unranked, unsure []ledger.Record
	for _, r := range sel {
		_, ok := Rank(r)
		switch {
		case IsUnsure(r):
			unsure = append(unsure, r)
		case ok:
			ranked = append(ranked, r)
		default:
			unranked = append(unranked, r)
		}
	}
	keys := make([]posting.Key, 0, len(sel))

	if len(sel) == 0 {
		b.WriteString("No new roles today.\n\n")
	}
	if len(ranked) > 0 {
		fmt.Fprintf(&b, "## Ranked roles (%d)\n\n", len(ranked))
		for _, r := range ranked {
			writeRole(&b, r)
			keys = append(keys, r.Key)
		}
	}
	// A posting explained without a rank was never scored (scoring off, or
	// a record from before it was on). Heading it as ranked would let a
	// skipped stage pass for a ranking.
	if len(unranked) > 0 {
		fmt.Fprintf(&b, "## Not ranked (%d)\n\n", len(unranked))
		b.WriteString("DevPrism did not score these, so they are in no particular order.\n\n")
		for _, r := range unranked {
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
	if saved := savedAt(recs, m, date); len(saved) > 0 {
		fmt.Fprintf(&b, "## Saved (%d)\n\n", len(saved))
		b.WriteString("Roles you saved in Jobs and have not applied to or hidden yet.\n\n")
		for i, r := range saved {
			if i == maxSaved {
				fmt.Fprintf(&b, "- …and %d more in Jobs\n", len(saved)-maxSaved)
				break
			}
			fmt.Fprintf(&b, "- %s at %s: %s\n", Escape(r.Posting.Title, maxFieldRunes), Escape(r.Posting.Company, maxFieldRunes), linkLine(r.Posting.URL))
		}
		b.WriteString("\n")
	}

	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "New today: %d | Dropped today: %d | Dead today: %d\n", c.New, c.Dropped, c.Dead)
	if c.AwaitingTriage > 0 {
		fmt.Fprintf(&b, "\nAwaiting triage: %d (not in this digest; later runs triage them, up to advance_per_run each)\n", c.AwaitingTriage)
	}
	return b.String(), keys
}

// Where is a posting's location for the digest, escaped: the places and
// remote scope the board's location names ("El Paso, TX · Remote (US)"), or
// the board's text as written when nothing in it is recognised.
func Where(p posting.Posting) string {
	if s := location.Parse(p.Location, p.Remote).String(); s != "" {
		return Escape(s, maxFieldRunes)
	}
	if s := Escape(p.Location, maxFieldRunes); s != "" {
		return s
	}
	return "location not listed"
}

func writeRole(b *strings.Builder, r ledger.Record) {
	p := r.Posting
	fmt.Fprintf(b, "### %s at %s\n\n", Escape(p.Title, maxFieldRunes), Escape(p.Company, maxFieldRunes))

	fmt.Fprintf(b, "- Where: %s\n", Where(p))
	if pay := payLine(p.Description); pay != "" {
		fmt.Fprintf(b, "- Pay: %s\n", pay)
	}
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

// payLine is the pay a description states, one short range per currency
// ("USD 120k–160k/yr; EUR 100k–120k/yr"), or "" when it states none. It is
// read from posting text, so it is escaped like any other posting field.
func payLine(desc string) string {
	pays := jobinsight.ParsePay(desc)
	parts := make([]string, 0, len(pays))
	for _, p := range pays {
		parts = append(parts, p.Short())
	}
	return Escape(strings.Join(parts, "; "), maxFieldRunes)
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
// atomically. It returns the path and the keys of the new roles the file
// includes; m is the user's Jobs marks, as for Render.
func Write(stateDir string, date time.Time, recs []ledger.Record, m map[posting.Key]marks.Mark, c Counts) (string, []posting.Key, error) {
	if stateDir == "" {
		return "", nil, errors.New("digest: empty state dir")
	}
	md, keys := Render(recs, m, date, c)
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
			// The link is the digest's only name until this sync: without it a
			// crash can lose the file after the caller marks its roles digested.
			if err := atomicfile.SyncDir(dir); err != nil {
				return "", nil, fmt.Errorf("digest: %s written, but syncing its directory failed: %w", filepath.Base(target), err)
			}
			return target, keys, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, fmt.Errorf("digest: %w", err)
		}
	}
	return "", nil, fmt.Errorf("digest: more than %d digests for %s", maxSameDay, stem)
}
