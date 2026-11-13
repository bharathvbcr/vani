package digest

import (
	"strings"
	"testing"
	"time"
)

// The board attention line (boards skipped this run, listings short for days)
// is written to the digest footer as one escaped line, and absent when there is
// none: a board problem that goes on shows every day, not only on its own run
// line.
func TestBoardAttentionLineInFooter(t *testing.T) {
	day := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	md, _ := Render(nil, nil, day, Counts{New: 1,
		BoardAttention: "Boards not being read (attention, not a failure): 1 skipped this run: page:<b>x</b>.example.com"})
	if !strings.Contains(md, `Boards not being read \(attention, not a failure\)`) {
		t.Fatalf("footer has no attention line:\n%s", md)
	}
	if strings.Contains(md, "<b>") {
		t.Fatalf("board text was not escaped:\n%s", md)
	}
	quiet, _ := Render(nil, nil, day, Counts{New: 1})
	if strings.Contains(quiet, "attention, not a failure") {
		t.Fatalf("an attention line with nothing to say:\n%s", quiet)
	}
}
