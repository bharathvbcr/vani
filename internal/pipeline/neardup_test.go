package pipeline

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// words builds a description of n distinct words with the given prefix.
func words(prefix string, n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "%s%d ", prefix, i)
	}
	return b.String()
}

// shares is a description whose first `same` of n words match words("duty", n).
func shares(same, n int) string { return words("duty", same) + words("other", n-same) }

// ndAdd writes a live posting first seen at `at`.
func ndAdd(t *testing.T, l ledger.Ledger, id, company, title, loc, desc string, at time.Time) posting.Key {
	t.Helper()
	k, err := posting.GreenhouseKey("acme", id)
	if err != nil {
		t.Fatal(err)
	}
	p := posting.Posting{Key: k, ATS: posting.Greenhouse, Company: company, Title: title, Location: loc, Description: desc}
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: at, Key: k, Status: posting.Live, Posting: &p}); err != nil {
		t.Fatal(err)
	}
	return k
}

func annotate(t *testing.T, l ledger.Ledger, now time.Time, opt NearDupOptions) NearDupReport {
	t.Helper()
	rep, err := AnnotateNearDuplicates(ctx, l, now, opt)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

var t1 = t0.Add(time.Hour)

func TestNearDupTitleVariantWithTheSameJDIsADuplicate(t *testing.T) {
	jd := words("duty", 100)
	for _, tc := range []struct{ name, earlier, later, coA, coB string }{
		{"bracketed remote tag", "Senior Engineer", "Senior Engineer (Remote)", "Acme", "Acme"},
		{"earlier carries the tag", "Senior Engineer (Remote)", "Senior Engineer", "Acme", "Acme"},
		{"trailing work mode", "Senior Engineer", "Senior Engineer - Hybrid", "Acme", "Acme"},
		{"requisition number", "Senior Engineer", "Senior Engineer R-20431", "Acme", "Acme"},
		{"legal suffix", "Senior Engineer", "Senior Engineer", "Acme", "Acme Inc."},
		{"legal suffixes both ways", "Senior Engineer", "Senior Engineer", "ACME Corp", "Acme, Inc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := openLedger(t)
			first := ndAdd(t, l, "1", tc.coA, tc.earlier, "Remote", jd, t0)
			second := ndAdd(t, l, "2", tc.coB, tc.later, "Remote", jd, t1)
			rep := annotate(t, l, t1, NearDupOptions{})
			if rep.Duplicates != 1 || rep.Partial {
				t.Fatalf("report %+v", rep)
			}
			wantDuplicate(t, l, second, first)
			if a := rec(t, l, second).Annotations; a[AnnDupMethod] != DupMethodShingle || a[AnnDupScore] != "1.00" {
				t.Fatalf("annotations %v", a)
			}
			wantLive(t, l, first)
		})
	}
}

func TestNearDupSurvivorIsTheEarliestFirstSeenWhateverTheKeyOrder(t *testing.T) {
	l := openLedger(t)
	late := ndAdd(t, l, "1", "Acme", "Engineer", "Remote", words("duty", 100), t1) // sorts first by key
	early := ndAdd(t, l, "9", "Acme", "Engineer", "Remote", words("duty", 100), t0)
	annotate(t, l, t1, NearDupOptions{})
	wantDuplicate(t, l, late, early)
	wantLive(t, l, early)
}

func TestNearDupSameJDInTwoCitiesAreSiblings(t *testing.T) {
	l := openLedger(t)
	jd := words("duty", 100)
	ny := ndAdd(t, l, "1", "Acme", "Engineer", "New York, NY", jd, t0)
	sf := ndAdd(t, l, "2", "Acme", "Engineer", "San Francisco, CA", jd, t1)
	sea := ndAdd(t, l, "3", "Acme", "Engineer", "Seattle, WA", jd, t1.Add(time.Hour))
	rep := annotate(t, l, t1, NearDupOptions{})
	if rep.Duplicates != 0 || rep.Siblings != 2 {
		t.Fatalf("report %+v", rep)
	}
	wantLive(t, l, ny, sf, sea)
	for _, k := range []posting.Key{sf, sea} {
		if a := rec(t, l, k).Annotations; a[AnnDupSiblingOf] != string(ny) || a[AnnDupScore] != "1.00" || a[DuplicateOfKey] != "" {
			t.Fatalf("%s annotations %v", k, a)
		}
	}
	if a := rec(t, l, ny).Annotations; a[AnnDupSiblingOf] != "" {
		t.Fatalf("the head carries %v", a)
	}
}

// Cases a bench ledger demoted wrongly before roleTitle and the remote places:
// templated JDs (shingle score 0.9-1.0) for different jobs at one company.
func TestNearDupDifferentRolesOnOneTemplateAreNeverDuplicates(t *testing.T) {
	jd := words("duty", 100)
	for _, tc := range []struct{ name, earlier, later string }{
		{"seniority", "Staff Software Engineer - IAM", "Senior Staff Software Engineer - IAM"},
		{"level dropped", "Software Engineer - Database Engine Internals", "Senior Software Engineer - Database Engine Internals"},
		{"bracketed team", "Solutions Architect - Digital Native Business (Healthtech)", "Solutions Architect - Digital Native Business (Fintech)"},
		{"bracketed term", "Research Intern, Inference (Winter 2027)", "Research Intern, Inference (Summer 2027)"},
		{"bracketed segment", "Delivery Solutions Architect - Public Sector", "Delivery Solutions Architect - Public Sector (Federal)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := openLedger(t)
			first := ndAdd(t, l, "1", "Acme", tc.earlier, "Belgrade, Serbia", jd, t0)
			second := ndAdd(t, l, "2", "Acme", tc.later, "Belgrade, Serbia", jd, t1)
			rep := annotate(t, l, t1, NearDupOptions{})
			if rep.Duplicates != 0 {
				t.Fatalf("report %+v", rep)
			}
			wantLive(t, l, first, second)
			if a := rec(t, l, second).Annotations; a[AnnDupCandidateOf] != string(first) {
				t.Fatalf("want a candidate of %s, got %v", first, a)
			}
		})
	}
}

func TestNearDupRemoteRolesInDifferentPlacesAreSiblings(t *testing.T) {
	jd := words("duty", 100)
	add := func(l ledger.Ledger, id, loc string, at time.Time) posting.Key {
		k, err := posting.GreenhouseKey("acme", id)
		if err != nil {
			t.Fatal(err)
		}
		p := posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Acme", Title: "Applied AI Engineer, Codex", Location: loc, Remote: true, Description: jd}
		if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: at, Key: k, Status: posting.Live, Posting: &p}); err != nil {
			t.Fatal(err)
		}
		return k
	}
	l := openLedger(t)
	london := add(l, "1", "London, UK", t0)
	madrid := add(l, "2", "Madrid, Spain", t1)
	texas := add(l, "3", "Remote - Texas", t1.Add(time.Hour))
	georgia := add(l, "4", "Remote - Georgia", t1.Add(2*time.Hour))
	rep := annotate(t, l, t1, NearDupOptions{})
	if rep.Duplicates != 0 {
		t.Fatalf("remote roles in different places were demoted: %+v", rep)
	}
	wantLive(t, l, london, madrid, texas, georgia)
	if a := rec(t, l, madrid).Annotations; a[AnnDupSiblingOf] != string(london) {
		t.Fatalf("madrid annotations %v", a)
	}
}

func TestNearDupPartialOverlapIsOnlyACandidate(t *testing.T) {
	l := openLedger(t)
	a := ndAdd(t, l, "1", "Acme", "Engineer", "Remote", words("duty", 100), t0)
	b := ndAdd(t, l, "2", "Acme", "Engineer", "Remote", shares(80, 100), t1) // 80 shared words: about 0.66
	c := ndAdd(t, l, "3", "Acme", "Engineer", "Remote", shares(50, 100), t1.Add(time.Hour))
	rep := annotate(t, l, t1, NearDupOptions{})
	if rep.Duplicates != 0 || rep.Candidates != 1 {
		t.Fatalf("report %+v", rep)
	}
	wantLive(t, l, a, b, c)
	an := rec(t, l, b).Annotations
	if an[AnnDupCandidateOf] != string(a) || an[AnnDupScore] < "0.60" || an[AnnDupScore] >= "0.90" {
		t.Fatalf("candidate annotations %v", an)
	}
	if got := rec(t, l, c).Annotations; got[AnnDupCandidateOf] != "" || got[AnnDupScore] != "" {
		t.Fatalf("a 0.3 overlap was annotated: %v", got)
	}
}

func TestNearDupNeedsRealDescriptionsOnBothSides(t *testing.T) {
	long := words("duty", 100)
	short := words("duty", NearDupMinWords-1)
	for _, tc := range []struct{ name, a, b string }{
		{"both empty", "", ""},
		{"both short and equal", short, short},
		{"one empty", long, ""},
		{"one short", long, short},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := openLedger(t)
			ndAdd(t, l, "1", "Acme", "Engineer", "Remote", tc.a, t0)
			ndAdd(t, l, "2", "Acme", "Engineer", "Remote", tc.b, t1)
			before := l.All()
			rep := annotate(t, l, t1, NearDupOptions{})
			if rep.Compared != 0 || rep.Skipped != 1 || rep.Written != 0 || !reflect.DeepEqual(l.All(), before) {
				t.Fatalf("report %+v", rep)
			}
		})
	}
}

func TestNearDupLeavesUnrelatedPostingsAlone(t *testing.T) {
	jd := words("duty", 100)
	l := openLedger(t)
	ndAdd(t, l, "1", "Acme", "Senior Engineer", "Remote", jd, t0)
	ndAdd(t, l, "2", "Globex", "Senior Engineer", "Remote", jd, t1)                       // another company
	ndAdd(t, l, "3", "Acme", "Engineer", "Remote", jd, t1)                                // title overlap 0.5
	ndAdd(t, l, "4", "Acme", "Senior Data Scientist", "Remote", jd, t1)                   // another role
	ndAdd(t, l, "5", "Acme", "Senior Engineer Frontend", "Remote", jd, t1.Add(time.Hour)) // 2/3
	before := l.All()
	rep := annotate(t, l, t1, NearDupOptions{})
	if rep.Written != 0 || rep.Compared != 0 || !reflect.DeepEqual(l.All(), before) {
		t.Fatalf("report %+v", rep)
	}
}

func TestNearDupNeverDemotesAMarkedOrDigestedPosting(t *testing.T) {
	jd := words("duty", 100)
	t.Run("marked", func(t *testing.T) {
		l := openLedger(t)
		first := ndAdd(t, l, "1", "Acme", "Engineer", "Remote", jd, t0)
		marked := ndAdd(t, l, "2", "Acme", "Engineer", "Remote", jd, t1)
		rep := annotate(t, l, t1, NearDupOptions{Protected: func(k posting.Key) bool { return k == marked }})
		if rep.Duplicates != 0 || rep.Candidates != 1 {
			t.Fatalf("report %+v", rep)
		}
		wantLive(t, l, first, marked)
		if got := rec(t, l, marked).Annotations; got[AnnDupCandidateOf] != string(first) || got[AnnDupScore] != "1.00" {
			t.Fatalf("annotations %v", got)
		}
	})
	t.Run("digested", func(t *testing.T) {
		l := openLedger(t)
		first := ndAdd(t, l, "1", "Acme", "Engineer", "Remote", jd, t0)
		second := ndAdd(t, l, "2", "Acme", "Engineer", "Remote", jd, t1)
		if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t1, Key: second, Status: posting.Digested}); err != nil {
			t.Fatal(err)
		}
		before := l.All()
		rep := annotate(t, l, t1, NearDupOptions{})
		if rep.Records != 1 || rep.Written != 0 || !reflect.DeepEqual(l.All(), before) {
			t.Fatalf("report %+v", rep)
		}
		wantLive(t, l, first)
	})
}

func TestNearDupDeadlineIsReportedAsPartial(t *testing.T) {
	l := openLedger(t)
	jd := words("duty", 100)
	ndAdd(t, l, "1", "Acme", "Engineer", "Remote", jd, t0)
	dup := ndAdd(t, l, "2", "Acme", "Engineer", "Remote", jd, t1)
	before := l.All()
	rep, err := AnnotateNearDuplicates(ctx, l, t1, NearDupOptions{Deadline: -time.Second})
	if err != nil {
		t.Fatalf("a deadline is a partial run, not an error: %v", err)
	}
	if !rep.Partial || rep.PartialInSimilarity || rep.Examined != 0 || rep.Records != 2 || rep.Written != 0 || !reflect.DeepEqual(l.All(), before) {
		t.Fatalf("report %+v", rep)
	}
	// Run to completion the same ledger is not partial, and the duplicate lands.
	rep = annotate(t, l, t1, NearDupOptions{})
	if rep.Partial || rep.Examined != 2 || rep.Duplicates != 1 {
		t.Fatalf("full run %+v", rep)
	}
	if rec(t, l, dup).Status != posting.Duplicate {
		t.Fatal("the duplicate did not land")
	}
}

func TestNearDupCancelledContextIsAnError(t *testing.T) {
	l := openLedger(t)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := AnnotateNearDuplicates(cctx, l, t1, NearDupOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestNearDupRerunWritesNothing(t *testing.T) {
	l := openLedger(t)
	jd := words("duty", 100)
	ndAdd(t, l, "1", "Acme", "Engineer", "Remote", jd, t0)
	ndAdd(t, l, "2", "Acme", "Engineer", "Remote", jd, t1)                             // duplicate
	ndAdd(t, l, "3", "Acme", "Engineer", "Austin, TX", jd, t1.Add(time.Hour))          // sibling
	ndAdd(t, l, "4", "Acme", "Engineer", "Remote", shares(80, 100), t1.Add(time.Hour)) // candidate
	first := annotate(t, l, t1, NearDupOptions{})
	if first.Written != 3 || first.Duplicates != 1 || first.Siblings != 1 || first.Candidates != 1 {
		t.Fatalf("first run %+v", first)
	}
	after := l.All()
	again := annotate(t, l, t1.Add(time.Hour), NearDupOptions{})
	if again.Written != 0 || !reflect.DeepEqual(l.All(), after) {
		t.Fatalf("second run %+v", again)
	}
	if again.Siblings != 1 || again.Candidates != 1 {
		t.Fatalf("a rerun must still report what it found: %+v", again)
	}
}

func TestNearDupForgetsAnAnnotationWhoseTargetLeft(t *testing.T) {
	l := openLedger(t)
	jd := words("duty", 100)
	ny := ndAdd(t, l, "1", "Acme", "Engineer", "New York, NY", jd, t0)
	sf := ndAdd(t, l, "2", "Acme", "Engineer", "San Francisco, CA", jd, t1)
	annotate(t, l, t1, NearDupOptions{})
	if rec(t, l, sf).Annotations[AnnDupSiblingOf] != string(ny) {
		t.Fatal("no sibling annotation to forget")
	}
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t1, Key: ny, Status: posting.Dead}); err != nil {
		t.Fatal(err)
	}
	annotate(t, l, t1, NearDupOptions{})
	if got := rec(t, l, sf).Annotations; got[AnnDupSiblingOf] != "" || got[AnnDupScore] != "" {
		t.Fatalf("stale annotations %v", got)
	}
}

func TestNearDupSimilarityOnlyAnnotatesCandidates(t *testing.T) {
	setup := func(t *testing.T) (*ledger.File, posting.Key) {
		l := openLedger(t)
		ndAdd(t, l, "1", "Acme", "Engineer", "Remote", words("duty", 100), t0)
		return l, ndAdd(t, l, "2", "Acme", "Engineer", "Remote", shares(80, 100), t1)
	}
	t.Run("not configured", func(t *testing.T) {
		l, _ := setup(t)
		if rep := annotate(t, l, t1, NearDupOptions{}); rep.SimilarityConfigured {
			t.Fatalf("report %+v", rep)
		}
	})
	t.Run("a reading", func(t *testing.T) {
		l, k := setup(t)
		calls := 0
		rep := annotate(t, l, t1, NearDupOptions{Similarity: func(_ context.Context, a, b string) (float64, bool, error) {
			calls++
			return 0.97654, true, nil
		}})
		if !rep.SimilarityConfigured || calls != 1 || rec(t, l, k).Annotations[AnnDupCosine] != "0.977" || rec(t, l, k).Status != posting.Live {
			t.Fatalf("report %+v, %d calls, %v", rep, calls, rec(t, l, k).Annotations)
		}
	})
	t.Run("failures are counted, never fatal", func(t *testing.T) {
		for name, f := range map[string]func(context.Context, string, string) (float64, bool, error){
			"error":   func(context.Context, string, string) (float64, bool, error) { return 0, false, errors.New("down") },
			"no data": func(context.Context, string, string) (float64, bool, error) { return 0.99, false, nil },
		} {
			l, k := setup(t)
			rep := annotate(t, l, t1, NearDupOptions{Similarity: f})
			if rep.SimilarityErrors != 1 || rec(t, l, k).Annotations[AnnDupCosine] != "" || rec(t, l, k).Annotations[AnnDupCandidateOf] == "" {
				t.Fatalf("%s: report %+v, %v", name, rep, rec(t, l, k).Annotations)
			}
		}
	})
}

func TestNearDupBoundsCandidatesPerPosting(t *testing.T) {
	l := openLedger(t)
	const n = NearDupMaxCandidates + 10
	for i := range n { // same title, unrelated descriptions
		ndAdd(t, l, fmt.Sprint(i), "Acme", "Engineer", "Remote", words(fmt.Sprintf("w%d_", i), 40), t0.Add(time.Duration(i)*time.Minute))
	}
	rep := annotate(t, l, t1, NearDupOptions{})
	wantCompared, wantCapped := 0, 0
	for i := range n {
		wantCompared += min(i, NearDupMaxCandidates)
		if i > NearDupMaxCandidates {
			wantCapped++
		}
	}
	if rep.Compared != wantCompared || rep.CandidatesCapped != wantCapped || rep.Written != 0 || rep.Partial {
		t.Fatalf("report %+v, want %d compared and %d capped", rep, wantCompared, wantCapped)
	}
}

func TestLooseNames(t *testing.T) {
	for in, want := range map[string]string{
		"Acme Inc.":               "acme",
		"ACME, Inc":               "acme",
		"Acme Co., Ltd.":          "acme",
		"Acme GmbH":               "acme",
		"Inc":                     "inc", // never reduced to nothing
		"Co Op Bank":              "co op bank",
		"Ünï Corporation":         "ünï",
		"":                        "",
		strings.Repeat("a", 5000): strings.Repeat("a", maxLooseCompanyBytes),
	} {
		if got := looseCompany(in); got != want {
			t.Errorf("looseCompany(%q) = %q, want %q", truncateBytes(in, 20), got, want)
		}
	}
	for in, want := range map[string]string{
		"Senior Engineer (Remote)":           "senior engineer",
		"Senior Engineer [Berlin] - Hybrid":  "senior engineer",
		"Senior Engineer R-20431":            "senior engineer",
		"Senior Engineer – On-site":          "senior engineer",
		"Senior Engineer - Remote Team Lead": "senior engineer remote team lead", // "remote" only strips as a tail
		"Engineer II":                        "engineer ii",
		"(Remote)":                           "",
	} {
		if got := strings.Join(looseTitleWords(in), " "); got != want {
			t.Errorf("looseTitleWords(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"Senior Engineer (Remote)":                 "senior engineer",
		"Senior Engineer - Hybrid":                 "senior engineer",
		"Senior Engineer R-20431":                  "senior engineer",
		"Senior Staff Software Engineer (Backend)": "senior staff software engineer backend",
		"Senior Staff Software Engineer - Backend": "senior staff software engineer backend",
		"Research Intern (Summer 2027)":            "research intern summer 2027",
	} {
		if got := roleTitle(in); got != want {
			t.Errorf("roleTitle(%q) = %q, want %q", in, got, want)
		}
	}
	if roleTitle("Staff Engineer") == roleTitle("Senior Staff Engineer") || roleTitle("Architect (Federal)") == roleTitle("Architect (SLED)") {
		t.Error("a different seniority or qualifier must be a different role")
	}
	loc := func(l string, remote bool) string { return looseLocation(posting.Posting{Location: l, Remote: remote}) }
	// A remote role keeps its places: a remote posting in Texas is not one in Georgia,
	// and a remote role in London is not the onsite one there.
	if loc("Remote - Texas", false) == loc("Remote - Georgia", false) || loc("London, UK", true) == loc("Madrid, Spain", true) {
		t.Error("remote postings in different places compare equal")
	}
	if loc("Remote - Texas", false) != loc("remote - texas", false) || loc("London, UK", true) == loc("London, UK", false) {
		t.Error("remote places compare wrongly")
	}
	if loc("New York, NY", false) != loc("new  york, NY", false) || loc("New York, NY", false) == loc("San Francisco, CA", false) {
		t.Error("places compare wrongly")
	}
	if loc("", false) != loc("", false) || loc("", false) == loc("Austin, TX", false) {
		t.Error("an unplaced posting must equal only another unplaced one")
	}
}
