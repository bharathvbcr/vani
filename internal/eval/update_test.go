package eval

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func mustLabels(t *testing.T, s *Store) map[posting.Key]Entry {
	t.Helper()
	got, _, err := s.Labels()
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func wouldApplyEntry(key, verdict string) Entry {
	return Entry{Key: posting.Key(key), Labels: map[string]string{WouldApply: verdict}, Source: "decile:3", PSelect: 0.5}
}

// A slot label written after a would_apply label keeps it, with its stratum and
// inclusion probability; the slot lands beside it.
func TestPutSlotLabelKeepsWouldApplyAndSampling(t *testing.T) {
	s, _, _ := newStore(t)
	if err := s.Put(wouldApplyEntry("gh:a:1", "yes")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{Key: "gh:a:1", Labels: map[string]string{"work_mode": "remote"}}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Labels()
	e := got["gh:a:1"]
	if e.Labels[WouldApply] != "yes" || e.Labels["work_mode"] != "remote" || e.Source != "decile:3" || e.PSelect != 0.5 {
		t.Fatalf("entry after slot label: %+v", e)
	}
}

// A re-label replaces the triage slots (no stale slot survives) but still
// keeps the would_apply.
func TestPutRelabelReplacesSlotsKeepsWouldApply(t *testing.T) {
	s, _, _ := newStore(t)
	if err := s.Put(Entry{Key: "gh:a:1", Labels: map[string]string{"work_mode": "remote", "seniority": Skip}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(wouldApplyEntry("gh:a:1", "maybe")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{Key: "gh:a:1", Labels: map[string]string{"work_mode": "onsite"}}); err != nil {
		t.Fatal(err)
	}
	got := mustLabels(t, s)["gh:a:1"].Labels
	if _, stale := got["seniority"]; stale || got["work_mode"] != "onsite" || got[WouldApply] != "maybe" {
		t.Fatalf("labels %v", got)
	}
}

// A would_apply label written after slot labels keeps them (the audit writes a
// would_apply-only entry), and a new text hash replaces the old one.
func TestPutWouldApplyKeepsSlotLabels(t *testing.T) {
	s, _, _ := newStore(t)
	h := HashText("one")
	if err := s.Put(Entry{Key: "gh:a:1", Labels: map[string]string{"work_mode": "remote"}, TextSHA256: h}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(wouldApplyEntry("gh:a:1", "no")); err != nil {
		t.Fatal(err)
	}
	e := mustLabels(t, s)["gh:a:1"]
	if e.Labels["work_mode"] != "remote" || e.Labels[WouldApply] != "no" || e.TextSHA256 != h {
		t.Fatalf("entry after would_apply: %+v", e)
	}
}

// Merge must not let a label with no slots through by borrowing prev's.
func TestPutStillRefusesEntryWithNoSlots(t *testing.T) {
	s, _, _ := newStore(t)
	if err := s.Put(Entry{Key: "gh:a:1", Labels: map[string]string{"work_mode": "remote"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{Key: "gh:a:1"}); err == nil {
		t.Fatal("an entry with no slots was accepted")
	}
}

// Independent Stores on one directory (as two processes have) serialise on the
// file lock: N concurrent Updates each add one slot or count, none is lost.
func TestUpdateAcrossStoresLosesNothing(t *testing.T) {
	first, labelDir, textDir := newStore(t)
	if err := first.Put(Entry{Key: "gh:a:1", Labels: map[string]string{"work_mode": "remote"}}); err != nil {
		t.Fatal(err)
	}
	const n = 10
	var wg sync.WaitGroup
	won := make(chan int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := NewStore(labelDir, textDir)
			if err != nil {
				t.Error(err)
				return
			}
			err = s.Update("gh:a:1", func(prev Entry, _ bool) (Entry, error) {
				if _, ok := prev.Labels[WouldApply]; ok {
					return Entry{}, errors.New("answered")
				}
				return Merge(prev, wouldApplyEntry("gh:a:1", []string{"yes", "no"}[i%2])), nil
			})
			if err == nil {
				won <- i
			}
		}()
	}
	wg.Wait()
	close(won)
	if len(won) != 1 {
		t.Fatalf("%d updates won, want exactly 1", len(won))
	}
	if e := mustLabels(t, first)["gh:a:1"]; e.Labels["work_mode"] != "remote" || e.Labels[WouldApply] == "" {
		t.Fatalf("entry %+v", e)
	}
}

// fn sees the entry as it is when the lock is taken, and an fn error writes
// nothing.
func TestUpdatePassesLatestEntryAndErrorWritesNothing(t *testing.T) {
	s, _, _ := newStore(t)
	err := s.Update("gh:a:1", func(prev Entry, exists bool) (Entry, error) {
		if exists || len(prev.Labels) != 0 {
			t.Errorf("prev of a new key: %+v exists=%v", prev, exists)
		}
		return Entry{}, errors.New("no")
	})
	if err == nil || err.Error() != "no" {
		t.Fatalf("err %v, want fn's error as is", err)
	}
	if got := mustLabels(t, s); len(got) != 0 {
		t.Fatalf("a failed update wrote %v", got)
	}
	if err := s.Update("gh:a:1", func(Entry, bool) (Entry, error) {
		return Entry{Key: "gh:other:1", Labels: map[string]string{"work_mode": "remote"}}, nil
	}); err == nil {
		t.Fatal("an update returning another key's entry was accepted")
	}
}

// The lock is held for the length of fn, so an fn that does store I/O (here a
// Put) is refused with ErrBusy after lockWait instead of deadlocking.
func TestUpdateFnThatWritesTheStoreFailsLoudly(t *testing.T) {
	old := lockWait
	lockWait = 100 * time.Millisecond
	defer func() { lockWait = old }()
	s, _, _ := newStore(t)
	var inner error
	err := s.Update("gh:a:1", func(Entry, bool) (Entry, error) {
		inner = s.Put(Entry{Key: "gh:b:1", Labels: map[string]string{"work_mode": "remote"}})
		return Entry{Key: "gh:a:1", Labels: map[string]string{"work_mode": "remote"}}, nil
	})
	if !errors.Is(inner, ErrBusy) {
		t.Fatalf("inner write: %v, want ErrBusy", inner)
	}
	if err != nil {
		t.Fatalf("outer update: %v", err)
	}
}

func TestUpdateOnNilStore(t *testing.T) {
	var s *Store
	if err := s.Update("gh:a:1", func(Entry, bool) (Entry, error) { return Entry{}, nil }); err == nil {
		t.Fatal("nil store accepted an update")
	}
}
