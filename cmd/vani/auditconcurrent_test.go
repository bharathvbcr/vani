package main

import (
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
)

// Concurrent answers to one key: all but one must be refused. The answer reads
// the labels, then appends; nothing serialises the two.
func TestAuditAnswerConcurrentSameKey(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 5, 1, 1)
	putEntry(t, state, eval.Entry{Key: "gh:k:004", Labels: map[string]string{"role_fit": "strong_fit"}})
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _, _ = do(t, "audit", "answer", "--key", "gh:k:004", "--verdict", []string{"yes", "no"}[i%2], "--source", "decile:3", "--p-select", "0.5")
		}()
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == 0 {
			ok++
		}
	}
	if ok != 1 {
		t.Errorf("%d concurrent answers succeeded for one key, want 1 (%v)", ok, codes)
	}
}

// A slot label written after an audit answer must not erase the would_apply,
// the mirror image of the merge the audit now does (eval.Label writes a
// slot-only entry, and the store keeps the latest line per key whole).
func TestSlotLabelAfterAuditKeepsWouldApply(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 5, 1, 1)
	mustCode(t, 0, "audit", "answer", "--key", "gh:k:001", "--verdict", "yes", "--source", "decile:3", "--p-select", "0.5")
	putEntry(t, state, eval.Entry{Key: "gh:k:001", Labels: map[string]string{"role_fit": "strong_fit"}})
	if _, ok := readLabels(t, state)["gh:k:001"].Labels[eval.WouldApply]; !ok {
		t.Error("a later slot label erased the audit answer")
	}
}
