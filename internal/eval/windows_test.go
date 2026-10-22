package eval

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// tick makes each label a second later than the last, so label-time order is
// the order the test wrote them in.
func tick(s *Store) {
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	n := 0
	s.Now = func() time.Time { n++; return t0.Add(time.Duration(n) * time.Second) }
}

// labelRange labels count postings "<prefix>NNN" remote and returns their
// records, every one with a lappi shadow reading. The first shadowRight postings
// have a wrong primary and a right shadow; the next primaryRight have a right
// primary and a wrong shadow; the rest agree and are right.
func labelRange(t *testing.T, s *Store, prefix string, count, shadowRight, primaryRight int) []ledger.Record {
	t.Helper()
	var recs []ledger.Record
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("%s%03d", prefix, i)
		if err := s.Put(Entry{Key: posting.Key(key), Labels: map[string]string{"work_mode": "remote"}}); err != nil {
			t.Fatal(err)
		}
		r := jevRec(key, "work_mode", "remote", "0.9000", "", true)
		r.Annotations["triage.work_mode.shadow_reading"] = "answered"
		r.Annotations["triage.work_mode.shadow_backend"] = "lappi"
		r.Annotations["triage.work_mode.shadow"] = "remote"
		switch {
		case i < shadowRight:
			r.Annotations["triage.work_mode"] = "onsite"
		case i < shadowRight+primaryRight:
			r.Annotations["triage.work_mode.shadow"] = "onsite"
		}
		recs = append(recs, r)
	}
	return recs
}

func memberCount(t *testing.T, s *Store) map[string]int {
	t.Helper()
	m, err := s.WindowMembers()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, w := range m {
		out[w]++
	}
	return out
}

func TestWindowsDisjointAndFillInOrder(t *testing.T) {
	s, _, _ := newStore(t)
	tick(s)
	recs := labelRange(t, s, "a", 130, 0, 0)
	if n, err := s.SyncWindows(recs); err != nil || n != 130 {
		t.Fatalf("sync: %d, %v", n, err)
	}
	if got := memberCount(t, s); got["window-1"] != 100 || got["window-2"] != 30 {
		t.Fatalf("members %v: window-1 closes at 100, the rest go to window-2", got)
	}
	// More labels, and a repeat sync: nothing already filed moves or doubles.
	before, _ := s.WindowMembers()
	recs = append(recs, labelRange(t, s, "b", 80, 0, 0)...)
	if n, err := s.SyncWindows(recs); err != nil || n != 70 {
		t.Fatalf("second sync: %d, %v (window-2 takes 70 more, the last 10 find no open window)", n, err)
	}
	if n, err := s.SyncWindows(recs); err != nil || n != 0 {
		t.Fatalf("repeat sync filed %d, %v", n, err)
	}
	after, _ := s.WindowMembers()
	for k, w := range before {
		if after[k] != w {
			t.Fatalf("%s moved from %s to %s", k, w, after[k])
		}
	}
	if got := memberCount(t, s); got["window-1"] != 100 || got["window-2"] != 100 || len(after) != 200 {
		t.Fatalf("members %v", got)
	}
	if _, ok, _ := s.OpenWindow(); ok {
		t.Fatal("both windows are closed")
	}
	// Disjoint: no key is in two windows (a map holds one value per key; the
	// counts above sum to the number of distinct keys).
	seen := map[posting.Key]bool{}
	for k := range after {
		if seen[k] {
			t.Fatalf("%s filed twice", k)
		}
		seen[k] = true
	}
}

func TestWindowsOnlyShadowAskedNonSkipLabels(t *testing.T) {
	s, _, _ := newStore(t)
	tick(s)
	recs := labelRange(t, s, "a", 3, 0, 0)
	// Not asked: a label without a shadow reading on the record.
	if err := s.Put(Entry{Key: "unasked", Labels: map[string]string{"work_mode": "remote"}}); err != nil {
		t.Fatal(err)
	}
	recs = append(recs, jevRec("unasked", "work_mode", "remote", "0.9000", "", true))
	// Skip only.
	if err := s.Put(Entry{Key: "skipped", Labels: map[string]string{"work_mode": Skip}}); err != nil {
		t.Fatal(err)
	}
	skipped := jevRec("skipped", "work_mode", "remote", "0.9000", "", true)
	skipped.Annotations["triage.work_mode.shadow_reading"] = "answered"
	recs = append(recs, skipped)
	if n, err := s.SyncWindows(recs); err != nil || n != 3 {
		t.Fatalf("filed %d, %v; want only the 3 shadow-asked, non-skip labels", n, err)
	}
}

func TestWindowsSurviveRestart(t *testing.T) {
	s, labelDir, textDir := newStore(t)
	tick(s)
	recs := labelRange(t, s, "a", 110, 0, 0)
	if _, err := s.SyncWindows(recs); err != nil {
		t.Fatal(err)
	}
	want, _ := s.WindowMembers()
	s2, err := NewStore(labelDir, textDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.WindowMembers()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("after restart: %d members, %v; want %d", len(got), err, len(want))
	}
	w, ok, err := s2.OpenWindow()
	if err != nil || !ok || w.Name != "window-2" {
		t.Fatalf("open window after restart: %v %v %v", w, ok, err)
	}
	if n, err := s2.SyncWindows(recs); err != nil || n != 0 {
		t.Fatalf("restart re-filed %d, %v", n, err)
	}
}

func TestWindowsRefuseChangedRegistration(t *testing.T) {
	s, _, _ := newStore(t)
	tick(s)
	recs := labelRange(t, s, "a", 5, 0, 0)
	if _, err := s.SyncWindows(recs); err != nil {
		t.Fatal(err)
	}
	old := Windows
	t.Cleanup(func() { Windows = old })
	Windows = []Window{{Name: "window-1", Size: 50}, {Name: "window-2", Size: 100}}
	if _, err := s.WindowMembers(); err == nil || !strings.Contains(err.Error(), "pre-registered") {
		t.Fatalf("a resized window must be refused, got %v", err)
	}
}

func windowVerdict(t *testing.T, s *Store, recs []ledger.Record) SlotVerdict {
	t.Helper()
	if _, err := s.SyncWindows(recs); err != nil {
		t.Fatal(err)
	}
	rep, err := BuildWindows(recs, s, Names{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.SlotVerdicts) != 1 {
		t.Fatalf("slot verdicts: %+v", rep.SlotVerdicts)
	}
	return rep.SlotVerdicts[0]
}

func TestSlotPromotableInOnlyOneWindowIsRefused(t *testing.T) {
	s, _, _ := newStore(t)
	tick(s)
	// window-1: the shadow right on 20 postings the primary got wrong.
	recs := labelRange(t, s, "a", 100, 20, 0)
	// window-2: the primary right on 20 the shadow got wrong.
	recs = append(recs, labelRange(t, s, "b", 100, 0, 20)...)
	v := windowVerdict(t, s, recs)
	if v.Promotable {
		t.Fatalf("promotable in one window only: %+v", v)
	}
	if v.Windows[0].Verdict != "promotable" || !strings.HasPrefix(v.Windows[1].Verdict, "not promotable") {
		t.Fatalf("per-window verdicts: %+v", v.Windows)
	}
	if !strings.Contains(v.Why, "window-2") || strings.Contains(v.Why, "window-1") {
		t.Fatalf("why should name only window-2: %q", v.Why)
	}
}

func TestSlotPromotableInBothWindowsIsPromotable(t *testing.T) {
	s, _, _ := newStore(t)
	tick(s)
	recs := labelRange(t, s, "a", 100, 20, 0)
	recs = append(recs, labelRange(t, s, "b", 100, 15, 1)...)
	v := windowVerdict(t, s, recs)
	if !v.Promotable || v.Windows[0].Verdict != "promotable" || v.Windows[1].Verdict != "promotable" {
		t.Fatalf("both windows promotable: %+v", v)
	}
	rep, _ := BuildWindows(recs, s, Names{})
	md := rep.Markdown()
	for _, want := range []string{"window-1: 100/100 postings (closed)", "window-2: 100/100", "work_mode / \"lappi\" vs \"jev\": promotable"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}

func TestSlotWithAnUnclosedWindowIsRefused(t *testing.T) {
	s, _, _ := newStore(t)
	tick(s)
	recs := labelRange(t, s, "a", 100, 20, 0)
	recs = append(recs, labelRange(t, s, "b", 60, 20, 0)...) // window-2 holds 60 of 100
	v := windowVerdict(t, s, recs)
	if v.Promotable || !strings.Contains(v.Why, "window-2 not closed (60/100") {
		t.Fatalf("an unclosed window cannot make a slot promotable: %+v", v)
	}
}

// fakeTeacher is a TeacherReadings over a map.
type fakeTeacher map[string]map[string]string

func (f fakeTeacher) Reading(key string) (map[string]string, bool) {
	m, ok := f[key]
	return m, ok
}

func queueFixture() []ledger.Record {
	rec := func(key string, extra map[string]string) ledger.Record {
		ann := map[string]string{"triage.work_mode": "remote", "triage.work_mode.reading": "answered"}
		for k, v := range extra {
			ann[k] = v
		}
		return mkRec(key, posting.Triaged, ann)
	}
	shadow := map[string]string{"triage.work_mode.shadow_reading": "answered"}
	var recs []ledger.Record
	recs = append(recs, rec("plain1", nil), rec("plain2", nil), rec("shadowed1", shadow), rec("shadowed2", shadow))
	recs = append(recs, rec("disagree1", map[string]string{"triage.work_mode.disagree": "onsite"}))
	recs = append(recs, rec("t1", nil), rec("t2", shadow))
	return recs
}

func TestQueueTeacherDisagreementsFirst(t *testing.T) {
	recs := queueFixture()
	teacher := fakeTeacher{
		"t1":        {"work_mode": "hybrid"},
		"t2":        {"work_mode": "onsite"},
		"plain1":    {"work_mode": "remote"}, // agrees: no promotion in order
		"disagree1": {"work_mode": "remote"},
	}
	got, err := QueueWith(recs, nil, 10, nil, nil, QueueOptions{Teacher: teacher})
	if err != nil {
		t.Fatal(err)
	}
	k := keys(got)
	if len(k) < 3 || k[0] != "t1" || k[1] != "t2" {
		t.Fatalf("teacher disagreements first, in key order: %v", k)
	}
	// After them: today's order (shadow disagreement, then shadowed, then the rest).
	base, _ := Queue(recs, nil, 10, nil, nil)
	var rest []string
	for _, key := range keys(base) {
		if key != "t1" && key != "t2" {
			rest = append(rest, key)
		}
	}
	if !reflect.DeepEqual(k[2:], rest) {
		t.Fatalf("after the teacher tier the order is Queue's: %v vs %v", k[2:], rest)
	}
}

func TestQueueNoTeacherKeepsOrderByteIdentical(t *testing.T) {
	recs := queueFixture()
	for i := 0; i < 40; i++ {
		recs = append(recs, mkRec(fmt.Sprintf("x%02d", i), posting.Triaged, map[string]string{"triage.work_mode": "remote"}))
	}
	for _, seed := range []int64{1, 2, 3} {
		want, err := Queue(recs, nil, 25, rand.New(rand.NewSource(seed)), nil)
		if err != nil {
			t.Fatal(err)
		}
		for name, opts := range map[string]QueueOptions{
			"nil teacher":      {},
			"empty teacher":    {Teacher: fakeTeacher{}},
			"agreeing teacher": {Teacher: fakeTeacher{"x01": {"work_mode": "remote"}}},
		} {
			got, err := QueueWith(recs, nil, 25, rand.New(rand.NewSource(seed)), nil, opts)
			if err != nil || !reflect.DeepEqual(keys(got), keys(want)) {
				t.Fatalf("seed %d, %s: %v, %v; want %v", seed, name, keys(got), err, keys(want))
			}
		}
	}
}

func TestQueueShadowOnlyForWindows(t *testing.T) {
	got, err := QueueWith(queueFixture(), nil, 10, nil, nil, QueueOptions{ShadowOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if !hasShadowReading(r) {
			t.Errorf("%s was not asked by the shadow", r.Key)
		}
	}
	if len(got) != 3 { // shadowed1, shadowed2, t2
		t.Fatalf("queue: %v", keys(got))
	}
}
