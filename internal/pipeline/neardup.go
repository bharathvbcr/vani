package pipeline

import (
	"cmp"
	"context"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/location"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
	"github.com/bharathvbcr/Vani/internal/textsim"
)

// Near-duplicate detection. The same role reaches the ledger more than once
// under keys no exact rule joins: a re-post with a new id, an agency and the
// employer, a title with and without "(Remote)". AnnotateNearDuplicates finds
// them by what the posting says (company, title, location, description) and
// either marks the later one a duplicate or leaves an annotation that says how
// close two postings are.
//
// It is an engine rule keyed on measured text overlap, not a model answer
// (CLAUDE.md Rule 7): only a near-total overlap at the same place demotes a
// posting; everything less is an annotation, and an uncertain pair keeps both.

const (
	// Annotation keys. Only the later of two postings carries them.
	AnnDupCandidateOf = "dup.candidate_of" // key of a posting this one may repeat
	AnnDupSiblingOf   = "dup.sibling_of"   // key of the same role listed for another place
	AnnDupScore       = "dup.score"        // shingle overlap with that posting, two decimals
	AnnDupMethod      = "dup.method"       // on a Duplicate: how it was found
	AnnDupCosine      = "dup.cosine"       // the optional similarity's reading of a candidate pair

	// DupMethodShingle is AnnDupMethod's value for this stage.
	DupMethodShingle = "shingle"

	// NearDupTitleMin is the token overlap two loose titles need to be compared.
	NearDupTitleMin = 0.8
	// NearDupDuplicate is the description overlap, at the same place, that
	// makes the later posting a Duplicate (or, at another place, a sibling).
	NearDupDuplicate = 0.90
	// NearDupCandidate is the least description overlap worth an annotation.
	NearDupCandidate = 0.60
	// NearDupShingleWords is the shingle length.
	NearDupShingleWords = 5
	// NearDupMinWords is how many words a description needs to be compared at
	// all: two empty or boilerplate stubs are not "the same text".
	NearDupMinWords = 30
	// NearDupMaxCandidates bounds the postings one posting is scored against.
	NearDupMaxCandidates = 50
	// NearDupDeadline bounds the whole stage.
	NearDupDeadline = 2 * time.Second
	// NearDupSimilarityShare is the share of the stage's deadline the optional
	// similarity may spend, all its readings together: the shingle pass always
	// keeps the rest.
	NearDupSimilarityShare = 0.5
	// NearDupSimilarityBreaker is how many similarity errors in a row stop the
	// readings for the rest of the stage.
	NearDupSimilarityBreaker = 3

	maxLooseTitleBytes   = 512
	maxLooseCompanyBytes = 256
)

// NearDupOptions configures AnnotateNearDuplicates; the zero value is valid.
type NearDupOptions struct {
	// Protected names postings that must never be demoted (the user's marks).
	// A protected posting that would be a duplicate is annotated as a candidate.
	Protected func(posting.Key) bool
	// Similarity is an optional second opinion on a candidate pair (an embedding
	// cosine over the two descriptions). It only annotates (AnnDupCosine): no
	// status depends on it. Nil means not configured, and the report says so. An
	// error or ok=false leaves the pair without a reading and is counted.
	//
	// Its readings have their own budget, NearDupSimilarityShare of the
	// deadline, which each call's ctx carries. Once the budget is spent, or
	// NearDupSimilarityBreaker errors come in a row, the remaining pairs are not
	// read (SimilaritySkipped) and the shingle pass goes on. A Similarity that
	// ignores ctx can still outrun the stage's deadline.
	Similarity func(ctx context.Context, a, b string) (cos float64, ok bool, err error)
	// Deadline bounds the stage; zero is NearDupDeadline, and a negative value
	// is an already-expired deadline (for tests).
	Deadline time.Duration
}

// NearDupReport says what one stage did, and how much of the ledger it read.
type NearDupReport struct {
	Records  int // postings the stage could compare (not terminal, with a company and a title)
	Examined int // of those, how many it reached before it stopped
	Compared int // pairs scored
	Skipped  int // pairs with a description under NearDupMinWords: not scored
	// Duplicates were marked Duplicate; Siblings and Candidates carry the
	// annotation after the run (new or not).
	Duplicates, Siblings, Candidates int
	Written                          int // ledger events appended; 0 when nothing changed
	// CandidatesCapped counts postings with more title matches than
	// NearDupMaxCandidates: only the closest were scored.
	CandidatesCapped int
	// Partial means the deadline cut the stage short: the postings after
	// Examined were not looked at and keep whatever annotations they had.
	Partial bool
	// PartialInSimilarity means the deadline that cut the stage ran out during
	// a similarity reading; otherwise a Partial cut came in the shingle pass.
	PartialInSimilarity bool
	// SimilarityConfigured is false when no Similarity was supplied.
	SimilarityConfigured bool
	// SimilarityErrors counts readings that failed or had no data.
	SimilarityErrors int
	// SimilaritySkipped counts candidate pairs left without a reading because
	// the similarity's budget ran out (SimilarityBudgetSpent) or its breaker
	// opened (SimilarityBreakerOpen); a reading the budget cut off mid-way is
	// one of them. Their statuses and shingle annotations are decided as usual.
	SimilaritySkipped     int
	SimilarityBudgetSpent bool
	SimilarityBreakerOpen bool
}

// legalSuffixes are dropped from the end of a company name.
var legalSuffixes = map[string]bool{
	"inc": true, "llc": true, "ltd": true, "corp": true, "co": true, "gmbh": true, "plc": true,
	"corporation": true, "incorporated": true,
}

var (
	bracketed   = regexp.MustCompile(`\([^)]*\)|\[[^\]]*\]|\{[^}]*\}`)
	modeTail    = regexp.MustCompile(`\s[-–—|]\s*(remote|hybrid|on-?site)\s*$`)
	modeBracket = regexp.MustCompile(`[(\[{]\s*(remote|hybrid|on-?site)\s*[)\]}]`)
	// A requisition id has a letter prefix ("R-20431") or five or more digits; a
	// bare four-digit number is usually a year ("Intern (Summer 2027)") and stays.
	reqID = regexp.MustCompile(`\b(?:[a-z]{1,3}-?\d{4,}|\d{5,})\b`)
)

// looseCompany is a company name without punctuation and legal suffixes.
func looseCompany(s string) string {
	ws := textsim.Words(truncateBytes(s, maxLooseCompanyBytes), maxLooseCompanyBytes)
	for len(ws) > 1 && legalSuffixes[ws[len(ws)-1]] {
		ws = ws[:len(ws)-1]
	}
	return strings.Join(ws, " ")
}

// looseTitleWords are the words of a title that name the role: without
// bracketed segments, a trailing work-mode tag and requisition numbers.
func looseTitleWords(s string) []string {
	s = strings.ToLower(truncateBytes(s, maxLooseTitleBytes))
	s = bracketed.ReplaceAllString(s, " ")
	s = modeTail.ReplaceAllString(s+" ", " ")
	s = reqID.ReplaceAllString(s, " ")
	return textsim.Words(s, maxLooseTitleBytes)
}

// roleTitle is a title reduced only by what never changes the job: case,
// punctuation, a work-mode tag ("(Remote)", " - Hybrid") and requisition
// numbers. Seniority words and other bracketed qualifiers stay, so two titles
// are equal here only when they name the same role.
func roleTitle(s string) string {
	s = strings.ToLower(truncateBytes(s, maxLooseTitleBytes))
	s = modeBracket.ReplaceAllString(s, " ")
	s = modeTail.ReplaceAllString(s+" ", " ")
	s = reqID.ReplaceAllString(s, " ")
	return strings.Join(textsim.Words(s, maxLooseTitleBytes), " ")
}

// looseLocation is a posting's places folded the way internal/location
// compares them, marked "remote" when the role is remote; text that names no
// place compares as written. A remote role keeps its places ("Remote - Texas"
// is not "Remote - Georgia"). Only equality is meaningful.
func looseLocation(p posting.Posting) string {
	loc := location.Parse(p.Location, p.Remote)
	parts := make([]string, 0, len(loc.Places))
	for _, pl := range loc.Places {
		parts = append(parts, location.NormArea(pl.Area)+"|"+pl.State+"|"+location.NormArea(pl.Region))
	}
	var out string
	if len(parts) == 0 {
		out = "text:" + location.NormArea(p.Location)
	} else {
		slices.Sort(parts)
		out = strings.Join(slices.Compact(parts), ";")
	}
	if loc.Remote {
		out = "remote:" + out
	}
	return out
}

func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

type dupRec struct {
	rec        ledger.Record
	company    string
	title      textsim.Set
	role       string // roleTitle: equal only for the same role, seniority and bracketed qualifier
	loc        string
	desc       string // the board's text (srcutil.SourceText), for the optional similarity
	shingle    textsim.Set
	words      int
	once       sync.Once
	cands      []dupMatch
	capped     bool
	candsReady bool
}

// text computes the description side of a record once.
func (d *dupRec) text() {
	d.once.Do(func() {
		d.desc = srcutil.SourceText(d.rec.Posting.Description)
		ws := textsim.Words(d.desc, textsim.DefaultMaxBytes)
		d.words = len(ws)
		if d.words >= NearDupMinWords {
			d.shingle = textsim.Shingles(ws, NearDupShingleWords)
		}
	})
}

type titleToken struct {
	company string
	hash    uint64
}

type dupMatch struct {
	j     int
	score float64
}

func better(a dupMatch, have dupMatch, haveOK bool) bool {
	return !haveOK || a.score > have.score || a.score == have.score && a.j < have.j
}

// AnnotateNearDuplicates compares the postings that have not left the
// pipeline and records what it finds, earliest first seen first:
//
//   - a later posting whose company (loosely) and title (loosely, token overlap
//     of at least NearDupTitleMin) match an earlier one and whose descriptions
//     overlap by NearDupDuplicate or more, with the same role (roleTitle) at the
//     same place, becomes a Duplicate of the earlier one;
//   - the same role at another place is that role's sibling (AnnDupSiblingOf),
//     never a duplicate: one requisition posted per city is several jobs;
//   - any other overlap of NearDupCandidate or more (a different seniority or
//     bracketed qualifier on one templated JD included) is a candidate
//     (AnnDupCandidateOf and AnnDupScore), kept as it is.
//
// Descriptions under NearDupMinWords are not compared. A posting the user
// protected is never demoted. Annotations are recomputed each run and only the
// differences are written, so a rerun over an unchanged ledger writes nothing.
// The comparing is bounded by NearDupDeadline; the events it decides are then
// written, bounded by ctx alone (each is an fsync, which says nothing about how
// much comparing is left). A deadline in the comparing is reported (Partial):
// the newest postings are decided first, and those not reached keep what they
// had. A cancelled ctx is an error.
func AnnotateNearDuplicates(ctx context.Context, led ledger.Ledger, now time.Time, opt NearDupOptions) (NearDupReport, error) {
	rep := NearDupReport{SimilarityConfigured: opt.Similarity != nil}
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	deadline := opt.Deadline
	if deadline == 0 {
		deadline = NearDupDeadline
	}
	sctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	var raw []ledger.Record
	for _, r := range led.All() {
		if r.Status.Terminal() || r.Posting.Company == "" || r.Posting.Title == "" {
			continue
		}
		raw = append(raw, r)
	}
	recs := make([]*dupRec, len(raw))
	buildRec := func(i int) {
		r := raw[i]
		recs[i] = &dupRec{
			rec: r, company: looseCompany(r.Posting.Company),
			title: textsim.TokenSet(looseTitleWords(r.Posting.Title)),
			role:  roleTitle(r.Posting.Title), loc: looseLocation(r.Posting),
		}
	}
	if workers := min(runtime.GOMAXPROCS(0), 8, len(raw)); workers <= 1 || len(raw) < 32 {
		for i := range raw {
			buildRec(i)
		}
	} else {
		var next atomic.Int64
		var wg sync.WaitGroup
		wg.Add(workers)
		for range workers {
			go func() {
				defer wg.Done()
				for {
					i := int(next.Add(1) - 1)
					if i >= len(raw) {
						return
					}
					buildRec(i)
				}
			}()
		}
		wg.Wait()
	}
	slices.SortFunc(recs, func(a, b *dupRec) int {
		return cmp.Or(a.rec.FirstSeen.Compare(b.rec.FirstSeen), cmp.Compare(a.rec.Key, b.rec.Key))
	})
	rep.Records = len(recs)

	// The title index holds every posting; titleMatches keeps the earlier ones.
	index := map[titleToken][]int{}
	for i, a := range recs {
		if a.company == "" || len(a.title) == 0 {
			continue
		}
		for h := range a.title {
			index[titleToken{a.company, h}] = append(index[titleToken{a.company, h}], i)
		}
	}

	// Pre-compute titleMatches and pre-warm shingles across CPU cores in
	// newest-first order, only for records that actually have an earlier title
	// match (overlap >= NearDupTitleMin), bounded to at most half the stage
	// deadline so decide always keeps time for the newest postings.
	if sctx.Err() == nil && len(recs) >= 32 && deadline > 0 {
		if workers := min(runtime.GOMAXPROCS(0), 8, len(recs)); workers > 1 {
			wctx, wcancel := context.WithTimeout(sctx, min(deadline/2, 500*time.Millisecond))
			var next atomic.Int64
			var wg sync.WaitGroup
			wg.Add(workers)
			for range workers {
				go func() {
					defer wg.Done()
					for wctx.Err() == nil {
						step := int(next.Add(1) - 1)
						i := len(recs) - 1 - step
						if i < 0 {
							return
						}
						a := recs[i]
						if a.company == "" || len(a.title) == 0 {
							a.candsReady = true
							continue
						}
						cands, capped := titleMatches(a, i, recs, index)
						a.cands, a.capped, a.candsReady = cands, capped, true
						if len(cands) == 0 || wctx.Err() != nil {
							continue
						}
						a.text()
						for _, tm := range cands {
							if wctx.Err() != nil {
								break
							}
							recs[tm.j].text()
						}
					}
				}()
			}
			wg.Wait()
			wcancel()
		}
	}

	// Decide phase: pure computation, bounded by the deadline. A posting's
	// outcome depends only on the outcomes of earlier ones (a demoted posting
	// is no survivor), so decide resolves those first and the newest postings
	// can be reached before the oldest: a deadline then costs the oldest
	// postings, whose pairs have had the most runs to be annotated. The
	// outcomes are the ones a pass from oldest to newest would reach.
	d := &nearDup{recs: recs, index: index, opt: opt, now: now, sctx: sctx, rep: &rep,
		out:       make([]*nearDupOutcome, len(recs)),
		simBudget: time.Duration(float64(deadline) * NearDupSimilarityShare)}
	for i := len(recs) - 1; i >= 0; i-- {
		if !d.decide(i) {
			break
		}
	}
	if d.cut {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		rep.Partial = true
		rep.PartialInSimilarity = d.simOverran
	}
	for _, o := range d.out {
		if o != nil {
			rep.Examined++
		}
	}

	// Write phase: bounded by ctx alone. The ledger fsyncs every event, so
	// counting writes against the deadline would cut the stage short on a
	// ledger that is merely large. The events go in batches (one fsync each),
	// every key once; Written counts only batches that were acknowledged.
	var evs []ledger.Event
	for _, o := range d.out {
		if o != nil && o.event != nil {
			evs = append(evs, *o.event)
		}
	}
	n, err := appendBatched(ctx, led, evs)
	rep.Written += n
	return rep, err
}

// nearDupOutcome is what the decide phase concluded for one posting: the
// event to write, or nil when its annotations already say it.
type nearDupOutcome struct {
	demoted bool
	event   *ledger.Event
}

// nearDup is one run's decide phase.
type nearDup struct {
	recs  []*dupRec
	index map[titleToken][]int
	opt   NearDupOptions
	now   time.Time
	sctx  context.Context
	rep   *NearDupReport
	out   []*nearDupOutcome // nil until decided
	cut   bool              // the deadline stopped the phase

	// The similarity's own budget: time spent in readings, all together.
	simBudget, simSpent time.Duration
	simFails            int  // errors in a row
	simOpen             bool // the breaker opened: no more readings
	simOverran          bool // the stage's deadline ran out during a reading
}

// decide settles posting i, and first every earlier posting it is scored
// against. It reports false once the deadline has cut the phase; a posting cut
// mid-way is left undecided, because deciding on part of its pairs could clear
// an annotation a full run keeps.
func (d *nearDup) decide(i int) bool {
	if d.out[i] != nil {
		return true
	}
	if d.cut || d.sctx.Err() != nil {
		d.cut = true
		return false
	}
	a := d.recs[i]
	var dup, sib, cand dupMatch
	var haveDup, haveSib, haveCand bool
	var compared, skipped int
	capped := false
	if a.company != "" && len(a.title) > 0 {
		var cands []dupMatch
		if a.candsReady {
			cands, capped = a.cands, a.capped
		} else {
			cands, capped = titleMatches(a, i, d.recs, d.index)
		}
		for _, tm := range cands {
			if !d.decide(tm.j) {
				return false
			}
			if d.out[tm.j].demoted {
				continue
			}
			b := d.recs[tm.j]
			a.text()
			b.text()
			if a.shingle == nil || b.shingle == nil {
				skipped++
				continue
			}
			compared++
			m := dupMatch{tm.j, textsim.Jaccard(a.shingle, b.shingle)}
			// A Duplicate is terminal and hidden, so it needs the same role as well as
			// the same text and place: the loose title (no brackets) only finds
			// candidates. "Staff" vs "Senior Staff", "(Federal)" vs "(SLED)" and
			// "(Summer 2027)" vs "(Winter 2027)" share a templated JD but are
			// different jobs, so they stay candidates.
			sameRole := a.role == b.role
			switch {
			case m.score >= NearDupDuplicate && sameRole && a.loc == b.loc:
				if !haveDup || m.j < dup.j {
					dup, haveDup = m, true
				}
			case m.score >= NearDupDuplicate && sameRole:
				if better(m, sib, haveSib) {
					sib, haveSib = m, true
				}
			case m.score >= NearDupCandidate:
				if better(m, cand, haveCand) {
					cand, haveCand = m, true
				}
			}
		}
	}
	if d.sctx.Err() != nil {
		d.cut = true
		return false
	}

	o := &nearDupOutcome{}
	protected := d.opt.Protected != nil && d.opt.Protected(a.rec.Key)
	want := map[string]string{}
	rep := d.rep
	switch {
	case haveDup && !protected:
		o.demoted = true
		o.event = duplicateEvent(d.now, a, d.recs[dup.j], dup.score)
		rep.Duplicates++
	case haveDup: // a duplicate the user has marked: noted, not demoted
		cand, haveCand = dup, true
		fallthrough
	case !haveSib && haveCand:
		want[AnnDupCandidateOf] = string(d.recs[cand.j].rec.Key)
		want[AnnDupScore] = scoreText(cand.score)
		if d.opt.Similarity != nil {
			cos, read, ok := d.cosine(a.desc, d.recs[cand.j].desc)
			if !ok {
				return false
			}
			if read {
				want[AnnDupCosine] = strconv.FormatFloat(cos, 'f', 3, 64)
			}
			// No reading leaves no dup.cosine: a stale one is forgotten below.
		}
		rep.Candidates++
	case haveSib:
		want[AnnDupSiblingOf] = string(d.recs[sib.j].rec.Key)
		want[AnnDupScore] = scoreText(sib.score)
		rep.Siblings++
	}
	if !o.demoted {
		ann := map[string]string{}
		for _, k := range []string{AnnDupCandidateOf, AnnDupSiblingOf, AnnDupScore, AnnDupCosine} {
			if a.rec.Annotations[k] != want[k] {
				ann[k] = want[k] // "" forgets a stale key
			}
		}
		if len(ann) > 0 {
			o.event = &ledger.Event{V: ledger.SchemaVersion, At: d.now, Key: a.rec.Key, Status: a.rec.Status,
				Annotations: ann, Reason: "near-duplicate annotation"}
		}
	}
	rep.Compared += compared
	rep.Skipped += skipped
	if capped {
		rep.CandidatesCapped++
	}
	d.out[i] = o
	return true
}

// cosine takes the optional similarity's reading of one candidate pair inside
// the similarity's own budget. read says a reading was taken; ok is false only
// when the stage's deadline cut the reading off, which cuts the phase as a
// deadline in the shingle pass does. Any other pair without a reading is
// counted: an error or no data in SimilarityErrors, a pair not read (budget
// spent, breaker open) or cut off by the budget in SimilaritySkipped.
func (d *nearDup) cosine(a, b string) (cos float64, read, ok bool) {
	rep := d.rep
	if d.simOpen || d.simSpent >= d.simBudget {
		rep.SimilaritySkipped++
		rep.SimilarityBudgetSpent = rep.SimilarityBudgetSpent || !d.simOpen
		return 0, false, true
	}
	cctx, cancel := context.WithTimeout(d.sctx, d.simBudget-d.simSpent)
	start := time.Now()
	cos, has, err := d.opt.Similarity(cctx, a, b)
	d.simSpent += time.Since(start)
	budgetCut := cctx.Err() != nil
	cancel()
	taken := err == nil && has
	if d.sctx.Err() != nil {
		d.simOverran = true
		if !taken { // the stage's deadline cut the reading off: not refused
			d.cut = true
			return 0, false, false
		}
	}
	switch {
	case taken:
		d.simFails = 0
		return cos, true, true
	case err != nil && budgetCut: // the budget ran out mid-reading: not taken, not refused
		d.simSpent = max(d.simSpent, d.simBudget)
		rep.SimilaritySkipped++
		rep.SimilarityBudgetSpent = true
	case err != nil:
		rep.SimilarityErrors++
		if d.simFails++; d.simFails >= NearDupSimilarityBreaker {
			d.simOpen = true
			rep.SimilarityBreakerOpen = true
		}
	default: // no data (a blank description): says nothing about the similarity's health
		rep.SimilarityErrors++
	}
	return 0, false, true
}

func scoreText(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }

func duplicateEvent(now time.Time, a, of *dupRec, score float64) *ledger.Event {
	ann := map[string]string{
		DuplicateOfKey: string(of.rec.Key), AnnDupMethod: DupMethodShingle, AnnDupScore: scoreText(score),
	}
	for _, k := range []string{AnnDupCandidateOf, AnnDupSiblingOf, AnnDupCosine} {
		if a.rec.Annotations[k] != "" {
			ann[k] = ""
		}
	}
	return &ledger.Event{V: ledger.SchemaVersion, At: now, Key: a.rec.Key, Status: posting.Duplicate,
		Annotations: ann, Reason: "duplicate of " + string(of.rec.Key) + " (" + DupMethodShingle + " " + scoreText(score) + ")"}
}

// titleMatches are the earlier postings of a's company whose titles overlap
// a's by NearDupTitleMin, closest first, at most NearDupMaxCandidates of them.
// capped says some were left out.
func titleMatches(a *dupRec, i int, recs []*dupRec, index map[titleToken][]int) (out []dupMatch, capped bool) {
	shared := map[int]int{}
	for h := range a.title {
		for _, j := range index[titleToken{a.company, h}] {
			shared[j]++
		}
	}
	for j, n := range shared {
		if j >= i {
			continue
		}
		if sim := float64(n) / float64(len(a.title)+len(recs[j].title)-n); sim >= NearDupTitleMin {
			out = append(out, dupMatch{j, sim})
		}
	}
	slices.SortFunc(out, func(x, y dupMatch) int {
		return cmp.Or(cmp.Compare(y.score, x.score), cmp.Compare(x.j, y.j))
	})
	if len(out) > NearDupMaxCandidates {
		return out[:NearDupMaxCandidates], true
	}
	return out, false
}
