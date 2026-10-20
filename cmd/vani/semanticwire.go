package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/digest"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/kbsync"
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
	if !w.cfg.SemanticOn() || ctx.Err() != nil {
		return nil, finish
	}
	lctx, cancel := context.WithTimeout(ctx, nearDupLoadTimeout)
	emb, closeEmb, err := openEmbedder(lctx, w.cfg)
	cancel()
	if err != nil {
		w.warnf("near-duplicate cosine off, shingles only: %s", oneLine(err.Error(), 200))
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
		w.warnf("near-duplicate vector cache off: %s", oneLine(err.Error(), 200))
	}
	titles := semantic.NewTitleIndex()
	for _, r := range w.led.All() {
		titles.Add(r.Posting.Title, srcutil.SourceText(r.Posting.Description)) // the text the stage hands sim
	}
	return semantic.NewTitledSimilarity(emb, cache, titles), func() {
		if cache != nil {
			if err := cache.Save(); err != nil {
				w.warnf("near-duplicate vector cache not saved: %s", oneLine(err.Error(), 200))
			}
		}
		if closeEmb != nil {
			_ = closeEmb()
		}
	}
}

// semanticProfile cuts the configured bank into the profile to embed.
func semanticProfile(cfg config.Config) (semantic.Profile, error) {
	b, err := kbsync.Load(cfg.KB.Bank)
	if err != nil {
		return semantic.Profile{}, err
	}
	return semantic.ProfileFromBank(b, cfg.TargetSummary)
}

// semanticReport is what one warm did, for the run record and the CLI. Note is
// empty when the warm ran to its end; otherwise it says why it did not, in the
// words "semantic matching not run: ..." when nothing could be scored.
type semanticReport struct {
	Ran         bool `json:"ran"`
	Scored      int  `json:"scored"`
	Cached      int  `json:"cached"`
	Failed      int  `json:"failed"`
	Skipped     int  `json:"skipped"`
	Unavailable bool `json:"unavailable,omitempty"`
	Canceled    bool `json:"canceled,omitempty"`
	// Rebuilt: the score cache could not be read and was started again.
	Rebuilt bool   `json:"cache_rebuilt,omitempty"`
	Note    string `json:"note,omitempty"`
}

func notRun(rep semanticReport, why string, err error) semanticReport {
	rep.Note = "semantic matching not run: " + why
	if err != nil {
		rep.Note += ": " + oneLine(err.Error(), 200)
	}
	rep.Unavailable = errors.Is(err, semantic.ErrUnavailable)
	return rep
}

// warmDocs are the postings the Jobs views can list (kept, or marked), freshest
// first by the one freshness rule (orderAdvance, jobinsight.FreshAge), so a
// capped warm scores what the owner is about to read.
func warmDocs(recs []ledger.Record, mk map[posting.Key]marks.Mark) []semantic.Doc {
	var todo []ledger.Record
	for _, r := range recs {
		if jobview.InView("all", r, mk[r.Key]) {
			todo = append(todo, r)
		}
	}
	orderAdvance(todo, recs, nil)
	docs := make([]semantic.Doc, len(todo))
	for i, r := range todo {
		docs[i] = semantic.Doc{Key: string(r.Key), Title: r.Posting.Title, Description: r.Posting.Description}
	}
	return docs
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
	return warmSemanticDocs(ctx, cfg, stateDir, warmDocs(recs, mk), max)
}

// The daily run spends semantic.warm_per_run (L) in two warms, so the posting
// the decision model reads first is already scored and the digest's kept
// postings are too. Before the advance phase it may embed up to
// L - L/4 live postings (they feed triage, so they go first); after it, the
// kept postings get whatever L the first warm did not use, at least L/4 when
// the first warm spends all it may. Cached postings cost nothing in either.
func preAdvanceBudget(limit int) int { return limit - limit/4 }

// preAdvanceDocs are the live postings the title rule would not drop, freshest
// first: the ones the advance phase may hand to the decision model.
func preAdvanceDocs(recs []ledger.Record, drop func(ledger.Record) bool) []semantic.Doc {
	var todo []ledger.Record
	for _, r := range recs {
		if r.Status == posting.Live && (drop == nil || !drop(r)) {
			todo = append(todo, r)
		}
	}
	orderAdvance(todo, recs, nil)
	docs := make([]semantic.Doc, len(todo))
	for i, r := range todo {
		docs[i] = semantic.Doc{Key: string(r.Key), Title: r.Posting.Title, Description: r.Posting.Description}
	}
	return docs
}

// warmBeforeAdvance scores the live postings pipeline.Advance may triage so that
// advanceOrder can put the best matches first. It only orders (Rule 7) and
// never fails the run: whatever stopped it is in the report kept for the run
// record, and the advance phase goes on regardless.
func (w *wired) warmBeforeAdvance(ctx context.Context, recs []ledger.Record, drop func(ledger.Record) bool) {
	if !w.cfg.SemanticOn() || ctx.Err() != nil {
		return
	}
	rep := warmSemanticDocs(ctx, w.cfg, w.stateDir, preAdvanceDocs(recs, drop), preAdvanceBudget(w.cfg.SemanticWarmLimit()))
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

// warmSemanticDocs scores up to max of docs; warmSemantic's body.
func warmSemanticDocs(ctx context.Context, cfg config.Config, stateDir string, docs []semantic.Doc, max int) semanticReport {
	var rep semanticReport
	if !cfg.SemanticOn() {
		return notRun(rep, "semantic.snapshot and kb.bank are not both set", nil)
	}
	prof, err := semanticProfile(cfg)
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
	r, werr := m.Warm(wctx, docs, max)
	rep.Ran = true
	rep.Scored, rep.Cached, rep.Failed, rep.Skipped = r.Scored, r.Cached, r.Failed, r.Skipped
	rep.Unavailable, rep.Canceled, rep.Rebuilt = r.Unavailable, r.Canceled, rebuilt
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
		fmt.Fprintf(e.stderr, "vani: semantic: scored %d, cached %d, failed %d, skipped %d\n", rep.Scored, rep.Cached, rep.Failed, rep.Skipped)
	}
	if rep.Rebuilt {
		fmt.Fprintln(e.stderr, "vani: semantic: the score cache was unreadable and was started again")
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
	prof, err := semanticProfile(cfg)
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
	if kbWarning != "" {
		if warning != "" {
			warning += "; "
		}
		warning += kbWarning
	}
	return digest.Options{Semantic: semanticRank(m), Rules: rules, TitleRank: titleRank, KBMatch: kb,
		AgeNeutral: jobinsight.AgeNeutralSet(cfg.AgeNeutralCompanies), HalfLifeDays: jobinsight.DefaultScoreHalfLifeDays,
		TopOpen: cfg.DigestTopOpen(), ExploreBelowFold: cfg.DigestExploreBelowFold(), ExploreDropped: cfg.DigestExploreDropped()}, warning
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
