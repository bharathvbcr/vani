package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestVerdictPredicates(t *testing.T) {
	for _, tc := range []struct {
		v                          Verdict
		backs, contradicts, refute bool
		rank                       int
	}{
		{VerdictPrimary, true, false, false, 0},
		{VerdictLedger, true, false, false, 1},
		{VerdictSkip, false, false, false, 2},
		{VerdictUnbacked, false, true, false, 3},
		{VerdictFail, false, true, false, 4},
		{VerdictScope, false, false, true, -1},
		{VerdictMismatch, false, false, true, -1},
		{VerdictConfirmed, false, false, false, -1},
		{"primary", false, false, false, -1}, // case matters: the report format is exact
		{"", false, false, false, -1},
		{"WEIRD", false, false, false, -1},
	} {
		if tc.v.BacksFigure() != tc.backs || tc.v.ContradictsFigure() != tc.contradicts || tc.v.RefutesFigure() != tc.refute || tc.v.validatorRank() != tc.rank {
			t.Errorf("%q: backs=%v contradicts=%v refutes=%v rank=%d", tc.v, tc.v.BacksFigure(), tc.v.ContradictsFigure(), tc.v.RefutesFigure(), tc.v.validatorRank())
		}
	}
	if VerdictScope.String() != "SCOPE" {
		t.Error("String must be the wire value")
	}
}

func TestIndependentVerifier(t *testing.T) {
	for by, want := range map[string]bool{
		"blind-1": true, "reader-hm-a": true,
		"": false, "  ": false, "author": false, "Author": false, " DRAFTER ": false, "me": false, "self": false, "user": false,
	} {
		if got := independentVerifier(by); got != want {
			t.Errorf("independentVerifier(%q) = %v, want %v", by, got, want)
		}
	}
}

// assessRig is one figure used by one sentence, with its source on disk.
func assessRig(t *testing.T) (*Claim, string, string) {
	t.Helper()
	root := w3aRoot(t)
	src := filepath.Join(root, "proj", "bench.txt")
	w3aWrite(t, src, "latency 5 ms\n")
	cl := &Claim{ID: "clmLat", Value: "5 ms", Check: "lat.check"}
	return cl, "Cut latency to {{clmLat}} by caching.", sha256File(src)
}

func blindPass(cl *Claim, sentence, sha, verdict, by string) LogEntry {
	return LogEntry{Pass: 2, Kind: "figure", Verdict: Verdict(verdict), Method: "blind-rederive", By: by, Value: cl.Value, Found: cl.Value,
		Evidence: "ev", SourcePath: "proj/bench.txt", SourceSHA: sha, SentenceHash: sentenceHash(sentence, cl.ID)}
}

var primaryVM = map[string]vmResult{"lat.check": {VerdictPrimary, "ok"}}

// An unknown, empty or lower-case verdict read from a log is never CONFIRMED:
// it fails pass 2 and is named in the problems.
func TestAssessUnknownVerdictIsNeverConfirmed(t *testing.T) {
	cl, sentence, sha := assessRig(t)
	for _, v := range []string{"WEIRD", "", "confirmed", "Confirmed ", "CONFIRMED2", "PRIMARY"} {
		log := VerifyLog{cl.ID: {blindPass(cl, sentence, sha, v, "blind-1")}}
		r := Assess(cl, log, primaryVM, []string{sentence})
		if r.P2OK || r.DoubleVerified() || len(r.Confirmed) != 0 {
			t.Errorf("verdict %q read as verified: %+v", v, r)
		}
		if want := "latest blind pass " + v + ": ev"; !strings.Contains(strings.Join(r.Problems, "; "), want) {
			t.Errorf("verdict %q: problems %q do not name it", v, r.Problems)
		}
		if len(r.Refuted) != 0 {
			t.Errorf("verdict %q refutes the sentence: %v", v, r.Refuted)
		}
	}
	// the same entry with CONFIRMED passes, so the cases above fail for the verdict alone
	log := VerifyLog{cl.ID: {blindPass(cl, sentence, sha, "CONFIRMED", "blind-1")}}
	if r := Assess(cl, log, primaryVM, []string{sentence}); !r.DoubleVerified() {
		t.Fatalf("control did not verify: %+v", r)
	}
}

// A validator verdict outside the report format never backs pass 1 either.
func TestAssessUnknownValidatorVerdictDoesNotBackPass1(t *testing.T) {
	cl, sentence, sha := assessRig(t)
	log := VerifyLog{cl.ID: {blindPass(cl, sentence, sha, "CONFIRMED", "blind-1")}}
	for _, v := range []Verdict{"WEIRD", "", "CONFIRMED", VerdictManual, VerdictSkip} {
		r := Assess(cl, log, map[string]vmResult{"lat.check": {v, "x"}}, []string{sentence})
		if r.P1 || r.DoubleVerified() {
			t.Errorf("validator verdict %q backed pass 1: %+v", v, r)
		}
	}
}

func TestAssessIndependenceIsARecordField(t *testing.T) {
	cl, sentence, sha := assessRig(t)
	for by, want := range map[string]bool{"blind-1": true, "author": false, "": false, "Drafter": false} {
		log := VerifyLog{cl.ID: {blindPass(cl, sentence, sha, "CONFIRMED", by)}}
		r := Assess(cl, log, primaryVM, []string{sentence})
		if r.Independent != want {
			t.Errorf("by=%q: Independent=%v, want %v", by, r.Independent, want)
		}
		if !r.DoubleVerified() {
			t.Errorf("by=%q: independence is recorded, not gated; export bytes must not change: %+v", by, r)
		}
	}
	// no blind pass read: independence is not established
	if r := Assess(cl, VerifyLog{}, primaryVM, []string{sentence}); r.Independent {
		t.Errorf("no pass read, Independent must be false: %+v", r)
	}
	if r := Assess(cl, VerifyLog{}, primaryVM, nil); r.Independent || r.P2OK {
		t.Errorf("no sentences: %+v", r)
	}
}

func TestAssessSentencesAreIndependentOfEachOther(t *testing.T) {
	cl, s0, sha := assessRig(t)
	s1 := "Held latency at {{clmLat}} under load."
	log := VerifyLog{cl.ID: {blindPass(cl, s0, sha, "CONFIRMED", "blind-1"), blindPass(cl, s1, sha, "CONFIRMED", "author")}}
	r := Assess(cl, log, primaryVM, []string{s0, s1})
	if !r.P2OK || len(r.Confirmed) != 2 || !r.Confirms(sentenceHash(s0, cl.ID)) || !r.Confirms(sentenceHash(s1, cl.ID)) {
		t.Fatalf("both sentences hold a CONFIRMED pass: %+v", r)
	}
	if r.Independent {
		t.Errorf("one of two verifiers is the author's seat: %+v", r)
	}
	if r.Confirms("nope") || r.Confirms("") {
		t.Error("Confirms matched a hash that is not confirmed")
	}
}
