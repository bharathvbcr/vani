package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// Semantic matching only orders and explains postings (Rule 7): nothing here
// drops one, and no failure here fails a run. Everything it writes down is a
// count or a reason; the bank's text and the postings' text never reach a log,
// the run record or an error message.

// semanticWarmTimeout bounds one warm: loading the model and embedding. Tests
// shorten it.
var semanticWarmTimeout = 10 * time.Minute

// openEmbedder loads the configured embedding model and returns it with its
// closer. Tests replace it with a fake.
var openEmbedder = func(ctx context.Context, cfg config.Config) (semantic.Embedder, func() error, error) {
	t, err := semantic.OpenTessl(ctx, semantic.TesslConfig{Snapshot: cfg.Semantic.Snapshot})
	if err != nil {
		return nil, nil, err
	}
	return t, t.Close, nil
}

// semanticModelPrefix is the model id the configured snapshot's scores start
// with (semantic.TesslConfig.ModelPrefix); the cache-only lookup refuses scores
// another model wrote. Tests whose fake embedder has its own id replace it.
var semanticModelPrefix = func(cfg config.Config) string {
	return semantic.TesslConfig{Snapshot: cfg.Semantic.Snapshot}.ModelPrefix()
}

func semanticDir(stateDir string) string { return filepath.Join(stateDir, "semantic") }

// nearDupLoadTimeout bounds loading the model for the near-duplicate stage.
// The load sits outside the stage's own deadline (pipeline.NearDupDeadline).
// Tests shorten it.
var nearDupLoadTimeout = 2 * time.Minute

// nearDupSimilarity is the near-duplicate stage's optional second opinion: the
// cosine of two candidate postings' title and first kilobyte of description
// (semantic.NewTitledSimilarity, docs/SIMILARITY.md), and a finish func that
// saves the vector cache once and closes the model. Call finish after the
// stage, whatever the stage returned.
//
// With semantic matching off or a model that will not load, sim is nil: the
// stage then runs on shingles alone and its report says the similarity was not
// configured, so a reading that could not be taken never looks like one that
// was. A load failure is a warning line, never a failed run. An unusable
// vector cache is rebuilt, and failing that the cosine runs uncached.
func (w *wired) nearDupSimilarity(ctx context.Context) (sim func(context.Context, string, string) (float64, bool, error), finish func()) {
	finish = func() {}
	// A previous run's cosine line must not leak into this one. Semantic
	// matching off is not a cosine that failed to load.
	w.mu.Lock()
	w.simOff, w.simNote = "", ""
	w.mu.Unlock()
	if !w.cfg.SemanticOn() || ctx.Err() != nil {
		return nil, finish
	}
	lctx, cancel := context.WithTimeout(ctx, nearDupLoadTimeout)
	emb, closeEmb, err := openEmbedder(lctx, w.cfg)
	cancel()
	if err != nil {
		msg := "near-duplicate cosine off, shingles only: " + oneLine(err.Error(), 200)
		w.warnf("%s", msg)
		w.noteSimilarity(true, msg)
		return nil, finish
	}
	dir := semanticDir(w.stateDir)
	cache, err := semantic.OpenVectorCache(dir, 0)
	if errors.Is(err, semantic.ErrCache) { // a cache holds nothing the model cannot recompute
		if rerr := semantic.ResetVectorCache(dir); rerr == nil {
			cache, err = semantic.OpenVectorCache(dir, 0)
		}
	}
	if err != nil {
		cache = nil // no caching this run; the cosine still works
		msg := "near-duplicate vector cache off: " + oneLine(err.Error(), 200)
		w.warnf("%s", msg)
		w.noteSimilarity(false, msg)
	}
	titles := semantic.NewTitleIndex()
	for _, r := range w.led.All() {
		// A closed record has no resident text (BodyEvicted) and Add skips a blank
		// one. The index is only asked about texts of postings the near-duplicate
		// pass still considers, which leaves out Dead, Dropped and Duplicate ones.
		titles.Add(r.Posting.Title, srcutil.SourceText(r.Posting.Description)) // the text the stage hands sim
	}
	return semantic.NewTitledSimilarity(emb, cache, titles), func() {
		if cache != nil {
			if err := cache.Save(); err != nil {
				msg := "near-duplicate vector cache not saved: " + oneLine(err.Error(), 200)
				w.warnf("%s", msg)
				w.noteSimilarity(false, msg)
			}
		}
		if closeEmb != nil {
			_ = closeEmb()
		}
	}
}

// semanticProfile cuts the configured bank into the profile to embed, and to
// digest for kbmatch's questions to a model: only the bullets the owner's
// selections list and kbsync.Eligible passes (eligibleBank), never the whole
// bank. With no selections or no rkb it is an error, so semantic matching reads
// "not run" instead of ranking by retired wording.
func semanticProfile(ctx context.Context, cfg config.Config) (semantic.Profile, error) {
	b, _, el, err := eligibleBank(ctx, cfg)
	if err != nil {
		return semantic.Profile{}, err
	}
	p, err := semantic.ProfileFromBank(el.Restrict(b), cfg.TargetSummary)
	if errors.Is(err, semantic.ErrEmpty) {
		return semantic.Profile{}, fmt.Errorf("%w (%s)", err, eligibleSummary(cfg, el))
	}
	return p, err
}

// boundedKBContext is a context for callers that hold none of their own: the
// ranking views read the profile to find their cache.
func boundedKBContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), eligibleKBTimeout)
}

// semanticReport is what one warm did, for the run record and the CLI. Note is
// empty when the warm ran to its end; otherwise it says why it did not, in the
// words "semantic matching not run: ..." when nothing could be scored.
type semanticReport struct {
	Ran     bool `json:"ran"`
	Scored  int  `json:"scored"`
	Cached  int  `json:"cached"`
	Failed  int  `json:"failed"`
	Skipped int  `json:"skipped"`
	// Rescored: postings scored from stored chunk vectors with no embedding.
	// Scored is what the model embedded, and only Scored spends warm_per_run.
	Rescored    int  `json:"rescored,omitempty"`
	Unavailable bool `json:"unavailable,omitempty"`
	Canceled    bool `json:"canceled,omitempty"`
	// Rebuilt: the score cache could not be read and was started again.
	Rebuilt bool `json:"cache_rebuilt,omitempty"`
	// The chunk-vector log (semantic.Report's Vectors* fields). VectorsRebuilt
	// is its own flag: losing the log costs a re-embed, unlike the score cache.
	VectorsRebuilt   bool   `json:"vectors_rebuilt,omitempty"`
	VectorsStale     int    `json:"vectors_stale,omitempty"`
	VectorsCorrupt   int    `json:"vectors_corrupt,omitempty"`
	VectorsTornBytes int64  `json:"vectors_torn_bytes,omitempty"`
	VectorsAppended  int    `json:"vectors_appended,omitempty"`
	VectorsRefused   int    `json:"vectors_refused,omitempty"`
	VectorsEvicted   int    `json:"vectors_evicted,omitempty"`
	VectorsCompacted bool   `json:"vectors_compacted,omitempty"`
	VectorsCount     int    `json:"vectors_count,omitempty"`
	VectorsBytes     int64  `json:"vectors_bytes,omitempty"`
	VectorsErr       string `json:"vectors_error,omitempty"`
	Note             string `json:"note,omitempty"`
}

// fromWarm copies a semantic.Report's counts into the report.
func (s *semanticReport) fromWarm(r semantic.Report) {
	s.Scored, s.Cached, s.Failed, s.Skipped, s.Rescored = r.Scored, r.Cached, r.Failed, r.Skipped, r.Rescored
	s.Unavailable, s.Canceled = r.Unavailable, r.Canceled
	s.VectorsRebuilt, s.VectorsStale, s.VectorsCorrupt, s.VectorsTornBytes = r.VectorsRebuilt, r.VectorsStale, r.VectorsCorrupt, r.VectorsTornBytes
	s.VectorsAppended, s.VectorsRefused, s.VectorsEvicted, s.VectorsCompacted = r.VectorsAppended, r.VectorsRefused, r.VectorsEvicted, r.VectorsCompacted
	s.VectorsCount, s.VectorsBytes, s.VectorsErr = r.VectorsCount, r.VectorsBytes, oneLine(r.VectorsErr, 200)
}

// countsLine is the warm's counts as one line. Rescored is shown only when
// there was one: a zero says nothing a reader needs.
func (s *semanticReport) countsLine() string {
	line := fmt.Sprintf("scored %d, cached %d, failed %d, skipped %d", s.Scored, s.Cached, s.Failed, s.Skipped)
	if s.Rescored > 0 {
		line += fmt.Sprintf(", rescored %d from stored vectors", s.Rescored)
	}
	return line
}

// vectorsLine says what the chunk-vector log did and how big it is, or "" when
// it holds nothing and did nothing. A log that could not be used says so: that
// must not read like one that was.
func (s *semanticReport) vectorsLine() string {
	var parts []string
	if s.VectorsCount > 0 {
		parts = append(parts, fmt.Sprintf("%d posting(s), %.1f MB", s.VectorsCount, float64(s.VectorsBytes)/(1<<20)))
	}
	if s.VectorsRebuilt {
		parts = append(parts, "the log was unreadable and was started again (postings are re-embedded as warm_per_run allows)")
	}
	if s.VectorsTornBytes > 0 {
		parts = append(parts, fmt.Sprintf("cut %d bytes of an incomplete tail", s.VectorsTornBytes))
	}
	if s.VectorsCorrupt > 0 {
		parts = append(parts, fmt.Sprintf("%d corrupt record(s)", s.VectorsCorrupt))
	}
	if s.VectorsStale > 0 {
		parts = append(parts, fmt.Sprintf("%d stale set(s) (the posting now chunks differently)", s.VectorsStale))
	}
	if s.VectorsRefused > 0 {
		parts = append(parts, fmt.Sprintf("%d posting(s) not stored (log full of higher-ranked postings, or unwritable)", s.VectorsRefused))
	}
	if s.VectorsEvicted > 0 {
		parts = append(parts, fmt.Sprintf("evicted %d", s.VectorsEvicted))
	}
	if s.VectorsErr != "" {
		parts = append(parts, "problem: "+s.VectorsErr)
	}
	if len(parts) == 0 {
		return ""
	}
	return "vectors: " + strings.Join(parts, "; ")
}

func notRun(rep semanticReport, why string, err error) semanticReport {
	rep.Note = "semantic matching not run: " + why
	if err != nil {
		rep.Note += ": " + oneLine(err.Error(), 200)
	}
	rep.Unavailable = errors.Is(err, semantic.ErrUnavailable)
	return rep
}

// warmOrder is the order a warm embeds in, for a cfg: the best Score first
// (jobinsight.Score with no semantic reading, the rank context the advance
// phase and kbmatch's candidates use, so the posting most worth reading is
// scored first), freshness as the tiebreak. Order only (Rule 7): a warm that
// the budget cuts short leaves the lowest-ranked postings for the next one.
func warmOrder(cfg config.Config) []orderOption {
	rules, titleRank := rankRules(cfg)
	return []orderOption{withWarmPriority(), withAgeNeutral(jobinsight.AgeNeutralSet(cfg.AgeNeutralCompanies)), withRank(rules, titleRank)}
}

// warmDocs are the postings the Jobs views can list (kept, or marked). With no
// opts they go freshest first by the one freshness rule (orderAdvance,
// jobinsight.FreshAge); warmOrder's opts put the best Score first, so a capped
// warm scores what the owner is about to read.
func warmDocs(recs []ledger.Record, mk map[posting.Key]marks.Mark, opts ...orderOption) []semantic.Doc {
	todo, _ := warmRecords(recs, mk, opts...)
	return recordDocs(todo)
}

// warmRecords are the records warmDocs embeds, in opts' order, with the Score
// they were ordered by (orderAdvance's, nil when opts read no Score).
func warmRecords(recs []ledger.Record, mk map[posting.Key]marks.Mark, opts ...orderOption) ([]ledger.Record, map[posting.Key]float64) {
	var todo []ledger.Record
	for _, r := range recs {
		// A closed job someone marked has no resident text (ledger.Record.BodyEvicted):
		// embedding its title alone would score a different text than the Jobs views
		// look up, so it is left out rather than warmed wrongly.
		if jobview.InView("all", r, mk[r.Key]) && !r.BodyEvicted {
			todo = append(todo, r)
		}
	}
	scores := orderAdvance(todo, recs, nil, opts...)
	return todo, scores
}

// warmRanked is the postings a warm embeds in warmOrder, with the priority the
// chunk-vector log keeps their vectors by: the same Score that ordered them, so
// what a warm embeds first is what a full log evicts last.
func warmRanked(recs []ledger.Record, mk map[posting.Key]marks.Mark, cfg config.Config) ([]semantic.Doc, map[string]float64) {
	todo, scores := warmRecords(recs, mk, warmOrder(cfg)...)
	return recordDocs(todo), vectorRanks(todo, scores)
}

// recordDocs are recs as the warm's input, in recs' order.
func recordDocs(recs []ledger.Record) []semantic.Doc {
	docs := make([]semantic.Doc, len(recs))
	for i, r := range recs {
		docs[i] = semantic.Doc{Key: string(r.Key), Title: r.Posting.Title, Description: r.Posting.Description}
	}
	return docs
}

// vectorRanks are the TextHashes of recs, each with its Score: the postings
// whose vectors the chunk-vector log keeps before it evicts anything else, and
// how long it keeps them (semantic.Matcher.SetVectorPriority; the higher Score
// lasts longer). One text under several postings takes the best Score. A posting
// without a Score (scores nil) ranks 0, so age decides among them. The result is
// never nil: an empty one names an empty live set.
func vectorRanks(recs []ledger.Record, scores map[posting.Key]float64) map[string]float64 {
	ranks := make(map[string]float64, len(recs))
	for _, r := range recs {
		h := semantic.TextHash(r.Posting.Title, r.Posting.Description)
		s := scores[r.Key]
		if old, ok := ranks[h]; !ok || s > old {
			ranks[h] = s
		}
	}
	return ranks
}

// openScoreCache opens the score cache for the model. A cache file that cannot
// be read holds nothing the model cannot recompute, so it is removed and
// started again; rebuilt says so.
func openScoreCache(emb semantic.Embedder, p semantic.Profile, dir string) (m *semantic.Matcher, rebuilt bool, err error) {
	m, err = semantic.NewMatcher(emb, p, dir)
	if err == nil || !errors.Is(err, semantic.ErrCache) {
		return m, false, err
	}
	for _, name := range []string{"semantic-profile.json", "semantic-scores.json"} {
		if rerr := os.Remove(filepath.Join(dir, name)); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return nil, false, errors.Join(err, rerr)
		}
	}
	m, err = semantic.NewMatcher(emb, p, dir)
	return m, err == nil, err
}

// warmSemantic scores up to max of recs' browsable postings against the
// owner's knowledge base and caches the scores under stateDir. It never returns
// an error: whatever stopped it is in the report.
func warmSemantic(ctx context.Context, cfg config.Config, stateDir string, recs []ledger.Record, mk map[posting.Key]marks.Mark, max int) semanticReport {
	docs, ranks := warmRanked(recs, mk, cfg)
	return warmSemanticWith(ctx, cfg, stateDir, docs, ranks, max)
}

// withMarkedText returns recs with the description of every record warmDocs
// would embed but All() left empty: a closed job (Dead, Dropped, Duplicate) the
// owner marked has its text evicted from memory (ledger.Record.BodyEvicted),
// and without it the job would lose its semantic score. Only those records are
// read back from l, never every closed job.
func withMarkedText(l ledger.Ledger, recs []ledger.Record, mk map[posting.Key]marks.Mark) []ledger.Record {
	for i, r := range recs {
		if r.BodyEvicted && jobview.InView("all", r, mk[r.Key]) {
			recs[i] = ledger.Full(l, r)
		}
	}
	return recs
}

// unreadableMarked counts the records warmDocs would embed but whose stored
// text is still evicted after withMarkedText (the ledger could not read it
// back): they are left out of the warm, and the run says how many.
func unreadableMarked(recs []ledger.Record, mk map[posting.Key]marks.Mark) int {
	n := 0
	for _, r := range recs {
		if r.BodyEvicted && jobview.InView("all", r, mk[r.Key]) {
			n++
		}
	}
	return n
}

// noteUnreadable adds the count of marked postings left out of the warm to the
// report's note. A count only, never any text.
func (r *semanticReport) noteUnreadable(n int) {
	if n == 0 {
		return
	}
	msg := fmt.Sprintf("%d marked posting(s) not warmed: stored description unreadable", n)
	if r.Note != "" {
		msg = r.Note + "; " + msg
	}
	r.Note = msg
}

// noteMarks records that the Jobs marks could not be read, so the warm saw
// kept postings only. The marks clause stays even when the earlier note is
// already long. A nil error changes nothing.
func noteMarks(r *semanticReport, err error) {
	if r == nil || err == nil {
		return
	}
	clause := "marks could not be read: " + oneLine(err.Error(), maxStageNote)
	if r.Note == "" {
		r.Note = clause
		return
	}
	joined := r.Note + "; " + clause
	if utf8.RuneCountInString(joined) <= 2*maxStageNote {
		r.Note = joined
		return
	}
	r.Note = oneLine(r.Note, maxStageNote) + "; " + clause
}

// warmSemanticRun is warmSemantic as the daily run calls it; a variable only so
// a test can see the records the run hands the warm.
var warmSemanticRun = warmSemantic

// The daily run spends semantic.warm_per_run (L) in two warms, so the posting
// the decision model reads first is already scored and the digest's kept
// postings are too. Before the advance phase it may embed up to
// L - L/4 live postings (they feed triage, so they go first); after it, the
// kept postings get whatever L the first warm did not use, at least L/4 when
// the first warm spends all it may. Cached postings cost nothing in either.
func preAdvanceBudget(limit int) int { return limit - limit/4 }

// postAdvanceBudget is what the first warm left of limit. Only the postings it
// embedded (Scored) were spent: Rescored ones came from stored vectors, cost no
// embedding, and are not counted against warm_per_run in either warm.
func postAdvanceBudget(limit int, pre *semanticReport) int {
	if pre == nil {
		return limit
	}
	return max(limit-pre.Scored, 0)
}

// preAdvanceDocs are the live postings the title rule would not drop: the ones
// the advance phase may hand to the decision model. They go freshest first, or
// by warmOrder's opts best Score first.
func preAdvanceDocs(recs []ledger.Record, drop func(ledger.Record) bool, opts ...orderOption) []semantic.Doc {
	var todo []ledger.Record
	for _, r := range recs {
		if r.Status == posting.Live && (drop == nil || !drop(r)) {
			todo = append(todo, r)
		}
	}
	orderAdvance(todo, recs, nil, opts...)
	return recordDocs(todo)
}

// warmBeforeAdvance scores the live postings pipeline.Advance may triage so that
// advanceOrder can put the best matches first. It only orders (Rule 7) and
// never fails the run: whatever stopped it is in the report kept for the run
// record, and the advance phase goes on regardless.
func (w *wired) warmBeforeAdvance(ctx context.Context, recs []ledger.Record, drop func(ledger.Record) bool) {
	if !w.cfg.SemanticOn() || ctx.Err() != nil {
		return
	}
	// The live set (what a compaction of the vector log keeps first) is every
	// browsable posting, ranked by Score, so it needs the marks. Marks that cannot
	// be read give no live set at all, never a partial one: the log then evicts by
	// age alone rather than treat a marked posting as closed.
	var ranks map[string]float64
	if mk, err := marks.ReadMarks(marks.Dir(w.stateDir)); err == nil {
		_, ranks = warmRanked(recs, mk, w.cfg)
	}
	rep := warmSemanticWith(ctx, w.cfg, w.stateDir, preAdvanceDocs(recs, drop, warmOrder(w.cfg)...), ranks, preAdvanceBudget(w.cfg.SemanticWarmLimit()))
	w.mu.Lock()
	w.semPre = &rep
	w.mu.Unlock()
	if rep.Note != "" {
		w.warnf("before advance: %s", rep.Note)
	}
}

// advanceOrder is the order option that ranks the advance phase by knowledge-base
// match, or nil (title rank, freshness, ledger order) when there is no cache.
func (w *wired) advanceOrder() orderOption {
	if !w.cfg.SemanticOn() {
		return nil
	}
	m, warning := semanticLookup(w.cfg, w.stateDir)
	if warning != "" {
		w.warnf("advance order: %s", warning)
	}
	neutral := jobinsight.AgeNeutralSet(w.cfg.AgeNeutralCompanies)
	rules, titleRank := rankRules(w.cfg)
	return func(s *orderSettings) {
		withSemantic(semanticRank(m))(s)
		withAgeNeutral(neutral)(s)
		withRank(rules, titleRank)(s)
	}
}

// warmSemanticDocs scores up to max of docs, naming no live set for the
// chunk-vector log (age alone decides what a compaction evicts).
func warmSemanticDocs(ctx context.Context, cfg config.Config, stateDir string, docs []semantic.Doc, max int) semanticReport {
	return warmSemanticWith(ctx, cfg, stateDir, docs, nil, max)
}

// warmSemanticWith scores up to max of docs; warmSemantic's body. ranks is the
// Score of each posting still browsable, by TextHash (nil names none): a
// compaction of the vector log evicts the vectors of the rest first, then the
// lowest Score (semantic.Matcher.SetVectorPriority).
func warmSemanticWith(ctx context.Context, cfg config.Config, stateDir string, docs []semantic.Doc, ranks map[string]float64, max int) semanticReport {
	var rep semanticReport
	if !cfg.SemanticOn() {
		return notRun(rep, "semantic.snapshot and kb.bank are not both set", nil)
	}
	prof, err := semanticProfile(ctx, cfg)
	if err != nil {
		return notRun(rep, "the knowledge base profile could not be built", err)
	}
	if len(docs) == 0 {
		rep.Ran = true
		return rep
	}
	wctx, cancel := context.WithTimeout(ctx, semanticWarmTimeout)
	defer cancel()
	emb, closeEmb, err := openEmbedder(wctx, cfg)
	if err != nil {
		return notRun(rep, "the embedding model could not be loaded", err)
	}
	defer func() { _ = closeEmb() }()
	m, rebuilt, err := openScoreCache(emb, prof, semanticDir(stateDir))
	if err != nil {
		return notRun(rep, "the score cache could not be opened", err)
	}
	m.SetVectorPriority(ranks)
	r, werr := m.Warm(wctx, docs, max)
	rep.Ran = true
	rep.fromWarm(r)
	rep.Rebuilt = rebuilt
	switch {
	case errors.Is(werr, semantic.ErrWarmBusy):
		rep.Ran = false
		rep.Note = werr.Error()
	case werr != nil && r.Unavailable:
		rep.Ran = false
		rep.Note = "semantic matching not run: " + oneLine(werr.Error(), 200)
	case werr != nil:
		rep.Note = "semantic matching stopped early: " + oneLine(werr.Error(), 200)
	}
	return rep
}

// logSemantic writes the report as one or two stderr lines of counts and reasons.
func (e *env) logSemantic(rep semanticReport) {
	if rep.Ran {
		fmt.Fprintf(e.stderr, "vani: semantic: %s\n", rep.countsLine())
	}
	if rep.Rebuilt {
		fmt.Fprintln(e.stderr, "vani: semantic: the score cache was unreadable and was started again")
	}
	if l := rep.vectorsLine(); l != "" {
		fmt.Fprintf(e.stderr, "vani: semantic: %s\n", l)
	}
	if rep.Note != "" {
		fmt.Fprintf(e.stderr, "vani: %s\n", rep.Note)
	}
}

// semanticLookup opens the cache-only matcher for cfg, loading no model. It
// returns nil when semantic matching is off, has no scores yet, or cannot be
// opened; warning says why in the last two cases. Unknown stays unknown: the
// ranker takes its prior, never a score of 0.
func semanticLookup(cfg config.Config, stateDir string) (m *semantic.Matcher, warning string) {
	if !cfg.SemanticOn() {
		return nil, ""
	}
	ctx, cancel := boundedKBContext()
	defer cancel()
	prof, err := semanticProfile(ctx, cfg)
	if err != nil {
		return nil, "knowledge-base matching is off: the profile could not be built: " + oneLine(err.Error(), 200)
	}
	m, err = semantic.OpenLookupFor(prof, semanticDir(stateDir), semanticModelPrefix(cfg))
	if errors.Is(err, semantic.ErrModelMismatch) {
		return nil, "knowledge-base scores are from another model; run `vani semantic warm`"
	}
	if err != nil {
		return nil, "knowledge-base matching is off: the score cache could not be read: " + oneLine(err.Error(), 200)
	}
	if m.Len() == 0 {
		return nil, "knowledge-base matching has no scores yet; run `vani semantic warm`"
	}
	return m, ""
}

// semanticScore is the Lookup keyed the way Warm stores: posting key and the
// hash of the text that was scored.
func semanticScore(m *semantic.Matcher, r ledger.Record) (semantic.Match, bool) {
	return m.Lookup(string(r.Key), semantic.TextHash(r.Posting.Title, r.Posting.Description))
}

// semanticRank is jobinsight.RankContext.Semantic over m; nil for no matcher.
func semanticRank(m *semantic.Matcher) func(ledger.Record) (float64, bool) {
	if m == nil {
		return nil
	}
	return func(r ledger.Record) (float64, bool) {
		mt, ok := semanticScore(m, r)
		// A thin match rests on too little text to rank by: unknown, so the
		// ranker takes its prior rather than a confident number.
		return mt.Score, ok && !mt.Thin
	}
}

// digestOptions are the digest's ranking inputs for cfg; the warning is for the
// caller to print.
func digestOptions(cfg config.Config, stateDir string) (digest.Options, string) {
	m, warning := semanticLookup(cfg, stateDir)
	rules, titleRank := rankRules(cfg)
	kb, kbWarning := kbmatchLookup(cfg, stateDir)
	weights, _, weightsWarning := rankWeights(cfg)
	for _, w := range []string{kbWarning, weightsWarning} {
		if w == "" {
			continue
		}
		if warning != "" {
			warning += "; "
		}
		warning += w
	}
	return digest.Options{Semantic: semanticRank(m), Rules: rules, TitleRank: titleRank, KBMatch: kb,
		AgeNeutral: jobinsight.AgeNeutralSet(cfg.AgeNeutralCompanies), HalfLifeDays: jobinsight.DefaultScoreHalfLifeDays,
		Weights: weights, TopOpen: cfg.DigestTopOpen(), ExploreBelowFold: cfg.DigestExploreBelowFold(),
		ExploreDropped: cfg.DigestExploreDropped()}, warning
}

// attachSemantic opens the lookup matcher into in (jobsinsight.go newInsight)
// and points the ranking at it. A failure is a warning on the listing and
// leaves semantic unknown.
func (in *insight) attachSemantic(cfg config.Config, stateDir string) {
	m, warning := semanticLookup(cfg, stateDir)
	if warning != "" {
		if in.info.Warning != "" {
			in.info.Warning += "; "
		}
		in.info.Warning += warning
	}
	in.sem = m
	in.rc.Semantic = semanticRank(m)
}

// semanticInto fills j's semantic match when one is cached for r.
func (in *insight) semanticInto(j *jobview.Job, r ledger.Record) {
	if in.sem == nil {
		return
	}
	if mt, ok := semanticScore(in.sem, r); ok {
		j.Semantic = &jobview.Semantic{Score: mt.Score, Evidence: mt.Evidence, Thin: mt.Thin}
	}
}
