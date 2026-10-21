package ledger

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// leanOracle checks stripDescription against the decoder on one line. For a
// line the decoder accepts, a shortened result must decode to the same Event
// with only Posting.Description cleared. It returns a failure message or "".
func leanOracle(line []byte) string {
	lean, cut, ok := stripDescription(line, nil)
	if !ok {
		return ""
	}
	if cut < 0 || len(lean) != len(line)-cut {
		return "cut/length mismatch"
	}
	var orig Event
	if err := json.Unmarshal(line, &orig); err != nil {
		return "" // an undecodable original is checked by the laundering test
	}
	var got Event
	if err := json.Unmarshal(lean, &got); err != nil {
		return "original decodes but shortened line does not: " + err.Error()
	}
	if orig.Posting == nil {
		return "stripped a line whose decode has no posting"
	}
	want := orig
	if cut > 0 { // cut == 0: nothing was removed, so the fold keeps the decode as is
		pp := *orig.Posting
		pp.Description = ""
		want.Posting = &pp
	}
	if !reflect.DeepEqual(got, want) {
		gb, _ := json.Marshal(got)
		wb, _ := json.Marshal(want)
		return "shortened decode differs:\n got  " + string(gb) + "\n want " + string(wb)
	}
	return ""
}

// hostileEvents are real ledger lines (as Marshal writes them) whose strings
// carry the shapes a structural scan can be fooled by.
func hostileEvents() [][]byte {
	hostile := []string{
		"plain", `quote " and \ backslash`, `"description":"evil"`, `\"`, `\\`, `\\"`, "é世\U0001F600",
		"bad utf8 \xff\xfe", `{"posting":{"description":"x"}}`, strings.Repeat("long ", 5000), `" \`,
		`","description":"second`, `"},"annotations":{"description":"`,
	}
	var seeds [][]byte
	for _, d := range hostile {
		p := posting.Posting{Key: "gh:a:1", ATS: "gh", Company: d, Title: d, Location: d, URL: "https://x/1", Description: d}
		e := Event{V: 1, At: t0, Key: "gh:a:1", Status: posting.Triaged, Posting: &p,
			Annotations: map[string]string{"description": d, "posting": d, d: d}, Reason: d}
		b, _ := json.Marshal(e)
		seeds = append(seeds, b)
		p2 := p
		p2.Description = ""
		e.Posting = &p2
		b, _ = json.Marshal(e)
		seeds = append(seeds, b)
		e.Posting = nil
		b, _ = json.Marshal(e)
		seeds = append(seeds, b)
	}
	return seeds
}

func isCanonical(line []byte) bool {
	var e Event
	if json.Unmarshal(line, &e) != nil {
		return false
	}
	b, err := json.Marshal(e)
	return err == nil && bytes.Equal(b, line)
}

// FuzzStripDescriptionCanonical fuzzes the lines the ledger writes (a line the
// fuzzer mangles out of Marshal's own output is skipped): whatever
// stripDescription shortens must decode to the original's decode with only the
// posting description cleared. Lines a writer could not produce (duplicate or
// escaped keys, whitespace) are the raw fuzzer's territory (FuzzStripDescription).
func FuzzStripDescriptionCanonical(f *testing.F) {
	for _, s := range hostileEvents() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		if !isCanonical(line) {
			t.Skip()
		}
		if msg := leanOracle(line); msg != "" {
			t.Fatalf("%s\nline: %q", msg, line)
		}
	})
}

// FuzzStripDescription is the raw variant: any bytes. The known key-confusion
// classes (duplicate or case-folded keys, escaped key names, whitespace before
// a colon) are deliberately not seeded: seeded, the run would fail before it
// mutated anything. They are pinned by a deterministic test instead.
func FuzzStripDescription(f *testing.F) {
	for _, s := range hostileEvents() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		if msg := leanOracle(line); msg != "" {
			t.Fatalf("%s\nline: %q", msg, line)
		}
	})
}

func TestStripDescriptionHostileEvents(t *testing.T) {
	for i, s := range hostileEvents() {
		if msg := leanOracle(s); msg != "" {
			t.Errorf("seed %d: %s", i, msg)
		}
	}
}
