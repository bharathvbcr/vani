package main

import (
	"sync"
	"testing"
)

// A reader racing the writer sees the old list or the new one, whole.
func TestAttackPinListReadDuringRewriteIsNeverPartial(t *testing.T) {
	warnBuf := pinWarnBuf(t)
	state := t.TempDir()
	old, nu := []string{"a1", "a2", "a3"}, []string{"b1", "b2", "b3", "b4", "b5"}
	if err := writeLabelledPins(state, old); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			h := old
			if i%2 == 0 {
				h = nu
			}
			if err := writeLabelledPins(state, h); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := 0; i < 300; i++ {
		got := pinLabelled(map[string]float64{}, state)
		if len(got) != len(old) && len(got) != len(nu) {
			t.Errorf("partial pin list: %v; warned %q", got, warnBuf.String())
			break
		}
	}
	close(stop)
	wg.Wait()
}
