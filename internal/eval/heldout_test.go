package eval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const d1Split = "d1-vani-jdtriage-2026-10-18"

func registered(t *testing.T) Split {
	t.Helper()
	sp, err := LookupSplit(d1Split)
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

// The registration's rule text: sha256('d1-vani-jdtriage-2026-10-18' || 0x00
// || posting.Key). The golden digest was computed outside Go, with
// printf 'd1-vani-jdtriage-2026-10-18\000gh:acme:123' | shasum -a 256,
// so an offline script can check its own layout against the same value.
func TestSplitRankByteLayout(t *testing.T) {
	sp := registered(t)
	if sp.Salt != d1Split || sp.Size != 30 {
		t.Fatalf("registered split %+v, want salt %q and 30 held out", sp, d1Split)
	}
	const golden = "f9dd4f52c576000adf51f125978bcf6811ee8942adc76046b89ee68871b60f88"
	got := sp.Rank("gh:acme:123")
	if hex.EncodeToString(got[:]) != golden {
		t.Fatalf("rank %x, want %s", got, golden)
	}
	// Neither a missing separator nor a trailing one is the same input.
	for _, wrong := range []string{d1Split + "gh:acme:123", d1Split + "\x00gh:acme:123\x00"} {
		if sha256.Sum256([]byte(wrong)) == got {
			t.Fatalf("rank equals sha256(%q)", wrong)
		}
	}
}

// Assign ranks by digest ascending, takes the first Size, ranks a repeated
// key once, and does not depend on input order.
func TestSplitAssignOrderAndDuplicates(t *testing.T) {
	sp := Split{Name: "t", Salt: "salt", Size: 3}
	var keys []posting.Key
	for i := 0; i < 8; i++ {
		keys = append(keys, posting.Key(fmt.Sprintf("gh:co:%d", i)))
	}
	// Oracle: hex digests sorted as strings.
	type kv struct {
		hex string
		key posting.Key
	}
	var want []kv
	for _, k := range keys {
		sum := sha256.Sum256(append(append([]byte("salt"), 0), k...))
		want = append(want, kv{hex.EncodeToString(sum[:]), k})
	}
	sort.Slice(want, func(i, j int) bool { return want[i].hex < want[j].hex })
	var wantHeld, wantFit []posting.Key
	for i, w := range want {
		if i < 3 {
			wantHeld = append(wantHeld, w.key)
		} else {
			wantFit = append(wantFit, w.key)
		}
	}

	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 5; trial++ {
		in := append(append([]posting.Key{}, keys...), keys[2], keys[5], keys[2])
		rng.Shuffle(len(in), func(i, j int) { in[i], in[j] = in[j], in[i] })
		held, fit := sp.Assign(in)
		if !reflect.DeepEqual(held, wantHeld) || !reflect.DeepEqual(fit, wantFit) {
			t.Fatalf("trial %d: held %v fit %v, want %v / %v", trial, held, fit, wantHeld, wantFit)
		}
	}
	if held, fit := sp.Assign(nil); held != nil || fit != nil {
		t.Fatalf("no keys: held %v fit %v", held, fit)
	}
}

// storeWith returns a store holding src's latest label for each of keys only.
func storeWith(t *testing.T, src *Store, keys []posting.Key) *Store {
	t.Helper()
	labels, _, err := src.Labels()
	if err != nil {
		t.Fatal(err)
	}
	dst, _, _ := newStore(t)
	for _, k := range keys {
		if err := dst.Put(labels[k]); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

// The registration requires the held-out computation to reproduce vani
// eval's all-labels numbers first: with every posting held out, its
// Promotion rows are exactly BuildReport's, over a ledger mixing "jev" and
// "d1-ultra" primaries, llm and unrecorded primaries, and one shadow
// recorded as "jev".
func TestHeldOutReproducesAllLabelsWhenAllAreHeldOut(t *testing.T) {
	names := Names{OpenRouter: "d1-ultra"}
	recs, s := renameFixture(t, LegacyOpenRouterName)
	want := BuildReport(recs, s, names)
	if len(want.Promotions) == 0 {
		t.Fatal("fixture has no promotion rows; the comparison would be vacuous")
	}
	all, h, err := BuildHeldOut(recs, s, names, Split{Name: "all", Salt: d1Split, Size: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(all, want) {
		t.Fatalf("all-labels report differs from BuildReport:\n%+v\n%+v", all, want)
	}
	if !reflect.DeepEqual(h.Promotions, want.Promotions) {
		t.Fatalf("held-out promotions differ from all-labels:\nheld %+v\nall  %+v", h.Promotions, want.Promotions)
	}
	if h.Ranked != 40 || len(h.HeldOut) != 40 || h.Fit != 0 {
		t.Fatalf("ranked %d held %d fit %d, want 40/40/0", h.Ranked, len(h.HeldOut), h.Fit)
	}
	// The alias is the same one: no primary is left as "jev", and the shadow
	// recorded as "jev" keeps its name.
	shadows := map[string]bool{}
	for _, p := range h.Promotions {
		if p.Primary == LegacyOpenRouterName {
			t.Fatalf("held-out row with primary jev: %+v", p)
		}
		shadows[p.Shadow] = true
	}
	if !shadows[LegacyOpenRouterName] || !shadows["local"] {
		t.Fatalf("shadow names %v, want jev and local as recorded", shadows)
	}
}

// With 40 labels, the registered split holds out 30 and its rows are what
// BuildReport computes over a label file holding only those 30.
func TestHeldOutIsTheFirstThirtyByRank(t *testing.T) {
	names := Names{OpenRouter: "d1-ultra"}
	sp := registered(t)
	recs, s := renameFixture(t, LegacyOpenRouterName)
	_, h, err := BuildHeldOut(recs, s, names, sp)
	if err != nil {
		t.Fatal(err)
	}
	if h.Ranked != 40 || len(h.HeldOut) != 30 || h.Fit != 10 {
		t.Fatalf("ranked %d held %d fit %d, want 40/30/10", h.Ranked, len(h.HeldOut), h.Fit)
	}
	var keys []posting.Key
	for _, r := range recs {
		keys = append(keys, r.Key)
	}
	wantHeld, _ := sp.Assign(keys)
	if !reflect.DeepEqual(h.HeldOut, wantHeld) {
		t.Fatalf("held-out keys %v, want %v", h.HeldOut, wantHeld)
	}
	oracle := BuildReport(recs, storeWith(t, s, wantHeld), names)
	if !reflect.DeepEqual(h.Promotions, oracle.Promotions) {
		t.Fatalf("held-out promotions\n%+v\nwant\n%+v", h.Promotions, oracle.Promotions)
	}
	all := BuildReport(recs, s, names)
	if reflect.DeepEqual(h.Promotions, all.Promotions) {
		t.Fatal("held-out rows equal all-labels rows; the fixture does not tell the halves apart")
	}

	// The rename moves no held-out number either.
	renamedRecs, renamedStore := renameFixture(t, "d1-ultra")
	_, hr, err := BuildHeldOut(renamedRecs, renamedStore, names, sp)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h, hr) {
		t.Fatalf("legacy and renamed ledgers split or score differently:\n%+v\n%+v", h, hr)
	}

	// Record order does not move the split or the rows.
	shuffled := append([]ledger.Record{}, recs...)
	rand.New(rand.NewSource(3)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	_, hs, err := BuildHeldOut(shuffled, s, names, sp)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h, hs) {
		t.Fatalf("record order changed the held-out result")
	}
}

// Fewer labels than the held-out size: every one is held out, nothing is fit,
// the rows equal the all-labels rows, and the verdict is insufficient labels.
func TestHeldOutWithFewerThanThirtyLabels(t *testing.T) {
	s, _, _ := newStore(t)
	var recs []ledger.Record
	for i := 0; i < 12; i++ {
		key := fmt.Sprintf("gh:co:%02d", i)
		if err := s.Put(Entry{Key: posting.Key(key), Labels: map[string]string{"work_mode": "remote"}}); err != nil {
			t.Fatal(err)
		}
		r := jevRec(key, "work_mode", "remote", "0.9000", "remote", true)
		r.Annotations["triage.work_mode.shadow_reading"] = "answered"
		r.Annotations["triage.work_mode.shadow_backend"] = "local"
		recs = append(recs, r)
	}
	all, h, err := BuildHeldOut(recs, s, Names{OpenRouter: "d1-ultra"}, registered(t))
	if err != nil {
		t.Fatal(err)
	}
	if h.Ranked != 12 || len(h.HeldOut) != 12 || h.Fit != 0 {
		t.Fatalf("ranked %d held %d fit %d", h.Ranked, len(h.HeldOut), h.Fit)
	}
	if !reflect.DeepEqual(h.Promotions, all.Promotions) || len(h.Promotions) != 1 {
		t.Fatalf("rows %+v vs %+v", h.Promotions, all.Promotions)
	}
	if v := h.Promotions[0].Verdict(); v != "insufficient labels (12/30)" {
		t.Fatalf("verdict %q", v)
	}
	if p := h.Promotions[0]; p.Primary != "d1-ultra" || p.Shadow != "local" {
		t.Fatalf("row names %+v", p)
	}
}

// A key relabelled (two lines) is one labelled posting, scored with its
// latest labels.
func TestHeldOutDuplicateLabelLinesRankOnce(t *testing.T) {
	s, _, _ := newStore(t)
	put := func(key, v string) {
		t.Helper()
		if err := s.Put(Entry{Key: posting.Key(key), Labels: map[string]string{"work_mode": v}}); err != nil {
			t.Fatal(err)
		}
	}
	put("gh:co:a", "onsite")
	put("gh:co:b", "remote")
	put("gh:co:a", "remote") // relabel: latest wins
	recs := []ledger.Record{
		jevRec("gh:co:a", "work_mode", "remote", "0.9", "remote", true),
		jevRec("gh:co:b", "work_mode", "remote", "0.9", "remote", true),
	}
	for _, r := range recs {
		r.Annotations["triage.work_mode.shadow_reading"] = "answered"
		r.Annotations["triage.work_mode.shadow_backend"] = "local"
	}
	_, h, err := BuildHeldOut(recs, s, Names{}, registered(t))
	if err != nil {
		t.Fatal(err)
	}
	if h.Ranked != 2 || len(h.HeldOut) != 2 {
		t.Fatalf("ranked %d held %v", h.Ranked, h.HeldOut)
	}
	p := h.Promotions[0]
	if p.Asked != 2 || p.PrimaryCorrect != 2 || p.ShadowCorrect != 2 {
		t.Fatalf("relabel not applied: %+v", p)
	}
}

// A skip label is not counted: a held-out posting skipped on a slot drops out
// of that slot's rows and stays in the others'. A posting whose every label
// is skip is still a labelled posting and still takes a place in the ranking,
// so the split is a function of the label file alone.
func TestHeldOutSkipLabels(t *testing.T) {
	sp := Split{Name: "t", Salt: d1Split, Size: 1000}
	build := func(skipKey string, allSkip bool) HeldOut {
		t.Helper()
		s, _, _ := newStore(t)
		var recs []ledger.Record
		for i := 0; i < 5; i++ {
			key := fmt.Sprintf("gh:co:%d", i)
			l := map[string]string{"work_mode": "remote", "seniority": "mid"}
			if key == skipKey {
				l["work_mode"] = Skip
				if allSkip {
					l["seniority"] = Skip
				}
			}
			if err := s.Put(Entry{Key: posting.Key(key), Labels: l}); err != nil {
				t.Fatal(err)
			}
			ann := map[string]string{}
			for _, slot := range []string{"work_mode", "seniority"} {
				pre := "triage." + slot
				v := l[slot]
				if v == Skip {
					v = map[string]string{"work_mode": "remote", "seniority": "mid"}[slot]
				}
				ann[pre] = v
				ann[pre+".reading"] = "answered"
				ann[pre+".backend"] = "d1-ultra"
				ann[pre+".shadow"] = v
				ann[pre+".shadow_reading"] = "answered"
				ann[pre+".shadow_backend"] = "local"
			}
			recs = append(recs, mkRec(key, posting.Triaged, ann))
		}
		_, h, err := BuildHeldOut(recs, s, Names{OpenRouter: "d1-ultra"}, sp)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	asked := func(h HeldOut) map[string]int {
		out := map[string]int{}
		for _, p := range h.Promotions {
			out[p.Slot] += p.Asked
		}
		return out
	}
	if got := asked(build("", false)); got["work_mode"] != 5 || got["seniority"] != 5 {
		t.Fatalf("no skip: %v", got)
	}
	if got := asked(build("gh:co:2", false)); got["work_mode"] != 4 || got["seniority"] != 5 {
		t.Fatalf("one slot skipped: %v", got)
	}
	h := build("gh:co:2", true)
	if got := asked(h); got["work_mode"] != 4 || got["seniority"] != 4 {
		t.Fatalf("all slots skipped: %v", got)
	}
	if h.Ranked != 5 || len(h.HeldOut) != 5 {
		t.Fatalf("an all-skip posting must still be ranked: ranked %d held %d", h.Ranked, len(h.HeldOut))
	}
}

// The held-out path refuses, with no figures, a label file it cannot read in
// full; the all-labels report counts and goes on, as before.
func TestHeldOutRefusesPartialOrUnreadableLabels(t *testing.T) {
	sp := registered(t)
	s, labelDir, _ := newStore(t)
	if err := s.Put(Entry{Key: "gh:co:a", Labels: map[string]string{"work_mode": "remote"}}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(labelDir, LabelsFile), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"v":1,"key":"gh:co:b","labels":{"work_mo`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if rep := BuildReport(nil, s, Names{}); rep.MalformedLines != 1 {
		t.Fatalf("all-labels report malformed %d, want 1", rep.MalformedLines)
	}
	if _, _, err := BuildHeldOut(nil, s, Names{}, sp); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("torn line: err %v", err)
	}

	if err := os.Remove(filepath.Join(labelDir, LabelsFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(labelDir, LabelsFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := BuildHeldOut(nil, s, Names{}, sp); err == nil {
		t.Fatal("unreadable label file accepted")
	}
	if _, _, err := BuildHeldOut(nil, s, Names{}, Split{Name: "zero"}); err == nil {
		t.Fatal("a split with no held-out size accepted")
	}
}

func TestLookupSplit(t *testing.T) {
	if _, err := LookupSplit(""); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, err := LookupSplit("d1-vani-jdtriage-2026-10-19"); err == nil || !strings.Contains(err.Error(), d1Split) {
		t.Fatalf("unknown name: %v", err)
	}
}

// The section states the rule and the counts, renders the same row format as
// the all-labels table, and quotes keys so a hand-edited key cannot forge a
// report line.
func TestHeldOutMarkdown(t *testing.T) {
	s, _, _ := newStore(t)
	key := "gh:co:a\n| work_mode | local | d1-ultra | 99 |"
	if err := s.Put(Entry{Key: posting.Key(key), Labels: map[string]string{"work_mode": "remote"}}); err != nil {
		t.Fatal(err)
	}
	r := jevRec(key, "work_mode", "remote", "0.9", "remote", true)
	r.Annotations["triage.work_mode.shadow_reading"] = "answered"
	r.Annotations["triage.work_mode.shadow_backend"] = "local"
	all, h, err := BuildHeldOut([]ledger.Record{r}, s, Names{OpenRouter: "d1-ultra"}, registered(t))
	if err != nil {
		t.Fatal(err)
	}
	md := h.Markdown()
	for _, frag := range []string{
		"## Held-out promotion: " + d1Split,
		`sha256("` + d1Split + `" || 0x00 || posting key)`,
		"labelled postings ranked: 1, held out: 1, fit: 0",
		"in-sample",
		"| work_mode | local | d1-ultra | 1 | 100.0% (1/1) | 100.0% (1/1) | 100.0% (1/1) | 100.0% (1/1) | insufficient labels (1/30) |",
		fmt.Sprintf("1. %q\n", key),
	} {
		if !strings.Contains(md, frag) {
			t.Errorf("held-out markdown lacks %q:\n%s", frag, md)
		}
	}
	if strings.Contains(md, "\n| work_mode | local | d1-ultra | 99 |") {
		t.Fatalf("a key forged a table row:\n%s", md)
	}
	// The all-labels markdown carries the same row: one formatter for both.
	if !bytes.Contains([]byte(all.Markdown()), []byte("| work_mode | local | d1-ultra | 1 |")) {
		t.Fatalf("all-labels markdown:\n%s", all.Markdown())
	}
	if strings.Contains(all.Markdown(), "Held-out") {
		t.Fatal("the all-labels report mentions the held-out split")
	}

	empty, err := func() (string, error) {
		s2, _, _ := newStore(t)
		_, h2, err := BuildHeldOut(nil, s2, Names{}, registered(t))
		return h2.Markdown(), err
	}()
	if err != nil || !strings.Contains(empty, "n/a") || !strings.Contains(empty, "ranked: 0, held out: 0, fit: 0") {
		t.Fatalf("empty label set: %v\n%s", err, empty)
	}
}
