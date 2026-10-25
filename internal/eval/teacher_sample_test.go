package eval

import (
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func sampleCands(n int) []TeacherCandidate {
	out := make([]TeacherCandidate, n)
	for i := range out {
		out[i] = TeacherCandidate{Posting: posting.Posting{Key: posting.Key(fmt.Sprintf("gh:x:%04d", i))}}
	}
	return out
}

func sampleKeys(c []TeacherCandidate) []string {
	out := make([]string, len(c))
	for i, x := range c {
		out[i] = string(x.Posting.Key)
	}
	return out
}

func TestSampleIsDeterministicForASeedAndDiffersAcrossSeeds(t *testing.T) {
	c := sampleCands(500)
	a, err := SampleCandidates(c, 50, 7)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := SampleCandidates(c, 50, 7)
	if !slices.Equal(sampleKeys(a), sampleKeys(b)) {
		t.Fatal("the same seed drew different samples")
	}
	d, _ := SampleCandidates(c, 50, 8)
	if slices.Equal(sampleKeys(a), sampleKeys(d)) {
		t.Fatal("two seeds drew the same sample")
	}
	// The draw has no duplicates and nothing outside the candidates.
	seen := map[string]bool{}
	for _, k := range sampleKeys(a) {
		if seen[k] {
			t.Fatalf("%s drawn twice", k)
		}
		seen[k] = true
	}
	if len(a) != 50 {
		t.Fatalf("drew %d, want 50", len(a))
	}
}

// A smaller draw is the prefix of a larger one for the same seed: this is what
// makes `--sample 1000 --max 300` a uniform subset of the 1000.
func TestSamplePrefixesNestAndDoNotMutateTheInput(t *testing.T) {
	c := sampleCands(400)
	orig := sampleKeys(c)
	big, _ := SampleCandidates(c, 300, 20261024)
	small, _ := SampleCandidates(c, 90, 20261024)
	if !slices.Equal(sampleKeys(small), sampleKeys(big)[:90]) {
		t.Fatal("a draw of 90 is not the first 90 of a draw of 300")
	}
	whole, _ := SampleCandidates(c, 10_000, 20261024)
	if !slices.Equal(sampleKeys(big), sampleKeys(whole)[:300]) {
		t.Fatal("a draw of 300 is not a prefix of the full shuffle")
	}
	if !slices.Equal(orig, sampleKeys(c)) {
		t.Fatal("the candidate slice was reordered")
	}
}

func TestSampleLargerThanTheCandidatesReturnsAllOfThem(t *testing.T) {
	c := sampleCands(12)
	got, err := SampleCandidates(c, 1000, 1)
	if err != nil || len(got) != 12 {
		t.Fatalf("got %d, %v", len(got), err)
	}
	a, b := sampleKeys(got), sampleKeys(c)
	slices.Sort(a)
	if !slices.Equal(a, b) {
		t.Fatalf("not a permutation of the candidates: %v", a)
	}
	if got, err := SampleCandidates(nil, 5, 1); err != nil || len(got) != 0 {
		t.Fatalf("no candidates: %d, %v", len(got), err)
	}
}

func TestSampleRejectsASizeBelowOne(t *testing.T) {
	for _, n := range []int{0, -1, -1 << 31} {
		if got, err := SampleCandidates(sampleCands(5), n, 1); err == nil || got != nil {
			t.Errorf("n=%d: got %v, %v; want an error and no sample", n, got, err)
		}
	}
}

// Uniformity, as a sanity check and not a proof: over many seeds, each of 10
// candidates is in a 3-of-10 sample about 30% of the time and leads a 1-draw
// about 10% of the time, and position bias in the draw order is absent. The
// seeds are fixed, so the test is not flaky; the bounds are 5 standard
// deviations of a fair draw.
func TestSampleIsUniform(t *testing.T) {
	const cands, draws = 10, 3000
	c := sampleCands(cands)
	in, first := map[string]int{}, map[string]int{}
	for seed := range uint64(draws) {
		s, _ := SampleCandidates(c, 3, seed)
		for _, k := range sampleKeys(s) {
			in[k]++
		}
		first[string(s[0].Posting.Key)]++
	}
	check := func(name string, counts map[string]int, p float64) {
		mean := p * draws
		sd := 5 * math.Sqrt(draws*p*(1-p))
		for _, x := range c {
			if n := float64(counts[string(x.Posting.Key)]); n < mean-sd || n > mean+sd {
				t.Errorf("%s: %s chosen %v times, want %.0f +- %.0f", name, x.Posting.Key, n, mean, sd)
			}
		}
	}
	check("inclusion", in, 0.3)
	check("first", first, 0.1)
}

func TestCurrentReadingsHasAndWithCurrentReading(t *testing.T) {
	const summary = "ml systems roles"
	s, err := OpenTeacherStore(filepath.Join(t.TempDir(), TeacherFile))
	if err != nil {
		t.Fatal(err)
	}
	fx, ph := FixtureSHA(), PromptHash(summary)
	ans := map[string]TeacherSlot{"role_fit": {Reading: "answered", Value: "strong_fit"}}
	for _, r := range []TeacherRow{
		currentRow("gh:x:0001", "haiku", fx, ph, ans),
		currentRow("gh:x:0002", "haiku", fx, PromptHash("another summary"), ans), // old prompt
		currentRow("gh:x:0003", "opus", fx, ph, ans),                             // other model
		currentRow("gh:x:0004", "haiku", fx, ph, map[string]TeacherSlot{"role_fit": {Reading: "failed"}}),
	} {
		if err := s.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	cur := s.Current("haiku", summary)
	got := sampleKeys(WithCurrentReading(sampleCands(6), cur))
	if want := []string{"gh:x:0001", "gh:x:0004"}; !slices.Equal(got, want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	// None matching, an unknown model and the zero value all keep nothing.
	if got := WithCurrentReading(sampleCands(6), s.Current("nobody", summary)); len(got) != 0 {
		t.Fatalf("an unknown model kept %d", len(got))
	}
	if got := WithCurrentReading(sampleCands(6), CurrentReadings{}); len(got) != 0 {
		t.Fatalf("the zero value kept %d", len(got))
	}
	if (CurrentReadings{}).Has("k") {
		t.Fatal("the zero value has a reading")
	}
}
