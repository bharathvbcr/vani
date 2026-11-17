package eval

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A record whose text was not loaded is refused by both labelling loops with
// ErrBodyEvicted naming the posting, before anything is shown or written, and
// the records before it in the session keep their labels.
func TestW1dLabelRefusesAnEvictedBodyWithACodedError(t *testing.T) {
	evicted := mkRec("gh:a:2", posting.Dropped, nil)
	evicted.Posting.Description, evicted.BodyEvicted = "", true

	t.Run("Label", func(t *testing.T) {
		s, _, _ := newStore(t)
		recs := []ledger.Record{mkRec("gh:a:1", posting.Triaged, nil), evicted}
		var out bytes.Buffer
		n, err := Label(strings.NewReader(strings.Repeat("1\n", 40)), &out, recs, s, Names{})
		if !errors.Is(err, ErrBodyEvicted) || !strings.Contains(err.Error(), "gh:a:2") {
			t.Fatalf("Label err = %v, want ErrBodyEvicted naming gh:a:2", err)
		}
		if _, has := mustLabels(t, s)["gh:a:2"]; has || n > 1 {
			t.Fatalf("n=%d: a label was written for the evicted posting", n)
		}
	})
	t.Run("LabelAudit", func(t *testing.T) {
		s, _, _ := newStore(t)
		recs := []ledger.Record{evicted}
		picks := []AuditPick{{Key: "gh:a:2", Source: "dropped:x", PSelect: 1}}
		n, err := LabelAudit(strings.NewReader("1\n"), &bytes.Buffer{}, picks, recs, s, Names{}, false)
		if !errors.Is(err, ErrBodyEvicted) || n != 0 || len(mustLabels(t, s)) != 0 {
			t.Fatalf("LabelAudit n=%d err=%v, want ErrBodyEvicted and no label", n, err)
		}
	})
}
