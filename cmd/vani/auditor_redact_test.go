package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// ownerPartOf is a would-apply question's text before the posting.
func ownerPartOf(t *testing.T, q decide.Question) string {
	t.Helper()
	owner, _, ok := strings.Cut(string(q.Context), "\nJOB POSTING:\n")
	if !ok {
		t.Fatalf("not a would-apply question:\n%s", q.Context)
	}
	return owner
}

// The auditor's own refusal covers every spelling the matcher knows: the bank
// holds "kernels" and "decode latency", and the list names "kernel" and
// "decode-latency" (the forms the whole-word matcher let through). Nothing is
// sent.
func TestAuditorRefusesAnOwnerTermInAnySpellingAndSendsNothing(t *testing.T) {
	e := auSetup(t)
	writeKBWords(t, e.state, "# test", "kernel", "decode-latency")
	f := &auFake{}
	auUse(t, f)
	code, _, stderr := do(t, "auditor", "calibrate", "--budget-usd", "1")
	if code == 0 || !strings.Contains(stderr, "the knowledge-base digest holds 2 identifying terms") || !strings.Contains(stderr, "nothing was sent") {
		t.Fatalf("exit %d, stderr %s", code, stderr)
	}
	if strings.Contains(stderr, "kernel") || strings.Contains(stderr, "decode") {
		t.Errorf("the refusal names a term:\n%s", stderr)
	}
	if len(f.sent) != 0 {
		t.Fatalf("a refused run sent %d question(s)", len(f.sent))
	}
}

// The send path: whatever calibrate's check did, the question a task asks comes
// from kbmatch.Prepare over the redacted digest. This asks it directly, as the
// teacher's runner does, with a list that names terms the digest holds in other
// spellings, and records the bytes handed to the decider.
func TestAuditorTaskSendsOnlyTheRedactedDigest(t *testing.T) {
	e := auSetup(t)
	writeKBWords(t, e.state, "# test", "kernel", "decode-latency", "Zephyrnet")
	cfg, err := config.Load(e.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	w, _, err := kbmatchWords(cfg, e.state, true, false)
	if err != nil {
		t.Fatal(err)
	}
	_, d, summary, err := kbmatchDigest(cfg, w)
	if err != nil {
		t.Fatal(err)
	}
	task := auditorTask(summary, d)
	var got []decide.Question
	dec := deciderFunc(func(_ context.Context, q decide.Question) (decide.Answer, error) {
		got = append(got, q)
		return decide.Answer{}, nil
	})
	p := posting.Posting{Key: "gh:a:1", ATS: "gh", Company: "SynthCo", Title: "Engineer", Description: "Tune GPU kernels for decode latency."}
	if _, err := task.Ask(context.Background(), dec, p); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d question(s) reached the decider", len(got))
	}
	owner := ownerPartOf(t, got[0])
	lower := strings.ToLower(owner)
	for _, term := range []string{"kernel", "decode latency", "decode-latency", "zephyrnet"} {
		if strings.Contains(lower, term) {
			t.Errorf("%q reached the decider (owner part):\n%s", term, owner)
		}
	}
	for _, want := range []string{"[redacted]", "batching scheduler", "Rust"} {
		if !strings.Contains(owner, want) {
			t.Errorf("owner part lacks %q:\n%s", want, owner)
		}
	}
	if !strings.Contains(string(got[0].Context), "Tune GPU kernels") {
		t.Errorf("the employer's posting was rewritten:\n%s", got[0].Context)
	}

	// A digest the redactor did not make, and a summary that holds a term, are
	// refused per posting and nothing reaches the decider.
	got = nil
	lit := kbmatch.Digest{Text: "Go, Zephyrnet\n", SHA256: "x"}
	if _, err := auditorTask(summary, lit).Ask(context.Background(), dec, p); !errors.Is(err, kbmatch.ErrNoRedactor) {
		t.Fatalf("literal digest: %v", err)
	}
	var ie *kbmatch.IdentifyingError
	if _, err := auditorTask("roles using kernels", d).Ask(context.Background(), dec, p); !errors.As(err, &ie) {
		t.Fatalf("summary term: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("%d refused question(s) reached the decider", len(got))
	}
}

// A run that the check lets through sends questions with no owner term: the
// list names terms (in several spellings) that appear nowhere in the shared text.
func TestAuditorCalibrateSendsNoOwnerTerm(t *testing.T) {
	e := auSetup(t)
	terms := []string{"Zephyrnet", "Quillbridge Labs", "jos\xc3\xa9"}
	writeKBWords(t, e.state, append([]string{"# test"}, terms...)...)
	f := &auFake{}
	auUse(t, f)
	mustCode(t, 0, "auditor", "calibrate", "--budget-usd", "1")
	if len(f.sent) != 4 {
		t.Fatalf("%d questions reached the decider, want 4", len(f.sent))
	}
	for _, s := range f.sent {
		low := strings.ToLower(s)
		for _, term := range []string{"zephyrnet", "quillbridge", "jose", "jos\xc3\xa9"} {
			if strings.Contains(low, term) {
				t.Errorf("%q was sent:\n%s", term, s)
			}
		}
		if !strings.Contains(s, "batching scheduler") {
			t.Errorf("a question lacks the knowledge-base digest:\n%s", s)
		}
	}
}
