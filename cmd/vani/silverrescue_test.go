package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/decide"
)

// silverWorld is the auditor's world with a go verdict (as in
// TestTeacherReportGivesAVerdictOnEnoughPostFreezeLabelsAndOnlyThose) plus
// model drops: wanted titles (gh:dw:N) the fake auditor picks, and unwanted
// ones (gh:du:N) it does not. A random auditor (random true) answers at random.
func silverWorld(t *testing.T, random bool) *psEnv {
	t.Helper()
	// The dropped postings' boards are watched, so a rescue of them waits for
	// the board's next listing, as every reopen of a closed posting does.
	e := psSetup(t, 14, 44, func(c *config.Config) {
		c.Watchlist = append(c.Watchlist, sources.Board{ATS: "gh", Token: "dw"}, sources.Board{ATS: "gh", Token: "du"})
		c.AllowedHosts = append(c.AllowedHosts, "boards-api.greenhouse.io")
	})
	l, err := ledger.Open(filepath.Join(e.state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	drop := map[string]string{"triage.backend": "d1-ultra", "triage.role_fit": "mismatch_domain", "triage.role_fit.reading": "answered", "triage.role_fit.p": "0.9500"}
	// Few wanted drops among many unwanted ones: the fake answers "a" when both
	// postings are wanted, so wanted drops meeting each other would blur scores.
	for i := range 12 {
		for _, k := range []struct{ kind, title string }{{"dw", psWanted}, {"du", psUnwant}} {
			if k.kind == "dw" && i >= 3 {
				continue
			}
			p := posting.Posting{Key: posting.Key(fmt.Sprintf("gh:%s:%d", k.kind, i)), ATS: "gh", Company: "SynthCo", Title: fmt.Sprintf("%s D%d", k.title, i),
				Location: "Remote", Description: fmt.Sprintf("A dropped role %d. More words about the work here.", i)}
			if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0.Add(-day), Key: p.Key, Status: posting.Dropped, Posting: &p, Annotations: drop}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	f := &psFake{}
	if random {
		var n atomic.Int64
		f.reply = func(decide.Question) decide.SlotReading {
			return decide.SlotReading{Reading: decide.Answered, Value: []string{"a", "b", "neither"}[(n.Add(1)*7)%3]}
		}
	}
	f.use(t)
	early := jobsT0.Add(-48 * time.Hour)
	e.label(t, "w", "yes", 0, 3, early)
	e.label(t, "u", "no", 0, 3, early)
	e.freeze(t)
	e.setCap(t, "5")
	late := jobsT0.Add(24 * time.Hour)
	e.label(t, "w", "yes", 3, 14, late)
	e.label(t, "u", "no", 3, 44, late)
	// Dropped postings the owner answered after the freeze: the planner gives
	// each gold posting stratum-mates, so these gather enough comparisons.
	e.label(t, "dw", "yes", 0, 3, late)
	e.label(t, "du", "no", 0, 4, late)
	mustCode(t, 0, "teacher", "--task", "would_apply_pair", "--budget-usd", "4", "--pairs", "500", "--concurrency", "1")
	return e
}

func silverReply(t *testing.T, args ...string) silverDropsReply {
	t.Helper()
	o, _ := mustCode(t, 0, append([]string{"retriage", "--silver-drops", "--json", "--min-games", "3"}, args...)...)
	var r silverDropsReply
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	return r
}

func TestSilverDropsReportsRescuesWithinTheCapAndOnlyOnGo(t *testing.T) {
	e := silverWorld(t, false)
	before := e.tree(t)
	r := silverReply(t)
	if r.Verdict != "go" || len(r.Candidates) == 0 || len(r.Rescued) != 0 {
		t.Fatalf("report: verdict %q, %d candidates, %d rescued", r.Verdict, len(r.Candidates), len(r.Rescued))
	}
	for _, c := range r.Candidates {
		if !strings.HasPrefix(string(c.Key), "gh:dw:") {
			t.Errorf("an unwanted drop is a candidate: %+v", c)
		}
	}
	if r.GoldYes < 2 || r.GoldNo != 0 || r.PrecHi == 0 {
		t.Errorf("precision on gold: %d yes %d no (%v-%v)", r.GoldYes, r.GoldNo, r.PrecLo, r.PrecHi)
	}
	if !strings.Contains(r.ScoreNote, "relative") {
		t.Error("the report does not say the score is stratum-relative")
	}
	dry := silverReply(t, "--rescue", "--dry-run", "--max-per-day", "2")
	if len(dry.Rescued) != 2 {
		t.Errorf("dry run would rescue %d, want 2", len(dry.Rescued))
	}
	snapshotEqual(t, "report and dry run", before, e.tree(t))

	got := silverReply(t, "--rescue", "--max-per-day", "2")
	if len(got.Rescued) != 2 || !slices.Equal(got.Deferred, got.Rescued) || got.Today != 0 {
		t.Fatalf("rescue: %+v", got.SilverRescue)
	}
	// A dropped posting is closed: the rescue cannot know its role is still
	// listed, so each waits, still dropped, as a pending silver reopen.
	snap, err := ledger.ReadSnapshot(filepath.Join(e.state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[posting.Key]ledger.Record{}
	for _, rec := range snap.Records {
		byKey[rec.Key] = rec
	}
	pending, err := pipeline.LoadPendingReopens(filepath.Join(e.state, pipeline.PendingReopenFile), jobsT0)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range got.Rescued {
		rec := byKey[k]
		if rec.Status != posting.Dropped || pending.Items[k].Class != "silver" || !strings.Contains(pending.Items[k].Reason, "score=") {
			t.Errorf("%s after rescue: %s, pending %+v", k, rec.Status, pending.Items[k])
		}
	}
	again := silverReply(t, "--rescue", "--max-per-day", "2")
	if len(again.Rescued) != 0 || again.Today != 2 || again.CapLeft != 0 {
		t.Errorf("second rescue the same day: %+v", again.SilverRescue)
	}
	text, _ := mustCode(t, 0, "retriage", "--silver-drops", "--min-games", "3")
	for _, want := range []string{"Silver rescue", "precision on gold", "report only"} {
		if !strings.Contains(text, want) {
			t.Errorf("text report lacks %q", want)
		}
	}
}

// An auditor whose picks do not follow the owner's gold is not trusted to
// rescue: the report still runs, the rescue is refused and nothing is written.
func TestSilverRescueRefusedWithoutGo(t *testing.T) {
	e := silverWorld(t, true)
	r := silverReply(t)
	if r.Verdict == "go" {
		t.Fatalf("a random auditor got a go")
	}
	before := e.tree(t)
	if _, se := mustCode(t, 1, "retriage", "--silver-drops", "--rescue", "--min-games", "3"); !strings.Contains(se, "verdict") {
		t.Errorf("refusal: %s", se)
	}
	snapshotEqual(t, "a refused rescue", before, e.tree(t))
}

func TestSilverDropsFlagRefusals(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	for _, args := range [][]string{
		{"--silver-drops", "--policy-drops"},
		{"--silver-drops", "--title-drops"},
		{"--rescue"},
		{"--policy-drops", "--rescue"},
		{"--stale-drops"}, // retired: --policy-drops --dry-run reports the same drops by class
		{"--silver-drops", "--min-score", "0.5"},
		{"--silver-drops", "--min-games", "2"},
		{"--silver-drops", "--max-per-day", "0"},
	} {
		mustCode(t, 2, append([]string{"retriage"}, args...)...)
	}
	// No frozen exemplar set, no answers: an error, never an empty report.
	if _, se := mustCode(t, 1, "retriage", "--silver-drops"); se == "" {
		t.Error("no auditor state reported nothing")
	}
}
