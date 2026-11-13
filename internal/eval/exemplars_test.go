package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// exLabels seeds nYes yes and nNo no labels of distinct postings "y0".., "n0"..,
// each with a ledger record titled to match.
func exLabels(nYes, nNo int, at time.Time) (map[posting.Key]Entry, []ledger.Record) {
	labels := map[posting.Key]Entry{}
	var recs []ledger.Record
	add := func(prefix string, n int, verdict string) {
		for i := range n {
			k := posting.Key(fmt.Sprintf("%s%d", prefix, i))
			labels[k] = Entry{V: SchemaVersion, Key: k, Labels: map[string]string{WouldApply: verdict}, Source: SourceManual, LabeledAt: at}
			recs = append(recs, ledger.Record{Key: k, Posting: posting.Posting{Key: k, Title: fmt.Sprintf("Title %s%d", prefix, i), Company: "Co " + prefix}})
		}
	}
	add("y", nYes, "yes")
	add("n", nNo, "no")
	return labels, recs
}

func TestFreezeCountsOnlyYesAndNoAndRefusesAThinSet(t *testing.T) {
	labels, recs := exLabels(4, 5, pairT0)
	labels["m0"] = Entry{Key: "m0", Labels: map[string]string{WouldApply: "maybe"}, Source: SourceManual}
	labels["s0"] = Entry{Key: "s0", Labels: map[string]string{WouldApply: Skip}, Source: SourceManual}
	labels["c0"] = Entry{Key: "c0", Labels: map[string]string{WouldApply: "yes"}, Source: "closed:dead", PSelect: 0.1}
	labels["t0"] = Entry{Key: "t0", Labels: map[string]string{"role_fit": "fit"}}
	set, st, err := FreezeExemplars(labels, recs, pairT0)
	if err != nil {
		t.Fatal(err)
	}
	if st.YesKept != 4 || st.NoKept != 5 || st.Maybe != 1 || st.Skipped != 1 || st.Closed != 1 {
		t.Errorf("stats %+v", st)
	}
	if len(set.Yes) != 4 || len(set.No) != 5 || set.Validate() != nil || set.SHA256 == "" {
		t.Errorf("set %+v err %v", set, set.Validate())
	}
	for _, e := range append(append([]ExemplarEntry{}, set.Yes...), set.No...) {
		if e.Key == "m0" || e.Key == "s0" || e.Key == "c0" || e.Key == "t0" {
			t.Errorf("%s is in the exemplar set", e.Key)
		}
	}

	thin, thinRecs := exLabels(2, 9, pairT0)
	if _, st, err := FreezeExemplars(thin, thinRecs, pairT0); err == nil || st.YesKept != 2 {
		t.Errorf("a set with 2 yes was frozen (err %v, stats %+v)", err, st)
	}
	if _, _, err := FreezeExemplars(nil, nil, pairT0); err == nil {
		t.Error("an empty label set was frozen")
	}
}

func TestFreezeNeedsLedgerTitlesAndFoldsRepeatedJobs(t *testing.T) {
	labels, recs := exLabels(5, 5, pairT0)
	// y0 and y1 are the same job reposted under two keys; y2 has no ledger record.
	recs[1].Posting.Title, recs[1].Posting.Company = " TITLE   y0 ", "co y"
	recs[0].Posting.Title, recs[0].Posting.Company = "Title y0", "Co y"
	recs = append(recs[:2], recs[3:]...) // drop y2's record
	set, st, err := FreezeExemplars(labels, recs, pairT0)
	if err != nil {
		t.Fatal(err)
	}
	if st.Repeats != 1 || st.NotInLedger != 1 || st.YesKept != 3 {
		t.Errorf("stats %+v", st)
	}
	if len(set.Reposts()) != 8 {
		t.Errorf("%d reposts keys, want 8", len(set.Reposts()))
	}
}

func TestFreezeBoundsEachClassDeterministically(t *testing.T) {
	labels, recs := exLabels(80, 80, pairT0)
	a, _, err := FreezeExemplars(labels, recs, pairT0)
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := FreezeExemplars(labels, recs, pairT0.Add(time.Hour))
	if len(a.Yes) != 30 || len(a.No) != 30 {
		t.Fatalf("%d yes, %d no kept, want the bound of 30", len(a.Yes), len(a.No))
	}
	if a.SHA256 != b.SHA256 {
		t.Error("the same labels froze to different sets (the subset is not deterministic, or the time is in the hash)")
	}
	first := a.Yes[0].Key
	if first == "y0" && a.Yes[1].Key == "y1" && a.Yes[2].Key == "y2" {
		t.Error("the bound kept the first postings by key, not a hash-ordered subset")
	}
}

func TestExemplarFileRoundTripAndNeverOverwrites(t *testing.T) {
	labels, recs := exLabels(3, 3, pairT0)
	set, _, err := FreezeExemplars(labels, recs, pairT0)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, ExemplarsFile)
	if _, err := ReadExemplars(path); err != ErrNotFrozen {
		t.Fatalf("a missing file: %v", err)
	}
	if err := WriteExemplars(path, set, false); err != nil {
		t.Fatal(err)
	}
	got, err := ReadExemplars(path)
	if err != nil || got.SHA256 != set.SHA256 || !got.FrozenAt.Equal(pairT0) {
		t.Fatalf("read back %+v err %v", got, err)
	}
	if err := WriteExemplars(path, set, false); err == nil {
		t.Error("a frozen set was overwritten")
	}
	newer, _, _ := FreezeExemplars(labels, recs, pairT0.Add(24*time.Hour))
	newer.Yes[0].Title = "A different title"
	newer.SHA256 = newer.contentSHA256()
	if err := WriteExemplars(path, newer, true); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "silver_exemplars.20261112T090000Z.json")
	old, err := ReadExemplars(archive)
	if err != nil || old.SHA256 != set.SHA256 {
		t.Errorf("the old set was not kept beside the new one: %v", err)
	}
	if cur, _ := ReadExemplars(path); cur.SHA256 != newer.SHA256 {
		t.Error("--replace did not write the new set")
	}
}

func TestExemplarFileRefusesTampering(t *testing.T) {
	labels, recs := exLabels(3, 3, pairT0)
	set, _, _ := FreezeExemplars(labels, recs, pairT0)
	path := filepath.Join(t.TempDir(), ExemplarsFile)
	if err := WriteExemplars(path, set, false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	for name, edit := range map[string]string{
		"a title edited":   strings.Replace(string(raw), "Title y0", "Title y0 EDITED", 1),
		"a hash edited":    strings.Replace(string(raw), set.SHA256[:8], "00000000", 1),
		"an unknown field": strings.Replace(string(raw), `"v": 1`, `"v": 1, "extra": true`, 1),
		"trailing data":    string(raw) + "{}",
		"not json":         "garbage",
		"empty":            "",
		"another version":  strings.Replace(string(raw), `"v": 1`, `"v": 2`, 1),
	} {
		if edit == string(raw) {
			t.Fatalf("%s: the edit changed nothing", name)
		}
		if err := os.WriteFile(path, []byte(edit), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadExemplars(path); err == nil || err == ErrNotFrozen {
			t.Errorf("%s was accepted (%v)", name, err)
		}
	}
	// A planted oversized file is refused, not read whole.
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxExemplarsFile+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadExemplars(path); err == nil {
		t.Error("an oversized exemplar file was accepted")
	}
}

func TestEvaluationLabelsAreOnlyWhatPostdatesTheFreeze(t *testing.T) {
	labels, recs := exLabels(4, 4, pairT0)
	set, _, err := FreezeExemplars(labels, recs, pairT0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[posting.Key]posting.Posting{}
	for _, r := range recs {
		byKey[r.Key] = r.Posting
	}
	after := pairT0.Add(2 * time.Hour)
	put := func(k, verdict, source string, at time.Time, title, company string) {
		labels[posting.Key(k)] = Entry{V: SchemaVersion, Key: posting.Key(k), Labels: map[string]string{WouldApply: verdict}, Source: source, PSelect: map[bool]float64{true: 0.2}[source != SourceManual], LabeledAt: at}
		byKey[posting.Key(k)] = posting.Posting{Key: posting.Key(k), Title: title, Company: company}
	}
	put("new-yes", "yes", SourceManual, after, "Brand New", "NewCo")
	put("new-no", "no", "decile:3", after, "Another New", "NewCo")
	put("at-freeze", "yes", SourceManual, pairT0.Add(time.Hour), "Exactly At", "NewCo") // not strictly after
	put("maybe", "maybe", SourceManual, after, "Maybe One", "NewCo")
	put("skip", Skip, SourceManual, after, "Skipped One", "NewCo")
	put("closed", "no", "closed:dead", after, "Closed One", "NewCo")
	put("repost", "yes", SourceManual, after, "  TITLE y0", "co Y") // the same job as exemplar y0
	put("orphan", "no", SourceManual, after, "", "")
	delete(byKey, "orphan") // no ledger record
	// An exemplar relabelled after the freeze: labels are latest-wins, so it looks new.
	relabel := labels["y1"]
	relabel.LabeledAt = after
	labels["y1"] = relabel

	got, st := EvaluationLabels(labels, set, byKey)
	if len(got) != 2 || got["new-yes"] != "yes" || got["new-no"] != "no" {
		t.Errorf("evaluation set %v", got)
	}
	// Before the freeze: the 7 exemplar labels not relabelled, and "at-freeze",
	// which is not strictly after it.
	want := EvalLabelStats{Kept: 2, BeforeFreeze: 8, Exemplar: 1, Repost: 1, NotInLedger: 1, Maybe: 1, Skipped: 1, Closed: 1}
	if st != want {
		t.Errorf("stats %+v, want %+v", st, want)
	}
	// A label made before the freeze never counts, however it is scored later.
	if _, ok := got["y0"]; ok {
		t.Error("an exemplar is in the evaluation set")
	}
	if _, ok := got["n3"]; ok {
		t.Error("a label made before the freeze is in the evaluation set")
	}
}
