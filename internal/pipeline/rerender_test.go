package pipeline

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// derived are the triage, scoring and explanation results a digested posting
// carries; a change of the posting's content forgets them, a re-render keeps them.
var derived = map[string]string{"triage.remote": "yes", "triage.backend": "d1-ultra", "score.total": "5",
	"gap.coverage_pct": "9", "explanation": "good fit", "rank": "62.5"}

// digestedWith files p, digests it and gives it the derived annotations.
func digestedWith(t *testing.T, l ledger.Ledger, p posting.Posting) {
	t.Helper()
	digestPosting(t, l, p)
	if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: p.Key, Status: posting.Digested,
		Annotations: derived, Reason: "annotate"}); err != nil {
		t.Fatal(err)
	}
}

func keepsDerived(t *testing.T, r ledger.Record) {
	t.Helper()
	for k, v := range derived {
		if r.Annotations[k] != v {
			t.Fatalf("annotation %s = %q, want %q (all: %v)", k, r.Annotations[k], v, r.Annotations)
		}
	}
}

func forgotDerived(t *testing.T, r ledger.Record) {
	t.Helper()
	for k := range derived {
		if _, ok := r.Annotations[k]; ok {
			t.Fatalf("annotation %s survived a content change: %v", k, r.Annotations)
		}
	}
}

// A board adapter that starts rendering pay ahead of the description (as Ashby
// did on 2026-10-19) changes no upstream content: a digested posting stays
// digested with its triage and explanation, and the stored posting takes the
// new rendering so the digest and UI show it.
func TestRenderOnlyChangeKeepsDigestedPosting(t *testing.T) {
	const jd = "Build the thing.\n\nYou have shipped Go."
	cases := []struct{ name, before, after string }{
		{"pay line added", jd, "Pay range: USD 120000 – 150000 per year\n\n" + jd},
		{"compensation summary added", jd, "Compensation: $120K – $150K • Offers Equity\n\n" + jd},
		{"several pay lines added", jd, "Pay range: USD 120000 – 150000 per year\nPay range: CAD 150000 – 180000 per year\n\n" + jd},
		{"pay line changed", "Pay range: USD 1 – 2 per year\n\n" + jd, "Pay range: USD 3 – 4 per year\n\n" + jd},
		{"pay line removed", "Pay range: USD 1 – 2 per year\n\n" + jd, jd},
		{"pay line added with whitespace re-render", jd, "Pay range: USD 1 – 2 per year\n\n\n  Build   the thing.\nYou have shipped Go.\n"},
		{"empty description gains pay line", "", "Pay range: USD 1 – 2 per year"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := openLedger(t)
			p := ghPost(t, "1", c.before)
			digestedWith(t, l, p)
			p2 := p
			p2.Description = c.after
			s, err := Reconcile(ctx, l, board, []posting.Posting{p2}, t0.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			r := rec(t, l, p.Key)
			if s.Changed != 0 || s.Rerendered != 1 || r.Status != posting.Digested {
				t.Fatalf("re-render went back to triage: %+v status %s", s, r.Status)
			}
			keepsDerived(t, r)
			if r.Posting.Description != c.after || r.Annotations[DescHashKey] != p2.DescriptionHash() {
				t.Fatalf("stored posting not re-rendered: %q hash %s", r.Posting.Description, r.Annotations[DescHashKey])
			}
			// The next run sees the new rendering as unchanged.
			s, _ = Reconcile(ctx, l, board, []posting.Posting{p2}, t0.Add(2*time.Hour))
			if s.Unchanged != 1 {
				t.Fatalf("second run: %+v", s)
			}
		})
	}
}

// A real upstream edit still re-triages, with or without a pay line around it,
// and so does pay-shaped text that is not a leading synthesized line.
func TestUpstreamEditStillRetriages(t *testing.T) {
	const jd = "Build the thing."
	pay := "Pay range: USD 1 – 2 per year\n\n"
	cases := []struct{ name, before, after string }{
		{"edit under an added pay line", jd, pay + "Build another thing."},
		{"edit under a kept pay line", pay + jd, pay + "Build another thing."},
		{"edit and pay line removed", pay + jd, "Build another thing."},
		{"pay-shaped text after the body", jd, jd + "\n\nPay range: USD 1 – 2 per year"},
		{"pay-shaped text inside a line", jd, "Our Pay range: USD 1 – 2 per year\n\n" + jd},
		{"description emptied", jd, ""},
		{"description emptied but pay kept", pay + jd, "Pay range: USD 1 – 2 per year"},
		{"empty description filled", "", "Build the thing."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := openLedger(t)
			p := ghPost(t, "1", c.before)
			digestedWith(t, l, p)
			p2 := p
			p2.Description = c.after
			s, err := Reconcile(ctx, l, board, []posting.Posting{p2}, t0.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			r := rec(t, l, p.Key)
			if s.Changed != 1 || r.Status != posting.Live {
				t.Fatalf("upstream edit not re-triaged: %+v status %s", s, r.Status)
			}
			forgotDerived(t, r)
			if r.Annotations[DescHashKey] != p2.DescriptionHash() {
				t.Fatalf("desc hash %s, want %s", r.Annotations[DescHashKey], p2.DescriptionHash())
			}
		})
	}
}

// A whitespace-only re-render of the same rendering is unchanged: no event.
func TestWhitespaceOnlyWithPayLineIsUnchanged(t *testing.T) {
	l := openLedger(t)
	p := ghPost(t, "1", "Pay range: USD 1 – 2 per year\n\nBuild the thing.")
	digestedWith(t, l, p)
	before := rec(t, l, p.Key).LastEvent
	p2 := p
	p2.Description = "Pay range: USD 1 – 2   per year\nBuild  the thing.\n\n"
	s, _ := Reconcile(ctx, l, board, []posting.Posting{p2}, t0.Add(time.Hour))
	if s.Unchanged != 1 || s.Rerendered != 0 || !rec(t, l, p.Key).LastEvent.Equal(before) {
		t.Fatalf("whitespace-only wrote an event: %+v", s)
	}
}

// A dead posting relisted with only a new rendering goes back to the status it
// died in, carrying the new rendering, as a relist of the same content does.
func TestDeadRelistedRerenderedRestoresStatus(t *testing.T) {
	l := openLedger(t)
	p := ghPost(t, "1", "Build the thing.")
	digestedWith(t, l, p)
	Reconcile(ctx, l, board, nil, t0.Add(time.Hour))
	Reconcile(ctx, l, board, nil, t0.Add(2*time.Hour))
	if r := rec(t, l, p.Key); r.Status != posting.Dead {
		t.Fatalf("setup: %s", r.Status)
	}
	p2 := p
	p2.Description = "Pay range: USD 1 – 2 per year\n\nBuild the thing."
	s, _ := Reconcile(ctx, l, board, []posting.Posting{p2}, t0.Add(3*time.Hour))
	r := rec(t, l, p.Key)
	if s.Reappeared != 1 || s.Changed != 0 || r.Status != posting.Digested {
		t.Fatalf("rerendered relist: %+v status %s", s, r.Status)
	}
	keepsDerived(t, r)
	if r.Posting.Description != p2.Description || r.Annotations[DescHashKey] != p2.DescriptionHash() {
		t.Fatalf("stored posting not re-rendered: %q", r.Posting.Description)
	}
}

// A posting missed once and back with only a new rendering keeps its status
// and takes the new rendering and its hash.
func TestMissedRelistedRerenderedKeepsStatus(t *testing.T) {
	l := openLedger(t)
	p := ghPost(t, "1", "Build the thing.")
	digestedWith(t, l, p)
	Reconcile(ctx, l, board, nil, t0.Add(time.Hour))
	p2 := p
	p2.Description = "Pay range: USD 1 – 2 per year\n\nBuild the thing."
	s, _ := Reconcile(ctx, l, board, []posting.Posting{p2}, t0.Add(2*time.Hour))
	r := rec(t, l, p.Key)
	if s.Reappeared != 1 || s.Changed != 0 || r.Status != posting.Digested || r.MissedRuns != 0 {
		t.Fatalf("rerendered reappearance: %+v %+v", s, r)
	}
	keepsDerived(t, r)
	if r.Posting.Description != p2.Description || r.Annotations[DescHashKey] != p2.DescriptionHash() {
		t.Fatalf("stored posting not re-rendered: %q hash %s", r.Posting.Description, r.Annotations[DescHashKey])
	}
}
