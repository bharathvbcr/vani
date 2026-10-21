package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func writeEventsFile(t testing.TB, dir, content string) {
	t.Helper()
	if err := os.WriteFile(eventsPath(dir), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func equalRec(a, b Record) bool { return reflect.DeepEqual(a, b) }

var propStatuses = []posting.Status{posting.Discovered, posting.Live, posting.Triaged, posting.Unsure, posting.Scored,
	posting.Explained, posting.Digested, posting.Dead, posting.Duplicate, posting.Dropped}

func propText(r *rand.Rand) string {
	if r.Intn(5) == 0 {
		return ""
	}
	alpha := []string{`"`, `\`, `\\`, `{`, `}`, `[`, `]`, `:`, `,`, `"description":"`, `"posting":{`, "\n", "\t", "é", "世", "\U0001F600", "\xff", "a", "b", " ", `"`, `<>&`, " "}
	var b strings.Builder
	for n := r.Intn(40); n > 0; n-- {
		b.WriteString(alpha[r.Intn(len(alpha))])
	}
	if r.Intn(300) == 0 {
		b.WriteString(strings.Repeat("long text ", 3000))
	}
	return b.String()
}

// propStream returns the events.jsonl bytes of one random, valid ledger.
func propStream(r *rand.Rand) string {
	keys := []posting.Key{"gh:a:1", "gh:a:2", "gh:b:1", "lv:c:9"}
	cos := []string{"Acme", "acme ", "Other", ""}
	var sb strings.Builder
	at := t0
	for n := 1 + r.Intn(14); n > 0; n-- {
		at = at.Add(time.Duration(1+r.Intn(100)) * time.Second)
		k := keys[r.Intn(len(keys))]
		e := Event{V: 1, At: at, Key: k, Status: propStatuses[r.Intn(len(propStatuses))]}
		if r.Intn(3) != 0 {
			e.Posting = &posting.Posting{Key: k, ATS: k.ATS(), Company: cos[r.Intn(len(cos))], Title: propText(r),
				URL: "https://x/" + string(k), Description: propText(r), Location: propText(r)}
		}
		if r.Intn(2) == 0 {
			e.Annotations = map[string]string{"description": propText(r), "posting": propText(r), AnnRank: "3"}
		}
		if r.Intn(3) == 0 {
			e.Reason = propText(r)
		}
		b, err := json.Marshal(e)
		if err != nil {
			panic(err)
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if r.Intn(6) == 0 { // a torn tail
		sb.WriteString(`{"v":1,"at":"2026-10-02T00:00:00Z","key":"gh:a:1","status":"live","posting":{"key":"gh:a:1","description":"par`)
	}
	return sb.String()
}

func TestScopedFoldEquivalenceProperty(t *testing.T) {
	r := rand.New(rand.NewSource(20261021))
	const streams = 800
	cut := 0
	for i := 0; i < streams; i++ {
		dir := t.TempDir()
		writeEventsFile(t, dir, propStream(r))
		full, err := ReadSnapshot(dir)
		if err != nil {
			t.Fatalf("stream %d: ReadSnapshot: %v", i, err)
		}
		all, err := ReadSnapshotScoped(dir, func(Snapshot) func(Record) bool { return func(Record) bool { return true } })
		if err != nil {
			t.Fatalf("stream %d: keep-all: %v", i, err)
		}
		if !reflect.DeepEqual(full, all) {
			t.Fatalf("stream %d: keep-all differs from ReadSnapshot\n full %+v\n all  %+v", i, full, all)
		}
		none, err := ReadSnapshotScoped(dir, func(Snapshot) func(Record) bool { return func(Record) bool { return false } })
		if err != nil {
			t.Fatalf("stream %d: keep-none: %v", i, err)
		}
		odd := 0
		mixed, err := ReadSnapshotScoped(dir, func(Snapshot) func(Record) bool {
			return func(Record) bool { odd++; return odd%2 == 1 }
		})
		if err != nil {
			t.Fatalf("stream %d: mixed: %v", i, err)
		}
		for _, c := range []struct {
			name string
			s    Snapshot
		}{{"keep-none", none}, {"mixed", mixed}} {
			if len(c.s.Records) != len(full.Records) || c.s.Torn != full.Torn {
				t.Fatalf("stream %d %s: shape differs", i, c.name)
			}
			for j := range full.Records {
				a, b := full.Records[j], c.s.Records[j]
				if b.BodyEvicted {
					cut++
					if b.Posting.Description != "" {
						t.Fatalf("stream %d %s: %s BodyEvicted with Description %q", i, c.name, b.Key, b.Posting.Description)
					}
					if a.Posting.Description == "" {
						t.Fatalf("stream %d %s: %s BodyEvicted but real description is empty", i, c.name, b.Key)
					}
				}
				b.Posting.Description, b.BodyEvicted = a.Posting.Description, a.BodyEvicted
				if !equalRec(a, b) {
					t.Fatalf("stream %d %s: record %s differs beyond description/BodyEvicted\n full %+v\n got  %+v", i, c.name, a.Key, a, b)
				}
			}
		}
	}
	if cut < streams { // the property must have exercised eviction, not vacuously passed
		t.Fatalf("only %d evicted records over %d streams", cut, streams)
	}
	t.Logf("%d streams, %d evicted records compared", streams, cut)
}

// bigLine builds a posting event line for key k with description d.
func bigLine(t *testing.T, k, company, d string) string {
	t.Helper()
	p := posting.Posting{Key: posting.Key(k), ATS: "gh", Company: company, Title: "T", URL: "https://x/" + k, Description: d}
	e := Event{V: 1, At: t0, Key: posting.Key(k), Status: posting.Triaged, Posting: &p}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// raceScope runs mutate between the fold and the hydration, then asks for
// every body. It returns what the read answered.
func raceScope(t *testing.T, dir string, mutate func()) (Snapshot, error) {
	t.Helper()
	return ReadSnapshotScoped(dir, func(Snapshot) func(Record) bool {
		mutate()
		return func(Record) bool { return true }
	})
}

func TestHydrationRace(t *testing.T) {
	a := bigLine(t, "gh:a:1", "Acme", "BODY-A-aaaaaaaa")
	b := bigLine(t, "gh:a:2", "Acme", "BODY-B-bbbbbbbb")
	if len(a) != len(b) {
		t.Fatal("fixture lines must be the same length")
	}
	bodyOf := func(s Snapshot, k posting.Key) string {
		for _, r := range s.Records {
			if r.Key == k {
				return r.Posting.Description
			}
		}
		return "<missing>"
	}
	mustBeCorrectOrUnavailable := func(name string, s Snapshot, err error) {
		t.Helper()
		if err != nil {
			if !errors.Is(err, ErrBodyUnavailable) {
				t.Errorf("%s: error is not ErrBodyUnavailable: %v", name, err)
			}
			return
		}
		for _, r := range s.Records {
			if r.BodyEvicted {
				t.Errorf("%s: record %s still evicted after a keep-all read", name, r.Key)
			}
		}
		if g := bodyOf(s, "gh:a:1"); g != "BODY-A-aaaaaaaa" {
			t.Errorf("%s: gh:a:1 got body %q", name, g)
		}
		if g := bodyOf(s, "gh:a:2"); g != "BODY-B-bbbbbbbb" {
			t.Errorf("%s: gh:a:2 got body %q", name, g)
		}
	}
	cases := []struct {
		name   string
		mutate func(dir string)
	}{
		{"append new line", func(dir string) {
			f, err := os.OpenFile(eventsPath(dir), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			f.WriteString(bigLine(t, "gh:a:3", "Acme", "BODY-C"))
			f.WriteString(`{"v":1,"at":"2026-10-0`) // torn tail appears after the fold
		}},
		{"swap two same-length lines", func(dir string) { writeEventsFile(t, dir, b+a) }},
		{"overwrite a line with the other's bytes", func(dir string) { writeEventsFile(t, dir, b+b) }},
		{"flip one byte inside the body", func(dir string) {
			writeEventsFile(t, dir, strings.Replace(a, "BODY-A-aaaaaaaa", "BODY-A-aaaaaaab", 1)+b)
		}},
		{"truncate mid first line", func(dir string) { writeEventsFile(t, dir, a[:len(a)/2]) }},
		{"truncate to first line only", func(dir string) { writeEventsFile(t, dir, a) }},
		{"truncate to empty", func(dir string) { writeEventsFile(t, dir, "") }},
		{"compaction shifts offsets (prefix inserted)", func(dir string) {
			writeEventsFile(t, dir, bigLine(t, "gh:z:1", "Zed", "x")+a+b)
		}},
		{"line replaced by same-length different key", func(dir string) {
			writeEventsFile(t, dir, strings.Replace(a, "gh:a:1", "gh:a:9", -1)+b)
		}},
		{"newline replaced by space", func(dir string) { writeEventsFile(t, dir, strings.TrimSuffix(a, "\n")+" "+b) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeEventsFile(t, dir, a+b)
			s, err := raceScope(t, dir, func() { c.mutate(dir) })
			mustBeCorrectOrUnavailable(c.name, s, err)
			t.Logf("%s: err=%v", c.name, err)
		})
	}
	t.Run("rename replacement keeps reading the old file", func(t *testing.T) {
		dir := t.TempDir()
		writeEventsFile(t, dir, a+b)
		s, err := raceScope(t, dir, func() {
			tmp := dir + "/events.new"
			if err := os.WriteFile(tmp, []byte(b+a), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(tmp, eventsPath(dir)); err != nil {
				t.Fatal(err)
			}
		})
		mustBeCorrectOrUnavailable("rename", s, err)
	})
	t.Run("torn tail at fold time is not hydrated and not leaked", func(t *testing.T) {
		dir := t.TempDir()
		torn := strings.TrimSuffix(bigLine(t, "gh:a:3", "Acme", "TORN-BODY"), "\n")
		writeEventsFile(t, dir, a+b+torn)
		s, err := raceScope(t, dir, func() { writeEventsFile(t, dir, a+b+torn+"\n") }) // the writer finishes it meanwhile
		if err != nil {
			t.Fatal(err)
		}
		if !s.Torn || len(s.Records) != 2 {
			t.Errorf("torn=%v records=%d", s.Torn, len(s.Records))
		}
		mustBeCorrectOrUnavailable("torn", s, nil)
		_ = fmt.Sprint()
	})
}
