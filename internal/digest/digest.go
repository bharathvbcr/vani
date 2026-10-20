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
	"crypto/sha256"
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
	return selectAt(recs, m, time.Now(), Options{})
}

// Options carry what the digest cannot compute itself.
type Options struct {
	// Semantic returns a role's calibrated 0-1 similarity to the knowledge
	// base, or false when none is cached for it (the ranker then takes its
	// prior: unknown is never scored as 0). Nil means no embeddings.
	Semantic func(r ledger.Record) (float64, bool)
	// Rules demote a kept role whose triage reading matches one (drop rules
	// it passed under their threshold, and rank penalties); TitleRank is the
	// title signals' Rank, nil for none. The same as `vani jobs` applies.
	Rules     []jobinsight.DropRule
	TitleRank func(title string) int
	// AgeNeutral is jobinsight.RankContext.AgeNeutral: employers whose undated
	// postings are not aged.
	AgeNeutral map[string]bool
	// KBMatch is jobinsight.RankContext.KBMatch: the language model's read of a
	// role's work, where it was asked.
	KBMatch func(r ledger.Record) (jobinsight.Slot, bool)
	// Weights is jobinsight.RankContext.Weights: the fitted rank weights, nil
	// for the hand weights.
	Weights *jobinsight.Weights

	// TopOpen lists this many of the best roles already digested and still
	// open, by Relevance (fit alone, age left out), under "Top open": the
	// "what is the best open role" answer beside the new roles' "what is new".
	// Zero lists none. At most MaxTopOpen.
	TopOpen int
	// ExploreBelowFold and ExploreDropped list this many roles picked at
	// random (seeded by the date) from digested roles the owner has not marked
	// and from dropped roles, under "Explore", so their judgement reaches the
	// postings the ranking and the drop rules would never show. Zero lists
	// none. Each at most MaxExplore.
	ExploreBelowFold, ExploreDropped int
}

// Bounds on Options' extra sections.
const (
	MaxTopOpen = 50
	MaxExplore = 10
)

// Shown is one role a rendered digest lists, in reading order: the record
// impressions are made of. Position counts from 1 over the whole digest.
type Shown struct {
	Key      posting.Key
	Section  string
	Position int
	Status   posting.Status
	// DropClass is DropClass of a dropped role listed for exploration.
	DropClass string
	Result    jobinsight.Result
}

// Section names Shown carries; they match the feedback package's.
const (
	SectionRanked   = "ranked"
	SectionUnranked = "not_ranked"
	SectionUnsure   = "unsure"
	SectionSaved    = "saved"
	SectionTopOpen  = "top_open"
	SectionExplore  = "explore"
)

// scorer scores records with one RankContext, built once from the whole ledger
// and the marks, and remembers each result.
type scorer struct {
	c    jobinsight.RankContext
	memo map[posting.Key]jobinsight.Result
}

func newScorer(all []ledger.Record, m map[posting.Key]marks.Mark, now time.Time, o Options) *scorer {
	c := jobinsight.NewRankContext(all, m, now)
	c.Semantic, c.DropRules, c.TitleRank, c.AgeNeutral, c.KBMatch = o.Semantic, o.Rules, o.TitleRank, o.AgeNeutral, o.KBMatch
	c.Weights = o.Weights
	return &scorer{c: c, memo: map[posting.Key]jobinsight.Result{}}
}

func (s *scorer) score(r ledger.Record) jobinsight.Result {
	if res, ok := s.memo[r.Key]; ok {
		return res
	}
	res := s.c.ScoreOf(r)
	s.memo[r.Key] = res
	return res
}

func selectAt(recs []ledger.Record, m map[posting.Key]marks.Mark, now time.Time, o Options) []ledger.Record {
	return selectWith(recs, m, newScorer(recs, m, now, o))
}

func selectWith(recs []ledger.Record, m map[posting.Key]marks.Mark, sc *scorer) []ledger.Record {
	var out []ledger.Record
	for _, r := range recs {
		if (r.Status == posting.Explained || r.Status == posting.Unsure) && m[r.Key] != marks.Hidden {
			out = append(out, r)
		}
	}
	sortDigest(out, sc)
	return out
}

// topOpen is up to n roles already digested, still listed and not marked
// (saved roles have their own section; hidden and applied ones are done), by
// Relevance, then Score, then key.
func topOpen(recs []ledger.Record, m map[posting.Key]marks.Mark, sc *scorer, n int) []ledger.Record {
	if n <= 0 {
		return nil
	}
	var out []ledger.Record
	for _, r := range recs {
		if r.Status == posting.Digested && m[r.Key] == marks.None {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := sc.score(out[i]), sc.score(out[j])
		if a.Relevance != b.Relevance {
			return a.Relevance > b.Relevance
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return out[i].Key < out[j].Key
	})
	return out[:min(n, len(out))]
}

// explore picks up to belowFold digested roles and up to dropped dropped roles,
// all unmarked (a marked role is already judged, and a saved one is listed
// under Saved) and none in skip, each pool ordered by a hash of the date and
// the key: random to the ranking, the same for a given day.
func explore(recs []ledger.Record, m map[posting.Key]marks.Mark, date time.Time, skip map[posting.Key]bool, belowFold, dropped int) []ledger.Record {
	var fold, drop []ledger.Record
	for _, r := range recs {
		switch {
		case r.Status == posting.Digested && m[r.Key] == marks.None && !skip[r.Key]:
			fold = append(fold, r)
		case r.Status == posting.Dropped && m[r.Key] == marks.None:
			drop = append(drop, r)
		}
	}
	day := date.Format("2006-01-02")
	pick := func(pool []ledger.Record, n int) []ledger.Record {
		if n <= 0 {
			return nil
		}
		h := make(map[posting.Key]string, len(pool))
		for _, r := range pool {
			sum := sha256.Sum256([]byte(day + "\x00" + string(r.Key)))
			h[r.Key] = string(sum[:])
		}
		sort.Slice(pool, func(i, j int) bool { return h[pool[i].Key] < h[pool[j].Key] })
		return pool[:min(n, len(pool))]
	}
	return append(pick(fold, belowFold), pick(drop, dropped)...)
}

// DropClass names why a dropped record was dropped, from its triage
// annotations (the folded record keeps no event reason): the backend that read
// it ("title" for a title reading) and the slot of the first drop rule its
// readings match, as "title:role_fit". It never names the rule's value: rule
// values can reveal the owner's work authorization (rankconfig.go), and the
// class reaches the digest and the feedback log. "" for a record that is not
// dropped.
func DropClass(r ledger.Record, rules []jobinsight.DropRule) string {
	if r.Status != posting.Dropped {
		return ""
	}
	backend := token(r.Annotations[ledger.AnnTriagePrefix+"backend"])
	if backend == "" {
		backend = "unknown"
	}
	for _, rule := range rules {
		v := strings.TrimSpace(r.Annotations[ledger.AnnTriagePrefix+rule.Slot])
		for _, want := range rule.Values {
			if v != "" && v == want {
				if b := token(r.Annotations[ledger.AnnTriagePrefix+rule.Slot+".backend"]); b != "" {
					backend = b
				}
				return backend + ":" + token(rule.Slot)
			}
		}
	}
	return backend + ":other"
}

// token keeps the characters of s that are safe in a class name, at most 40.
func token(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if b.Len() >= 40 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' || r == '/' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Saved returns the roles the user saved in Jobs whose posting is still
// listed (not dead, not a duplicate), whatever their pipeline status, in
// digest order.
func Saved(recs []ledger.Record, m map[posting.Key]marks.Mark) []ledger.Record {
	return savedAt(recs, m, time.Now(), Options{})
}

func savedAt(recs []ledger.Record, m map[posting.Key]marks.Mark, now time.Time, o Options) []ledger.Record {
	return savedWith(recs, m, newScorer(recs, m, now, o))
}

func savedWith(recs []ledger.Record, m map[posting.Key]marks.Mark, sc *scorer) []ledger.Record {
	var out []ledger.Record
	for _, r := range recs {
		if m[r.Key] == marks.Saved && r.Status != posting.Dead && r.Status != posting.Duplicate {
			out = append(out, r)
		}
	}
	sortDigest(out, sc)
	return out
}

// sortDigest orders out by jobinsight.Score, built from the whole ledger (so
// the first-crawl and company-marks parts see every posting) and the marks.
// The digest has no keyword vocabulary, so that part takes its prior for every
// role alike; the embedding part, rules and title signals come from Options, as
// `vani jobs` applies them. A role DevPrism did not score stays behind the scored
// ones: Render lists them under "Not ranked".
func sortDigest(out []ledger.Record, sc *scorer) {
	score := make(map[posting.Key]float64, len(out))
	for _, r := range out {
		score[r.Key] = sc.score(r).Score
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
	return RenderWith(recs, m, date, c, Options{})
}

// RenderWith is Render with o's ranking inputs.
func RenderWith(recs []ledger.Record, m map[posting.Key]marks.Mark, date time.Time, c Counts, o Options) (string, []posting.Key) {
	md, keys, _ := RenderShown(recs, m, date, c, o)
	return md, keys
}

// RenderShown is RenderWith that also returns every role the digest lists, in
// reading order, with its score: what impressions are made of. keys are only
// the new roles (the caller marks them Digested); Top open, Explore and Saved
// roles are listed without changing their status.
func RenderShown(recs []ledger.Record, m map[posting.Key]marks.Mark, date time.Time, c Counts, o Options) (string, []posting.Key, []Shown) {
	sc := newScorer(recs, m, date, o)
	sel := selectWith(recs, m, sc)
	var shown []Shown
	show := func(r ledger.Record, section string) {
		shown = append(shown, Shown{Key: r.Key, Section: section, Position: len(shown) + 1, Status: r.Status,
			DropClass: DropClass(r, o.Rules), Result: sc.score(r)})
	}
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
			show(r, SectionRanked)
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
			show(r, SectionUnranked)
		}
	}
	if len(unsure) > 0 {
		fmt.Fprintf(&b, "## Unsure (%d)\n\n", len(unsure))
		b.WriteString("Triage could not decide these; they are kept for you to judge.\n\n")
		for _, r := range unsure {
			writeRole(&b, r)
			keys = append(keys, r.Key)
			show(r, SectionUnsure)
		}
	}
	top := topOpen(recs, m, sc, min(o.TopOpen, MaxTopOpen))
	if len(top) > 0 {
		fmt.Fprintf(&b, "## Top open (%d)\n\n", len(top))
		b.WriteString("The best roles listed before and still open, by fit alone: age is left out, so a strong role does not sink because it is a few weeks old.\n\n")
		for _, r := range top {
			writeBrief(&b, r, sc.score(r), "")
			show(r, SectionTopOpen)
		}
		b.WriteString("\n")
	}
	skip := make(map[posting.Key]bool, len(top))
	for _, r := range top {
		skip[r.Key] = true
	}
	if ex := explore(recs, m, date, skip, min(o.ExploreBelowFold, MaxExplore), min(o.ExploreDropped, MaxExplore)); len(ex) > 0 {
		fmt.Fprintf(&b, "## Explore (%d)\n\n", len(ex))
		b.WriteString("Picked at random from roles ranked lower and roles that were dropped, so Vani can learn what it misses. Judge them with `vani audit judge <key> yes|maybe|no`.\n\n")
		for _, r := range ex {
			writeBrief(&b, r, sc.score(r), DropClass(r, o.Rules))
			show(r, SectionExplore)
		}
		b.WriteString("\n")
	}
	if saved := savedWith(recs, m, sc); len(saved) > 0 {
		fmt.Fprintf(&b, "## Saved (%d)\n\n", len(saved))
		b.WriteString("Roles you saved in Jobs and have not applied to or hidden yet.\n\n")
		for i, r := range saved {
			if i == maxSaved {
				fmt.Fprintf(&b, "- …and %d more in Jobs\n", len(saved)-maxSaved)
				break
			}
			fmt.Fprintf(&b, "- %s at %s: %s\n", Escape(r.Posting.Title, maxFieldRunes), Escape(r.Posting.Company, maxFieldRunes), linkLine(r.Posting.URL))
			show(r, SectionSaved)
		}
		b.WriteString("\n")
	}

	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "New today: %d | Dropped today: %d | Dead today: %d\n", c.New, c.Dropped, c.Dead)
	if c.AwaitingTriage > 0 {
		fmt.Fprintf(&b, "\nAwaiting triage: %d (not in this digest; later runs triage them, up to advance_per_run each)\n", c.AwaitingTriage)
	}
	return b.String(), keys, shown
}

// writeBrief lists a role that is not new today: title, company, where, link,
// its relevance, its key (for `vani audit`) and, for a dropped role, why.
func writeBrief(b *strings.Builder, r ledger.Record, res jobinsight.Result, dropClass string) {
	p := r.Posting
	fmt.Fprintf(b, "- **%s** at %s (%s): %s\n", Escape(p.Title, maxFieldRunes), Escape(p.Company, maxFieldRunes), Where(p), linkLine(p.URL))
	fmt.Fprintf(b, "  - Relevance %s · priority %s · key `%s`\n", strconv.FormatFloat(res.Relevance, 'f', 0, 64),
		strconv.FormatFloat(res.Score, 'f', 1, 64), codeSpan(string(r.Key)))
	if dropClass != "" {
		fmt.Fprintf(b, "  - Dropped: %s\n", Escape(dropClass, 80))
	}
}

// codeSpan makes s safe inside a Markdown code span, where nothing but a
// backtick is special: it drops backticks, whitespace and control characters
// and keeps at most maxFieldRunes, so the key reads back exactly as stored.
func codeSpan(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if r == '`' || unicode.IsSpace(r) || unicode.IsControl(r) {
			continue
		}
		if n == maxFieldRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
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
	return WriteWith(stateDir, date, recs, m, c, Options{})
}

// WriteWith is Write with o's ranking inputs.
func WriteWith(stateDir string, date time.Time, recs []ledger.Record, m map[posting.Key]marks.Mark, c Counts, o Options) (string, []posting.Key, error) {
	path, keys, _, err := WriteShown(stateDir, date, recs, m, c, o)
	return path, keys, err
}

// WriteShown is WriteWith that also returns the roles the written file lists
// (RenderShown). When no file is written (nothing new, and today's digest
// exists) shown is nil: nothing new was put in front of the owner.
func WriteShown(stateDir string, date time.Time, recs []ledger.Record, m map[posting.Key]marks.Mark, c Counts, o Options) (string, []posting.Key, []Shown, error) {
	if stateDir == "" {
		return "", nil, nil, errors.New("digest: empty state dir")
	}
	md, keys, shown := RenderShown(recs, m, date, c, o)
	dir := filepath.Join(stateDir, "digests")
	stem := date.Format("2006-01-02")
	if path := filepath.Join(dir, stem+".md"); len(keys) == 0 {
		if _, err := os.Lstat(path); err == nil {
			// Nothing new, and today's digest already exists: leave it alone.
			return path, nil, nil, nil
		}
	}
	path, keys, err := writeFile(dir, stem, md, keys)
	if err != nil {
		return "", nil, nil, err
	}
	return path, keys, shown, nil
}

func writeFile(dir, stem, md string, keys []posting.Key) (string, []posting.Key, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("digest: %w", err)
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
		target := filepath.Join(dir, stem+".md")
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
