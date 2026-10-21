package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// nastyBodies are descriptions that would fool a naive scan for the field:
// quotes, backslash runs before a quote, the field's own name and shape inside
// the text, and non-ASCII.
var nastyBodies = []string{
	"plain text",
	`says "description":"x" inside`,
	`ends with a backslash \`,
	`double \\ then quote " and more`,
	`{"posting":{"description":"evil"}} [ ] { }`,
	"line\nbreak\ttab and unicode é世界 \U0001F600",
	strings.Repeat("a long body ", 2000),
	"bad utf8 \xff\xfe tail",
}

func scopedFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	f := mustOpen(t, dir)
	ctx := context.Background()
	for i, d := range nastyBodies {
		k := posting.Key("gh:acme:" + string(rune('a'+i)))
		co := "Acme"
		if i%2 == 1 {
			co = "Other"
		}
		p := posting.Posting{Key: k, Company: co, Title: "T", URL: "https://x/" + string(k), Description: d}
		e := ev(string(k), posting.Triaged, t0)
		e.Posting = &p
		// An annotation named like the field must not be taken for it.
		e.Annotations = map[string]string{"description": "annotation text", AnnRank: "7"}
		if err := f.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
		// A later event without a posting keeps the body but moves the status.
		if err := f.Append(ctx, ev(string(k), posting.Scored, t0.Add(1))); err != nil {
			t.Fatal(err)
		}
	}
	// Re-posted with a new body, and re-posted with none.
	for _, c := range []struct {
		k string
		d string
	}{{"gh:acme:a", "second body"}, {"gh:acme:c", ""}} {
		p := posting.Posting{Key: posting.Key(c.k), Company: "Acme", Title: "T2", URL: "https://x/" + c.k, Description: c.d}
		e := ev(c.k, posting.Triaged, t0.Add(2))
		e.Posting = &p
		if err := f.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Append(ctx, ev("gh:acme:nobody", posting.Live, t0)); err != nil { // no posting at all
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReadSnapshotScopedKeepAllEqualsReadSnapshot(t *testing.T) {
	dir := scopedFixture(t)
	want, err := ReadSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadSnapshotScoped(dir, func(Snapshot) func(Record) bool { return func(Record) bool { return true } })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keep-all scoped snapshot differs from ReadSnapshot:\n got %+v\nwant %+v", got, want)
	}
}

// A record whose body was left out has every other field intact, is marked
// BodyEvicted, and has an empty Description; a record with no body to leave
// out is not marked.
func TestReadSnapshotScopedSkippedRecordsAreMarkedAndOtherwiseComplete(t *testing.T) {
	dir := scopedFixture(t)
	full, _ := ReadSnapshot(dir)
	for name, scope := range map[string]func(Snapshot) func(Record) bool{
		"nil scope":  nil,
		"nil keep":   func(Snapshot) func(Record) bool { return nil },
		"keep Acme":  func(Snapshot) func(Record) bool { return func(r Record) bool { return r.Posting.Company == "Acme" } },
		"keep none":  func(Snapshot) func(Record) bool { return func(Record) bool { return false } },
		"keep Other": func(Snapshot) func(Record) bool { return func(r Record) bool { return r.Posting.Company == "Other" } },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ReadSnapshotScoped(dir, scope)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Records) != len(full.Records) {
				t.Fatalf("%d records, want %d", len(got.Records), len(full.Records))
			}
			var keep func(Record) bool
			if scope != nil {
				keep = scope(got)
			}
			for i, g := range got.Records {
				w := full.Records[i]
				loaded := keep != nil && keep(g)
				switch {
				case w.Posting.Description == "":
					if g.BodyEvicted {
						t.Fatalf("%s: no body to skip but BodyEvicted", g.Key)
					}
				case loaded && w.BodyEvicted:
					t.Fatalf("%s: unexpected", g.Key)
				case loaded:
					if g.BodyEvicted || g.Posting.Description != w.Posting.Description {
						t.Fatalf("%s: kept record has body %q evicted=%v, want %q", g.Key, g.Posting.Description, g.BodyEvicted, w.Posting.Description)
					}
				default:
					if !g.BodyEvicted || g.Posting.Description != "" {
						t.Fatalf("%s: skipped record must be BodyEvicted with no text: %q evicted=%v", g.Key, g.Posting.Description, g.BodyEvicted)
					}
					g.BodyEvicted, g.Posting.Description = false, w.Posting.Description
				}
				g.BodyEvicted = false
				if !reflect.DeepEqual(g, w) {
					t.Fatalf("%s: record differs beyond the body:\n got %+v\nwant %+v", g.Key, g, w)
				}
			}
		})
	}
}

// Torn tails and mid-file corruption behave as in ReadSnapshot.
func TestReadSnapshotScopedTornAndCorrupt(t *testing.T) {
	dir := scopedFixture(t)
	path := filepath.Join(dir, "events.jsonl")
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(`{"v":1,"at":"2026-10-01T12:00:00Z","key":"gh:c:2","posting":{"description":"cut off`)
	fh.Close()
	want, err := ReadSnapshot(dir)
	if err != nil || !want.Torn {
		t.Fatalf("setup: %v torn=%v", err, want.Torn)
	}
	got, err := ReadSnapshotScoped(dir, func(Snapshot) func(Record) bool { return func(Record) bool { return true } })
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("torn: %v, differs=%v", err, !reflect.DeepEqual(got, want))
	}

	raw, _ := os.ReadFile(path)
	bad := strings.Replace(string(raw), `"status":"triaged"`, `"status":"bogus"`, 1)
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshotScoped(dir, nil); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("mid-file corruption: %v, want ErrCorrupt", err)
	}
	if _, err := ReadSnapshotScoped(filepath.Join(dir, "nope"), nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing ledger: %v", err)
	}
}

// A body read back must be the one that was folded: a file rewritten between
// the fold and the read is ErrBodyUnavailable, never other text.
func TestReadSnapshotScopedRefusesAChangedLine(t *testing.T) {
	dir := scopedFixture(t)
	path := filepath.Join(dir, "events.jsonl")
	_, err := ReadSnapshotScoped(dir, func(Snapshot) func(Record) bool {
		raw, _ := os.ReadFile(path)
		// Same length, different bytes.
		changed := strings.Replace(string(raw), "second body", "SECOND body", 1)
		if changed == string(raw) {
			t.Fatal("setup: nothing to change")
		}
		if err := os.WriteFile(path, []byte(changed), 0o600); err != nil {
			t.Fatal(err)
		}
		return func(Record) bool { return true }
	})
	if !errors.Is(err, ErrBodyUnavailable) {
		t.Fatalf("got %v, want ErrBodyUnavailable", err)
	}
}

func TestStripDescription(t *testing.T) {
	marshal := func(e Event) []byte {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, d := range nastyBodies {
		e := Event{V: 1, Key: "gh:a:1", Status: posting.Triaged, At: t0,
			Posting:     &posting.Posting{Key: "gh:a:1", Title: "T", Description: d},
			Annotations: map[string]string{"description": d}}
		line := marshal(e)
		lean, cut, ok := stripDescription(line, nil)
		if !ok {
			t.Fatalf("%q: not stripped", d)
		}
		var back Event
		if err := json.Unmarshal(lean, &back); err != nil {
			t.Fatalf("%q: lean line does not decode: %v", d, err)
		}
		var whole Event
		if err := json.Unmarshal(line, &whole); err != nil {
			t.Fatal(err)
		}
		if back.Posting.Description != "" || back.Annotations["description"] != whole.Annotations["description"] || back.Posting.Title != "T" {
			t.Fatalf("%q: lean decoded to %+v", d, back)
		}
		if cut != len(line)-len(lean) {
			t.Fatalf("cut %d, want %d", cut, len(line)-len(lean))
		}
	}
	for name, line := range map[string]string{
		"no posting":           `{"v":1,"key":"k","annotations":{"description":"x"}}`,
		"posting null":         `{"v":1,"key":"k","posting":null}`,
		"no description":       `{"v":1,"key":"k","posting":{"title":"t"}}`,
		"description at top":   `{"v":1,"description":"x","posting":{"title":"t"}}`,
		"only in annotations":  `{"v":1,"posting":{"title":"t"},"annotations":{"description":"x"}}`,
		"space before colon":   `{"v":1,"posting":{"description" :"x"}}`,
		"escaped key":          `{"v":1,"posting":{"descr` + `\` + `u0069ption":"x"}}`,
		"non-string value":     `{"v":1,"posting":{"description":5}}`,
		"unterminated string":  `{"v":1,"posting":{"description":"x`,
		"empty":                ``,
		"not json":             `garbage`,
		"description as value": `{"v":1,"posting":{"title":"description","url":"x"}}`,
	} {
		if _, _, ok := stripDescription([]byte(line), nil); ok {
			t.Errorf("%s: stripped a line that has no posting description", name)
		}
	}
}

// A line stripDescription cannot shorten is still folded whole, and a body the
// scan left in place is not marked as left out.
func TestReadSnapshotScopedFallsBackToAWholeDecode(t *testing.T) {
	dir := t.TempDir()
	line := `{"v":1,"at":"2026-10-01T12:00:00Z","key":"gh:a:1","status":"triaged","posting":{"key":"gh:a:1","company":"A","title":"t","url":"u","description" : "spaced out","seen_at":"2026-10-01T12:00:00Z"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	want, err := ReadSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadSnapshotScoped(dir, func(Snapshot) func(Record) bool { return func(Record) bool { return false } })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) || got.Records[0].Posting.Description != "spaced out" || got.Records[0].BodyEvicted {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
