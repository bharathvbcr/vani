package eval

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func newStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	root := t.TempDir()
	labels, text := filepath.Join(root, "repo", "eval"), filepath.Join(root, "state", "text")
	s, err := NewStore(labels, text)
	if err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }
	return s, labels, text
}

func TestStoreRoundTripAndLatestWins(t *testing.T) {
	s, labelDir, _ := newStore(t)
	if err := s.Put(Entry{Key: "gh:a:1", Labels: map[string]string{"work_mode": "remote", "seniority": Skip}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{Key: "gh:a:2", Labels: map[string]string{"work_mode": "hybrid"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{Key: "gh:a:1", Labels: map[string]string{"work_mode": "onsite"}}); err != nil {
		t.Fatal(err)
	}
	got, bad, err := s.Labels()
	if err != nil || bad != 0 {
		t.Fatalf("err=%v bad=%d", err, bad)
	}
	if len(got) != 2 || got["gh:a:1"].Labels["work_mode"] != "onsite" || got["gh:a:2"].Labels["work_mode"] != "hybrid" {
		t.Fatalf("latest-wins failed: %+v", got)
	}
	if _, has := got["gh:a:1"].Labels["seniority"]; has {
		t.Error("latest line must replace the earlier one, not merge")
	}
	if got["gh:a:1"].V != SchemaVersion || got["gh:a:1"].LabeledAt.IsZero() {
		t.Errorf("defaults not filled: %+v", got["gh:a:1"])
	}
	raw, _ := os.ReadFile(filepath.Join(labelDir, LabelsFile))
	if n := strings.Count(string(raw), "\n"); n != 3 {
		t.Errorf("append-only: %d lines, want 3", n)
	}
}

func TestStoreRefusesInvalidLabels(t *testing.T) {
	s, labelDir, _ := newStore(t)
	bad := []Entry{
		{Key: "", Labels: map[string]string{"work_mode": "remote"}},
		{Key: "k", Labels: nil},
		{Key: "k", Labels: map[string]string{"nope": "remote"}},
		{Key: "k", Labels: map[string]string{"work_mode": "strong_fit"}}, // another slot's option
		{Key: "k", Labels: map[string]string{"work_mode": ""}},
		{Key: "k", Labels: map[string]string{"work_mode": "remote"}, TextSHA256: "../../etc/passwd"},
	}
	for i, e := range bad {
		if err := s.Put(e); err == nil {
			t.Errorf("case %d accepted: %+v", i, e)
		}
	}
	if _, err := os.Stat(filepath.Join(labelDir, LabelsFile)); !os.IsNotExist(err) {
		t.Errorf("a refused label touched the file: %v", err)
	}
}

func TestStoreSkipsTornAndCorruptLines(t *testing.T) {
	s, labelDir, _ := newStore(t)
	path := filepath.Join(labelDir, LabelsFile)
	if err := os.WriteFile(path, []byte(`{"v":1,"key":"gh:a:0","labels":{"work_mode":"rem`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{Key: "gh:a:1", Labels: map[string]string{"work_mode": "remote"}}); err != nil {
		t.Fatal(err)
	}
	got, bad, err := s.Labels()
	if err != nil || bad != 1 || len(got) != 1 || got["gh:a:1"].Labels["work_mode"] != "remote" {
		t.Fatalf("torn tail glued onto next line: err=%v bad=%d got=%+v", err, bad, got)
	}
	// An oversized line is counted, not parsed.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(strings.Repeat("x", maxLabelLine+10) + "\n")
	f.Close()
	if _, bad, err := s.Labels(); err != nil || bad != 2 {
		t.Fatalf("oversized line: err=%v bad=%d", err, bad)
	}
}

func TestTextCacheIsHashNamedPrivateAndTraversalSafe(t *testing.T) {
	s, _, textDir := newStore(t)
	h, err := s.PutText("some description")
	if err != nil {
		t.Fatal(err)
	}
	if h != HashText("some description") || len(h) != 64 {
		t.Fatalf("hash %q", h)
	}
	st, err := os.Stat(filepath.Join(textDir, h))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("stat %v perm %v", err, st)
	}
	if h2, err := s.PutText("some description"); err != nil || h2 != h {
		t.Fatalf("second put: %v %q", err, h2)
	}
	if got, err := s.Text(h); err != nil || got != "some description" {
		t.Fatalf("Text: %v %q", err, got)
	}
	if _, err := s.Text("../" + h); err == nil {
		t.Error("path traversal accepted")
	}
	if _, err := s.PutText(strings.Repeat("a", MaxTextBytes+1)); err == nil {
		t.Error("oversized text accepted")
	}
	ents, _ := os.ReadDir(textDir)
	if len(ents) != 1 {
		t.Errorf("leftover temp files: %v", ents)
	}
}

func mkRec(key string, st posting.Status, ann map[string]string) ledger.Record {
	return ledger.Record{Key: posting.Key(key), Status: st, Annotations: ann,
		Posting: posting.Posting{Key: posting.Key(key), Title: "T " + key, Company: "Acme", Location: "Remote", Description: "desc " + key}}
}

func keys(rs []ledger.Record) []string {
	var out []string
	for _, r := range rs {
		out = append(out, string(r.Key))
	}
	return out
}

// A label only counts toward a shadow's verdict on a posting the shadow was
// asked about, so those come before postings triaged without a shadow (for
// example, everything triaged before the shadow was configured).
func TestQueuePrefersPostingsTheShadowSaw(t *testing.T) {
	s, _, _ := newStore(t)
	tri := map[string]string{"triage.work_mode": "remote", "triage.work_mode.reading": "answered"}
	shadowed := map[string]string{"triage.work_mode": "remote", "triage.work_mode.reading": "answered",
		"triage.work_mode.shadow_reading": "answered", "triage.work_mode.shadow": "remote"}
	shadowedUnsure := map[string]string{"triage.unsure": "slot x abstained", "triage.work_mode.shadow_reading": "failed"}
	recs := []ledger.Record{
		mkRec("old-unsure", posting.Unsure, tri),
		mkRec("old", posting.Triaged, tri),
		mkRec("seen", posting.Triaged, shadowed),
		mkRec("seen-unsure", posting.Triaged, shadowedUnsure),
		mkRec("dis", posting.Triaged, map[string]string{"triage.work_mode": "remote", "triage.work_mode.disagree": "true"}),
	}
	got := keys(queue(recs, s, 100, nil))
	want := []string{"dis", "seen-unsure", "seen", "old-unsure", "old"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order %v, want %v", got, want)
	}
	if got := keys(queue(recs, s, 2, rand.New(rand.NewSource(7)))); strings.Join(got, ",") != "dis,seen-unsure" {
		t.Fatalf("n=2: %v", got)
	}
}

func TestQueueOrderingAndExclusions(t *testing.T) {
	s, _, _ := newStore(t)
	tri := map[string]string{"triage.work_mode": "remote", "triage.work_mode.reading": "answered"}
	dis := map[string]string{"triage.work_mode": "remote", "triage.work_mode.disagree": "true"}
	recs := []ledger.Record{
		mkRec("p1", posting.Triaged, tri),
		mkRec("p2", posting.Triaged, tri),
		mkRec("u1", posting.Unsure, tri),
		mkRec("u2", posting.Triaged, map[string]string{"triage.unsure": "slot x abstained"}),
		mkRec("d1", posting.Triaged, dis),
		mkRec("d2", posting.Unsure, dis), // disagree outranks unsure
		mkRec("done", posting.Triaged, dis),
		mkRec("dead", posting.Dead, dis),
		mkRec("dup", posting.Duplicate, tri),
		mkRec("bare", posting.Live, nil),
		mkRec("p3", posting.Explained, tri),
	}
	if err := s.Put(Entry{Key: "done", Labels: map[string]string{"work_mode": "remote"}}); err != nil {
		t.Fatal(err)
	}
	got := keys(queue(recs, s, 100, nil))
	want := []string{"d1", "d2", "u1", "u2", "p1", "p2", "p3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order %v, want %v", got, want)
	}
	// Tiers hold under shuffling, and the same seed gives the same queue.
	a := keys(queue(recs, s, 100, rand.New(rand.NewSource(7))))
	b := keys(queue(recs, s, 100, rand.New(rand.NewSource(7))))
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatalf("not deterministic: %v vs %v", a, b)
	}
	tier := func(k string) int {
		switch k {
		case "d1", "d2":
			return 0
		case "u1", "u2":
			return 1
		}
		return 2
	}
	for i := 1; i < len(a); i++ {
		if tier(a[i]) < tier(a[i-1]) {
			t.Fatalf("tier order broken: %v", a)
		}
	}
	if len(a) != len(want) {
		t.Fatalf("lost or duplicated: %v", a)
	}
	// n caps, keeping the highest tiers; n <= 0 is empty.
	if got := keys(queue(recs, s, 3, rand.New(rand.NewSource(1)))); len(got) != 3 || tier(got[0]) != 0 || tier(got[1]) != 0 || tier(got[2]) != 1 {
		t.Errorf("n=3: %v", got)
	}
	if queue(recs, s, 0, nil) != nil || queue(nil, s, 5, nil) != nil {
		t.Error("empty cases")
	}
}

func TestLabelScriptedSession(t *testing.T) {
	s, labelDir, textDir := newStore(t)
	long := strings.Repeat("é", 3000)
	r1 := mkRec("gh:acme:1", posting.Triaged, map[string]string{
		"triage.work_mode": "remote", "triage.work_mode.reading": "answered", "triage.work_mode.backend": "jev",
		"triage.work_mode.p": "0.9100", "triage.work_mode.shadow": "onsite", "triage.work_mode.disagree": "true",
	})
	prefix := "SECRET-JD-TEXT \x1b[31mred\x1b[0m "
	r1.Posting.Description = prefix + long
	r2 := mkRec("gh:acme:2", posting.Triaged, nil)
	r3 := mkRec("gh:acme:3", posting.Triaged, nil)
	in := strings.Join([]string{
		"9", "abc", "", "0", // bad input re-prompts (role_fit)
		"1",                // role_fit = strong_fit
		"S",                // sponsorship skip
		"3",                // work_mode = onsite
		"2",                // seniority = mid
		"2",                // r2 role_fit = partial_fit
		"q",                // quit mid-posting: r2 is not written
		"1", "1", "1", "1", // never read
	}, "\n") + "\n"
	var out strings.Builder
	n, err := Label(strings.NewReader(in), &out, []ledger.Record{r1, r2, r3}, s, Names{})
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got, _, _ := s.Labels()
	e, ok := got["gh:acme:1"]
	if !ok || len(got) != 1 {
		t.Fatalf("labels: %+v", got)
	}
	want := map[string]string{"role_fit": "strong_fit", "sponsorship": Skip, "work_mode": "onsite", "seniority": "mid"}
	for k, v := range want {
		if e.Labels[k] != v {
			t.Errorf("%s = %q, want %q", k, e.Labels[k], v)
		}
	}
	if c := strings.Count(out.String(), "Not an option"); c != 4 {
		t.Errorf("re-prompts = %d, want 4\n%s", c, out.String())
	}
	o := out.String()
	if strings.Contains(o, "\x1b") {
		t.Error("terminal escape reached the output")
	}
	if !strings.Contains(o, "first 1500 of") || strings.Count(o, "é") != DescriptionPreviewRunes-len([]rune(plain(prefix))) {
		t.Errorf("preview not capped at %d runes (é count %d)", DescriptionPreviewRunes, strings.Count(o, "é"))
	}
	for _, frag := range []string{"T gh:acme:1", "Acme", "work_mode: remote (answered, jev, p=0.9100, shadow=onsite, DISAGREE)"} {
		if !strings.Contains(o, frag) {
			t.Errorf("output lacks %q", frag)
		}
	}
	// The text is in the private cache under its hash, and never in the label file.
	raw, _ := os.ReadFile(filepath.Join(labelDir, LabelsFile))
	if strings.Contains(string(raw), "SECRET-JD-TEXT") || strings.Contains(string(raw), "é") {
		t.Fatalf("description text leaked into labels.jsonl: %s", raw)
	}
	if e.TextSHA256 != HashText(r1.Posting.Description) || !strings.Contains(string(raw), e.TextSHA256) {
		t.Errorf("hash not recorded: %q", e.TextSHA256)
	}
	if txt, err := os.ReadFile(filepath.Join(textDir, e.TextSHA256)); err != nil || string(txt) != r1.Posting.Description {
		t.Errorf("cached text: %v", err)
	}
}

func TestLabelEndOfInputStopsWithoutWriting(t *testing.T) {
	s, _, _ := newStore(t)
	var out strings.Builder
	n, err := Label(strings.NewReader("1\n1\n"), &out, []ledger.Record{mkRec("k", posting.Triaged, nil)}, s, Names{})
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got, _, _ := s.Labels(); len(got) != 0 {
		t.Errorf("partial posting written: %+v", got)
	}
	// Over-long input line is an error, not a hang or a label.
	_, err = Label(strings.NewReader(strings.Repeat("1", maxInputLine+1)+"\n"), &out, []ledger.Record{mkRec("k", posting.Triaged, nil)}, s, Names{})
	if err == nil {
		t.Error("oversized input line accepted")
	}
}

func jevRec(key, slot, got, p, shadow string, answered bool) ledger.Record {
	pre := "triage." + slot
	ann := map[string]string{pre + ".backend": "jev", pre + ".reading": "answered"}
	if !answered {
		ann[pre+".reading"] = "abstained"
	} else {
		ann[pre] = got
		ann[pre+".p"] = p
	}
	if shadow != "" {
		ann[pre+".shadow"] = shadow
	}
	return mkRec(key, posting.Triaged, ann)
}

func TestReportMath(t *testing.T) {
	s, _, _ := newStore(t)
	put := func(key string, l map[string]string) {
		t.Helper()
		if err := s.Put(Entry{Key: posting.Key(key), Labels: l}); err != nil {
			t.Fatal(err)
		}
	}
	put("A", map[string]string{"work_mode": "remote"})
	put("B", map[string]string{"work_mode": "onsite"})
	put("C", map[string]string{"work_mode": "hybrid"})
	put("D", map[string]string{"work_mode": "remote"})
	put("E", map[string]string{"work_mode": Skip})
	put("F", map[string]string{"sponsorship": "not_stated"})
	put("G", map[string]string{"work_mode": "remote"}) // labeled but no triage yet
	put("gone", map[string]string{"work_mode": "remote"})
	f := mkRec("F", posting.Unsure, map[string]string{
		"triage.sponsorship.backend": "llm", "triage.sponsorship.reading": "abstained"})
	recs := []ledger.Record{
		jevRec("A", "work_mode", "remote", "0.9700", "remote", true), // correct, shadow agrees
		jevRec("B", "work_mode", "remote", "0.6200", "onsite", true), // wrong, shadow right
		jevRec("C", "work_mode", "hybrid", "0.8500", "remote", true), // correct, shadow wrong
		jevRec("D", "work_mode", "", "", "", false),                  // abstained
		jevRec("E", "work_mode", "remote", "0.9900", "", true),       // skip: excluded
		f,
		mkRec("G", posting.Live, nil),
		mkRec("unlabeled", posting.Triaged, map[string]string{"triage.work_mode.reading": "answered"}),
	}
	rep := BuildReport(recs, s, Names{})
	if rep.Records != 8 || rep.WithTriage != 7 || rep.LabelEntries != 8 || rep.Matched != 7 || rep.NoTriage != 1 {
		t.Errorf("header: %+v", rep)
	}
	var wm, sp *Cell
	for i := range rep.Cells {
		switch c := &rep.Cells[i]; c.Slot {
		case "work_mode":
			wm = c
		case "sponsorship":
			sp = c
		}
	}
	if wm == nil || wm.Backend != "jev" || wm.Labeled != 4 || wm.Answered != 3 || wm.Correct != 2 {
		t.Fatalf("work_mode cell: %+v", wm)
	}
	if p := pct(wm.Correct, wm.Answered); p != "66.7% (2/3)" {
		t.Errorf("precision %q", p)
	}
	if c := pct(wm.Answered, wm.Labeled); c != "75.0% (3/4)" {
		t.Errorf("coverage %q", c)
	}
	if sp == nil || sp.Labeled != 1 || sp.Answered != 0 {
		t.Fatalf("sponsorship cell: %+v", sp)
	}
	if p := pct(sp.Correct, sp.Answered); p != "n/a (0/0)" {
		t.Errorf("precision over zero answered must be n/a, got %q", p)
	}

	// Threshold table: answered/correct at each cut, coverage over the 4 labeled.
	if len(rep.Thresholds) != 1 || rep.Thresholds[0].Slot != "work_mode" {
		t.Fatalf("thresholds: %+v", rep.Thresholds)
	}
	wantAns := map[float64][2]int{0.5: {3, 2}, 0.6: {3, 2}, 0.7: {2, 2}, 0.8: {2, 2}, 0.9: {1, 1}, 0.95: {1, 1}}
	for _, row := range rep.Thresholds[0].Rows {
		w := wantAns[row.P]
		if row.Answered != w[0] || row.Correct != w[1] || row.Labeled != 4 {
			t.Errorf("p>=%.2f: %+v, want answered %d correct %d", row.P, row, w[0], w[1])
		}
	}

	if len(rep.Shadow) != 1 {
		t.Fatalf("shadow rows: %+v", rep.Shadow)
	}
	sh := rep.Shadow[0]
	// jevRec records no shadow backend, and D has no shadow annotation at all, so
	// the row is the "unknown" shadow's over the three postings it was asked about.
	if sh.Shadow != UnknownBackend || sh.Labeled != 3 || sh.ShadowAnswered != 3 || sh.ShadowCorrect != 2 || sh.BothAnswered != 3 ||
		sh.Agree != 1 || sh.Disagree != 2 || sh.PrimaryRight != 1 || sh.ShadowRight != 1 {
		t.Errorf("shadow: %+v", sh)
	}

	md := rep.Markdown()
	for _, frag := range []string{
		"| work_mode | jev | 4 | 3 | 2 | 66.7% (2/3) | 75.0% (3/4) |",
		"| sponsorship | llm | 1 | 0 | 0 | n/a (0/0) | 0.0% (0/1) |",
		"| 0.90 | 1 | 1 | 100.0% (1/1) | 25.0% (1/4) |",
		"| 0.50 | 3 | 2 | 66.7% (2/3) | 75.0% (3/4) |",
		"| work_mode | unknown | 3 | 3 | 66.7% (2/3) | 3 | 33.3% (1/3) | 2 | 1 | 1 |",
		"labeled postings: 8, matched to a ledger record: 7",
		"sample",
	} {
		if !strings.Contains(md, frag) {
			t.Errorf("markdown lacks %q\n%s", frag, md)
		}
	}
	if strings.Contains(md, "NaN") || strings.Contains(md, "Inf") {
		t.Errorf("NaN/Inf in markdown:\n%s", md)
	}
}

func TestReportEmpty(t *testing.T) {
	s, _, _ := newStore(t)
	rep := BuildReport(nil, s, Names{})
	md := rep.Markdown()
	if !strings.Contains(md, "n/a") || strings.Contains(md, "NaN") {
		t.Errorf("empty report:\n%s", md)
	}
	// Only-skip labels leave every denominator empty.
	if err := s.Put(Entry{Key: "A", Labels: map[string]string{"work_mode": Skip}}); err != nil {
		t.Fatal(err)
	}
	rep = BuildReport([]ledger.Record{jevRec("A", "work_mode", "remote", "0.9", "", true)}, s, Names{})
	if len(rep.Cells) != 0 || strings.Contains(rep.Markdown(), "NaN") {
		t.Errorf("skip-only: %+v", rep.Cells)
	}
	// An unreadable label file is stated, not reported as an empty clean set.
	if err := os.Remove(filepath.Join(s.labelDir, LabelsFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.labelDir, LabelsFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if md := BuildReport(nil, s, Names{}).Markdown(); !strings.Contains(md, "COULD NOT BE READ") {
		t.Errorf("load error hidden:\n%s", md)
	}
}

func TestPromotionVerdicts(t *testing.T) {
	for _, tc := range []struct {
		p    Promotion
		want string
	}{
		{Promotion{Asked: 29, ShadowAnswered: 29, ShadowCorrect: 29, PrimaryAnswered: 1, PrimaryCorrect: 0}, "insufficient labels (29/30)"},
		// 15 of 20 disagreements to the shadow: lower bound 0.544 >= 0.4.
		{Promotion{Asked: 30, ShadowAnswered: 30, PrimaryAnswered: 30, Paired: 30, PrimaryOnly: 5, ShadowOnly: 15}, "promotable"},
		// The boundary of the disagreement floor: 10 of 10 to the shadow.
		{Promotion{Asked: 30, ShadowAnswered: 30, PrimaryAnswered: 30, Paired: 30, ShadowOnly: 10}, "promotable"},
		// A tie on 20 disagreements proves too little: lower bound 0.302 < 0.4.
		{Promotion{Asked: 30, ShadowAnswered: 30, PrimaryAnswered: 30, Paired: 30, PrimaryOnly: 10, ShadowOnly: 10},
			"not promotable: shadow right in 10 of 20 disagreements, lower bound 0.30 < 0.40"},
		// 12 of 20: point estimate 60%, lower bound 0.394 < 0.4.
		{Promotion{Asked: 30, ShadowAnswered: 30, PrimaryAnswered: 30, Paired: 30, PrimaryOnly: 8, ShadowOnly: 12},
			"not promotable: shadow right in 12 of 20 disagreements, lower bound 0.39 < 0.40"},
		// Nine disagreements are never enough, however lopsided.
		{Promotion{Asked: 30, ShadowAnswered: 30, PrimaryAnswered: 30, Paired: 30, ShadowOnly: 9}, "insufficient disagreements (9/10)"},
		{Promotion{Asked: 30, ShadowAnswered: 30, PrimaryAnswered: 30, Paired: 30}, "insufficient disagreements (0/10)"},
		// Paired postings are what the comparison rests on: 29 are too few.
		{Promotion{Asked: 30, ShadowAnswered: 29, PrimaryAnswered: 28, Paired: 27, ShadowOnly: 10}, "insufficient paired labels (27/30)"},
		{Promotion{Asked: 30, ShadowAnswered: 20, PrimaryAnswered: 28, Paired: 20, ShadowOnly: 20}, "not promotable: coverage 20/30 < primary 28/30"},
		{Promotion{Asked: 30, ShadowAnswered: 0, PrimaryAnswered: 28}, "not promotable: the shadow answered none"},
		{Promotion{Asked: 30, ShadowAnswered: 10}, "no primary baseline"},
	} {
		if got := tc.p.Verdict(); !strings.HasPrefix(got, tc.want) {
			t.Errorf("%+v: verdict %q, want prefix %q", tc.p, got, tc.want)
		}
	}
}

// A shadow is judged against each primary backend separately: postings Jev
// answered and postings the LLM answered (say, after Jev's daily cap) are two
// baselines, never pooled into one "chain" figure.
func TestPromotionIsPerPrimaryBackend(t *testing.T) {
	s, _, _ := newStore(t)
	var recs []ledger.Record
	for i := 0; i < 40; i++ {
		key := fmt.Sprintf("Q%02d", i)
		if err := s.Put(Entry{Key: posting.Key(key), Labels: map[string]string{"work_mode": "remote"}}); err != nil {
			t.Fatal(err)
		}
		r := jevRec(key, "work_mode", "remote", "0.9000", "", true)
		if i >= 30 { // the LLM answered these ten, wrongly on all
			r.Annotations["triage.work_mode.backend"] = "llm"
			r.Annotations["triage.work_mode"] = "onsite"
		} else if i < 3 { // Jev wrong on three
			r.Annotations["triage.work_mode"] = "hybrid"
		}
		r.Annotations["triage.work_mode.shadow_reading"] = "answered"
		r.Annotations["triage.work_mode.shadow_backend"] = "local"
		r.Annotations["triage.work_mode.shadow"] = "remote"
		if i == 0 || i == 1 {
			r.Annotations["triage.work_mode.shadow"] = "onsite" // shadow wrong on two Jev postings
		}
		recs = append(recs, r)
	}
	rep := BuildReport(recs, s, Names{})
	if len(rep.Promotions) != 2 {
		t.Fatalf("promotions: %+v", rep.Promotions)
	}
	jev, llm := rep.Promotions[0], rep.Promotions[1]
	// The shadow beat Jev on one posting (i=2) and never lost to it: a single
	// disagreement, so no verdict however clean the record.
	if jev.Primary != "jev" || jev.Asked != 30 || jev.PrimaryCorrect != 27 || jev.ShadowCorrect != 28 ||
		jev.Paired != 30 || jev.PrimaryOnly != 0 || jev.ShadowOnly != 1 || jev.Verdict() != "insufficient disagreements (1/10)" {
		t.Fatalf("jev row %+v: %s", jev, jev.Verdict())
	}
	// Ten LLM postings: too few for a verdict on their own, however badly the
	// LLM did; pooled, they would have dragged the baseline to 27/40.
	if llm.Primary != "llm" || llm.Asked != 10 || !strings.HasPrefix(llm.Verdict(), "insufficient labels (10/30)") {
		t.Fatalf("llm row %+v: %s", llm, llm.Verdict())
	}
	if md := rep.Markdown(); !strings.Contains(md, "| work_mode | local | jev | 30 |") {
		t.Fatalf("markdown lacks the per-primary row:\n%s", md)
	}
}

// The promotion table counts only postings where the shadow was asked, per
// shadow backend, and compares primary and shadow on exactly those.
func TestReportPromotion(t *testing.T) {
	s, _, _ := newStore(t)
	var recs []ledger.Record
	for i := 0; i < 32; i++ {
		key := "P" + string(rune(0x41+i%26)) + string(rune(0x30+i/26))
		if err := s.Put(Entry{Key: posting.Key(key), Labels: map[string]string{"work_mode": "remote"}}); err != nil {
			t.Fatal(err)
		}
		r := jevRec(key, "work_mode", "remote", "0.9000", "", true)
		if i < 2 {
			r.Annotations["triage.work_mode"] = "onsite" // primary wrong on two
		}
		r.Annotations["triage.work_mode.shadow_reading"] = "answered"
		r.Annotations["triage.work_mode.shadow_backend"] = "lappi"
		r.Annotations["triage.work_mode.shadow"] = "remote"
		if i == 31 {
			r.Annotations["triage.work_mode.shadow_reading"] = "abstained"
			delete(r.Annotations, "triage.work_mode.shadow")
		}
		recs = append(recs, r)
	}
	// A labeled posting from before the shadow existed is not in the table.
	if err := s.Put(Entry{Key: "old", Labels: map[string]string{"work_mode": "remote"}}); err != nil {
		t.Fatal(err)
	}
	recs = append(recs, jevRec("old", "work_mode", "onsite", "0.9000", "", true))

	rep := BuildReport(recs, s, Names{})
	if len(rep.Promotions) != 1 {
		t.Fatalf("promotions: %+v", rep.Promotions)
	}
	p := rep.Promotions[0]
	if p.Shadow != "lappi" || p.Asked != 32 || p.ShadowAnswered != 31 || p.ShadowCorrect != 31 ||
		p.PrimaryAnswered != 32 || p.PrimaryCorrect != 30 {
		t.Fatalf("promotion row: %+v", p)
	}
	// Shadow precision 100% beats 93.75%, but it answered 31 of 32 and the
	// primary 32: no worse coverage fails.
	if v := p.Verdict(); !strings.HasPrefix(v, "not promotable: coverage") {
		t.Fatalf("verdict %q", v)
	}
	if md := rep.Markdown(); !strings.Contains(md, "| work_mode | lappi | jev | 32 | 100.0% (31/31) | 96.9% (31/32) | 93.8% (30/32) | 100.0% (32/32) | 0 | 31 | 0 | 2 | not promotable: coverage 31/32 < primary 32/32 |") {
		t.Fatalf("markdown lacks the promotion row:\n%s", md)
	}
}
