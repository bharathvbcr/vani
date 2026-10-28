package ledger

import (
	"context"
	"reflect"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// A posting's application-form summary is stored with the posting: it survives
// a reopen, a scoped read that leaves the description out, and a later event
// that carries no posting. Its text is employer text, so the fixture holds
// the shapes that fool a scan for the "description" field.
func TestApplyFormSurvivesTheLedger(t *testing.T) {
	form := &posting.ApplyForm{
		V: posting.ApplyFormVersion, Questions: 4, Required: 3, Essays: 1, Uploads: []string{"Cover letter"},
		Gates: []posting.FormGate{{
			Kind: posting.GateSponsorship, Text: `Do you need "description":"evil" sponsorship? {"posting":{"description":"x"}}`,
			Options: []string{`a "description":"b"`, "Yes", "No"}, Flag: posting.FlagNoSponsorAnswer,
		}},
	}
	dir := t.TempDir()
	f := mustOpen(t, dir)
	p := posting.Posting{Key: "ab:acme:1", Company: "Acme", Title: "T", URL: "https://x/1", Description: "the body", Form: form}
	e := ev("ab:acme:1", posting.Triaged, t0)
	e.Posting = &p
	if err := f.Append(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := f.Append(context.Background(), ev("ab:acme:1", posting.Scored, t0.Add(1))); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	snap, err := ReadSnapshot(dir)
	if err != nil || len(snap.Records) != 1 {
		t.Fatalf("%v %d", err, len(snap.Records))
	}
	if got := snap.Records[0].Posting.Form; !reflect.DeepEqual(got, form) {
		t.Fatalf("form after a read:\n got %+v\nwant %+v", got, form)
	}

	scoped, err := ReadSnapshotScoped(dir, nil)
	if err != nil || len(scoped.Records) != 1 {
		t.Fatalf("%v %d", err, len(scoped.Records))
	}
	r := scoped.Records[0]
	if !r.BodyEvicted || r.Posting.Description != "" {
		t.Fatalf("the scoped read kept the description: %+v", r)
	}
	if !reflect.DeepEqual(r.Posting.Form, form) {
		t.Fatalf("form after a scoped read:\n got %+v\nwant %+v", r.Posting.Form, form)
	}
}

// A posting with no form writes no apply_form key, so a ledger line is the
// bytes it always was, and an old state file reads back with a nil form.
func TestPostingWithoutAFormWritesNoKey(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	p := posting.Posting{Key: "gh:acme:1", Company: "Acme", Title: "T", URL: "https://x/1", Description: "d"}
	e := ev("gh:acme:1", posting.Live, t0)
	e.Posting = &p
	if err := f.Append(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	snap, err := ReadSnapshot(dir)
	if err != nil || snap.Records[0].Posting.Form != nil {
		t.Fatalf("%v %+v", err, snap.Records)
	}
}
