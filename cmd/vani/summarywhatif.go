package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// The summary what-if (`vani relevance --what-if-summary FILE`): what a draft
// target_summary would do to the order of the owner's judged postings, computed
// on this machine with no model spend and nothing written to the state dir.
//
// Two Score parts read the summary directly: keyword (the target terms a
// posting names) and semantic (the knowledge-base profile carries the summary
// as a chunk, and its calibration anchors follow it). Only those two are
// swapped. The triage readings (fit and the mismatch gate) were answered
// against the live summary and stay as they are; re-asking them costs model
// spend. kb_match stays as it is too. So "nothing moves" here does not mean
// the summary does not matter.
//
// The semantic part is rescored from the stored chunk vectors in a temporary
// copy of the vector log, for the live summary and for the draft, by the same
// code: the live arm is a null test against the scores the ranking uses. A
// posting whose semantic part is not known in both arms is compared with it
// unknown in both, so coverage cannot pass for a change.

// maxWhatIfSummaryFile bounds the read of the draft file; the summary itself is
// held to jdtriage.MaxTargetSummaryBytes after its spaces are folded.
const maxWhatIfSummaryFile = 64 << 10

// whatIfSemanticTimeout bounds loading the model, embedding the two profiles
// and rescoring the pool.
const whatIfSemanticTimeout = 10 * time.Minute

// whatIfCopied are the semantic cache files the what-if copies: the chunk-vector
// log and the text-vector cache. The scores and the profile are left behind,
// so every score in either arm is computed here.
var whatIfCopied = []string{"vectors.log", "semantic-vectors.json"}

// maxTermList bounds the target terms listed as added or removed.
const maxTermList = 60

type summaryWhatIf struct {
	Weights string `json:"weights"`
	Pool    int    `json:"pool"`
	// Held names what the what-if leaves as it is.
	Held []string `json:"held"`
	// TargetTerms counts the vocabulary terms each summary names; Added and
	// Removed list the difference (at most maxTermList each).
	LiveTerms  int             `json:"live_terms"`
	DraftTerms int             `json:"draft_terms"`
	Added      []string        `json:"added"`
	Removed    []string        `json:"removed"`
	Semantic   whatIfSemantic  `json:"semantic"`
	Variants   []shadowVariant `json:"variants"`
	Error      string          `json:"error,omitempty"`
}

// whatIfSemantic is how the semantic arm went. Computed false gives the reason;
// the variants then swap the keyword part only.
type whatIfSemantic struct {
	Computed bool   `json:"computed"`
	Reason   string `json:"reason,omitempty"`
	// Rescored, Skipped and Failed are the live arm's counts (the draft's are
	// the same postings); KnownBoth the postings whose part is known in both.
	Rescored  int `json:"rescored"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
	KnownBoth int `json:"known_both"`
	// Embedded counts the judged postings embedded here because the log held
	// no vectors for them (both arms then read the same vectors).
	Embedded int `json:"embedded"`
	// TornBytes is the incomplete tail cut from the copied log (copied while a
	// warm appended); the whole records before it were used.
	TornBytes int64 `json:"torn_bytes,omitempty"`
	// The null test: the live arm against the scores the ranking reads, over
	// the postings known in both. Differ counts those apart by more than 1e-6.
	NullCompared int     `json:"null_compared"`
	NullDiffer   int     `json:"null_differ"`
	NullMaxDiff  float64 `json:"null_max_diff"`
	// WantedCovered counts the postings answered yes whose part is known in
	// both arms, of WantedTotal.
	WantedCovered int `json:"wanted_covered"`
	WantedTotal   int `json:"wanted_total"`
}

// semScore is one posting's semantic part as the ranking reads it.
type semScore struct {
	v  float64
	ok bool
}

// readDraftSummary reads and folds the draft summary at path.
func readDraftSummary(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxWhatIfSummaryFile+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxWhatIfSummaryFile {
		return "", fmt.Errorf("%s is over %d bytes", path, maxWhatIfSummaryFile)
	}
	s, err := jdtriage.NormalizeSummary(string(b))
	if err != nil {
		return "", err
	}
	if s == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return s, nil
}

// buildSummaryWhatIf is the what-if of the draft summary in path. A source that
// cannot be read is an Error; a semantic arm that could not run says why.
func (e *env) buildSummaryWhatIf(ctx context.Context, done map[posting.Key]eval.Entry, snap ledger.Snapshot, path string) summaryWhatIf {
	out := summaryWhatIf{
		Held:     []string{"triage readings (fit part, mismatch gate): answered against the live summary", "kb_match"},
		Added:    []string{},
		Removed:  []string{},
		Variants: []shadowVariant{},
	}
	draft, err := readDraftSummary(path)
	if err != nil {
		out.Error = "draft summary: " + err.Error()
		return out
	}
	cfg, err := e.loadConfig()
	if err != nil {
		out.Error = "config: " + err.Error()
		return out
	}
	live, used, warn := rankWeights(cfg)
	out.Weights = used
	if warn != "" {
		out.Weights += " (" + warn + ")"
	}
	w := jobinsight.DefaultWeights()
	if live != nil {
		w = *live
	}
	me, err := marks.Read(marks.Dir(e.stateDir))
	if err != nil {
		out.Error = "read marks: " + err.Error()
		return out
	}
	in, err := e.newInsight(jobinsight.DefaultScoreHalfLifeDays, snap, me)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	recs := uniqueRecords(snap)
	out.Pool = openCount(recs)

	liveTerms := in.target
	if liveTerms == nil {
		liveTerms = map[string]bool{}
	}
	draftTerms := in.vocab.Terms(draft)
	out.LiveTerms, out.DraftTerms = len(liveTerms), len(draftTerms)
	out.Added, out.Removed = termDiff(draftTerms, liveTerms), termDiff(liveTerms, draftTerms)

	isJudged := func(k posting.Key) bool {
		e, ok := done[k]
		if !ok {
			return false
		}
		_, judged := eval.ModeYesOrMaybe.Remap(e.Labels[eval.WouldApply], false)
		return judged
	}
	semLive, semDraft, sem := e.whatIfSemanticArms(ctx, cfg, draft, recs, in.rc.Semantic, isJudged)
	out.Semantic = sem

	base := in.rc
	kw := in.rc
	kw.Target = len(draftTerms) > 0
	kw.Hits = func(r ledger.Record) (int, int) { return in.hitsAgainst(draftTerms, r) }
	var both jobinsight.RankContext
	if sem.Computed {
		known := func(k posting.Key) bool { return semLive[k].ok && semDraft[k].ok }
		arm := func(m map[posting.Key]semScore) func(ledger.Record) (float64, bool) {
			return func(r ledger.Record) (float64, bool) {
				if !known(r.Key) {
					return 0, false
				}
				return m[r.Key].v, true
			}
		}
		base.Semantic, kw.Semantic = arm(semLive), arm(semLive)
		both = kw
		both.Semantic = arm(semDraft)
		for _, r := range recs {
			if known(r.Key) {
				out.Semantic.KnownBoth++
			}
			if e, ok := done[r.Key]; ok && e.Labels[eval.WouldApply] == "yes" {
				out.Semantic.WantedTotal++
				if known(r.Key) {
					out.Semantic.WantedCovered++
				}
			}
		}
	}
	rel := func(rc jobinsight.RankContext) []float64 {
		v := make([]float64, len(recs))
		for i, r := range recs {
			v[i] = jobinsight.ScoreWith(jobinsight.InputsFor(r, rc), w).Relevance
		}
		return v
	}
	baseRel := rel(base)
	eng := engineeringOf(snap)
	out.Variants = append(out.Variants, compareArms("draft summary, keyword part only", recs, done, eng, baseRel, rel(kw)))
	if sem.Computed {
		out.Variants = append(out.Variants, compareArms("draft summary, keyword and semantic parts", recs, done, eng, baseRel, rel(both)))
	}
	return out
}

// termDiff lists the terms of a not in b, sorted, at most maxTermList.
func termDiff(a, b map[string]bool) []string {
	out := []string{}
	for _, t := range slices.Sorted(maps.Keys(a)) {
		if !b[t] {
			out = append(out, t)
		}
	}
	if len(out) > maxTermList {
		out = out[:maxTermList]
	}
	return out
}

// whatIfSemanticArms rescores every posting of recs from the stored chunk
// vectors under the live summary and under draft, in a temporary copy of the
// semantic cache that is removed before it returns. The live state dir is only
// read. liveRank is the ranking's own semantic part, for the null test.
func (e *env) whatIfSemanticArms(ctx context.Context, cfg config.Config, draft string, recs []ledger.Record, liveRank func(ledger.Record) (float64, bool), isJudged func(posting.Key) bool) (live, drafted map[posting.Key]semScore, rep whatIfSemantic) {
	notRun := func(why string, err error) (map[posting.Key]semScore, map[posting.Key]semScore, whatIfSemantic) {
		rep.Reason = why
		if err != nil {
			rep.Reason += ": " + oneLine(err.Error(), 200)
		}
		return nil, nil, rep
	}
	if !cfg.SemanticOn() {
		return notRun("semantic matching is not configured (semantic.snapshot and kb.bank)", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, whatIfSemanticTimeout)
	defer cancel()
	profLive, err := semanticProfile(ctx, cfg)
	if err != nil {
		return notRun("the live knowledge-base profile could not be built", err)
	}
	dcfg := cfg
	dcfg.TargetSummary = draft
	profDraft, err := semanticProfile(ctx, dcfg)
	if err != nil {
		return notRun("the draft's knowledge-base profile could not be built", err)
	}
	tmp, err := os.MkdirTemp("", "vani-summary-whatif-")
	if err != nil {
		return notRun("no temporary directory", err)
	}
	defer os.RemoveAll(tmp)
	copied := 0
	for _, name := range whatIfCopied {
		ok, err := copyIfPresent(filepath.Join(semanticDir(e.stateDir), name), filepath.Join(tmp, name))
		if err != nil {
			return notRun("the semantic cache could not be copied", err)
		}
		if ok {
			copied++
		}
	}
	if copied == 0 {
		return notRun("there is no chunk-vector log to rescore from (run `vani semantic warm`)", nil)
	}
	emb, closeEmb, err := openEmbedder(ctx, cfg)
	if err != nil {
		return notRun("the embedding model could not be loaded (a build without -tags tessl has none)", err)
	}
	defer func() { _ = closeEmb() }()
	mLive, err := semantic.NewMatcher(emb, profLive, tmp)
	if err != nil {
		return notRun("the live arm's matcher could not be opened", err)
	}
	mDraft, err := semantic.NewMatcher(emb, profDraft, tmp)
	if err != nil {
		return notRun("the draft arm's matcher could not be opened", err)
	}
	// score warms docs under m (embedding at most max postings, rescoring the
	// rest from stored vectors) and reads the part of each of recs into into.
	// The two matchers share the copy's score file, which holds one profile's
	// scores, so each warm's answers are read out before the other's runs.
	score := func(m *semantic.Matcher, recs []ledger.Record, max int, into map[posting.Key]semScore) (semantic.Report, error) {
		docs := recordDocs(recs)
		m.SetMaxEntries(len(docs) + 1)
		r, err := m.Warm(ctx, docs, max)
		if err != nil {
			return r, err
		}
		// A torn tail is a copy taken while a warm appended: the log is cut
		// back to its last whole record and the rest is sound. Anything else
		// wrong with the log stops the arm.
		if r.VectorsErr != "" && r.VectorsTornBytes == 0 {
			return r, errors.New(r.VectorsErr)
		}
		rank := semanticRank(m)
		for _, rec := range recs {
			v, ok := rank(rec)
			into[rec.Key] = semScore{v, ok}
		}
		return r, nil
	}
	live, drafted = make(map[posting.Key]semScore, len(recs)), make(map[posting.Key]semScore, len(recs))
	// First the whole pool, from stored vectors only (max 0: no posting is
	// embedded; only the profiles and their anchors are), with nothing yet
	// appended to the log that a compaction could make room for.
	r, err := score(mLive, recs, 0, live)
	if err != nil {
		return notRun("the live arm could not be rescored", err)
	}
	rep.Rescored, rep.Skipped, rep.Failed, rep.TornBytes = r.Rescored, r.Skipped, r.Failed, r.VectorsTornBytes
	if r.Rescored == 0 {
		return notRun("no posting could be rescored from the stored vectors", nil)
	}
	if _, err := score(mDraft, recs, 0, drafted); err != nil {
		return notRun("the draft arm could not be rescored", err)
	}
	if liveRank != nil {
		for _, rec := range recs {
			v, ok := liveRank(rec)
			a := live[rec.Key]
			if !ok || !a.ok {
				continue
			}
			rep.NullCompared++
			d := v - a.v
			if d < 0 {
				d = -d
			}
			if d > 1e-6 {
				rep.NullDiffer++
			}
			rep.NullMaxDiff = max(rep.NullMaxDiff, d)
		}
	}
	// Then the judged postings again, embedding those the log holds no vectors
	// for (local model, no spend): the log keeps only what the ranking warmed,
	// and the owner's answers are the postings the comparison is about. The
	// append may compact the copy's log, which no longer matters: the pool's
	// scores were read above. Both arms read the same vectors.
	var judged []ledger.Record
	for _, rec := range recs {
		if isJudged(rec.Key) {
			judged = append(judged, rec)
		}
	}
	if len(judged) > 0 {
		r, err := score(mLive, judged, len(judged), live)
		if err != nil {
			return notRun("the judged postings could not be scored under the live summary", err)
		}
		rep.Embedded = r.Scored
		if _, err := score(mDraft, judged, len(judged), drafted); err != nil {
			return notRun("the judged postings could not be scored under the draft", err)
		}
	}
	rep.Computed = true
	return live, drafted, rep
}

// copyIfPresent copies src to dst; a missing src is (false, nil).
func copyIfPresent(src, dst string) (bool, error) {
	in, err := os.Open(src)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return false, err
	}
	return true, out.Close()
}

// Markdown renders the what-if.
func (s summaryWhatIf) Markdown() string {
	var b strings.Builder
	b.WriteString("\n## Summary what-if\n\nWhat the draft target summary would do to the order of the judged postings, computed here: no model is asked and nothing is written. Only the keyword and semantic parts are swapped.\n\n")
	if s.Error != "" {
		fmt.Fprintf(&b, "not computed: %s\n", s.Error)
		return b.String()
	}
	fmt.Fprintf(&b, "- held as they are: %s\n", strings.Join(s.Held, "; "))
	b.WriteString("- the summary's main lever is the triage reading, which this cannot measure: no movement here does not mean the summary does not matter\n")
	fmt.Fprintf(&b, "- weights: %s\n- open postings (percentiles are over these): %d\n", s.Weights, s.Pool)
	fmt.Fprintf(&b, "- target terms: %d live, %d draft; added: %s; removed: %s\n", s.LiveTerms, s.DraftTerms, listOrNone(s.Added), listOrNone(s.Removed))
	sm := s.Semantic
	if !sm.Computed {
		fmt.Fprintf(&b, "- semantic part not computed: %s. The variant below swaps the keyword part only.\n", sm.Reason)
	} else {
		fmt.Fprintf(&b, "- semantic part: %d postings rescored from stored vectors (%d skipped, %d failed); %d known in both arms, the rest compared as unknown in both\n", sm.Rescored, sm.Skipped, sm.Failed, sm.KnownBoth)
		fmt.Fprintf(&b, "- null test (live summary rescored here against the scores the ranking reads): %d compared, %d apart by more than 1e-6, largest gap %.2g\n", sm.NullCompared, sm.NullDiffer, sm.NullMaxDiff)
		fmt.Fprintf(&b, "- judged postings embedded here because the log held no vectors for them (local model, both arms): %d\n", sm.Embedded)
		fmt.Fprintf(&b, "- postings answered yes with the semantic part known in both arms: %d of %d\n", sm.WantedCovered, sm.WantedTotal)
		if sm.NullDiffer > 0 {
			b.WriteString("- the null test failed: the rescoring does not reproduce the live scores, so the semantic variant is not to be read\n")
		}
	}
	base := "live"
	if sm.Computed {
		base = "live summary rescored here"
	}
	for _, v := range s.Variants {
		b.WriteString(v.Markdown(base))
	}
	return b.String()
}

func listOrNone(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}
