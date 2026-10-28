package marks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What a binary that knows only today's marks.jsonl lines does with a line it
// does not know. These pin the mechanism that decided how stages are stored:
// the reader does not skip an unknown line, it refuses the whole store, and a
// writer refuses to append to it. A new mark value, a new line version or a new
// field in marks.jsonl would therefore lock every installed older binary out of
// the Jobs list and the digest, so post-apply stages live in their own file
// (stages.go) and marks.jsonl stays what an older binary reads.

// rawStore writes content as marks.jsonl (with its lock file, as a writer
// leaves them) and returns the directory.
func rawStore(t *testing.T, content string) string {
	t.Helper()
	dir := Dir(t.TempDir())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, lockName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

const (
	goodLine = `{"v":1,"at":"2026-10-19T09:00:00Z","key":"ab:cohere:87e9fd4e","mark":"saved"}` + "\n"
	// Each of these is whole, valid JSON that today's reader does not know.
	futureMark    = `{"v":1,"at":"2026-10-19T10:00:00Z","key":"ab:cohere:87e9fd4e","mark":"interview"}` + "\n"
	futureVersion = `{"v":2,"at":"2026-10-19T10:00:00Z","key":"ab:cohere:87e9fd4e","mark":"applied"}` + "\n"
	futureField   = `{"v":1,"at":"2026-10-19T10:00:00Z","key":"ab:cohere:87e9fd4e","mark":"applied","stage":"interview"}` + "\n"
)

func TestReaderRefusesWholeStoreOnUnknownLine(t *testing.T) {
	for name, bad := range map[string]string{"mark value": futureMark, "line version": futureVersion, "field": futureField} {
		for pos, content := range map[string]string{"last": goodLine + bad, "middle": goodLine + bad + goodLine} {
			dir := rawStore(t, content)
			m, err := Read(dir)
			if !errors.Is(err, ErrCorrupt) || m != nil {
				t.Errorf("%s as the %s line: Read = %v, %v; want nil and ErrCorrupt (the good line is not served either)", name, pos, m, err)
			}
			if mm, err := ReadMarks(dir); !errors.Is(err, ErrCorrupt) || mm != nil {
				t.Errorf("%s as the %s line: ReadMarks = %v, %v", name, pos, mm, err)
			}
			before, _ := os.ReadFile(filepath.Join(dir, fileName))
			if _, err := SetMark(dir, k1, Hidden, t0); !errors.Is(err, ErrCorrupt) {
				t.Errorf("%s as the %s line: a write to the store = %v, want ErrCorrupt", name, pos, err)
			}
			if after, _ := os.ReadFile(filepath.Join(dir, fileName)); string(after) != string(before) {
				t.Errorf("%s as the %s line: the refused write changed the file", name, pos)
			}
			if err := Compact(dir); !errors.Is(err, ErrCorrupt) {
				t.Errorf("%s as the %s line: Compact = %v, want ErrCorrupt", name, pos, err)
			}
		}
	}
}

func TestReaderErrorNamesTheLine(t *testing.T) {
	_, err := Read(rawStore(t, goodLine+futureMark+goodLine))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v, want it to name line 2", err)
	}
}

// The scheme that follows from the above: a stage change appends to
// stages.jsonl and leaves marks.jsonl byte for byte as it was, so a binary that
// reads only marks.jsonl (every installed one) reads it as before and sees an
// applied posting at any stage as applied. A marks.jsonl written alongside
// stages holds nothing the strict reader above would refuse.
func TestStagesNeverTouchMarksJSONL(t *testing.T) {
	dir := Dir(t.TempDir())
	if _, err := SetApplied(dir, k1, 2, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := SetNote(dir, k1, "n", t0); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, fileName))
	for _, s := range []Stage{StageScreening, StageInterview, StageRejected, StageNone, StageOffer, StageWithdrawn} {
		if _, err := SetStage(dir, k1, s, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if after, _ := os.ReadFile(filepath.Join(dir, fileName)); string(after) != string(before) {
			t.Fatalf("SetStage(%q) changed marks.jsonl:\n%s\nwas\n%s", s, after, before)
		}
	}
	if err := Compact(dir); err != nil {
		t.Fatal(err)
	}
	// The older reader: marks.jsonl alone, in the lines it knows.
	only := rawStore(t, string(mustRead(t, filepath.Join(dir, fileName))))
	m, err := Read(only)
	if err != nil || m[k1].Mark != Applied || m[k1].ResumeVersion != 2 || m[k1].Note != "n" || m[k1].Stage != StageNone {
		t.Fatalf("marks.jsonl alone reads %+v, %v", m[k1], err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(mustRead(t, filepath.Join(dir, fileName)))), "\n") {
		for _, banned := range []string{`"stage"`, `"interview"`, `"offer"`, `"rejected"`, `"withdrawn"`, `"screening"`} {
			if strings.Contains(line, banned) {
				t.Errorf("marks.jsonl line %s names a stage", line)
			}
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Compaction keeps the latest mark's time, not the first one's: the date first
// applied is not in marks.jsonl once history is compacted, so it cannot be
// derived from the line history alone.
func TestCompactionDropsSupersededMarkTimes(t *testing.T) {
	dir := Dir(t.TempDir())
	if _, err := SetMark(dir, k1, Applied, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := SetMark(dir, k1, Applied, t0.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := Compact(dir); err != nil {
		t.Fatal(err)
	}
	m, err := Read(dir)
	if err != nil || !m[k1].MarkedAt.Equal(t0.Add(48*time.Hour)) {
		t.Fatalf("after compaction MarkedAt = %v (%v), want the later time", m[k1].MarkedAt, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, fileName))
	if strings.Contains(string(b), "2026-10-19T09:00:00Z") {
		t.Fatalf("the first time survived compaction: %s", b)
	}
}
