package ledger

import (
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// A description that is not valid UTF-8 is written by encoding/json with U+FFFD
// in place of the bad bytes. The fingerprint taken in Append must be of what
// the line holds, or the body is "unavailable" the moment it is evicted and a
// reopen is refused.
func TestAdvEvictInvalidUTF8Body(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	defer f.Close()
	bad := "salary \xff\xfe range"
	appendAll(t, f, bodyEvent("gh:a:1", posting.Live, t0, bad), ev("gh:a:1", posting.Dead, t0.Add(time.Hour)))
	g, _ := f.Get("gh:a:1")
	if g.BodyEvicted || g.Posting.Description == "" {
		t.Errorf("Get after evicting an invalid-UTF-8 body: evicted=%v desc=%q", g.BodyEvicted, g.Posting.Description)
	}
	if err := f.Append(bg, ev("gh:a:1", posting.Triaged, t0.Add(2*time.Hour))); err != nil {
		t.Fatalf("reopen of an invalid-UTF-8 body refused: %v", err)
	}
}
