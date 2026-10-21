package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Lines whose key names or shape make the structural scan's cut ambiguous
// (duplicate or case-folded keys, escaped key names, whitespace) and lines with
// invalid description contents. stripDescription must not cut any of them; the
// scoped fold must agree with ReadSnapshot on every one.

const hdr = `{"v":1,"at":"2026-10-01T12:00:00Z","key":"gh:a:1","status":"triaged",`

// JSON-escaped spellings of the key names (the decoder reads them as the plain keys).
var (
	escDesc    = `"` + string(rune(92)) + `u0064escription"`
	escPosting = `"` + string(rune(92)) + `u0070osting"`
)

var confusionLines = map[string]string{
	// Go's decoder takes the LAST description; the scan cuts the first and leaves the real one.
	"dup description (A,B)":                     hdr + `"posting":{"key":"gh:a:1","description":"A","description":"B"}}`,
	"dup description via escaped key (A,esc B)": hdr + `"posting":{"key":"gh:a:1","description":"A",` + escDesc + `:"B"}}`,
	"escaped key first then literal":            hdr + `"posting":{"key":"gh:a:1",` + escDesc + `:"B","description":"A"}}`,
	"dup posting objects (merged by decoder)":   hdr + `"posting":{"key":"gh:a:1","description":"A"},"posting":{"key":"gh:a:1","description":"B"}}`,
	"case-folded Posting second":                hdr + `"posting":{"key":"gh:a:1","description":"A"},"Posting":{"description":"B"}}`,
	// whitespace before the colon of a depth-1 key leaves inPosting stale: the annotation is cut.
	"annotations key with space before colon": hdr + `"posting":{"key":"gh:a:1","title":"t"},"annotations" :{"description":"ann"}}`,
	// the posting key itself escaped: decode reads it as posting, scan sees no posting.
	"escaped posting key, literal description in annotations": hdr + escPosting + `:{"key":"gh:a:1","description":"A"},"annotations":{"description":"ann"}}`,
	// Unicode case folding: encoding/json folds U+017F (long s) to s and U+212A (Kelvin) to k.
	"long-s posting key":                        hdr + `"po` + "\u017f" + `ting":{"key":"gh:a:1","description":"A"}}`,
	"long-s description key":                    hdr + `"posting":{"key":"gh:a:1","de` + "\u017f" + `cription":"A","description":"B"}}`,
	"upper-case keys":                           hdr + `"POSTING":{"key":"gh:a:1","DESCRIPTION":"A"}}`,
	"upper then lower":                          hdr + `"posting":{"key":"gh:a:1","DESCRIPTION":"A","description":"B"}}`,
	"whitespace after colon":                    hdr + `"posting":{"key":"gh:a:1","description": "A"}}`,
	"leading space":                             ` ` + hdr + `"posting":{"key":"gh:a:1","description":"A"}}`,
	"description key repeats after annotations": hdr + `"posting":{"key":"gh:a:1","description":"A"},"annotations":{"description":"x"},"posting":{"description":"B"}}`,
}

// Every confusion line must decode the same through the scoped fold as through
// ReadSnapshot once the body is asked for, and its fast path must not be taken
// unless the cut is the decoder's description.
func TestScopedFoldKeepAllMatchesSnapshotOnConfusion(t *testing.T) {
	for name, line := range confusionLines {
		dir := t.TempDir()
		writeEventsFile(t, dir, line+"\n")
		full, err := ReadSnapshot(dir)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		all, err := ReadSnapshotScoped(dir, func(Snapshot) func(Record) bool { return func(Record) bool { return true } })
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !equalRec(full.Records[0], all.Records[0]) {
			t.Errorf("%s:\n full %+v\n scoped %+v", name, full.Records[0], all.Records[0])
		}
	}
}

// The annotations-with-space line decodes with the description the annotation
// carries and the posting's own description cut out only if it is unambiguous.
func TestAnnotationsWithSpaceDecodes(t *testing.T) {
	line := confusionLines["annotations key with space before colon"]
	dir := t.TempDir()
	writeEventsFile(t, dir, line+"\n")
	got, err := ReadSnapshotScoped(dir, func(Snapshot) func(Record) bool { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got.Records[0].Annotations["description"] != "ann" {
		t.Errorf("annotations = %v, want description=ann", got.Records[0].Annotations)
	}
}

// Canonical ledger lines (what Marshal writes) always take the fast path: no
// fallback to a whole decode, and every description is cut.
func TestCanonicalLinesNeverFallBack(t *testing.T) {
	var sb strings.Builder
	for i, d := range append([]string{"plain", "", `q" \ \\ "description":"x"`}, nastyBodies...) {
		k := posting.Key(fmt.Sprintf("gh:a:%d", i))
		p := posting.Posting{Key: k, ATS: "gh", Company: "A", Title: "t", URL: "u", Description: d + "x"}
		b, err := json.Marshal(Event{V: 1, At: t0, Key: k, Status: posting.Triaged, Posting: &p,
			Annotations: map[string]string{"description": d, "posting": d}, Reason: d})
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	st := &leanState{bodies: map[posting.Key]leanBody{}}
	recs := map[posting.Key]*slot{}
	if _, _, err := replayWith(strings.NewReader(sb.String()), recs, nil, st); err != nil {
		t.Fatal(err)
	}
	if st.whole != 0 {
		t.Errorf("%d canonical lines fell back to a whole decode", st.whole)
	}
	if len(st.bodies) != len(recs) {
		t.Errorf("%d bodies cut for %d records", len(st.bodies), len(recs))
	}
}

// Both readers agree on corruption: an invalid description is ErrCorrupt for
// each, mid-file; at the tail it is a torn line for each.
func TestScopedAndSnapshotAgreeOnBadDescriptions(t *testing.T) {
	bq := string(rune(92))
	for name, d := range map[string]string{
		"bad escape":         "bad" + bq + "qx",
		"short unicode":      "x" + bq + "u12",
		"non-hex unicode":    "x" + bq + "u12zz",
		"raw newline":        "a\nb",
		"raw control":        "a\x01b",
		"raw tab":            "a\tb",
		"trailing backslash": "x" + bq + bq + bq,
	} {
		bad := hdr + `"posting":{"key":"gh:a:1","description":"` + d + `"}}`
		ok := `{"v":1,"at":"2026-10-01T12:00:00Z","key":"gh:a:2","status":"triaged","posting":{"key":"gh:a:2","description":"fine"}}`
		for _, content := range []string{bad + "\n" + ok + "\n", ok + "\n" + bad + "\n", ok + "\n" + bad} {
			dir := t.TempDir()
			writeEventsFile(t, dir, content)
			full, ferr := ReadSnapshot(dir)
			lean, lerr := ReadSnapshotScoped(dir, func(Snapshot) func(Record) bool { return nil })
			if (ferr == nil) != (lerr == nil) || errors.Is(ferr, ErrCorrupt) != errors.Is(lerr, ErrCorrupt) {
				t.Errorf("%s: snapshot err %v, scoped err %v", name, ferr, lerr)
			}
			if ferr == nil && (len(full.Records) != len(lean.Records) || full.Torn != lean.Torn) {
				t.Errorf("%s: snapshot %d records torn=%v, scoped %d torn=%v", name, len(full.Records), full.Torn, len(lean.Records), lean.Torn)
			}
		}
	}
}

func TestValidStringBodyMatchesDecoder(t *testing.T) {
	for _, s := range []string{``, `a`, `\"`, `\\`, `\/`, `\b\f\n\r\t`, `\u0041`, `\uD800`, `\u00e9x`, `\u004`, `\u`, `\`, `\q`, `\u00g0`, "\x01", "\x1f", " ", "\x7f", "\xff", `\\\`} {
		var v string
		want := json.Unmarshal([]byte(`"`+s+`"`), &v) == nil
		if got := validStringBody([]byte(s)); got != want {
			t.Errorf("%q: validStringBody=%v, decoder accepts=%v", s, got, want)
		}
	}
}

func TestStripDescriptionKeyConfusion(t *testing.T) {
	for name, line := range confusionLines {
		if msg := leanOracle([]byte(line)); msg != "" {
			t.Errorf("%s: %s", name, msg)
		}
	}
}

// Same-key descriptions cut by scan never leave a non-empty Description on a
// BodyEvicted record: check the fold, not only the strip.
func TestScopedFoldKeepNoneLeaksOrCorruptsOnConfusion(t *testing.T) {
	for name, line := range confusionLines {
		var want Event
		if err := json.Unmarshal([]byte(line), &want); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		dir := t.TempDir()
		writeEventsFile(t, dir, line+"\n")
		full, err := ReadSnapshot(dir)
		if err != nil {
			t.Fatal(err)
		}
		none, err := ReadSnapshotScoped(dir, func(Snapshot) func(Record) bool { return nil })
		if err != nil {
			t.Fatal(err)
		}
		a, b := full.Records[0], none.Records[0]
		if b.BodyEvicted && b.Posting.Description != "" {
			t.Errorf("%s: record is BodyEvicted yet carries Description %q", name, b.Posting.Description)
		}
		b.Posting.Description, b.BodyEvicted = a.Posting.Description, a.BodyEvicted
		if !equalRec(a, b) {
			t.Errorf("%s: lean fold changed something besides the description:\n full %+v\n lean %+v", name, a, b)
		}
	}
}

// A mid-file line the decoder refuses (bad escape inside the description) is
// ErrCorrupt for ReadSnapshot; the scan cuts the bad bytes out and accepts it.
func TestScopedAcceptsCorruptLineReadSnapshotRefuses(t *testing.T) {
	bad := hdr + `"posting":{"key":"gh:a:1","description":"bad\qescape"}}`
	ok := hdr + `"posting":{"key":"gh:a:2","description":"fine"}}`
	ok = `{"v":1,"at":"2026-10-01T12:00:00Z","key":"gh:a:2","status":"triaged","posting":{"key":"gh:a:2","description":"fine"}}`
	dir := t.TempDir()
	writeEventsFile(t, dir, bad+"\n"+ok+"\n")
	if _, err := ReadSnapshot(dir); err == nil {
		t.Fatal("precondition: ReadSnapshot should refuse the line")
	}
	if _, err := ReadSnapshotScoped(dir, func(Snapshot) func(Record) bool { return nil }); err == nil {
		t.Errorf("ReadSnapshotScoped accepted a mid-file line ReadSnapshot reports as ErrCorrupt")
	}
}
