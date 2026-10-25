package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const tcOpus = "claude-opus-5-5"

// tcSeedMore adds n more d1-ultra postings (gh:b:00 ...) to the ledger tcSetup
// made, so a sample has something to draw from: 2 askable postings from tcSetup
// plus n.
func tcSeedMore(t *testing.T, state string, n int) []string {
	t.Helper()
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	long := strings.Repeat("You will build and operate services that serve language models. ", 20)
	keys := []string{"gh:a:1", "gh:a:2"}
	for i := range n {
		p := posting.Posting{Key: posting.Key(fmt.Sprintf("gh:b:%02d", i)), ATS: "gh", Company: "SynthCo", Title: fmt.Sprintf("Engineer gh:b:%02d", i), Description: long}
		ann := map[string]string{"triage.role_fit": "adjacent", "triage.role_fit.reading": "answered", "triage.role_fit.backend": "d1-ultra"}
		e := ledger.Event{V: 1, At: jobsT0.Add(-day), Key: p.Key, Status: posting.Explained, Posting: &p, Annotations: ann}
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, string(p.Key))
	}
	slices.Sort(keys)
	return keys
}

var tcAsked = regexp.MustCompile(`Engineer (gh:[a-z]:\w+)`)

// tcAskedKeys is the postings the fake was asked about, in the order asked
// (concurrency 1 keeps that the order the run issued them).
func tcAskedKeys(f *tcFake) []string {
	var out []string
	for _, s := range f.sent {
		if m := tcAsked.FindStringSubmatch(s); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

// tcDrawn is the order SampleCandidates draws keys in, computed independently
// of the command.
func tcDrawn(t *testing.T, keys []string, n int, seed uint64) []string {
	t.Helper()
	var c []eval.TeacherCandidate
	for _, k := range keys {
		c = append(c, eval.TeacherCandidate{Posting: posting.Posting{Key: posting.Key(k)}})
	}
	d, err := eval.SampleCandidates(c, n, seed)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, x := range d {
		out = append(out, string(x.Posting.Key))
	}
	return out
}

func TestTeacherSampleFlagsAreValidated(t *testing.T) {
	e := tcSetup(t)
	tcNoModel(t)
	for _, args := range [][]string{
		{"teacher", "--of", "d1-ultra", "--dry-run", "--sample", "0"},
		{"teacher", "--of", "d1-ultra", "--dry-run", "--sample", "-3"},
		{"teacher", "--of", "d1-ultra", "--dry-run", "--sample", "x"},
		{"teacher", "--of", "d1-ultra", "--dry-run", "--seed", "5"},                   // a seed with nothing to draw
		{"teacher", "--of", "d1-ultra", "--dry-run", "--sample", "2", "--seed", "-1"}, // a seed is unsigned
	} {
		code, _, stderr := do(t, args...)
		if code != 2 {
			t.Errorf("%v: exit %d, want a usage error (2); stderr %s", args, code, stderr)
		}
	}
	if code, _, _ := do(t, "teacher", "--of", "d1-ultra", "--budget-usd", "1", "--sample", "0"); code != 2 {
		t.Errorf("a real run with --sample 0 exited %d, want 2", code)
	}
	if _, err := os.Stat(filepath.Join(e.state, eval.TeacherFile)); err == nil {
		t.Error("a refused run wrote the readings log")
	}
}

func TestTeacherSampleLargerThanTheCandidatesAsksAboutAll(t *testing.T) {
	e := tcSetup(t)
	tcSeedMore(t, e.state, 5)
	tcNoModel(t)
	o, _ := mustCode(t, 0, "teacher", "--of", "d1-ultra", "--dry-run", "--sample", "1000", "--seed", "3")
	for _, want := range []string{"sampled 7 of 7 postings (seed 3)", "7 postings read by d1-ultra", "7 to ask"} {
		if !strings.Contains(o, want) {
			t.Errorf("output lacks %q:\n%s", want, o)
		}
	}
}

func TestTeacherSampleIsDeterministicAndMaxTakesItsPrefixAndStoredAreSkipped(t *testing.T) {
	e := tcSetup(t)
	keys := tcSeedMore(t, e.state, 30)
	f := &tcFake{}
	tcUse(t, f)
	args := func(extra ...string) []string {
		return append([]string{"teacher", "--of", "d1-ultra", "--budget-usd", "5", "--concurrency", "1", "--sample", "10", "--seed", "20261024"}, extra...)
	}
	drawn := tcDrawn(t, keys, 10, 20261024)
	if len(drawn) != 10 {
		t.Fatalf("drew %d", len(drawn))
	}

	// --max cuts the sample, in drawn order: the first four, not the first four by key.
	o, _ := mustCode(t, 0, args("--max", "4")...)
	if !strings.Contains(o, "sampled 10 of 32 postings (seed 20261024)") || !strings.Contains(o, "asked 4, stored 4") {
		t.Fatalf("output:\n%s", o)
	}
	if got := tcAskedKeys(f); !slices.Equal(got, drawn[:4]) {
		t.Fatalf("asked %v, want the first four drawn %v", got, drawn[:4])
	}

	// The same seed again draws the same sample; the four stored are skipped and
	// --max 4 takes the next four of it.
	o, _ = mustCode(t, 0, args("--max", "4")...)
	if !strings.Contains(o, "already stored 4") {
		t.Fatalf("second run did not count the stored readings:\n%s", o)
	}
	if got := tcAskedKeys(f); !slices.Equal(got, drawn[:8]) {
		t.Fatalf("after two runs asked %v, want %v", got, drawn[:8])
	}

	// An unbounded third run finishes the sample and nothing outside it.
	mustCode(t, 0, args()...)
	got := tcAskedKeys(f)
	if len(got) != 10 || !slices.Equal(got, drawn) {
		t.Fatalf("after three runs asked %v, want exactly the sample %v", got, drawn)
	}

	// A different seed draws a different sample.
	if slices.Equal(drawn, tcDrawn(t, keys, 10, 1)) {
		t.Fatal("two seeds drew the same sample")
	}
}

func TestTeacherWithReadingsOfRestrictsTheSampleToWhatTheModelRead(t *testing.T) {
	e := tcSetup(t)
	keys := tcSeedMore(t, e.state, 30)
	f := &tcFake{}
	tcUse(t, f)

	// Haiku (the configured triage model) reads a sample of 6.
	mustCode(t, 0, "teacher", "--of", "d1-ultra", "--budget-usd", "5", "--concurrency", "1", "--sample", "6", "--seed", "11")
	haiku := tcDrawn(t, keys, 6, 11)
	if got := tcAskedKeys(f); !slices.Equal(got, haiku) {
		t.Fatalf("haiku was asked %v, want %v", got, haiku)
	}

	// Opus, restricted to what Haiku read, with a sample larger than that, asks
	// about exactly those six and about nothing Haiku did not read.
	f.sent = nil
	o, _ := mustCode(t, 0, "teacher", "--of", "d1-ultra", "--model", tcOpus, "--with-readings-of", tcHaiku,
		"--budget-usd", "5", "--concurrency", "1", "--sample", "100", "--seed", "20261024")
	if !strings.Contains(o, "kept 6 of 32 postings that have a current reading by "+tcHaiku) || !strings.Contains(o, "sampled 6 of 6") {
		t.Fatalf("output:\n%s", o)
	}
	got := tcAskedKeys(f)
	want := slices.Clone(haiku)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("opus was asked %v, want the haiku set %v", got, want)
	}

	// A model that read nothing keeps nothing, asks nothing and is not an error.
	f.sent = nil
	o, _ = mustCode(t, 0, "teacher", "--of", "d1-ultra", "--model", tcOpus, "--with-readings-of", "claude-nobody",
		"--budget-usd", "5", "--sample", "10")
	if !strings.Contains(o, "kept 0 of 32") || !strings.Contains(o, "sampled 0 of 0") || len(f.sent) != 0 {
		t.Fatalf("sent %d:\n%s", len(f.sent), o)
	}
}
