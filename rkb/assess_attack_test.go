package main

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Attack tests for Assess (card ft-326520969690ee945e4f65d38f2cc8bd).
// TestAssessMatchesPre4a53b96 replays a few thousand seeded logs and compares
// every field of the assessment with testdata/assess/attack_old.golden, which
// was produced by running the same generator against evaluate() as it stood at
// 4a53b96. RKB_UPDATE_GOLDEN=1 rewrites it (only meaningful on that code).

var attackVerdicts = []string{
	"CONFIRMED", "confirmed", " CONFIRMED", "CONFIRMED ", "CONFIRMED\n", "СONFIRMED", "CONFIRMED​", "CONFIRMED2",
	"", "SCOPE", "MISMATCH", "UNSUPPORTED", "NO_ANSWER", "QUOTE_NOT_FOUND", "FAIL", "PRIMARY", "MANUAL", "WEIRD",
	strings.Repeat("CONFIRMED", 600),
}

func attackFormat(r Report) string {
	var keys []string
	for k := range r.Refuted {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var ref []string
	for _, k := range keys {
		ref = append(ref, k+"="+r.Refuted[k])
	}
	return fmt.Sprintf("p1=%v p1v=%q p2=%v confirmed=%q problems=%q refuted=%q", r.P1, string(r.P1Verdict), r.P2OK, r.Confirmed, r.Problems, ref)
}

func attackRig(t *testing.T) (cl *Claim, sentences []string, sha string) {
	t.Helper()
	root := w3aRoot(t)
	src := filepath.Join(root, "proj", "bench.txt")
	w3aWrite(t, src, "latency 5 ms\n")
	return &Claim{ID: "clmLat", Value: "5 ms", Check: "lat.check"},
		[]string{"Cut latency to {{clmLat}} by caching.", "Held latency at {{clmLat}} under load."}, sha256File(src)
}

func TestAssessMatchesPre4a53b96(t *testing.T) {
	cl, sents, sha := attackRig(t)
	rng := rand.New(rand.NewSource(20261118))
	pick := func(xs []string) string { return xs[rng.Intn(len(xs))] }
	pickV := func() string {
		if rng.Intn(2) == 0 {
			return "CONFIRMED"
		}
		return pick(attackVerdicts)
	}
	vmVerdicts := []string{"", "", "PRIMARY", "LEDGER", "SKIP", "UNBACKED", "FAIL"}
	shas := []string{sha, sha, sha, "", strings.Repeat("0", 64)}
	paths := []string{"proj/bench.txt", "proj/bench.txt", "proj/missing.txt", ""}
	values := []string{cl.Value, cl.Value, cl.Value, "6 ms"}
	var sb strings.Builder
	for i := 0; i < 4000; i++ {
		var entries []LogEntry
		for n := rng.Intn(7); n > 0; n-- {
			e := LogEntry{Pass: 1 + rng.Intn(2), Kind: "figure", Verdict: Verdict(pickV()), Method: "m", By: "blind-1",
				At: pick([]string{"2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z", "2024-01-01T00:00:00Z", ""}), Value: pick(values), Evidence: "ev " + pick(attackVerdicts),
				SourcePath: pick(paths), SourceSHA: pick(shas)}
			if rng.Intn(4) > 0 {
				e.Found = e.Value
			}
			if e.Pass == 2 {
				e.SentenceHash = pick([]string{sentenceHash(sents[0], cl.ID), sentenceHash(sents[0], cl.ID), sentenceHash(sents[1], cl.ID), "deadbeef"})
			}
			entries = append(entries, e)
		}
		var vm map[string]vmResult
		switch rng.Intn(4) {
		case 0:
			vm = nil
		case 1:
			vm = map[string]vmResult{}
		default:
			if v := pick(vmVerdicts); v != "" {
				vm = map[string]vmResult{cl.Check: {Verdict(v), "detail"}}
			} else {
				vm = map[string]vmResult{"other.check": {Verdict("PRIMARY"), "x"}}
			}
		}
		var ss []string
		switch rng.Intn(5) {
		case 0:
			ss = nil
		case 1:
			ss = []string{}
		case 2:
			ss = sents[:1]
		case 3:
			ss = []string{sents[0], sents[0]} // duplicated sentence
		default:
			ss = sents
		}
		var log VerifyLog
		if rng.Intn(10) > 0 {
			log = VerifyLog{cl.ID: entries}
		}
		fmt.Fprintf(&sb, "%d %s\n", i, attackFormat(Assess(cl, log, vm, ss)))
	}
	got := sb.String()
	golden := filepath.Join("testdata", "assess", "attack_old.golden")
	if os.Getenv("RKB_UPDATE_GOLDEN") == "1" {
		w3aWrite(t, golden, got)
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		wl, gl := strings.Split(string(want), "\n"), strings.Split(got, "\n")
		for i := range wl {
			if i >= len(gl) || wl[i] != gl[i] {
				t.Fatalf("assessment differs from 4a53b96 at case %d\nold: %.600s\nnew: %.600s", i, wl[i], gl[min(i, len(gl)-1)])
			}
		}
		t.Fatalf("assessment differs from 4a53b96 (length %d vs %d)", len(want), len(got))
	}
	if strings.Count(got, "p2=true") < 100 || strings.Count(got, "p1=true") < 200 {
		t.Errorf("generator no longer reaches the verified states often enough: p1=%d p2=%d", strings.Count(got, "p1=true"), strings.Count(got, "p2=true"))
	}
}

// Verdict spellings that are not exactly CONFIRMED never confirm a sentence
// or a first pass, whatever else the entry says.
func TestAssessLookalikeVerdictsNeverConfirm(t *testing.T) {
	cl, sents, sha := attackRig(t)
	for _, v := range []string{"confirmed", " CONFIRMED", "CONFIRMED ", "CONFIRMED\n", "СONFIRMED", "CONFIRMED​", "", strings.Repeat("CONFIRMED", 600)} {
		p2 := LogEntry{Pass: 2, Kind: "figure", Verdict: Verdict(v), By: "blind-1", Value: cl.Value, Found: cl.Value, SourcePath: "proj/bench.txt", SourceSHA: sha, SentenceHash: sentenceHash(sents[0], cl.ID)}
		r := Assess(cl, VerifyLog{cl.ID: {p2}}, map[string]vmResult{cl.Check: {VerdictPrimary, "ok"}}, sents[:1])
		if r.P2OK || len(r.Confirmed) != 0 || r.DoubleVerified() {
			t.Errorf("pass 2 verdict %.20q confirmed: %+v", v, r)
		}
		p1 := LogEntry{Pass: 1, Kind: "figure", Verdict: Verdict(v), By: "reader", Value: cl.Value, Found: cl.Value, SourcePath: "proj/bench.txt", SourceSHA: sha}
		r = Assess(cl, VerifyLog{cl.ID: {p1}}, nil, nil)
		if r.P1 || r.P1Verdict != "" {
			t.Errorf("pass 1 verdict %.20q backed the figure: %+v", v, r)
		}
	}
}

// The latest entry for a sentence decides, by position in the log, not by its
// At stamp: equal stamps, reversed stamps and a refuting pass after a CONFIRMED
// all behave the same way.
func TestAssessLatestPassWinsByLogOrder(t *testing.T) {
	cl, sents, sha := attackRig(t)
	vm := map[string]vmResult{cl.Check: {VerdictPrimary, "ok"}}
	mk := func(v, at string) LogEntry {
		e := blindPass(cl, sents[0], sha, v, "blind-1")
		e.At = at
		return e
	}
	for _, tc := range []struct {
		name     string
		entries  []LogEntry
		verified bool
		refuted  bool
	}{
		{"confirmed then mismatch", []LogEntry{mk("CONFIRMED", "2025-01-01"), mk("MISMATCH", "2025-01-02")}, false, true},
		{"mismatch then confirmed", []LogEntry{mk("MISMATCH", "2025-01-01"), mk("CONFIRMED", "2025-01-02")}, true, false},
		{"equal stamps, confirmed last", []LogEntry{mk("SCOPE", "t"), mk("CONFIRMED", "t")}, true, false},
		{"equal stamps, scope last", []LogEntry{mk("CONFIRMED", "t"), mk("SCOPE", "t")}, false, true},
		{"older stamp last, confirmed last", []LogEntry{mk("MISMATCH", "2025-01-02"), mk("CONFIRMED", "2024-01-01")}, true, false},
		{"duplicate confirmed", []LogEntry{mk("CONFIRMED", "t"), mk("CONFIRMED", "t")}, true, false},
		{"confirmed then unknown", []LogEntry{mk("CONFIRMED", "t"), mk("WEIRD", "t")}, false, false},
	} {
		r := Assess(cl, VerifyLog{cl.ID: tc.entries}, vm, sents[:1])
		if r.DoubleVerified() != tc.verified || (len(r.Refuted) == 1) != tc.refuted {
			t.Errorf("%s: %+v", tc.name, r)
		}
		if tc.verified && len(r.Confirmed) != 1 {
			t.Errorf("%s: duplicate entries must confirm the sentence once: %v", tc.name, r.Confirmed)
		}
	}
}

// Degenerate inputs: nil maps and logs, no sentences, a claim with no value or
// no check, a validator entry with no detail, a repeated sentence.
func TestAssessDegenerateInputs(t *testing.T) {
	cl, sents, sha := attackRig(t)
	if r := Assess(cl, nil, nil, nil); r.P1 || r.P2OK || r.Independent || len(r.Confirmed) != 0 || len(r.Problems) == 0 {
		t.Errorf("all nil: %+v", r)
	}
	if r := Assess(cl, VerifyLog{}, map[string]vmResult{}, []string{}); r.DoubleVerified() {
		t.Errorf("empty: %+v", r)
	}
	// PRIMARY with no sentences: pass 1 holds, pass 2 never does
	if r := Assess(cl, nil, map[string]vmResult{cl.Check: {VerdictPrimary, ""}}, nil); !r.P1 || r.P2OK || r.DoubleVerified() {
		t.Errorf("zero sentences: %+v", r)
	}
	// a FAIL with no detail still voids pass 1 and says why
	r := Assess(cl, nil, map[string]vmResult{cl.Check: {VerdictFail, ""}}, sents)
	if r.P1 || !strings.Contains(strings.Join(r.Problems, ";"), "validator FAIL: ") {
		t.Errorf("FAIL without detail: %+v", r)
	}
	// the same sentence twice is confirmed twice, as before, and still verifies
	log := VerifyLog{cl.ID: {blindPass(cl, sents[0], sha, "CONFIRMED", "blind-1")}}
	r = Assess(cl, log, map[string]vmResult{cl.Check: {VerdictPrimary, "ok"}}, []string{sents[0], sents[0]})
	if !r.DoubleVerified() || len(r.Confirmed) != 2 {
		t.Errorf("duplicated sentence: %+v", r)
	}
	// a claim with no value and no check: entries with an empty value match it
	empty := &Claim{ID: "clmEmpty"}
	e := LogEntry{Pass: 2, Kind: "figure", Verdict: VerdictConfirmed, By: "blind-1", SourcePath: "proj/bench.txt", SourceSHA: sha, SentenceHash: sentenceHash(sents[0], empty.ID)}
	r = Assess(empty, VerifyLog{empty.ID: {e}}, nil, []string{"Did {{clmEmpty}}."})
	if r.P1 || r.DoubleVerified() {
		t.Errorf("no value, no validator: %+v", r)
	}
}

// Report.Independent decides exactly what claims ingest's deny-list on
// 4a53b96 decided (strings.ToLower of the trimmed name against author,
// drafter, me, self, user; empty refused): independent iff ingest accepts.
func TestAssessIndependenceMatchesOldIngestDenyList(t *testing.T) {
	cl, sents, sha := attackRig(t)
	old := func(by string) bool {
		by = strings.TrimSpace(by)
		if by == "" {
			return false
		}
		switch strings.ToLower(by) {
		case "author", "drafter", "me", "self", "user":
			return false
		}
		return true
	}
	for _, by := range []string{"", " ", "Author", " author", "author ", "AUTHOR", "drafter", "Drafter", "me", "ME", "Self", "self\n", "user", "USER",
		"Аuthor", "authоr", "author​", "me2", "blind-1", "the author", "K"} {
		log := VerifyLog{cl.ID: {blindPass(cl, sents[0], sha, "CONFIRMED", by)}}
		r := Assess(cl, log, map[string]vmResult{cl.Check: {VerdictPrimary, "ok"}}, sents[:1])
		if r.Independent != old(by) {
			t.Errorf("by=%q: Independent=%v, old ingest accepts=%v", by, r.Independent, old(by))
		}
		if !r.DoubleVerified() {
			t.Errorf("by=%q: independence must not gate verification: %+v", by, r)
		}
	}
	// the latest entry for each sentence is the one read
	for _, tc := range []struct {
		first, last string
		want        bool
	}{{"author", "blind-1", true}, {"blind-1", "author", false}} {
		log := VerifyLog{cl.ID: {blindPass(cl, sents[0], sha, "CONFIRMED", tc.first), blindPass(cl, sents[0], sha, "CONFIRMED", tc.last)}}
		if r := Assess(cl, log, nil, sents[:1]); r.Independent != tc.want {
			t.Errorf("%s then %s: Independent=%v, want %v", tc.first, tc.last, r.Independent, tc.want)
		}
	}
}
