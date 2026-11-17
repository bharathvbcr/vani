package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// A dropped record whose body cannot be read back is not a source of readings:
// keyed under the hash of empty text it would hand its reading to any posting
// with no text. Records whose body reads back still seed the cache.
func TestW1dReadCacheSeedSkipsUnreadableBody(t *testing.T) {
	dir := t.TempDir()
	l := reopenLedger(t, dir, nil)
	a, b := dupPost(t, "1", "Remote", longDesc), dupPost(t, "2", "Remote", "Operate large ML serving fleets, remote.")
	reconcileFunnel(t, l, a, b)
	tr := &seqTriager{answers: []decide.Answer{okAns("onsite", 0.95), okAns("onsite", 0.95)}}
	for _, k := range []posting.Key{a.Key, b.Key} {
		if err := newRunner(l, tr, Funnel{Cache: NewReadCache(), ModelID: "m1"}).Advance(ctx, k); err != nil {
			t.Fatal(err)
		}
		if rec(t, l, k).Status != posting.Dropped {
			t.Fatalf("setup: %s is %s, want dropped", k, rec(t, l, k).Status)
		}
	}
	// Only a's line is edited, after the ledger evicted it: its read-back fails.
	path := filepath.Join(dir, "events.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	garbled := strings.Repeat("Z", len(longDesc))
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), longDesc, garbled)), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := rec(t, l, a.Key); !r.BodyEvicted {
		t.Fatalf("setup: a's body reads back (%q)", r.Posting.Description)
	}

	c := NewReadCache()
	c.seed(l, "m1")
	if len(c.entries) != 1 {
		t.Fatalf("seeded %d readings, want 1 (b only)", len(c.entries))
	}
	for _, e := range c.entries {
		if e.from != b.Key {
			t.Fatalf("seeded a reading from %s, want %s", e.from, b.Key)
		}
	}
	if _, ok := c.entries[readKey(posting.Posting{Title: a.Title, Location: a.Location}, "m1")]; ok {
		t.Fatal("a reading is keyed under the hash of empty text")
	}
}
