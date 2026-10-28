package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/rankfit"
	"github.com/bharathvbcr/Vani/internal/resume"
	"github.com/bharathvbcr/decide"
)

// The JSON forms of the commands that spend or write, run against the fakes
// their text tests use: a real teacher run, a kbmatch run that asks, a kb sync
// that applies, replaces and deletes, and a rankfit that writes its file. Each
// reply decodes whole, agrees with what was asked and written, keeps progress
// text off stdout, and carries no private text.

func TestTeacherRunJSON(t *testing.T) {
	e := tcSetup(t)
	f := &tcFake{}
	tcUse(t, f)
	// $0.0016 admits one question of the two (TestTeacherRunStopsAtTheLimitCleanlyThenResumes).
	args := []string{"--json", "teacher", "--of", "d1-ultra", "--budget-usd", "0.0016", "--concurrency", "1"}
	o, errOut := mustCode(t, 0, args...)
	var r teacherReply
	decodeOne(t, o, &r)
	if r.DryRun || r.Plan == nil || r.Plan.ToAsk != 2 || r.Run == nil {
		t.Fatalf("reply %+v", r)
	}
	if r.Run.Asked != 1 || r.Run.Stored != 1 || r.Run.NotAsked != 1 || r.Run.Stopped != eval.TeacherStoppedAtLimit || r.Error != "" {
		t.Errorf("run %+v", *r.Run)
	}
	if len(f.sent) != 1 {
		t.Errorf("asked %d times, the reply says 1", len(f.sent))
	}
	for _, s := range []string{o, errOut} {
		for _, bad := range []string{tcSecret, tcDroppedT} {
			if strings.Contains(s, bad) {
				t.Errorf("output carries %q", bad)
			}
		}
	}
	// The resume asks the other one, and the store holds both.
	f.meter.Store(0)
	o, _ = mustCode(t, 0, "teacher", "--json", "--of", "d1-ultra", "--budget-usd", "1")
	r = teacherReply{}
	decodeOne(t, o, &r)
	if r.Run == nil || r.Run.Asked != 1 || r.Run.Stored != 1 || r.Run.Stopped != "" || len(f.sent) != 2 {
		t.Errorf("resume %+v (sent %d)", r.Run, len(f.sent))
	}
	if st, err := eval.OpenTeacherStore(filepath.Join(e.state, eval.TeacherFile)); err != nil || st.Len() != 2 {
		t.Errorf("store: %v", err)
	}
}

func TestKBMatchRunJSON(t *testing.T) {
	k := twoPostings(t)
	f := &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "unrelated"})}
	useFake(t, f)
	o, _ := mustCode(t, 0, "--json", "kbmatch", "run")
	var r kbmatchRunReply
	decodeOne(t, o, &r)
	if r.DryRun || r.Plan == nil || r.Plan.Candidates != 2 || r.Plan.Mismatch != 1 || r.Plan.Thin != 1 || r.Result == nil {
		t.Fatalf("reply %s", o)
	}
	if res := r.Result; res.Asked != 2 || res.Answered != 2 || res.ByValue["core"] != 1 || res.ByValue["unrelated"] != 1 || res.NotRead != 0 || res.Stopped != "" {
		t.Errorf("result %+v", *res)
	}
	if len(f.contexts) != 2 {
		t.Errorf("asked %d times, the reply says 2", len(f.contexts))
	}
	for _, secret := range []string{"SecretCo", "OtherCo"} {
		if strings.Contains(o, secret) {
			t.Errorf("the reply carries %q", secret)
		}
	}
	if st, err := kbmatch.OpenStore(filepath.Join(k.state, kbmatch.FileName)); err != nil || st.Len() != 2 {
		t.Errorf("store: %v", err)
	}
}

// A run stopped by an outage exits 1 and keeps what it asked beside the
// failure, as the text form keeps its summary.
func TestKBMatchRunStoppedJSON(t *testing.T) {
	k := kbmSetup(t,
		kbmSeed{"gh:a:1", "A", "First", longDesc, "partial_fit"},
		kbmSeed{"gh:a:2", "B", "Second", longDesc, "partial_fit"},
		kbmSeed{"gh:a:3", "C", "Third", longDesc, "partial_fit"},
		kbmSeed{"gh:a:4", "D", "Fourth", longDesc, "partial_fit"},
		kbmSeed{"gh:a:5", "E", "Fifth", longDesc, "partial_fit"},
	)
	n := 0
	f := &fakeKB{answer: func(string) decide.SlotReading {
		if n++; n == 1 {
			return decide.SlotReading{Reading: decide.Answered, Value: "partial"}
		}
		return decide.SlotReading{Reading: decide.Unavailable, Detail: "budget"}
	}}
	useFake(t, f)
	args := []string{"kbmatch", "run", "--json"}
	o, _ := mustCode(t, exitRuntime, args...)
	failureIn(t, args, o)
	var r kbmatchRunReply
	decodeOne(t, o, &r)
	if r.Result == nil || r.Result.Asked != 4 || r.Result.Answered != 1 || !strings.Contains(r.Result.Stopped, "the last 3 questions got no usable answer") {
		t.Fatalf("reply %s", o)
	}
	if st, err := kbmatch.OpenStore(filepath.Join(k.state, kbmatch.FileName)); err != nil || st.Len() != 1 {
		t.Errorf("store: %v", err)
	}
}

// kbBulletText is the bank's bullet text; a kb sync reply names ids, never it.
var kbBulletText = []string{"Cut latency", "Shipped a widget"}

func TestKBSyncAppliesJSON(t *testing.T) {
	bank, rkb, _ := kbApprovedSimple(t, "/opt/devprism/devprism")
	f := &fakeDevPrism{confirm: map[string]bool{"beta-proj": true}}
	fakeStart(t, f)
	o, errOut := mustCode(t, 0, "--json", "kb", "sync", "--bank", bank, "--rkb", rkb)
	var r kbSyncReply
	decodeOne(t, o, &r)
	if r.DryRun || r.Replace || r.Result == nil || r.Error != "" {
		t.Fatalf("reply %s", o)
	}
	status := map[string]string{}
	for _, oc := range r.Result.Outcomes {
		status[oc.ID] = oc.Status
	}
	if status["alpha-job"] != "added" || status["beta-proj"] != "needs_confirmation" || len(status) != 2 {
		t.Errorf("outcomes %+v", r.Result.Outcomes)
	}
	if strings.Join(f.upserts, ",") != "alpha-job,beta-proj" || !f.closed {
		t.Errorf("upserts %v, closed %v", f.upserts, f.closed)
	}
	for _, s := range []string{o, errOut} {
		for _, bad := range kbBulletText {
			if strings.Contains(s, bad) {
				t.Errorf("output carries the bullet text %q", bad)
			}
		}
	}
}

func TestKBSyncReplaceJSON(t *testing.T) {
	bank, rkb, f, r := replaceSetupCmd(t)
	o, _ := mustCode(t, 0, "kb", "sync", "--json", "--replace", "--bank", bank, "--rkb", rkb)
	var jr kbSyncReply
	decodeOne(t, o, &jr)
	if !jr.Replace || jr.Backup == nil || jr.Backup.Blocks != 4 || r.backups != 1 || jr.Deletes == nil || jr.Deleted == nil {
		t.Fatalf("reply %s", o)
	}
	var planned, deleted []string
	for _, b := range jr.Deletes.Delete {
		planned = append(planned, b.ID)
	}
	for _, oc := range jr.Deleted.Outcomes {
		deleted = append(deleted, oc.ID)
	}
	want := strings.Join(f.deletes, ",")
	if want != "edu_old3,exp_old1,pro_old2" || strings.Join(planned, ",") != want || strings.Join(deleted, ",") != want {
		t.Errorf("planned %v, reported %v, deleted %v", planned, deleted, f.deletes)
	}
	if jr.BlocksBefore == nil || *jr.BlocksBefore != 4 {
		t.Errorf("blocks_before %v", jr.BlocksBefore)
	}
}

// A sync that stops part-way keeps what it did beside the failure.
func TestKBSyncPartialJSON(t *testing.T) {
	bank, rkb, _ := kbApprovedSimple(t, "/opt/devprism/devprism")
	f := &failNthUpsert{fakeDevPrism: &fakeDevPrism{}, n: 2}
	old := startKB
	startKB = func(context.Context, resume.Config) (kbClient, error) { return &kbFailClient{f}, nil }
	t.Cleanup(func() { startKB = old })
	args := []string{"--json", "kb", "sync", "--bank", bank, "--rkb", rkb}
	o, _ := mustCode(t, exitRuntime, args...)
	msg, _ := failureIn(t, args, o)
	var r kbSyncReply
	decodeOne(t, o, &r)
	if !strings.Contains(msg, "devprism went away") || r.Result == nil || len(r.Result.Outcomes) == 0 || r.Result.Outcomes[0].Status != "added" {
		t.Errorf("reply %s", o)
	}
}

func TestRankfitWritesJSON(t *testing.T) {
	_, stateDir := setup(t)
	fixedNow(t)
	mustCode(t, 0, "init")
	seedEval(t, stateDir, 220)
	o, errOut := mustCode(t, 0, "--json", "rankfit", "--part", "platt")
	var r rankfitReply
	decodeOne(t, o, &r)
	if r.DryRun || !r.Written || r.Path != weightsPath(stateDir) || r.PlattUsable["d1-ultra"] == 0 || r.PlattUsable["jev"] == 0 || r.Error != "" {
		t.Fatalf("reply %s", o)
	}
	if strings.Contains(errOut, "{") {
		t.Errorf("stderr holds JSON: %q", errOut)
	}
	f, err := rankfit.Load(r.Path)
	if err != nil || len(f.Calibration) != 2 {
		t.Fatalf("the file the reply names: %+v, %v", f, err)
	}
	// A dry run reports the same counts and writes nothing.
	if err := os.Remove(r.Path); err != nil {
		t.Fatal(err)
	}
	o, _ = mustCode(t, 0, "--json", "rankfit", "--part", "platt", "--dry-run")
	var dry rankfitReply
	decodeOne(t, o, &dry)
	if !dry.DryRun || dry.Written || dry.PlattUsable["d1-ultra"] != r.PlattUsable["d1-ultra"] {
		t.Errorf("dry run %s", o)
	}
	if _, err := os.Stat(r.Path); !os.IsNotExist(err) {
		t.Errorf("a dry run wrote %s", r.Path)
	}
}
