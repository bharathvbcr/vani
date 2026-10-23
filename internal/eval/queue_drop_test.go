package eval

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func postingKey(s string) posting.Key { return posting.Key(s) }

// queue is Queue for tests that expect labels to be readable.
func queue(recs []ledger.Record, s *Store, n int, rng *rand.Rand) []ledger.Record {
	out, err := Queue(recs, s, n, rng, nil)
	if err != nil {
		panic(err)
	}
	return out
}

var roleFitRule = []DropRule{{Slot: "role_fit", Values: []string{"mismatch_domain"}}, {Slot: "seniority", Values: []string{"staff_plus"}}}

func dropped(key, backend, slot, val, slotBackend string) ledger.Record {
	ann := map[string]string{"triage.backend": backend, "triage." + slot: val}
	if slotBackend != "" {
		ann["triage."+slot+".backend"] = slotBackend
	}
	return mkRec(key, posting.Dropped, ann)
}

func TestDropClass(t *testing.T) {
	cases := []struct {
		name string
		rec  ledger.Record
		want string
	}{
		{"title role_fit", dropped("a", "title", "role_fit", "mismatch_domain", ""), "title:role_fit"},
		{"model role_fit", dropped("b", "d1-ultra", "role_fit", "mismatch_domain", "d1-ultra"), "model:role_fit"},
		{"merged chain, slot answered by title", dropped("c", "d1-ultra+title", "seniority", "staff_plus", "title"), "title:seniority"},
		{"merged chain, slot answered by model", dropped("d", "d1-ultra+llm", "seniority", "staff_plus", "llm"), "model:seniority"},
		{"no rule matches", dropped("e", "d1-ultra", "role_fit", "strong_fit", ""), "model:other"},
		{"no backend annotation", mkRec("f", posting.Dropped, nil), "unknown:other"},
		{"not dropped", mkRec("g", posting.Triaged, map[string]string{"triage.role_fit": "mismatch_domain"}), ""},
		{"hostile backend text is not a class", dropped("h", "T!tle\n; rm -rf", "role_fit", "mismatch_domain", ""), "model:role_fit"},
	}
	for _, c := range cases {
		if got := DropClass(c.rec, roleFitRule); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// The class never carries a rule value (it can reveal work authorization).
	if got := DropClass(dropped("i", "title", "role_fit", "mismatch_domain", ""), roleFitRule); strings.Contains(got, "mismatch") {
		t.Errorf("class names the rule value: %q", got)
	}
}

// Dropped postings were pooled with the kept ones in the triage tiers, so a
// drop rule's false drops were a small, unmeasurable share. They are now a
// separate stratum, balanced across drop classes.
func TestQueueDrawsDroppedAsOwnStratumByClass(t *testing.T) {
	s, _, _ := newStore(t)
	tri := map[string]string{"triage.work_mode": "remote", "triage.work_mode.reading": "answered"}
	var recs []ledger.Record
	for _, k := range []string{"k1", "k2", "k3", "k4", "k5", "k6"} {
		recs = append(recs, mkRec(k, posting.Triaged, tri))
	}
	for _, k := range []string{"t1", "t2", "t3", "t4"} {
		recs = append(recs, dropped(k, "title", "role_fit", "mismatch_domain", ""))
	}
	recs = append(recs, dropped("m1", "d1-ultra", "role_fit", "mismatch_domain", "d1-ultra"))

	got, err := Queue(recs, s, 6, rand.New(rand.NewSource(3)), roleFitRule)
	if err != nil {
		t.Fatal(err)
	}
	classes := map[string]int{}
	kept := 0
	for _, r := range got {
		if r.Status == posting.Dropped {
			classes[DropClass(r, roleFitRule)]++
		} else {
			kept++
		}
	}
	if len(got) != 6 || kept != 4 || classes["title:role_fit"] != 1 || classes["model:role_fit"] != 1 {
		t.Fatalf("want 4 kept + one per drop class, got kept=%d classes=%v (%v)", kept, classes, keys(got))
	}
	// Same seed, same queue.
	again, _ := Queue(recs, s, 6, rand.New(rand.NewSource(3)), roleFitRule)
	if strings.Join(keys(got), ",") != strings.Join(keys(again), ",") {
		t.Fatalf("not deterministic: %v vs %v", keys(got), keys(again))
	}
}

func TestQueueDroppedEdgeCases(t *testing.T) {
	s, _, _ := newStore(t)
	// Only dropped postings exist: they fill the whole queue (the share lends).
	recs := []ledger.Record{dropped("t1", "title", "role_fit", "mismatch_domain", ""), dropped("t2", "title", "role_fit", "mismatch_domain", "")}
	if got := queue(recs, s, 5, nil); len(got) != 2 {
		t.Errorf("dropped-only pool: %v", keys(got))
	}
	// Never the dead, never an already labelled dropped posting.
	if err := s.Put(Entry{Key: "t1", Labels: map[string]string{"work_mode": "remote"}}); err != nil {
		t.Fatal(err)
	}
	recs = append(recs, mkRec("dead", posting.Dead, map[string]string{"triage.backend": "title"}))
	if got := keys(queue(recs, s, 5, nil)); strings.Join(got, ",") != "t2" {
		t.Errorf("labelled or dead leaked: %v", got)
	}
	// A dropped share never exceeds a third of n while kept postings last.
	var many []ledger.Record
	for i := 0; i < 30; i++ {
		many = append(many, dropped("d"+string(rune('a'+i%26))+string(rune('a'+i/26)), "title", "role_fit", "mismatch_domain", ""))
		many = append(many, mkRec("p"+string(rune('a'+i%26))+string(rune('a'+i/26)), posting.Triaged, map[string]string{"triage.work_mode": "remote"}))
	}
	got := queue(many, s, 9, rand.New(rand.NewSource(1)))
	nd := 0
	for _, r := range got {
		if r.Status == posting.Dropped {
			nd++
		}
	}
	if len(got) != 9 || nd != 3 {
		t.Errorf("n=9: %d dropped of %d", nd, len(got))
	}
}

// Queue used to treat unreadable labels as none, and could re-queue work that
// was done. Like `label`, it must fail closed.
func TestQueueFailsClosedOnUnreadableLabels(t *testing.T) {
	s, labelDir, _ := newStore(t)
	// A directory where labels.jsonl should be makes the read fail.
	if err := os.Mkdir(filepath.Join(labelDir, LabelsFile), 0o755); err != nil {
		t.Fatal(err)
	}
	recs := []ledger.Record{mkRec("a", posting.Triaged, map[string]string{"triage.work_mode": "remote"})}
	got, err := Queue(recs, s, 5, nil, nil)
	if err == nil || got != nil {
		t.Fatalf("unreadable labels: got %v, err %v; want no queue and an error", keys(got), err)
	}
	// A nil store is the one caller that asked for no label check.
	if got, err := Queue(recs, nil, 5, nil, nil); err != nil || len(got) != 1 {
		t.Errorf("nil store: %v %v", keys(got), err)
	}
}
