package feedback

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func pool(kept, dropped int) []Candidate {
	var cs []Candidate
	for i := 0; i < kept; i++ {
		cs = append(cs, Candidate{Key: posting.Key(fmt.Sprintf("gh:k:%03d", i)), Priority: float64(i % 100)})
	}
	for i := 0; i < dropped; i++ {
		cl := []string{"title:role_fit", "title:seniority", "liquid/d1-ultra:seniority"}[i%3]
		cs = append(cs, Candidate{Key: posting.Key(fmt.Sprintf("gh:d:%03d", i)), Dropped: true, DropClass: cl})
	}
	return cs
}

func TestSampleStratifies(t *testing.T) {
	picks := Sample(pool(200, 60), nil, 20, "2026-W43")
	if len(picks) != 20 {
		t.Fatalf("%d picks", len(picks))
	}
	deciles, classes := map[string]int{}, map[string]int{}
	for _, p := range picks {
		switch {
		case strings.HasPrefix(p.Source, "decile:"):
			deciles[p.Source]++
		case strings.HasPrefix(p.Source, "dropped:"):
			classes[p.Source]++
		default:
			t.Fatalf("source %q", p.Source)
		}
	}
	if len(deciles) != 10 || len(classes) != 3 {
		t.Fatalf("deciles %v classes %v: want all ten deciles and all three classes", deciles, classes)
	}
	if n := classes["dropped:title:role_fit"] + classes["dropped:title:seniority"] + classes["dropped:liquid/d1-ultra:seniority"]; n != 6 {
		t.Fatalf("%d dropped picks, want 6 of 20", n)
	}
	if picks[0].Source != "decile:10" {
		t.Fatalf("first pick %q, want the top decile first", picks[0].Source)
	}
	// The same week gives the same batch; another week another.
	if !slices.Equal(Sample(pool(200, 60), nil, 20, "2026-W43"), picks) {
		t.Fatal("the batch changed within a week")
	}
	if slices.Equal(Sample(pool(200, 60), nil, 20, "2026-W44"), picks) {
		t.Fatal("the batch is the same in another week")
	}
}

func TestSampleSkipsJudgedDuplicatesAndBadKeys(t *testing.T) {
	cs := append(pool(10, 0), Candidate{Key: "gh:k:000"}, Candidate{Key: ""}, Candidate{Key: "a\nb"})
	judged := map[posting.Key]bool{"gh:k:001": true}
	picks := Sample(cs, judged, 50, "w")
	if len(picks) != 9 {
		t.Fatalf("%d picks, want the 9 unjudged distinct valid keys", len(picks))
	}
	seen := map[posting.Key]bool{}
	for _, p := range picks {
		if p.Key == "gh:k:001" || seen[p.Key] || p.Key == "" {
			t.Fatalf("picked %q", p.Key)
		}
		seen[p.Key] = true
	}
}

// A short pool lends its places; empty pools and silly sizes are safe.
func TestSampleEdges(t *testing.T) {
	if got := Sample(pool(3, 50), nil, 20, "w"); len(got) != 20 {
		t.Fatalf("short kept pool: %d picks", len(got))
	}
	if got := Sample(pool(50, 0), nil, 20, "w"); len(got) != 20 {
		t.Fatalf("no dropped: %d picks", len(got))
	}
	if got := Sample(nil, nil, 20, "w"); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
	if got := Sample(pool(10, 10), nil, -5, "w"); len(got) != 0 {
		t.Fatalf("negative n: %v", got)
	}
	if got := Sample(pool(500, 500), nil, 1_000_000, "w"); len(got) != MaxSample {
		t.Fatalf("huge n: %d", len(got))
	}
	long := Candidate{Key: "gh:d:x", Dropped: true, DropClass: strings.Repeat("c", 200)}
	if got := Sample([]Candidate{long}, nil, 5, "w"); len(got) != 1 || len(got[0].Source) > maxNameBytes {
		t.Fatalf("long class: %+v", got)
	}
}

func TestSummarize(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	var js []Judgment
	for i := 0; i < 6; i++ {
		v := VerdictNo
		if i < 2 {
			v = VerdictYes
		}
		js = append(js, Judgment{At: now, Key: posting.Key(fmt.Sprintf("d%d", i)), Verdict: v, Source: "dropped:title:role_fit"})
	}
	js = append(js,
		Judgment{At: now, Key: "t1", Verdict: VerdictYes, Source: "decile:10"},
		Judgment{At: now.Add(-30 * 24 * time.Hour), Key: "t2", Verdict: VerdictNo, Source: "decile:10"},
		Judgment{At: now.Add(-time.Hour), Key: "t2", Verdict: VerdictMaybe, Source: "decile:10"}, // re-judged: replaces
		Judgment{At: now, Key: "e1", Verdict: VerdictSkip, Source: SourceExplore},
	)
	s := Summarize(js, now)
	if s.Judged != 9 || s.Usable != 8 || s.ByVerdict[VerdictMaybe] != 1 || s.ByVerdict[VerdictNo] != 4 {
		t.Fatalf("summary %+v", s)
	}
	fd := s.FalseDropByClass["title:role_fit"]
	if fd.Judged != 6 || fd.Hits != 2 || fd.Rate == nil || *fd.Rate != 2.0/6 {
		t.Fatalf("false drops %+v", fd)
	}
	if d := s.WouldApplyByDecile["10"]; d.Judged != 2 || d.Hits != 2 || d.Rate != nil {
		t.Fatalf("decile 10 %+v: two judged is unmeasured, not a rate", d)
	}
	if s.Explore.Judged != 0 {
		t.Fatalf("a skip counted: %+v", s.Explore)
	}
	if s.CalibrationReady || s.WeightFitReady {
		t.Fatal("ready with 8 labels")
	}
	if s.ThisWeek != 9 {
		t.Fatalf("this week %d", s.ThisWeek)
	}
}

func TestSummarizeThresholds(t *testing.T) {
	now := time.Now()
	var js []Judgment
	for i := 0; i < WeightFitMin; i++ {
		js = append(js, Judgment{At: now, Key: posting.Key(fmt.Sprintf("k%d", i)), Verdict: VerdictNo})
	}
	if s := Summarize(js[:CalibrationMin-1], now); s.CalibrationReady {
		t.Fatal("calibration ready one short")
	}
	if s := Summarize(js[:CalibrationMin], now); !s.CalibrationReady || s.WeightFitReady {
		t.Fatalf("at %d: %+v", CalibrationMin, s)
	}
	if s := Summarize(js, now); !s.WeightFitReady {
		t.Fatal("weight fit not ready at threshold")
	}
	// Skips never count toward a threshold.
	for i := range js {
		js[i].Verdict = VerdictSkip
	}
	if s := Summarize(js, now); s.CalibrationReady || s.Usable != 0 {
		t.Fatalf("skips counted: %+v", s)
	}
}
