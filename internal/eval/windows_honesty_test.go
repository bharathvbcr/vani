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

// These tests failed against d058dd6: each is a finding from the adversarial
// review of the label windows (typed-nil teacher, frozen labels, time order, permanent failure, windows.json integrity).

// ptrTeacher is the shape a real teacher store has: a pointer receiver that
// reads a field. A nil *ptrTeacher stored in the interface is a typed nil.
type ptrTeacher struct{ m map[string]map[string]string }

func (p *ptrTeacher) Reading(key string) (map[string]string, bool) {
	v, ok := p.m[key]
	return v, ok
}

// QueueOptions.Teacher documents that a typed nil leaves the order alone; the
// nil guard in teacherDisagrees is "t == nil", which a typed nil passes.
func TestWindowHonestyTypedNilTeacherDoesNotPanic(t *testing.T) {
	var tn *ptrTeacher
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("QueueWith panicked on a typed-nil Teacher: %v", r)
		}
	}()
	got, err := QueueWith(queueFixture(), nil, 10, nil, nil, QueueOptions{Teacher: tn})
	if err != nil || len(got) == 0 {
		t.Fatalf("queue: %v, %v", keys(got), err)
	}
}

// A label that changes after its posting was filed is scored by its new value:
// membership is frozen, evidence is not. A closed window's verdict moves when
// a label is edited, so a window that "closed" promotable can be edited to
// refuse (or the reverse) with no trace.
func TestWindowHonestyRelabelAfterClosingKeepsClosedVerdict(t *testing.T) {
	s, _, _ := newStore(t)
	tick(s)
	recs := labelRange(t, s, "a", 100, 20, 0)
	recs = append(recs, labelRange(t, s, "b", 100, 20, 0)...)
	v := windowVerdict(t, s, recs)
	if !v.Promotable {
		t.Fatalf("setup: %+v", v)
	}
	for i := 0; i < 20; i++ {
		key := posting.Key(fmt.Sprintf("a%03d", i))
		if err := s.Put(Entry{Key: key, Labels: map[string]string{"work_mode": "onsite"}}); err != nil {
			t.Fatal(err)
		}
	}
	v2 := windowVerdict(t, s, recs)
	if v2.Promotable != v.Promotable {
		t.Fatalf("a closed window's verdict flipped from %v to %v by editing labels after it closed: %+v", v.Promotable, v2.Promotable, v2.Windows)
	}
}

// A posting labelled early whose shadow reading is recorded only later is
// filed late: window-2 then holds a label older than window-1's last.
func TestWindowHonestyWindowsDisjointInTime(t *testing.T) {
	s, _, _ := newStore(t)
	tick(s)
	recs := labelRange(t, s, "a", 101, 0, 0)
	late := recs[0]
	pending := append([]ledger.Record(nil), recs[1:]...)
	if _, err := s.SyncWindows(pending); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncWindows(append(pending, late)); err != nil {
		t.Fatal(err)
	}
	m, _ := s.WindowMembers()
	labels, _, _ := s.Labels()
	var max1, min2 time.Time
	for k, w := range m {
		at := labels[k].LabeledAt
		if w == "window-1" && at.After(max1) {
			max1 = at
		}
		if w == "window-2" && (min2.IsZero() || at.Before(min2)) {
			min2 = at
		}
	}
	if !min2.IsZero() && min2.Before(max1) {
		t.Fatalf("window-2 holds a posting labelled at %v, before window-1's last label at %v: not disjoint in time", min2, max1)
	}
}

// A window closes at 100 and never grows. A row that ends short of the floors
// (5 discordant of 10) can never reach them, yet the report says only
// "insufficient disagreements (5/10)", the words for a window still filling.
func TestWindowHonestyClosedWindowThatCannotPassSaysSo(t *testing.T) {
	s, _, _ := newStore(t)
	tick(s)
	recs := labelRange(t, s, "a", 100, 5, 0)
	recs = append(recs, labelRange(t, s, "b", 100, 20, 0)...)
	if _, err := s.SyncWindows(recs); err != nil {
		t.Fatal(err)
	}
	rep, err := BuildWindows(recs, s, Names{})
	if err != nil {
		t.Fatal(err)
	}
	md := rep.Markdown()
	if !strings.Contains(md, "final") && !strings.Contains(md, "cannot") && !strings.Contains(md, "can never") {
		t.Fatalf("closed window-1 can never promote, but the report says only %q", rep.SlotVerdicts[0].Why)
	}
}

// A field this version does not know (a newer writer's) is dropped on the next
// rewrite: a downgrade silently loses data instead of refusing.
func TestWindowHonestyWindowsFileUnknownFieldNotSilentlyDropped(t *testing.T) {
	s, _, _ := newStore(t)
	tick(s)
	recs := labelRange(t, s, "a", 2, 0, 0)
	if err := os.WriteFile(filepath.Join(s.labelDir, WindowsFile), []byte(`{"v":1,"windows":[{"name":"window-1","size":100},{"name":"window-2","size":100}],"members":{},"closed_at":{"window-1":"2026-10-21"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.SyncWindows(recs)
	raw, _ := os.ReadFile(filepath.Join(s.labelDir, WindowsFile))
	if err == nil && !strings.Contains(string(raw), "closed_at") {
		t.Fatalf("unknown field silently dropped on rewrite: %s", raw)
	}
}

// Members whose labels vanished leave OpenWindow and BuildWindows disagreeing
// about whether window-1 is closed.
func TestWindowHonestyMembersWithoutLabelsAgreeOnClosed(t *testing.T) {
	s, labelDir, _ := newStore(t)
	tick(s)
	recs := labelRange(t, s, "a", 100, 0, 0)
	if _, err := s.SyncWindows(recs); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(labelDir, LabelsFile)); err != nil {
		t.Fatal(err)
	}
	if w, ok, err := s.OpenWindow(); err != nil || !ok || w.Name != "window-2" {
		t.Fatalf("open: %v %v %v", w, ok, err)
	}
	rep, err := BuildWindows(recs, s, Names{})
	if err == nil && rep.Members["window-1"] < 100 {
		t.Fatalf("OpenWindow says window-1 is closed (100 filed) but BuildWindows counts %d members, and no one is told the labels are missing", rep.Members["window-1"])
	}
}
