package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Concurrent Puts onto a torn tail: only the fragment is counted bad, every
// label lands, and no Put glues onto the fragment.
func TestConcurrentPutsOnTornTailLoseNothingAndSkipOnlyTheFragment(t *testing.T) {
	s, labelDir, _ := newStore(t)
	if err := os.WriteFile(filepath.Join(labelDir, LabelsFile), []byte(`{"v":1,"key":"gh:a:0","labels":{"work_mode":"rem`), 0o644); err != nil {
		t.Fatal(err)
	}
	const n = 12
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Put(Entry{Key: keyN(i), Labels: map[string]string{"work_mode": "remote"}}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, bad, err := s.Labels()
	if err != nil || bad != 1 || len(got) != n {
		t.Fatalf("labels %d bad %d err %v, want %d and exactly 1", len(got), bad, err, n)
	}
}

func TestPutAfterLoneCarriageReturnTail(t *testing.T) {
	s, labelDir, _ := newStore(t)
	if err := os.WriteFile(filepath.Join(labelDir, LabelsFile), []byte("\r"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{Key: "gh:a:1", Labels: map[string]string{"work_mode": "remote"}}); err != nil {
		t.Fatal(err)
	}
	got, bad, err := s.Labels()
	if err != nil || bad != 0 || len(got) != 1 {
		t.Fatalf("labels %d bad %d err %v", len(got), bad, err)
	}
}

func keyN(i int) posting.Key { return posting.Key(fmt.Sprintf("gh:a:%d", i)) }
