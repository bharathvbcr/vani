package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

const (
	nearText = "Requirements: kubernetes clusters, golang operators, platform tooling, service mesh, observability pipelines."
	farText  = "Requirements: accounting ledgers, payroll invoices, quarterly audit spreadsheets, vendor reconciliation."
	// overlaySeed's week opens on 2026-09-28; the seeded labels are older.
	overlaySeed = "2026-W40"
)

var overlayLabelledAt = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)

// overlayState seeds a state dir for the overlay draws: six yes answers and six
// no answers (decile labels from before overlaySeed's week) on postings whose
// text is near (yes) or far (no) from the platform text; 60 unlabelled near
// postings, the even ones engineering-titled; 20 unlabelled far ones; and a
// repost of a yes posting (same title and company). Every posting's vectors
// are warmed with the fake embedder, so the taste column passes: nearness to
// a yes orders the answers, while every title scores alike.
func overlayState(t *testing.T, warm bool) string {
	t.Helper()
	_, state := setup(t)
	mustCode(t, 0, "init")
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	var docs []semantic.Doc
	rank := 0
	add := func(key, title, desc string) {
		rank++
		if !strings.Contains(desc, "gh:") {
			desc += " " + key
		}
		p := posting.Posting{Key: posting.Key(key), ATS: "gh", Company: "Acme", Title: title, Description: desc}
		e := ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Digested, Posting: &p, Annotations: map[string]string{ledger.AnnRank: fmt.Sprint(rank),
			"triage.role_fit": "strong_fit", "triage.role_fit.reading": "answered", "triage.role_fit.backend": "d1-ultra", "triage.role_fit.p": "0.9000"}}
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		docs = append(docs, semantic.Doc{Key: key, Title: p.Title, Description: p.Description})
	}
	for i := 0; i < 6; i++ {
		add(fmt.Sprintf("gh:y:%03d", i), fmt.Sprintf("Role y%d", i), nearText)
		add(fmt.Sprintf("gh:n:%03d", i), fmt.Sprintf("Role n%d", i), farText)
	}
	for i := 0; i < 60; i++ {
		title := fmt.Sprintf("Role k%d", i)
		if i%2 == 0 {
			title = fmt.Sprintf("Software Engineer k%d", i)
		}
		add(fmt.Sprintf("gh:k:%03d", i), title, nearText)
	}
	for i := 0; i < 20; i++ {
		add(fmt.Sprintf("gh:f:%03d", i), fmt.Sprintf("Role f%d", i), farText)
	}
	add("gh:r:000", "Role y0", nearText+" gh:y:000") // a repost of gh:y:000, its text too: the nearest of all to that anchor
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := eval.NewStore(filepath.Join(state, "eval", "labels"), filepath.Join(state, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		for _, x := range []struct{ prefix, v string }{{"gh:y", "yes"}, {"gh:n", "no"}} {
			k := posting.Key(fmt.Sprintf("%s:%03d", x.prefix, i))
			if err := s.Put(eval.Entry{Key: k, Labels: map[string]string{eval.WouldApply: x.v}, Source: "decile:5", PSelect: 0.5, LabeledAt: overlayLabelledAt}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if warm {
		// The profile shares words with the near text, so the warm's
		// calibration has a spread to anchor on.
		prof := semantic.Profile{Chunks: []semantic.Chunk{{Label: "skills", Text: "kubernetes clusters golang operators", Weight: 1}, {Label: "role", Text: "platform tooling engineer", Weight: 1}}, Target: "platform engineer"}
		m, err := semantic.NewMatcher(semantic.NewFakeEmbedder(64, nil), prof, semanticDir(state))
		if err != nil {
			t.Fatal(err)
		}
		if rep, err := m.Warm(context.Background(), docs, 1000); err != nil {
			t.Fatalf("Warm: %v %+v", err, rep)
		}
	}
	return state
}

// noOverlays turns every overlay off, for a test about the random draw alone:
// each overlay is on by default.
var noOverlays = []string{"--disagree=0", "--disagree-rejects=0", "--yes-neighbours=0", "--engineering=0"}

// withNoOverlays is args with every overlay off; a later flag in args wins.
func withNoOverlays(args ...string) []string {
	return append(slices.Clone(noOverlays), args...)
}

func overlayDraw(t *testing.T, args ...string) auditJSON {
	t.Helper()
	o, _ := mustCode(t, 0, append([]string{"--json", "audit", "--seed", overlaySeed}, args...)...)
	var d auditJSON
	decodeOne(t, o, &d)
	return d
}

// randomOf is the picks of a draw from the random strata, sorted by key.
func randomOf(d auditJSON) []auditPickJSON {
	out := slices.DeleteFunc(slices.Clone(d.Picks), func(p auditPickJSON) bool {
		return eval.SelectedOnReading(p.Stratum) || eval.EngineeringStratum(p.Stratum)
	})
	slices.SortFunc(out, func(a, b auditPickJSON) int { return strings.Compare(string(a.Key), string(b.Key)) })
	return out
}

// --yes-neighbours draws from the unlabelled postings nearest the owner's yes
// answers, over a random draw that is the same without it; a far posting, a
// labelled one and a repost of a labelled one are never in the pool.
func TestAuditYesNeighboursDrawNearTheYesAnswers(t *testing.T) {
	overlayState(t, true)
	base := overlayDraw(t, "--scored", "10", "--dropped", "0", "--closed", "0")
	with := overlayDraw(t, "--scored", "10", "--dropped", "0", "--closed", "0", "--yes-neighbours", "20")
	if !slices.Equal(randomOf(with), randomOf(base)) {
		t.Fatalf("the random picks changed with --yes-neighbours:\n got %v\nwant %v", randomOf(with), randomOf(base))
	}
	var near []auditPickJSON
	for _, p := range with.Picks {
		if p.Stratum == eval.CellYesNeighbour {
			near = append(near, p)
		}
	}
	if len(near) != 20 {
		t.Fatalf("%d yes-neighbour picks, want 20: %v", len(near), with.Picks)
	}
	for _, p := range near {
		if !strings.HasPrefix(string(p.Key), "gh:k:") {
			t.Errorf("%s drawn as a yes neighbour (far, labelled or a repost)", p.Key)
		}
		if !(p.PSelect > 0 && p.PSelect <= 20.0/40) {
			t.Errorf("%s p_select %v: want 20 over a pool of at least 40", p.Key, p.PSelect)
		}
	}
	for _, st := range with.Strata {
		if st.Stratum == eval.CellYesNeighbour && (st.Pool < 40 || st.Pool > 50) {
			t.Errorf("yes-neighbour pool %d: want the 50 nearest less the random picks among them", st.Pool)
		}
	}
	// A label drawn there is counted apart from every estimate, and in the yield.
	p := near[0]
	mustCode(t, 0, "audit", "answer", "--key", string(p.Key), "--verdict", "yes", "--source", p.Stratum, "--p-select", fmt.Sprint(p.PSelect))
	o, _ := mustCode(t, 0, "--json", "relevance")
	var rep relevanceReply
	decodeOne(t, o, &rep)
	if rep.Relevance.Selected != 1 || rep.YesYield == nil || len(rep.YesYield.Cells) != 1 || rep.YesYield.Cells[0].Cell != eval.CellYesNeighbour {
		t.Errorf("relevance selected %d, yield %+v", rep.Relevance.Selected, rep.YesYield)
	}
	if rep.Taste == nil || !rep.Taste.Passes {
		t.Errorf("the relevance report's taste column %+v does not pass, though the draw's gate did", rep.Taste)
	}
}

// The pool itself: the 50 nearest unlabelled near postings, never a far one, a
// labelled one, a repost of a labelled one (here the nearest of all to its
// anchor) or one the overlays may not draw.
func TestYesNeighbourPoolMembers(t *testing.T) {
	state := overlayState(t, true)
	e := &env{stateDir: state, stderr: &strings.Builder{}}
	b, err := e.browse()
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.openEvalStore("")
	if err != nil {
		t.Fatal(err)
	}
	done, _, err := s.Labels()
	if err != nil {
		t.Fatal(err)
	}
	var cands []eval.AuditCandidate
	for _, r := range b.snap.Records {
		c := eval.AuditCandidate{Key: r.Key, Score: 1}
		if r.Key == "gh:k:001" {
			c = eval.AuditCandidate{Key: r.Key, Closed: true, Class: "dead"} // not drawable by an overlay
		}
		cands = append(cands, c)
	}
	if err := e.markYesNeighbours(cands, done, b.snap, overlaySeed); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range cands {
		if !c.NearYes {
			continue
		}
		n++
		if !strings.HasPrefix(string(c.Key), "gh:k:") || c.Closed {
			t.Errorf("%+v is in the pool", c)
		}
	}
	if n != 50 {
		t.Errorf("pool %d, want %d", n, 50)
	}
}

// Anchors are yes answers from before the seed's week: with every yes answer
// made after it opened there is nothing to be near, an error and no draw.
func TestAuditYesNeighboursAnchorBeforeTheWeek(t *testing.T) {
	overlayState(t, true)
	_, e := mustCode(t, 1, "audit", "--list", "--seed", "2026-W01", "--yes-neighbours", "5")
	if !strings.Contains(e, "no yes answer from before 2026-W01") {
		t.Fatalf("stderr %q", e)
	}
}

// The cell is refused unless the taste column passes, and needs a vector
// store. Asked for explicitly, either missing is an error; at its default
// quota the cell is skipped by name and the rest of the draw goes ahead.
func TestAuditYesNeighboursRefusedWithoutTheTasteGate(t *testing.T) {
	state := overlayState(t, false)
	_, e := mustCode(t, 1, "audit", "--list", "--seed", overlaySeed, "--yes-neighbours", "5")
	if !strings.Contains(e, "there is no vector store yet") {
		t.Fatalf("no store, explicit: stderr %q", e)
	}
	if o, _ := mustCode(t, 0, "audit", "--list", "--seed", overlaySeed); !strings.Contains(o, "overlay not drawn: yes-neighbour cell: audit --yes-neighbours: there is no vector store yet") {
		t.Fatalf("no store, default: %s", o)
	}
	// With the overlays off the vector store is never needed.
	mustCode(t, 0, append([]string{"audit", "--list", "--seed", overlaySeed}, noOverlays...)...)

	// Vectors in place, but four yes answers: the taste column is
	// insufficient below five, so it does not pass.
	state = overlayState(t, true)
	labelsDir, textDir := filepath.Join(state, "eval", "labels"), filepath.Join(state, "eval", "text")
	if err := os.RemoveAll(labelsDir); err != nil {
		t.Fatal(err)
	}
	s, err := eval.NewStore(labelsDir, textDir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		for _, x := range []struct{ prefix, v string }{{"gh:y", "yes"}, {"gh:n", "no"}} {
			if x.v == "yes" && i >= 4 {
				continue
			}
			k := posting.Key(fmt.Sprintf("%s:%03d", x.prefix, i))
			if err := s.Put(eval.Entry{Key: k, Labels: map[string]string{eval.WouldApply: x.v}, Source: "decile:5", PSelect: 0.5, LabeledAt: overlayLabelledAt}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, e := mustCode(t, 1, "audit", "--list", "--seed", overlaySeed, "--yes-neighbours", "5"); !strings.Contains(e, "taste column does not pass") {
		t.Fatalf("four yes, explicit: stderr %q", e)
	}
	if o, _ := mustCode(t, 0, "audit", "--list", "--seed", overlaySeed); !strings.Contains(o, "overlay not drawn: yes-neighbour cell: audit --yes-neighbours: refused, the taste column does not pass") {
		t.Fatalf("four yes, default: %s", o)
	}
}

// --engineering draws at random from the engineering-titled postings, over the
// same random draw; a label drawn there stays out of the weight fit, counted.
func TestAuditEngineeringDrawsEngineeringTitles(t *testing.T) {
	overlayState(t, false)
	if !eval.TitleIsEngineering("Software Engineer k0") || eval.TitleIsEngineering("Role k1") {
		t.Fatal("fixture titles do not split on eval.TitleIsEngineering")
	}
	base := overlayDraw(t, withNoOverlays("--scored", "10", "--dropped", "0", "--closed", "0")...)
	with := overlayDraw(t, withNoOverlays("--scored", "10", "--dropped", "0", "--closed", "0", "--engineering", "100")...)
	if !slices.Equal(randomOf(with), randomOf(base)) {
		t.Fatalf("the random picks changed with --engineering:\n got %v\nwant %v", randomOf(with), randomOf(base))
	}
	random := map[posting.Key]bool{}
	for _, p := range base.Picks {
		random[p.Key] = true
	}
	want := 0
	for i := 0; i < 60; i += 2 {
		if !random[posting.Key(fmt.Sprintf("gh:k:%03d", i))] {
			want++
		}
	}
	var eng []auditPickJSON
	for _, p := range with.Picks {
		if eval.EngineeringStratum(p.Stratum) {
			eng = append(eng, p)
			var n int
			if c, _ := fmt.Sscanf(string(p.Key), "gh:k:%03d", &n); c != 1 || n%2 != 0 || p.Stratum != eval.EngineeringKept {
				t.Errorf("%s drawn as %s: not an engineering-titled kept posting", p.Key, p.Stratum)
			}
		}
	}
	if len(eng) != want {
		t.Fatalf("%d engineering picks, want every unpicked engineering posting (%d)", len(eng), want)
	}
	p := eng[0]
	mustCode(t, 0, "audit", "answer", "--key", string(p.Key), "--verdict", "yes", "--source", p.Stratum, "--p-select", fmt.Sprint(p.PSelect))
	o, _ := mustCode(t, 0, "rankfit", "--dry-run")
	if !strings.Contains(o, "engineering group left out of the fit (until its weights are post-stratified): 1") {
		t.Errorf("rankfit does not count the engineering label apart:\n%s", o)
	}
}

func TestAuditOverlayFlagBounds(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 10, 2, 2)
	for _, args := range [][]string{
		{"--yes-neighbours", "-1"},
		{"--engineering", "-1"},
		{"--engineering", fmt.Sprint(maxAuditQuota + 1)},
		{"--scored", "1", "--dropped", "0", "--closed", "0", "--disagree", "1", "--yes-neighbours", "2"}, // the neighbour cell counts in the cap
	} {
		mustCode(t, 2, append([]string{"audit", "--list"}, args...)...)
		mustCode(t, 2, append([]string{"audit", "answer", "--key", "gh:k:001", "--verdict", "no"}, args...)...)
	}
	// Engineering places count on neither side of the cap.
	mustCode(t, 0, "audit", "--list", "--scored", "1", "--dropped", "0", "--closed", "0", "--engineering", "50")
}

// The overlays share one eligibility rule: never a closed posting, nor a
// dropped one the auditor leaves out.
func TestOverlayEligible(t *testing.T) {
	for _, c := range []struct {
		c    eval.AuditCandidate
		want bool
	}{
		{eval.AuditCandidate{Key: "a", Score: 1}, true},
		{eval.AuditCandidate{Key: "b", Dropped: true, Class: "title:role_fit"}, true},
		{eval.AuditCandidate{Key: "c", Closed: true, Class: "dead"}, false},
		{eval.AuditCandidate{Key: "d", Dropped: true, Class: "exclude:title"}, false},
		{eval.AuditCandidate{Key: "e", Dropped: true, Class: "model:work_mode"}, false},
		{eval.AuditCandidate{Key: "f", Dropped: true, Class: "title:sponsorship"}, false},
	} {
		if got := overlayEligible(c.c); got != c.want {
			t.Errorf("%+v: %v, want %v", c.c, got, c.want)
		}
	}
}
