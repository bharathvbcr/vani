package pipeline

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func formA() *posting.ApplyForm {
	return &posting.ApplyForm{V: posting.ApplyFormVersion, Questions: 4, Required: 2,
		Gates: []posting.FormGate{{Kind: posting.GateSponsorship, Text: "Will you require sponsorship?", YesNo: true}}}
}

func formB() *posting.ApplyForm {
	f := formA()
	f.Gates[0].Text = "Are you authorized to work without sponsorship?"
	f.Gates[0].Flag = posting.FlagAuthorizedYesNo
	return f
}

// A posting listed before the form was read is stored without one. The next
// listing that reads it stores the summary with no change of status, triage or
// text, and the run after that sees nothing to do.
func TestFormReadForTheFirstTimeRefreshesTheStoredPosting(t *testing.T) {
	l := openLedger(t)
	p := fieldsJob(t)
	digestedWith(t, l, p)
	p2 := p
	p2.Form = formA()
	p2.SeenAt = t0.Add(time.Hour)
	s := reconcileFull(t, l, t0.Add(time.Hour), p2)
	r := rec(t, l, p.Key)
	if s.Refreshed != 1 || s.Unchanged != 0 || s.Changed != 0 || s.Rerendered != 0 || r.Status != posting.Digested {
		t.Fatalf("summary %+v status %s", s, r.Status)
	}
	keepsDerived(t, r)
	if !r.Posting.Form.Equal(formA()) || r.Posting.Description != p.Description || r.Posting.Title != p.Title {
		t.Fatalf("stored %+v", r.Posting)
	}
	if s := reconcileFull(t, l, t0.Add(2*time.Hour), p2); s.Unchanged != 1 || s.Refreshed != 0 {
		t.Fatalf("second run: %+v", s)
	}
}

func TestFormChangeRefreshesTheStoredPosting(t *testing.T) {
	l := openLedger(t)
	p := fieldsJob(t)
	p.Form = formA()
	digestedWith(t, l, p)
	p2 := p
	p2.Form = formB()
	s := reconcileFull(t, l, t0.Add(time.Hour), p2)
	r := rec(t, l, p.Key)
	if s.Refreshed != 1 || !r.Posting.Form.Equal(formB()) || r.Status != posting.Digested {
		t.Fatalf("summary %+v stored %+v", s, r.Posting.Form)
	}
	keepsDerived(t, r)
}

// A listing that did not read the form (a capped or failed read) says nothing
// about it: the stored summary stands, and the listing is unchanged.
func TestUnreadFormNeverErasesTheStoredOne(t *testing.T) {
	l := openLedger(t)
	p := fieldsJob(t)
	p.Form = formA()
	digestedWith(t, l, p)
	p2 := p
	p2.Form = nil
	s := reconcileFull(t, l, t0.Add(time.Hour), p2)
	if s.Unchanged != 1 || s.Refreshed != 0 || !rec(t, l, p.Key).Posting.Form.Equal(formA()) {
		t.Fatalf("summary %+v stored %+v", s, rec(t, l, p.Key).Posting.Form)
	}
	if s := reconcilePart(t, l, t0.Add(2*time.Hour), p2); s.Unchanged != 1 || !rec(t, l, p.Key).Posting.Form.Equal(formA()) {
		t.Fatalf("partial listing: %+v", s)
	}
}

// The same holds when the text changes in the same listing: the new version of
// the posting keeps the form read for the old one until a read replaces it.
func TestUnreadFormSurvivesATextChange(t *testing.T) {
	for name, edit := range map[string]func(*posting.Posting){
		"re-rendered": func(p *posting.Posting) { p.Description = "Build the thing.\n\nPay: USD 100k" },
		"changed":     func(p *posting.Posting) { p.Description = "Build a different thing entirely, with new duties." },
	} {
		t.Run(name, func(t *testing.T) {
			l := openLedger(t)
			p := fieldsJob(t)
			p.Form = formA()
			digestedWith(t, l, p)
			p2 := p
			p2.Form = nil
			edit(&p2)
			reconcileFull(t, l, t0.Add(time.Hour), p2)
			r := rec(t, l, p.Key)
			if r.Posting.Description != p2.Description || !r.Posting.Form.Equal(formA()) {
				t.Fatalf("stored %q %+v", r.Posting.Description, r.Posting.Form)
			}
		})
	}
}

func TestNewPostingStoresItsForm(t *testing.T) {
	l := openLedger(t)
	p := ghPost(t, "7", "A role.")
	p.Form = formB()
	if s := reconcileFull(t, l, t0, p); s.New != 1 {
		t.Fatalf("%+v", s)
	}
	if got := rec(t, l, p.Key).Posting.Form; !got.Equal(formB()) {
		t.Fatalf("%+v", got)
	}
	// A new posting whose form was not read is stored without one: nil, not an
	// empty summary.
	q := ghPost(t, "8", "Another role.")
	if s := reconcileFull(t, l, t0, p, q); s.New != 1 {
		t.Fatalf("%+v", s)
	}
	if got := rec(t, l, q.Key).Posting.Form; got != nil {
		t.Fatalf("an unread form was stored as %+v", got)
	}
}
