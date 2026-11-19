package semantic

import (
	"context"
	"os"
	"testing"
)

// A stale .tmp left by a crashed compaction (the disk-full case this check exists
// for) holds the space the next compaction needs; checkSpace runs before it is removed.
func TestAttackStaleTmpBlocksCompactionForever(t *testing.T) {
	s := filledStore(t, 6)
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, make([]byte, 1<<16), 0o600); err != nil {
		t.Fatal(err)
	}
	needed := s.hdrLen + 6*primSize
	s.freeFn = func(string) (uint64, error) {
		if _, err := os.Stat(tmp); err == nil {
			return uint64(needed - 1), nil // the stale file holds the space
		}
		return uint64(needed), nil
	}
	if ev, err := s.compact(context.Background(), vecPrio{}, evictTo(s.maxBytes)); err != nil || !ev.Compacted {
		t.Fatalf("compaction refused because of its own stale temp file: %+v %v", ev, err)
	}
}

