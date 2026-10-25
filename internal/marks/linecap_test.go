package marks

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// The caps must not drift back into collision: the longest accepted note and
// key, each all six-byte escapes, plus the envelope of an actual encoded line,
// have to fit maxLineBytes.
func TestLineCapCoversWorstCaseNoteKeyAndEnvelope(t *testing.T) {
	m := Applied // the longest mark name, so the envelope is worst-case
	for _, mk := range []Mark{Saved, Applied, Hidden} {
		if len(mk) > len(m) {
			m = mk
		}
	}
	empty := ""
	at := time.Date(2026, 10, 20, 23, 59, 59, 999999999, time.UTC)
	envelope := func() int {
		b, err := json.Marshal(event{V: Version, At: at, Key: "", Mark: &m, Note: &empty, ResumeVersion: MaxResumeVersion})
		if err != nil {
			t.Fatal(err)
		}
		return len(b) + 1 // newline
	}()
	if envelope > maxEnvelope {
		t.Fatalf("an encoded envelope is %d bytes, over maxEnvelope %d", envelope, maxEnvelope)
	}
	if need := MaxNoteBytes*maxEscape + MaxKeyBytes*maxEscape + envelope; maxLineBytes < need {
		t.Fatalf("maxLineBytes %d < note+key worst case %d", maxLineBytes, need)
	}
	if maxLineBytes < MaxNoteBytes*6+MaxKeyBytes*6+envelope {
		t.Fatalf("maxLineBytes %d is below %d*6 + %d*6 + %d", maxLineBytes, MaxNoteBytes, MaxKeyBytes, envelope)
	}
	// And the real thing, encoded rather than computed.
	key := posting.Key("ab:" + strings.Repeat("<", MaxKeyBytes-3))
	note := strings.Repeat("<", MaxNoteBytes)
	b, err := json.Marshal(event{V: Version, At: at, Key: key, Mark: &m, Note: &note, ResumeVersion: MaxResumeVersion})
	if err != nil {
		t.Fatal(err)
	}
	if len(b)+1 > maxLineBytes {
		t.Fatalf("worst-case encoded line is %d bytes, over %d", len(b)+1, maxLineBytes)
	}
}

// A note over MaxNoteBytes is refused by the note-size check before any
// line-size check, so the caller learns the rule they broke, not a derived one.
func TestOverlongNoteIsRefusedByNoteSizeBeforeLineSize(t *testing.T) {
	for _, n := range []int{MaxNoteBytes + 1, maxLineBytes} { // the second also exceeds the line cap once encoded
		note := strings.Repeat("<", n)
		_, err := SetNote(Dir(t.TempDir()), k1, note, t0)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "note is") || strings.Contains(err.Error(), "line is over") {
			t.Fatalf("%d-byte note: want the note-size error, got %v", n, err)
		}
	}
}
