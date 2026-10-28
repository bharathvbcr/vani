package marks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func appliedStore(t *testing.T, at time.Time) string {
	t.Helper()
	dir := Dir(t.TempDir())
	if _, err := SetMark(dir, k1, Applied, at); err != nil {
		t.Fatal(err)
	}
	return dir
}

func stageLines(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, stagesName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func mustStage(t *testing.T, dir string, k posting.Key, s Stage, at time.Time) Entry {
	t.Helper()
	e, err := SetStage(dir, k, s, at)
	if err != nil {
		t.Fatalf("SetStage(%q) = %v", s, err)
	}
	return e
}

func TestStageVocabulary(t *testing.T) {
	for _, s := range append([]Stage{StageNone}, Stages...) {
		if !s.Valid() {
			t.Errorf("%q is declared and not Valid", s)
		}
		// A stage must never pass for a mark: every reader of the marks files
		// switches on the four mark values.
		if s != StageNone && Mark(s).Valid() {
			t.Errorf("%q is both a stage and a mark", s)
		}
	}
	for _, s := range []Stage{"applied", "saved", "Interview", " offer", "ghosted", "interview\n"} {
		if s.Valid() {
			t.Errorf("%q is Valid", s)
		}
	}
	if got := []Mark{None, Saved, Applied, Hidden}; len(got) != 4 || Mark("interview").Valid() {
		t.Error("the mark vocabulary changed: stages must not become marks")
	}
}

// Every pair of stages, set from a freshly applied posting.
func TestEveryTransition(t *testing.T) {
	all := append([]Stage{StageNone}, Stages...)
	for _, from := range all {
		for _, to := range all {
			dir := appliedStore(t, t0)
			if from != StageNone {
				mustStage(t, dir, k1, from, t0.Add(time.Hour))
			}
			e, err := SetStage(dir, k1, to, t0.Add(2*time.Hour))
			wantErr := from.Terminal() && to.Terminal() && from != to
			if wantErr {
				if !errors.Is(err, ErrInvalid) {
					t.Errorf("%q -> %q: err = %v, want ErrInvalid", from, to, err)
				}
				if m, _ := Read(dir); m[k1].Stage != from {
					t.Errorf("%q -> %q: refused, but the stage is now %q", from, to, m[k1].Stage)
				}
				continue
			}
			if err != nil || e.Stage != to || e.Mark != Applied {
				t.Errorf("%q -> %q: %+v, %v", from, to, e, err)
			}
			if m, _ := Read(dir); m[k1] != e {
				t.Errorf("%q -> %q: Read %+v != returned %+v", from, to, m[k1], e)
			}
		}
	}
}

func TestStageNeedsAnAppliedPosting(t *testing.T) {
	for _, mk := range []Mark{None, Saved, Hidden} {
		dir := Dir(t.TempDir())
		if mk != None {
			if _, err := SetMark(dir, k1, mk, t0); err != nil {
				t.Fatal(err)
			}
		}
		for _, s := range Stages {
			if _, err := SetStage(dir, k1, s, t0.Add(time.Hour)); !errors.Is(err, ErrInvalid) {
				t.Errorf("%q posting -> %q: err = %v, want ErrInvalid", mk, s, err)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, stagesName)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%q posting: a refused stage left a stages file (%v)", mk, err)
		}
	}
	// A posting that is only noted, and one of another key.
	dir := appliedStore(t, t0)
	if _, err := SetStage(dir, "ab:cohere:other", StageOffer, t0); !errors.Is(err, ErrInvalid) {
		t.Errorf("unmarked key: %v", err)
	}
	for name, err := range map[string]error{
		"unknown stage": func() error { _, err := SetStage(dir, k1, "ghosted", t0); return err }(),
		"bad key":       func() error { _, err := SetStage(dir, "nokey", StageOffer, t0); return err }(),
		"zero time":     func() error { _, err := SetStage(dir, k1, StageOffer, time.Time{}); return err }(),
	} {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
}

func TestAppliedDateIsKeptOnceAndFurthestNeverFalls(t *testing.T) {
	dir := appliedStore(t, t0)
	steps := []struct {
		stage    Stage
		at       time.Duration
		furthest Stage
	}{
		{StageScreening, 24, StageScreening},
		{StageInterview, 48, StageInterview},
		{StageRejected, 72, StageInterview},  // ended after an interview: still says interview
		{StageInterview, 80, StageInterview}, // reopened
		{StageScreening, 90, StageInterview}, // set back: the furthest is not
		{StageNone, 95, StageInterview},
		{StageOffer, 100, StageOffer},
		{StageWithdrawn, 120, StageOffer},
	}
	for _, st := range steps {
		e := mustStage(t, dir, k1, st.stage, t0.Add(st.at*time.Hour))
		if e.Stage != st.stage || e.Furthest != st.furthest || !e.MarkedAt.Equal(t0) || !e.StageAt.Equal(t0.Add(st.at*time.Hour)) {
			t.Fatalf("after %q: %+v (want furthest %q, MarkedAt %v)", st.stage, e, st.furthest, t0)
		}
	}
	// The same after a compaction and from a fresh read.
	before, _ := Read(dir)
	if err := Compact(dir); err != nil {
		t.Fatal(err)
	}
	after, err := Read(dir)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("compaction changed the read:\nbefore %+v\nafter  %+v (%v)", before, after, err)
	}
	if after[k1].Furthest != StageOffer || !after[k1].MarkedAt.Equal(t0) {
		t.Fatalf("after compaction: %+v", after[k1])
	}
}

func TestEndedWithoutAnyStageSaysApplied(t *testing.T) {
	dir := appliedStore(t, t0)
	e := mustStage(t, dir, k1, StageRejected, t0.Add(time.Hour))
	if e.Stage != StageRejected || e.Furthest != StageNone {
		t.Fatalf("%+v: rejected straight after applying reached no stage", e)
	}
}

func TestSameStageWritesNothing(t *testing.T) {
	dir := appliedStore(t, t0)
	mustStage(t, dir, k1, StageInterview, t0.Add(time.Hour))
	e := mustStage(t, dir, k1, StageInterview, t0.Add(2*time.Hour))
	if !e.StageAt.Equal(t0.Add(time.Hour)) || len(stageLines(t, dir)) != 1 {
		t.Fatalf("%+v, %d lines: setting the stage a posting has must change nothing", e, len(stageLines(t, dir)))
	}
	e = mustStage(t, dir, k1, StageNone, t0.Add(3*time.Hour)) // undo is a recorded change like any other
	if e.Stage != StageNone || len(stageLines(t, dir)) != 2 {
		t.Fatalf("undo: %+v, %d lines", e, len(stageLines(t, dir)))
	}
}

func TestApplyingAgainNeverUndoesAStage(t *testing.T) {
	dir := appliedStore(t, t0)
	mustStage(t, dir, k1, StageInterview, t0.Add(time.Hour))
	marksBefore, _ := os.ReadFile(filepath.Join(dir, fileName))
	for name, set := range map[string]func() (Entry, error){
		"SetMark":    func() (Entry, error) { return SetMark(dir, k1, Applied, t0.Add(48*time.Hour)) },
		"SetApplied": func() (Entry, error) { return SetApplied(dir, k1, 3, t0.Add(48*time.Hour)) },
	} {
		e, err := set()
		if err != nil || e.Stage != StageInterview || !e.MarkedAt.Equal(t0) || e.ResumeVersion != 0 {
			t.Errorf("%s: %+v, %v", name, e, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, fileName)); string(b) != string(marksBefore) {
		t.Error("applying again wrote to marks.jsonl")
	}
	// Even after the stage is set back, the application keeps its date.
	mustStage(t, dir, k1, StageNone, t0.Add(50*time.Hour))
	e, err := SetMark(dir, k1, Applied, t0.Add(60*time.Hour))
	if err != nil || !e.MarkedAt.Equal(t0) || e.Furthest != StageInterview {
		t.Fatalf("after a set-back: %+v, %v", e, err)
	}
	// A posting that never had a stage keeps today's behaviour.
	plain := Dir(t.TempDir())
	SetMark(plain, k1, Applied, t0)
	if e, _ := SetApplied(plain, k1, 2, t0.Add(time.Hour)); !e.MarkedAt.Equal(t0.Add(time.Hour)) || e.ResumeVersion != 2 {
		t.Errorf("a posting with no stage history: %+v", e)
	}
}

func TestLeavingAppliedNeedsTheStageSetBack(t *testing.T) {
	for _, mk := range []Mark{None, Saved, Hidden} {
		dir := appliedStore(t, t0)
		mustStage(t, dir, k1, StageRejected, t0.Add(time.Hour))
		if _, err := SetMark(dir, k1, mk, t0.Add(2*time.Hour)); !errors.Is(err, ErrInvalid) {
			t.Errorf("-> %q while rejected: %v, want ErrInvalid", mk, err)
		}
		if m, _ := Read(dir); m[k1].Mark != Applied || m[k1].Stage != StageRejected {
			t.Errorf("-> %q: refused but the entry is %+v", mk, m[k1])
		}
		mustStage(t, dir, k1, StageNone, t0.Add(3*time.Hour))
		if _, err := SetMark(dir, k1, mk, t0.Add(4*time.Hour)); err != nil {
			t.Errorf("-> %q after the set-back: %v", mk, err)
		}
	}
}

// Un-applying and applying again is a new application: nothing of the old one
// is carried over, though its lines are still in stages.jsonl.
func TestAppliedAgainAfterLeavingIsANewApplication(t *testing.T) {
	dir := appliedStore(t, t0)
	mustStage(t, dir, k1, StageInterview, t0.Add(time.Hour))
	mustStage(t, dir, k1, StageNone, t0.Add(2*time.Hour))
	if _, err := SetMark(dir, k1, Saved, t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := SetStage(dir, k1, StageOffer, t0.Add(4*time.Hour)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a saved posting took a stage: %v", err)
	}
	e, err := SetMark(dir, k1, Applied, t0.Add(5*time.Hour))
	if err != nil || e.Stage != StageNone || e.Furthest != StageNone || !e.MarkedAt.Equal(t0.Add(5*time.Hour)) {
		t.Fatalf("applied again: %+v, %v", e, err)
	}
	if m, _ := Read(dir); m[k1] != e {
		t.Fatalf("Read %+v != %+v", m[k1], e)
	}
}

func TestNoteKeepsTheStage(t *testing.T) {
	dir := appliedStore(t, t0)
	staged := mustStage(t, dir, k1, StageInterview, t0.Add(time.Hour))
	e, err := SetNote(dir, k1, "panel on Thursday", t0.Add(2*time.Hour))
	if err != nil || e.Stage != staged.Stage || e.Furthest != staged.Furthest || !e.StageAt.Equal(staged.StageAt) || e.Note == "" {
		t.Fatalf("a note dropped the stage from the reply: %+v, %v", e, err)
	}
	if !e.LastChange().Equal(staged.StageAt) {
		t.Errorf("a note moved LastChange: %v", e.LastChange())
	}
}

func TestLastChange(t *testing.T) {
	dir := appliedStore(t, t0)
	if e, _ := Read(dir); !e[k1].LastChange().Equal(t0) {
		t.Errorf("no stage: %v", e[k1].LastChange())
	}
	e := mustStage(t, dir, k1, StageScreening, t0.Add(time.Hour))
	if !e.LastChange().Equal(t0.Add(time.Hour)) {
		t.Errorf("staged: %v", e.LastChange())
	}
	// A stage written with a clock earlier than the applied time does not pull
	// LastChange back before it.
	e = mustStage(t, dir, k1, StageInterview, t0.Add(-time.Hour))
	if !e.LastChange().Equal(t0) {
		t.Errorf("skewed: %v", e.LastChange())
	}
	if !(Entry{}).LastChange().IsZero() {
		t.Error("an empty entry has a last change")
	}
}

// Duplicate lines and lines with one timestamp: the file's order decides.
func TestDuplicateAndSameTimeEvents(t *testing.T) {
	dir := appliedStore(t, t0)
	at := t0.Add(time.Hour)
	mustStage(t, dir, k1, StageScreening, at)
	mustStage(t, dir, k1, StageInterview, at) // same instant: the later line wins
	mustStage(t, dir, k1, StageRejected, at)
	e, _ := Read(dir)
	if e[k1].Stage != StageRejected || e[k1].Furthest != StageInterview {
		t.Fatalf("%+v", e[k1])
	}
	// The same line twice, as a crashed retry or a hand copy leaves it.
	lines := stageLines(t, dir)
	b, _ := os.ReadFile(filepath.Join(dir, stagesName))
	if err := os.WriteFile(filepath.Join(dir, stagesName), append(b, []byte(lines[len(lines)-1]+"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if again, err := Read(dir); err != nil || again[k1] != e[k1] {
		t.Fatalf("a repeated line changed the entry: %+v (%v)", again[k1], err)
	}
	if err := Compact(dir); err != nil {
		t.Fatal(err)
	}
	if got := stageLines(t, dir); len(got) != 2 {
		t.Fatalf("compacted to %d lines, want furthest + current at their own times, %v", len(got), got)
	}
	if again, err := Read(dir); err != nil || again[k1] != e[k1] {
		t.Fatalf("compaction: %+v (%v)", again[k1], err)
	}
}

func TestStagesBelongToTheirApplication(t *testing.T) {
	dir := appliedStore(t, t0)
	mustStage(t, dir, k1, StageOffer, t0.Add(time.Hour))
	// Another binary applies again: the applied time moves (marks.go), and the
	// stage line, which names the old time, no longer belongs to it.
	line := fmt.Sprintf(`{"v":1,"at":%q,"key":%q,"mark":"applied"}`+"\n", t0.Add(24*time.Hour).Format(time.RFC3339Nano), k1)
	f, _ := os.OpenFile(filepath.Join(dir, fileName), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(line)
	f.Close()
	e, err := Read(dir)
	if err != nil || e[k1].Mark != Applied || e[k1].Stage != StageNone || e[k1].Furthest != StageNone {
		t.Fatalf("%+v, %v", e[k1], err)
	}
	if m, _ := ReadMarks(dir); m[k1] != Applied {
		t.Fatalf("ReadMarks %v", m[k1])
	}
	// A stage line for a posting with no mark at all is ignored the same way.
	ghost := stageEvent{V: 1, At: t0, Key: "ab:cohere:ghost", AppliedAt: t0, Stage: StageOffer}
	b, _ := json.Marshal(ghost)
	g, _ := os.OpenFile(filepath.Join(dir, stagesName), os.O_APPEND|os.O_WRONLY, 0o600)
	g.Write(append(b, '\n'))
	g.Close()
	if all, err := Read(dir); err != nil || len(all) != 1 {
		t.Fatalf("%+v, %v", all, err)
	}
}

// An installed older binary has no stages.jsonl and never reads one: a store
// with only marks.jsonl, in the lines it writes, reads the same as before.
func TestStoreWithoutStagesFileReadsAsBefore(t *testing.T) {
	dir := rawStore(t, goodLine+`{"v":1,"at":"2026-10-19T10:00:00Z","key":"ab:cohere:87e9fd4e","mark":"applied","resume_version":2}`+"\n")
	m, err := Read(dir)
	if err != nil || m[k1].Mark != Applied || m[k1].Stage != StageNone || m[k1].ResumeVersion != 2 {
		t.Fatalf("%+v, %v", m[k1], err)
	}
	if _, err := os.Stat(filepath.Join(dir, stagesName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a read created stages.jsonl")
	}
}

func TestStageLogUnknownLinesRefuseTheStageRead(t *testing.T) {
	good := fmt.Sprintf(`{"v":1,"at":"2026-10-20T09:00:00Z","key":%q,"applied_at":"2026-10-19T09:00:00Z","stage":"interview"}`+"\n", k1)
	for name, bad := range map[string]string{
		"future version": `{"v":2,"at":"2026-10-20T09:00:00Z","key":"ab:cohere:87e9fd4e","applied_at":"2026-10-19T09:00:00Z","stage":"offer"}` + "\n",
		"unknown stage":  `{"v":1,"at":"2026-10-20T09:00:00Z","key":"ab:cohere:87e9fd4e","applied_at":"2026-10-19T09:00:00Z","stage":"ghosted"}` + "\n",
		"unknown field":  `{"v":1,"at":"2026-10-20T09:00:00Z","key":"ab:cohere:87e9fd4e","applied_at":"2026-10-19T09:00:00Z","stage":"offer","x":1}` + "\n",
		"no applied_at":  `{"v":1,"at":"2026-10-20T09:00:00Z","key":"ab:cohere:87e9fd4e","stage":"offer"}` + "\n",
		"not an object":  "[]\n",
		"bad key":        `{"v":1,"at":"2026-10-20T09:00:00Z","key":"x","applied_at":"2026-10-19T09:00:00Z","stage":"offer"}` + "\n",
	} {
		for pos, content := range map[string]string{"middle": good + bad + good, "last": good + bad} {
			dir := rawStore(t, goodLine+`{"v":1,"at":"2026-10-19T09:00:00Z","key":"ab:cohere:87e9fd4e","mark":"applied"}`+"\n")
			if err := os.WriteFile(filepath.Join(dir, stagesName), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Read(dir); !errors.Is(err, ErrCorrupt) {
				t.Errorf("%s as the %s line: Read = %v, want ErrCorrupt", name, pos, err)
			}
			if _, err := ReadApplied(dir); !errors.Is(err, ErrCorrupt) {
				t.Errorf("%s as the %s line: ReadApplied = %v", name, pos, err)
			}
			// The marks themselves still read: the digest's duplicate rules and
			// the ranking need no stage.
			if m, err := ReadMarks(dir); err != nil || m[k1] != Applied {
				t.Errorf("%s as the %s line: ReadMarks = %v, %v", name, pos, m, err)
			}
			if _, err := SetStage(dir, k1, StageOffer, t0); !errors.Is(err, ErrCorrupt) {
				t.Errorf("%s as the %s line: SetStage = %v, want ErrCorrupt", name, pos, err)
			}
			// Fail closed: the write that could undo or contradict a stage waits.
			if _, err := SetMark(dir, k1, Hidden, t0.Add(48*time.Hour)); !errors.Is(err, ErrCorrupt) {
				t.Errorf("%s as the %s line: SetMark on an applied posting = %v, want ErrCorrupt", name, pos, err)
			}
			if _, err := SetMark(dir, "ab:cohere:other", Saved, t0); err != nil {
				t.Errorf("%s as the %s line: a mark on another posting = %v", name, pos, err)
			}
		}
	}
}

func TestTornStageLineIsSkippedAndCut(t *testing.T) {
	dir := appliedStore(t, t0)
	mustStage(t, dir, k1, StageScreening, t0.Add(time.Hour))
	f, _ := os.OpenFile(filepath.Join(dir, stagesName), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"v":1,"at":"2026-10-2`)
	f.Close()
	if m, err := Read(dir); err != nil || m[k1].Stage != StageScreening {
		t.Fatalf("a torn tail: %+v, %v", m[k1], err)
	}
	mustStage(t, dir, k1, StageInterview, t0.Add(2*time.Hour))
	for _, l := range stageLines(t, dir) {
		if !json.Valid([]byte(l)) {
			t.Fatalf("the torn tail is still in the file: %q", l)
		}
	}
}

func TestOversizedStageLinesAndFile(t *testing.T) {
	dir := appliedStore(t, t0)
	mustStage(t, dir, k1, StageScreening, t0.Add(time.Hour))
	path := filepath.Join(dir, stagesName)
	good, _ := os.ReadFile(path)
	huge := `{"v":1,"at":"2026-10-20T09:00:00Z","key":"` + strings.Repeat("a", 2*maxStageLineBytes) + `"}` + "\n"
	if err := os.WriteFile(path, []byte(huge+string(good)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); !errors.Is(err, ErrCorrupt) {
		t.Errorf("an oversized line: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat(string(good), MaxStageFileBytes/len(good)+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); !errors.Is(err, ErrCorrupt) {
		t.Errorf("an oversized file: %v", err)
	}
	if _, err := SetStage(dir, k1, StageOffer, t0); !errors.Is(err, ErrCorrupt) {
		t.Errorf("a write to an oversized file: %v", err)
	}
}

func TestStageLineCapCoversAFullSizeKey(t *testing.T) {
	key := posting.Key("ab:" + strings.Repeat("<", MaxKeyBytes-3))
	at := time.Date(2026, 10, 20, 23, 59, 59, 999999999, time.UTC)
	b, err := json.Marshal(stageEvent{V: stageVersion, At: at, Key: key, AppliedAt: at, Stage: StageWithdrawn})
	if err != nil || len(b)+1 > maxStageLineBytes {
		t.Fatalf("worst-case stage line is %d bytes (%v), cap %d", len(b)+1, err, maxStageLineBytes)
	}
	dir := Dir(t.TempDir())
	if _, err := SetMark(dir, key, Applied, at); err != nil {
		t.Fatal(err)
	}
	if e, err := SetStage(dir, key, StageWithdrawn, at); err != nil || e.Stage != StageWithdrawn {
		t.Fatalf("%+v, %v", e, err)
	}
}

func TestStagesFileIsOwnerOnlyAndNotThroughALink(t *testing.T) {
	dir := appliedStore(t, t0)
	mustStage(t, dir, k1, StageScreening, t0.Add(time.Hour))
	if st, _ := os.Stat(filepath.Join(dir, stagesName)); st.Mode().Perm()&0o077 != 0 {
		t.Errorf("stages.jsonl is %v", st.Mode().Perm())
	}
	dir = appliedStore(t, t0)
	target := filepath.Join(t.TempDir(), "elsewhere.jsonl")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, stagesName)); err != nil {
		t.Fatal(err)
	}
	if _, err := SetStage(dir, k1, StageScreening, t0.Add(time.Hour)); !errors.Is(err, atomicfile.ErrLinkedLeaf) {
		t.Fatalf("SetStage through a symlink = %v, want ErrLinkedLeaf", err)
	}
	if b, _ := os.ReadFile(target); len(b) != 0 {
		t.Error("the link's target was written")
	}
}

func TestReadAppliedHasNoNotesAndOnlyApplied(t *testing.T) {
	dir := appliedStore(t, t0)
	k2 := posting.Key("ab:cohere:two")
	SetMark(dir, k2, Saved, t0)
	SetNote(dir, k1, "secret note", t0)
	SetNote(dir, k2, "another", t0)
	mustStage(t, dir, k1, StageInterview, t0.Add(time.Hour))
	m, err := ReadApplied(dir)
	if err != nil || len(m) != 1 || m[k1].Note != "" || !m[k1].NoteAt.IsZero() || m[k1].Stage != StageInterview {
		t.Fatalf("%+v, %v", m, err)
	}
	if empty, err := ReadApplied(Dir(t.TempDir())); err != nil || len(empty) != 0 {
		t.Fatalf("a store never written: %v %v", empty, err)
	}
}

func TestConcurrentStageWriters(t *testing.T) {
	dir := Dir(t.TempDir())
	const n = 8
	keys := make([]posting.Key, n)
	for i := range keys {
		keys[i], _ = posting.GreenhouseKey("acme", fmt.Sprint(i))
		if _, err := SetMark(dir, keys[i], Applied, t0); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := range keys {
		for _, s := range []Stage{StageScreening, StageInterview, StageOffer} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := SetStage(dir, keys[i], s, t0.Add(time.Hour)); err != nil {
					t.Errorf("%s %s: %v", keys[i], s, err)
				}
			}()
		}
	}
	wg.Wait()
	m, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if m[k].Furthest != StageOffer || m[k].Stage == StageNone {
			t.Errorf("%s: %+v (all three stages were set)", k, m[k])
		}
	}
	if got := len(stageLines(t, dir)); got != n*3 {
		t.Errorf("%d lines for %d postings and 3 distinct stages each, want %d", got, n, n*3)
	}
}

// Compaction folds to the same state, whatever the history was.
func TestStageCompactionKeepsTheFoldedState(t *testing.T) {
	all := append([]Stage{StageNone}, Stages...)
	for seed := range 6 {
		dir := Dir(t.TempDir())
		var keys []posting.Key
		for i := range 5 {
			k, _ := posting.GreenhouseKey("acme", fmt.Sprint(i))
			keys = append(keys, k)
			SetMark(dir, k, Applied, t0.Add(time.Duration(i)*time.Minute))
		}
		at := t0.Add(time.Hour)
		for i := range 60 {
			k := keys[(i*7+seed)%len(keys)]
			at = at.Add(time.Minute)
			if _, err := SetStage(dir, k, all[(i*5+seed*3)%len(all)], at); err != nil && !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
		}
		SetMark(dir, keys[0], Saved, at) // may be refused while staged; either way compaction must agree
		before, err := Read(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := Compact(dir); err != nil {
			t.Fatal(err)
		}
		after, err := Read(dir)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("seed %d: compaction changed the state:\n%+v\n%+v (%v)", seed, before, after, err)
		}
		if n := len(stageLines(t, dir)); n > 2*len(keys) {
			t.Errorf("seed %d: %d lines after compaction, want at most 2 per application", seed, n)
		}
		if err := Compact(dir); err != nil {
			t.Fatal(err)
		}
		if again, _ := Read(dir); !reflect.DeepEqual(after, again) {
			t.Fatalf("seed %d: a second compaction changed the state", seed)
		}
	}
}

func TestCompactionDropsStagesOfClearedPostings(t *testing.T) {
	dir := appliedStore(t, t0)
	mustStage(t, dir, k1, StageInterview, t0.Add(time.Hour))
	mustStage(t, dir, k1, StageNone, t0.Add(2*time.Hour))
	SetMark(dir, k1, None, t0.Add(3*time.Hour))
	if err := Compact(dir); err != nil {
		t.Fatal(err)
	}
	if n := len(stageLines(t, dir)); n != 1 || stageLines(t, dir)[0] != "" {
		t.Fatalf("%d lines: %q", n, stageLines(t, dir))
	}
}

// A write that would pass the file's cap compacts the history first.
func TestStageWriteCompactsAFullFile(t *testing.T) {
	dir := appliedStore(t, t0)
	mustStage(t, dir, k1, StageScreening, t0.Add(time.Hour))
	path := filepath.Join(dir, stagesName)
	one, _ := os.ReadFile(path)
	// A long history of toggling the same posting: every line but the first
	// and last is superseded.
	n := MaxStageFileBytes / len(one) // what is left is less than one more line
	if err := os.WriteFile(path, []byte(strings.Repeat(string(one), n)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err != nil {
		t.Fatal(err)
	}
	e, err := SetStage(dir, k1, StageInterview, t0.Add(2*time.Hour))
	if err != nil || e.Stage != StageInterview || e.Furthest != StageInterview {
		t.Fatalf("%+v, %v", e, err)
	}
	if got := len(stageLines(t, dir)); got > 3 {
		t.Fatalf("%d lines: the write did not compact", got)
	}
}

// Live stages that alone fill the file are refused, never trimmed.
func TestStageWriteRefusedWhenLiveStagesFillTheFile(t *testing.T) {
	dir := Dir(t.TempDir())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, lockName), nil, 0o600)
	var marksB, stagesB strings.Builder
	at := t0.Format(time.RFC3339Nano)
	var last posting.Key
	// Every line is as long as the one a write would add, so what is left
	// when the file is "full" is less than that line.
	lineLen := len(fmt.Sprintf(`{"v":1,"at":%q,"key":%q,"applied_at":%q,"stage":"screening"}`+"\n", at, "gh:acme:00000000", at))
	for i := 0; stagesB.Len()+lineLen <= MaxStageFileBytes; i++ {
		last = posting.Key(fmt.Sprintf("gh:acme:%08d", i))
		fmt.Fprintf(&marksB, `{"v":1,"at":%q,"key":%q,"mark":"applied"}`+"\n", at, last)
		fmt.Fprintf(&stagesB, `{"v":1,"at":%q,"key":%q,"applied_at":%q,"stage":"screening"}`+"\n", at, last, at)
	}
	os.WriteFile(filepath.Join(dir, fileName), []byte(marksB.String()), 0o600)
	os.WriteFile(filepath.Join(dir, stagesName), []byte(stagesB.String()), 0o600)
	before, _ := os.ReadFile(filepath.Join(dir, stagesName))
	if _, err := SetStage(dir, last, StageInterview, t0.Add(time.Hour)); !errors.Is(err, ErrFull) {
		t.Fatalf("err = %v, want ErrFull", err)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, stagesName)); string(after) != string(before) {
		t.Fatal("a refused write changed the file")
	}
}
