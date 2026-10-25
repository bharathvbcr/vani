package semantic

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The ceiling anchor aggregates like a posting does: a posting's raw score is
// the mean of its best topChunks chunk maxima out of at most MaxPostingChunks
// chunks (a top-third order statistic), so the anchor's ceiling is the mean of
// the best third of ITS chunk maxima. These tests compute the expected ceiling
// independently of rawScoreN (embed the anchor sentences, take each one's best
// cosine against the profile vectors, sort, average the top k) and compare.

// anchorMaxima embeds the on-domain anchor for target with a fresh fake
// embedder and returns each chunk's best cosine against the matcher's profile
// vectors, best first. Every anchor chunk has weight 1 (TestAnchorHasNoTitleLine).
func anchorMaxima(t *testing.T, m *Matcher, target string) []float64 {
	t.Helper()
	chunks := onDomainAnchor(target)
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		if c.Weight != 1 {
			t.Fatalf("anchor chunk %d has weight %v; the independent mean assumes 1", i, c.Weight)
		}
		texts[i] = c.Text
	}
	rows, err := NewFakeEmbedder(256, synonyms).Embed(context.Background(), texts, KindQuery)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float64, len(rows))
	for i, r := range rows {
		v, ok := validRow(r, m.pd.dim)
		if !ok {
			t.Fatalf("anchor row %d unusable", i)
		}
		best := math.Inf(-1)
		for _, pv := range m.pd.vecs {
			best = math.Max(best, dot(v, pv))
		}
		out[i] = best
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(out)))
	return out
}

func meanTop(xs []float64, k int) float64 {
	var s float64
	for _, x := range xs[:k] {
		s += x
	}
	return s / float64(k)
}

// strongSentences echo the bank (fittingLines paraphrase eight bullets); weakSentences
// are off-domain lines of four or more words, none a near-duplicate of another.
var weakSentences = []string{
	"Bake croissants and plated desserts for daily restaurant service.",
	"Decorate custom wedding cakes and manage the bakery ovens.",
	"Order flour butter and chocolate for the pastry kitchen.",
	"Greet customers and process returns at the retail checkout.",
	"Drive a tractor trailer on regional freight routes overnight.",
	"Plan third grade lessons in reading and arithmetic.",
	"Clean teeth and take dental x-rays for family patients.",
	"Load pallets with a forklift in a distribution center.",
}

func sentences(lines []string) string {
	var sb strings.Builder
	for i, l := range lines {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(strings.TrimSuffix(l, ".") + ".")
	}
	return sb.String()
}

// targetOf builds a target of strong lines followed by weak ones.
func targetOf(strong, weak int) string {
	return sentences(append(append([]string(nil), fittingLines[:strong]...), weakSentences[:weak]...))
}

// TestCeilingIsTheTopThirdOfTheAnchor: an anchor of six sentences, two of which
// fit the bank and four of which do not, calibrates to the mean of the two
// strong sentences' maxima (k = 2 of 6), not to the mean of all six. The mean of
// all six sits below every posting that fits, which is what saturated 82% of
// the owner's saved jobs at 0.95 and above.
func TestCeilingIsTheTopThirdOfTheAnchor(t *testing.T) {
	target := targetOf(2, 4)
	m, c := calibrateTarget(t, target)
	if c.AnchorChunks != 6 {
		t.Fatalf("anchor has %d chunks, want 6 for this test", c.AnchorChunks)
	}
	xs := anchorMaxima(t, m, target)
	top2, all6 := meanTop(xs, 2), meanTop(xs, 6)
	t.Logf("maxima %.3f; ceiling %.4f; mean of the top 2 %.4f; mean of all 6 %.4f; floor %.4f", xs, c.Ceil, top2, all6, c.Floor)
	if top2-all6 < 0.1 || math.Abs(top2-meanTop(xs, 1)) < 1e-3 || math.Abs(top2-meanTop(xs, 3)) < 0.05 {
		t.Fatalf("the test inputs do not tell k=2 from k=1, 3 or 6: top-2 %.4f, top-1 %.4f, top-3 %.4f, all %.4f", top2, meanTop(xs, 1), meanTop(xs, 3), all6)
	}
	if math.Abs(c.Ceil-top2) > 1e-6 {
		t.Errorf("ceiling %.6f, want the mean of the two strongest anchor maxima %.6f (the mean of all six is %.6f)", c.Ceil, top2, all6)
	}
}

// TestCeilingDepthByAnchorSize: the depth is the same third of the chunk count a
// posting keeps (round(n*8/24), at least 1), for every size from one sentence
// to a dozen. A target that is one or two sentences is read at depth 1, so its
// ceiling is its best line, and the ceiling is the anchor's raw aggregate with
// no evidence discount (a one-chunk anchor is still "thin").
func TestCeilingDepthByAnchorSize(t *testing.T) {
	for _, tc := range []struct{ n, k int }{{1, 1}, {2, 1}, {3, 1}, {4, 1}, {5, 2}, {6, 2}, {9, 3}, {12, 4}} {
		t.Run(fmt.Sprintf("n=%d", tc.n), func(t *testing.T) {
			// k strong lines and the rest weak, so a mean at any other depth differs.
			target := targetOf(tc.k, tc.n-tc.k)
			m, c := calibrateTarget(t, target)
			if c.AnchorChunks != tc.n {
				t.Fatalf("anchor has %d chunks, want %d", c.AnchorChunks, tc.n)
			}
			if got := anchorDepth(tc.n); got != tc.k {
				t.Errorf("anchorDepth(%d) = %d, want %d", tc.n, got, tc.k)
			}
			xs := anchorMaxima(t, m, target)
			want := meanTop(xs, tc.k)
			if tc.n > tc.k && want-meanTop(xs, tc.k+1) < 0.01 {
				t.Fatalf("the test inputs do not tell depth %d from %d: %.4f vs %.4f", tc.k, tc.k+1, want, meanTop(xs, tc.k+1))
			}
			if math.Abs(c.Ceil-want) > 1e-6 {
				t.Errorf("ceiling %.6f, want the mean of the top %d of %d maxima %.6f (%.3f)", c.Ceil, tc.k, tc.n, want, xs)
			}
		})
	}
}

// TestAnchorDepthBounds: the depth is never below one and never above what a
// posting keeps, whatever size it is asked for.
func TestAnchorDepthBounds(t *testing.T) {
	for _, n := range []int{-100, -1, 0, 1} {
		if got := anchorDepth(n); got != 1 {
			t.Errorf("anchorDepth(%d) = %d, want 1", n, got)
		}
	}
	if got := anchorDepth(MaxPostingChunks); got != topChunks {
		t.Errorf("anchorDepth(%d) = %d, want topChunks %d: a full anchor reads like a full posting", MaxPostingChunks, got, topChunks)
	}
	for _, n := range []int{MaxPostingChunks + 1, 1000, math.MaxInt32, math.MaxInt} {
		if got := anchorDepth(n); got != topChunks {
			t.Errorf("anchorDepth(%d) = %d, want it capped at topChunks %d", n, got, topChunks)
		}
	}
	for n := 1; n <= MaxPostingChunks; n++ {
		if got, prev := anchorDepth(n), anchorDepth(n-1); got < prev {
			t.Errorf("anchorDepth(%d) = %d is below anchorDepth(%d) = %d", n, got, n-1, prev)
		}
	}
}

// TestOffDomainAnchorsAreReadInFull: the floor anchors are each a title and
// three lines, at most topChunks chunks, so reading them at depth topChunks is
// reading all of them, exactly as before. The floor is the mean over them of the
// weight-aware mean of every chunk's best cosine.
func TestOffDomainAnchorsAreReadInFull(t *testing.T) {
	m, c := calibrateTarget(t, calTarget)
	var rawSum float64
	for _, a := range offDomainAnchors {
		chunks := ChunkPosting(a.title, a.desc)
		if len(chunks) == 0 || len(chunks) > topChunks {
			t.Fatalf("off-domain anchor %q has %d chunks; depth topChunks=%d reads all of them only up to that", a.title, len(chunks), topChunks)
		}
		texts := make([]string, len(chunks))
		for i, ch := range chunks {
			texts[i] = ch.Text
		}
		rows, err := NewFakeEmbedder(256, synonyms).Embed(context.Background(), texts, KindQuery)
		if err != nil {
			t.Fatal(err)
		}
		var num, den float64
		for i, r := range rows {
			v, _ := validRow(r, m.pd.dim)
			best := math.Inf(-1)
			for _, pv := range m.pd.vecs {
				best = math.Max(best, dot(v, pv))
			}
			num += chunks[i].Weight * best
			den += chunks[i].Weight
		}
		rawSum += num / den
	}
	if want := rawSum / float64(len(offDomainAnchors)); math.Abs(c.Floor-want) > 1e-6 {
		t.Errorf("floor %.6f, want the mean over all chunks of each off-domain anchor %.6f", c.Floor, want)
	}
}

// TestFitMeetsTheTopThirdCeiling: with a target of two sentences that fit and
// four that do not, a posting of eight lines that fit lands inside (0.6, 0.95)
// and one that fits on half its lines scores below it. The old ceiling (the
// mean of all six) was below the fitting posting's raw score and saturated it.
func TestFitMeetsTheTopThirdCeiling(t *testing.T) {
	// The target's two fitting sentences echo the bank closely (a summary the
	// owner wrote from their own work); the posting's lines paraphrase it.
	m, c := calibrateTarget(t, sentences(append(splitSentences(calTarget)[:2:2], weakSentences[:4]...)))
	fitLines := fittingLines
	fit := Doc{Key: "fit", Title: "Platform Engineer", Description: strings.Join(fitLines, "\n")}
	half := Doc{Key: "half", Title: "Platform Engineer", Description: strings.Join(append(append([]string(nil), fitLines[:4]...), weakSentences[:4]...), "\n")}
	mustWarm(t, m, fit, half)
	f, h := lookup(t, m, fit), lookup(t, m, half)
	t.Logf("floor %.4f ceil %.4f; fit raw %.4f score %.4f; half raw %.4f score %.4f", c.Floor, c.Ceil, f.Raw, f.Score, h.Raw, h.Score)
	if f.Thin || f.Score <= 0.6 || f.Score >= 0.95 {
		t.Errorf("a posting of eight fitting lines scored %.4f (thin %v), want it inside (0.6, 0.95)", f.Score, f.Thin)
	}
	if !(h.Score < f.Score) {
		t.Errorf("half off-domain %.4f is not below the fitting posting %.4f", h.Score, f.Score)
	}
}

// TestTopThirdCalibrationIsDeterministic: two matchers on the same inputs agree
// on the ceiling and on a posting's raw and score to the bit, and rescoring the
// posting from its stored vectors in a third gives the same bits again.
func TestTopThirdCalibrationIsDeterministic(t *testing.T) {
	target := targetOf(2, 4)
	doc := Doc{Key: "fit", Title: "Platform Engineer", Description: strings.Join(fittingLines, "\n")}
	var cs []Calibration
	var ms []Match
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		// The first two matchers use their own empty caches; the third reuses the
		// first's, so its posting is rescored from the vector log, not embedded.
		d := filepath.Join(dir, fmt.Sprintf("c%d", i))
		if i == 2 {
			d = filepath.Join(dir, "c0")
			if err := os.Remove(filepath.Join(d, scoresFileName)); err != nil {
				t.Fatal(err)
			}
		}
		m, err := NewMatcher(NewFakeEmbedder(256, synonyms), calProfile(t, target), d)
		if err != nil {
			t.Fatal(err)
		}
		rep := mustWarm(t, m, doc)
		c, ok := m.Calibration()
		if !ok {
			t.Fatal("no calibration")
		}
		if i == 2 && (rep.Rescored != 1 || rep.Scored != 0) {
			t.Fatalf("the third matcher embedded the posting instead of rescoring it: %+v", rep)
		}
		cs, ms = append(cs, c), append(ms, lookup(t, m, doc))
	}
	for i := 1; i < 3; i++ {
		if math.Float64bits(cs[0].Ceil) != math.Float64bits(cs[i].Ceil) || math.Float64bits(cs[0].Floor) != math.Float64bits(cs[i].Floor) ||
			math.Float64bits(ms[0].Raw) != math.Float64bits(ms[i].Raw) || math.Float64bits(ms[0].Score) != math.Float64bits(ms[i].Score) {
			t.Fatalf("matcher %d differs from matcher 0: %+v %+v vs %+v %+v", i, cs[i], ms[i], cs[0], ms[0])
		}
	}
}

// TestScoringVersionBumpRescoresFromVectors: a cache an older binary wrote (the
// previous Version's profile hash, a stale ceiling) is not served by this one;
// the profile and anchors are embedded again and every posting is rescored from
// the chunk-vector log, which is kept (ChunkVersion did not move), so not one
// posting is embedded again. This is what the 4 to 5 bump costs the owner:
// seconds, not the hours a re-embed of the ledger takes.
func TestScoringVersionBumpRescoresFromVectors(t *testing.T) {
	p := synthProfile(t)
	if p.hashAt(Version-1) == p.Hash() {
		t.Fatalf("the profile hash does not carry Version: v%d and v%d hash alike", Version-1, Version)
	}
	docs := vecDocs(6)
	dir := filepath.Join(t.TempDir(), "cache")
	_, rep := warmIn(t, newSpy(), p, dir, docs)
	if rep.Scored != len(docs) {
		t.Fatalf("first warm: %+v", rep)
	}
	before, err := VectorStatus(dir)
	if err != nil || before.Records != len(docs) || before.Version != ChunkVersion {
		t.Fatalf("vector log before: %+v %v", before, err)
	}

	// Turn both cache files into the previous binary's: its hash, a ceiling
	// that is wrong for this one, and a score no posting has now.
	const staleCeil = 0.123456
	for _, name := range []string{profileFileName, scoresFileName} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if doc["profile_hash"] != p.Hash() {
			t.Fatalf("%s holds hash %v, want the current %s", name, doc["profile_hash"], p.Hash())
		}
		doc["profile_hash"] = p.hashAt(Version - 1)
		if name == profileFileName {
			doc["ceil"] = staleCeil
		} else {
			for _, e := range doc["entries"].(map[string]any) {
				e.(map[string]any)["score"] = 0.5
			}
		}
		if raw, err = json.Marshal(doc); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s := newSpy()
	m, rep := warmIn(t, s, p, dir, docs)
	if rep.Rescored != len(docs) || rep.Scored != 0 || rep.VectorsRebuilt || rep.VectorsAppended != 0 || rep.VectorsStale != 0 || rep.VectorsCorrupt != 0 {
		t.Fatalf("the bump did not rescore from the vector log: %+v", rep)
	}
	posted := map[string]bool{}
	for _, d := range docs {
		for _, c := range ChunkPosting(d.Title, d.Description) {
			posted[c.Text] = true
		}
	}
	for _, text := range s.texts {
		if posted[text] {
			t.Fatalf("a posting chunk was embedded again: %q", text)
		}
	}
	if c, ok := m.Calibration(); !ok || c.Ceil == staleCeil {
		t.Fatalf("the stale ceiling was served: %+v %v", c, ok)
	}
	assertSame(t, matchesOf(t, m, docs), oracle(t, p, docs))
	after, err := VectorStatus(dir)
	if err != nil || after.Generation != before.Generation || after.Records != before.Records || after.Version != ChunkVersion {
		t.Fatalf("the vector log was replaced: before %+v after %+v %v", before, after, err)
	}
}
