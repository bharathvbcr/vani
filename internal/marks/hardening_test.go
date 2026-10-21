package marks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func countSyncDir(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	var dirs []string
	old := syncDir
	syncDir = func(d string) error {
		mu.Lock()
		defer mu.Unlock()
		dirs = append(dirs, d)
		return nil
	}
	t.Cleanup(func() { syncDir = old })
	return &dirs
}

func TestFirstWriteSyncsParentDirOnceThenNever(t *testing.T) {
	dir := Dir(t.TempDir())
	dirs := countSyncDir(t)
	if _, err := SetMark(dir, k1, Saved, t0); err != nil {
		t.Fatal(err)
	}
	if len(*dirs) != 1 || (*dirs)[0] != dir {
		t.Fatalf("first creation of marks.jsonl: want exactly one SyncDir(%s), got %v", dir, *dirs)
	}
	for i := range 3 {
		if _, err := SetNote(dir, k1, fmt.Sprint("n", i), t0.Add(time.Duration(i+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if len(*dirs) != 1 {
		t.Fatalf("later writes must not sync the directory: %v", *dirs)
	}
}

func TestExistingEmptyStoreFileIsNotACreation(t *testing.T) {
	dir := Dir(t.TempDir())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dirs := countSyncDir(t)
	if _, err := SetMark(dir, k1, Saved, t0); err != nil {
		t.Fatal(err)
	}
	if len(*dirs) != 0 {
		t.Fatalf("a pre-existing empty file is not a creation: %v", *dirs)
	}
}

func TestConcurrentFirstWritersSyncAtLeastOnceAndLoseNothing(t *testing.T) {
	dir := Dir(t.TempDir())
	dirs := countSyncDir(t)
	const n = 8
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k := posting.Key(fmt.Sprintf("ab:co:%08d", i))
			if _, err := SetMark(dir, k, Saved, t0); err != nil {
				t.Errorf("write %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if len(*dirs) < 1 {
		t.Fatal("racing first writers: zero directory syncs is not allowed")
	}
	if m, err := ReadMarks(dir); err != nil || len(m) != n {
		t.Fatalf("marks = %d, err = %v", len(m), err)
	}
}

// encodedLine is the line write would store for a note on key k.
func encodedLine(t *testing.T, k posting.Key, note string) int {
	t.Helper()
	b, err := json.Marshal(event{V: Version, At: t0, Key: k, Note: &note})
	if err != nil {
		t.Fatal(err)
	}
	return len(b) + 1
}

func TestWorstCaseNoteAndKeyFitTheLineCap(t *testing.T) {
	longKey := posting.Key("ab:" + strings.Repeat("<", MaxKeyBytes-3))
	if err := CheckKey(longKey); err != nil {
		t.Fatalf("test key must be valid: %v", err)
	}
	got := encodedLine(t, longKey, strings.Repeat("<", MaxNoteBytes))
	if got > maxLineBytes {
		t.Fatalf("worst-case line is %d bytes, over the %d cap", got, maxLineBytes)
	}
	if bound := (MaxNoteBytes+MaxKeyBytes)*maxEscape + maxEnvelope; bound > maxLineBytes || got > bound {
		t.Fatalf("documented bound %d, cap %d, actual %d", bound, maxLineBytes, got)
	}
}

func TestNoteBoundaryTable(t *testing.T) {
	cases := []struct {
		name string
		unit string
	}{
		{"less-than", "<"},
		{"ampersand", "&"},
		{"greater-than", ">"},
		{"U+2028", " "},
		{"U+2029", " "},
		{"tab", "\t"},
		{"quote", `"`},
		{"backslash", `\`},
		{"newline", "\n"},
		{"two-byte rune", "é"},
		{"three-byte rune", "€"},
		{"four-byte rune", "😀"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.unit == " " || c.unit == " " {
				// hasControl refuses the line separators outright, so they never
				// reach the encoder; the table pins that they are refused cleanly.
				if err := CheckNote(c.unit); !errors.Is(err, ErrInvalid) {
					t.Fatalf("%q should be refused: %v", c.unit, err)
				}
				return
			}
			max := strings.Repeat(c.unit, MaxNoteBytes/len(c.unit))
			dir := Dir(t.TempDir())
			if _, err := SetNote(dir, k1, max, t0); err != nil {
				t.Fatalf("a %d-byte note of %q must be accepted: %v", len(max), c.unit, err)
			}
			m, err := Read(dir)
			if err != nil || m[k1].Note != max {
				t.Fatalf("round trip: err=%v, equal=%v", err, m[k1].Note == max)
			}
			over := strings.Repeat(c.unit, MaxNoteBytes/len(c.unit)+1)
			err = CheckNote(over)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "note is") || strings.Contains(err.Error(), "line is over") {
				t.Fatalf("an over-size note must get the note-size message: %v", err)
			}
			if _, err := SetNote(dir, k1, over, t0.Add(time.Minute)); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "note is") {
				t.Fatalf("SetNote over-size: %v", err)
			}
		})
	}
}

func TestExactly2000AndOver2000LessThanNotes(t *testing.T) {
	dir := Dir(t.TempDir())
	ok := strings.Repeat("<", 2000)
	if _, err := SetNote(dir, k1, ok, t0); err != nil {
		t.Fatalf("2000 '<' bytes: %v", err)
	}
	if m, err := Read(dir); err != nil || m[k1].Note != ok {
		t.Fatalf("round trip: %v", err)
	}
	// And it survives compaction, which rewrites the same lines.
	if err := Compact(dir); err != nil {
		t.Fatal(err)
	}
	if m, err := Read(dir); err != nil || m[k1].Note != ok {
		t.Fatalf("round trip after compaction: %v", err)
	}
	err := CheckNote(strings.Repeat("<", 2001))
	if err == nil || !strings.Contains(err.Error(), "note is 2001 bytes, over 2000") {
		t.Fatalf("2001 bytes: %v", err)
	}
}

func TestWorstCaseLongKeyWithWorstNoteRoundTrips(t *testing.T) {
	dir := Dir(t.TempDir())
	k := posting.Key("ab:" + strings.Repeat("&", MaxKeyBytes-3))
	note := strings.Repeat("&", MaxNoteBytes)
	if _, err := SetNote(dir, k, note, t0); err != nil {
		t.Fatal(err)
	}
	if m, err := Read(dir); err != nil || m[k].Note != note {
		t.Fatalf("round trip: %v", err)
	}
}
