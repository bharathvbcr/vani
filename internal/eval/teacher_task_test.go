package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// The jd_triage identity and the bytes it sends, captured from the code before
// tasks existed (2026-11-09, commit 7060b74). A change to any of them makes
// every stored jd_triage reading stale, so it must be deliberate.
const (
	goldenSummary = "early-career ML systems roles"
	goldenFixture = "0cf04ad791d67fb04bf4e1c5be9b3206f61cf356dddf0a3054c9a8b5a13be47d"
	goldenPrompt  = "adfed9564a844a34321fd919de1a8bb182334fc280b5de73de7d7caa0b529ea8"
	goldenMsgs    = "c2234345e0239c8616009c8a385ddd48be46eccb84ffe2c36d3fe2f15f01ce61"
)

var goldenPosting = posting.Posting{Key: "gh:golden:1", Title: "Inference Engineer", Location: "Remote", Description: "Build and run model serving. Tune kernels."}

type capDecider func(context.Context, decide.Question) (decide.Answer, error)

func (f capDecider) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	return f(ctx, q)
}

func TestJDTriageTaskKeepsTheIdentityAndBytesOfBeforeTasks(t *testing.T) {
	task := JDTriageTask(goldenSummary)
	if task.Name != "" || task.Digest != "" {
		t.Fatalf("jd_triage task carries name %q digest %q, want both empty so its rows stay as they were", task.Name, task.Digest)
	}
	if task.Fixture != goldenFixture || FixtureSHA() != goldenFixture {
		t.Fatalf("fixture %s, want %s", task.Fixture, goldenFixture)
	}
	if task.Prompt != goldenPrompt || PromptHash(goldenSummary) != goldenPrompt {
		t.Fatalf("prompt hash %s, want %s", task.Prompt, goldenPrompt)
	}
	var sys, user string
	if _, err := task.Ask(context.Background(), capDecider(func(_ context.Context, q decide.Question) (decide.Answer, error) {
		sys, user = llmbackend.Messages(q)
		return decide.Answer{}, nil
	}), goldenPosting); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(sys + "\x00" + user))
	if got := hex.EncodeToString(sum[:]); got != goldenMsgs {
		t.Fatalf("rendered messages hash %s, want %s", got, goldenMsgs)
	}
}

// fakeTask is a synthetic task with a digest, asking with a one-slot question.
func fakeTask(digest string) TeacherTask {
	return TeacherTask{
		Name: "career.kb_match", Fixture: "fx", Prompt: "pr", Digest: digest,
		Ask: func(ctx context.Context, d decide.Decider, p posting.Posting) (decide.Answer, error) {
			return d.Decide(ctx, decide.Question{Task: "career.kb_match", Prompt: "p", Context: []byte(p.Description),
				Slots: []decide.Slot{{Name: "kb_match", Options: []string{"core", "partial", "adjacent", "unrelated"}}}})
		},
	}
}

// slotFake answers the one slot by posting key.
type slotFake struct {
	by    map[posting.Key]decide.SlotReading
	asked []posting.Key
}

func (f *slotFake) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	k := posting.Key(q.Context)
	f.asked = append(f.asked, k)
	return decide.Answer{Slots: map[string]decide.SlotReading{"kb_match": f.by[k]}}, nil
}

func taskRun(t *testing.T, s *TeacherStore, d decide.Decider, task TeacherTask, keys ...string) TeacherResult {
	t.Helper()
	var cands []TeacherCandidate
	for _, k := range keys {
		cands = append(cands, TeacherCandidate{Posting: posting.Posting{Key: posting.Key(k), Description: k}})
	}
	r := TeacherRun{D: d, Model: "m", Summary: "s", Store: s, Task: task, Concurrency: 1, LimitNano: 1e12,
		Spent: func(context.Context) (int64, error) { return 0, nil }, Worst: func(decide.Question) int64 { return 1 },
		Now: func() time.Time { return time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC) }}
	res, err := r.Run(context.Background(), cands)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestTeacherTaskDigestChangeReasksAndRowsCarryTheIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), TeacherFile)
	s := mustOpenTeacher(t, path)
	f := &slotFake{by: map[posting.Key]decide.SlotReading{"k1": {Reading: decide.Answered, Value: "core"}, "k2": {Reading: decide.Answered, Value: "partial"}}}

	if res := taskRun(t, s, f, fakeTask("digestA"), "k1", "k2"); res.Stored != 2 {
		t.Fatalf("first run %+v", res)
	}
	if res := taskRun(t, s, f, fakeTask("digestA"), "k1", "k2"); res.Skipped != 2 || res.Asked != 0 {
		t.Fatalf("same digest asked again: %+v", res)
	}
	if res := taskRun(t, s, f, fakeTask("digestB"), "k1", "k2"); res.Asked != 2 || res.Stored != 2 {
		t.Fatalf("a new digest did not re-ask: %+v", res)
	}
	// A reopened store (the identity read back from the file) agrees.
	s2 := mustOpenTeacher(t, path)
	if !s2.Has("k1", fakeTask("digestB").Ident(posting.Posting{Key: "k1", Description: "k1"}, "m")) {
		t.Fatal("reopened store lost the digestB reading")
	}
	if s2.Has("k1", fakeTask("digestC").Ident(posting.Posting{Key: "k1", Description: "k1"}, "m")) {
		t.Fatal("a digest never asked counts as stored")
	}
	if !s2.CurrentOf("m", fakeTask("digestA")).Has("k1") || s2.CurrentOf("m", fakeTask("digestC")).Has("k1") {
		t.Fatal("CurrentOf does not key on the digest")
	}
	// The task's rows never reach the jd_triage view the label queue reads.
	if s2.Current("m", "s").Has("k1") {
		t.Fatal("a kb_match row is current for jd_triage")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(bytes.SplitN(raw, []byte("\n"), 2)[0], &row); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"task": "career.kb_match", "kb_digest_sha": "digestA", "fixture_sha": "fx", "prompt_hash": "pr", "model": "m"} {
		if row[k] != want {
			t.Errorf("row %s = %v, want %q", k, row[k], want)
		}
	}
}

func TestJDTriageRowsAreWrittenWithoutTheTaskFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), TeacherFile)
	s := mustOpenTeacher(t, path)
	d := capDecider(func(_ context.Context, q decide.Question) (decide.Answer, error) {
		a := decide.Answer{Slots: map[string]decide.SlotReading{}}
		for _, sl := range q.Slots {
			a.Slots[sl.Name] = decide.SlotReading{Reading: decide.Answered, Value: sl.Options[0]}
		}
		return a, nil
	})
	if res := taskRun(t, s, d, TeacherTask{}, "gh:a:1"); res.Stored != 1 {
		t.Fatalf("%+v", res)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"task"`)) || bytes.Contains(raw, []byte(`kb_digest_sha`)) {
		t.Fatalf("a jd_triage row carries task fields: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"fixture_sha":"`+goldenFixture+`"`)) {
		t.Fatalf("a jd_triage row lacks the fixture hash: %s", raw)
	}
}

func TestTeacherRowKeepsAbstentionNoneAndFailureApart(t *testing.T) {
	path := filepath.Join(t.TempDir(), TeacherFile)
	s := mustOpenTeacher(t, path)
	f := &slotFake{by: map[posting.Key]decide.SlotReading{
		"answered":  {Reading: decide.Answered, Value: "unrelated"},
		"abstained": {Reading: decide.Abstained}, // what llmbackend makes of a "none" reply
		"none-val":  {Reading: decide.Answered, Value: "none"},
		"failed":    {Reading: decide.Failed, Detail: "unparseable"},
	}}
	if res := taskRun(t, s, f, fakeTask("d"), "answered", "abstained", "none-val", "failed"); res.Stored != 4 || res.Unusable != 2 {
		t.Fatalf("%+v", res)
	}
	got := map[posting.Key]TeacherSlot{}
	for k, rows := range mustOpenTeacher(t, path).byKey {
		got[k] = rows[0].Slots["kb_match"]
	}
	want := map[posting.Key]TeacherSlot{
		"answered":  {Reading: "answered", Value: "unrelated"},
		"abstained": {Reading: "abstained"},
		"none-val":  {Reading: "failed", Detail: "invalid_value"},
		"failed":    {Reading: "failed", Detail: "unparseable"},
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s stored %+v, want %+v", k, got[k], w)
		}
	}
	// The label queue's view leaves an abstention out: it is not a value.
	c := mustOpenTeacher(t, path).CurrentOf("m", fakeTask("d"))
	if _, ok := c.Reading("abstained"); ok {
		t.Error("an abstention reads as a value")
	}
	if v, ok := c.Reading("answered"); !ok || v["kb_match"] != "unrelated" {
		t.Errorf("answered reading %v %v", v, ok)
	}
}

func TestTeacherRunRefusesAHalfBuiltTask(t *testing.T) {
	s := mustOpenTeacher(t, filepath.Join(t.TempDir(), TeacherFile))
	r := TeacherRun{D: &slotFake{}, Model: "m", Store: s, Task: TeacherTask{Name: "career.kb_match", Digest: "d"}, Concurrency: 1, LimitNano: 1,
		Spent: func(context.Context) (int64, error) { return 0, nil }, Worst: func(decide.Question) int64 { return 1 }}
	if _, err := r.Run(context.Background(), nil); err == nil {
		t.Fatal("a task with no question or hashes ran")
	}
}

func TestReadKeyList(t *testing.T) {
	keys, err := ReadKeyList(strings.NewReader("gh:a:2\r\n\n  gh:a:1  \ngh:b:9"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(keys, []posting.Key{"gh:a:2", "gh:a:1", "gh:b:9"}) {
		t.Fatalf("keys %v, want the file's order", keys)
	}
	for name, tc := range map[string]struct{ in, want string }{
		"empty":     {"\n \n", "no keys"},
		"duplicate": {"gh:a:1\ngh:a:2\ngh:a:1\n", "gh:a:1 (lines 1 and 3)"},
		"two words": {"gh:a:1 gh:a:2\n", "line 1"},
		"long line": {strings.Repeat("x", maxKeyBytes+1) + "\n", "longer than"},
	} {
		_, err := ReadKeyList(strings.NewReader(tc.in))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want it to say %q", name, err, tc.want)
		}
	}
	if _, err := ReadKeyList(&endless{}); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("an endless file: %v", err)
	}
	if _, err := ReadKeyList(iotestErr{}); err == nil {
		t.Error("a read error was not reported")
	}
}

// endless is an unbounded stream of short keys.
type endless struct{ n int }

func (e *endless) Read(p []byte) (int, error) {
	for i := range p {
		if e.n%10 == 9 {
			p[i] = '\n'
		} else {
			p[i] = 'a' + byte(e.n%7)
		}
		e.n++
	}
	return len(p), nil
}

type iotestErr struct{}

func (iotestErr) Read([]byte) (int, error) { return 0, errors.New("disk gone") }

func TestRestrictToKeysKeepsTheFileOrderAndNamesTheMissing(t *testing.T) {
	var cands []TeacherCandidate
	for _, k := range []string{"a", "b", "c", "d"} {
		cands = append(cands, TeacherCandidate{Posting: posting.Posting{Key: posting.Key(k)}})
	}
	kept, missing := RestrictToKeys(cands, []posting.Key{"c", "zz", "a"})
	var got []string
	for _, c := range kept {
		got = append(got, string(c.Posting.Key))
	}
	if !slices.Equal(got, []string{"c", "a"}) || !slices.Equal(missing, []posting.Key{"zz"}) {
		t.Fatalf("kept %v missing %v", got, missing)
	}
	path := filepath.Join(t.TempDir(), "draws", "x.keys")
	if err := WriteKeyList(path, kept); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	back, err := ReadKeyList(f)
	if err != nil || !slices.Equal(back, []posting.Key{"c", "a"}) {
		t.Fatalf("round trip %v %v", back, err)
	}
}
