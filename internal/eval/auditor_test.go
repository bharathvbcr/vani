package eval

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// bruteAUC is the oracle: every (wanted, unwanted) pair, ties counting half.
func bruteAUC(s []float64, pos []bool) (float64, bool) {
	var num, den float64
	for i := range s {
		for j := range s {
			if !pos[i] || pos[j] {
				continue
			}
			den++
			switch {
			case s[i] > s[j]:
				num++
			case s[i] == s[j]:
				num += 0.5
			}
		}
	}
	if den == 0 {
		return 0, false
	}
	return num / den, true
}

func TestAUCMatchesThePairwiseOracleWithTies(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 9))
	for trial := range 300 {
		n := 1 + rng.IntN(40)
		s, pos := make([]float64, n), make([]bool, n)
		for i := range n {
			s[i] = float64(rng.IntN(4)) // heavy ties, like yes/maybe/no
			pos[i] = rng.IntN(3) == 0
		}
		got, ok := AUC(s, pos)
		want, wok := bruteAUC(s, pos)
		if ok != wok || (ok && math.Abs(got-want) > 1e-12) {
			t.Fatalf("trial %d: AUC %v,%v want %v,%v (s %v pos %v)", trial, got, ok, want, wok, s, pos)
		}
	}
}

func TestAUCIsUndefinedForOneClassAndBadInput(t *testing.T) {
	for name, c := range map[string]struct {
		s   []float64
		pos []bool
	}{
		"empty":        {nil, nil},
		"all wanted":   {[]float64{1, 2}, []bool{true, true}},
		"none wanted":  {[]float64{1, 2}, []bool{false, false}},
		"length skew":  {[]float64{1, 2}, []bool{true}},
		"NaN score":    {[]float64{math.NaN(), 1}, []bool{true, false}},
		"infinite one": {[]float64{math.Inf(1), 1}, []bool{true, false}},
	} {
		if _, ok := AUC(c.s, c.pos); ok {
			t.Errorf("%s: AUC defined", name)
		}
	}
	if a, ok := AUC([]float64{2, 0}, []bool{true, false}); !ok || a != 1 {
		t.Errorf("perfect separation: %v %v", a, ok)
	}
	if a, ok := AUC([]float64{1, 1}, []bool{true, false}); !ok || a != 0.5 {
		t.Errorf("all tied: %v %v", a, ok)
	}
}

// items builds n judged postings in group: wanted ones score high on the
// auditor when auditorGood, and the baselines score at random.
func items(group string, nPos, nNeg int, auditorGood bool, seed uint64) []AuditorItem {
	rng := rand.New(rand.NewPCG(seed, 1))
	var out []AuditorItem
	for i := range nPos + nNeg {
		w := i < nPos
		a := float64(rng.IntN(3))
		if auditorGood {
			a = 0
			if w {
				a = 2
			}
		}
		out = append(out, AuditorItem{Key: posting.Key(fmt.Sprintf("gh:%s:%d", group, i)), Group: group, Wanted: w, Yes: w && i%2 == 0,
			Auditor: a, Teacher: float64(rng.IntN(5)), Relevance: rng.Float64() * 100, HasAuditor: true, HasTeacher: true, HasRelevance: true})
	}
	return out
}

func TestGateCannotBeComputedWithNothingOrOneClass(t *testing.T) {
	a, err := BuildAuditorAgreement(nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if a.Gate.Verdict != GateCannot {
		t.Fatalf("no postings: verdict %q", a.Gate.Verdict)
	}
	// The kept group has only unwanted postings: AUC undefined, never a pass.
	in := append(items(GroupKept, 0, 30, true, 1), items(GroupDropped, 15, 15, true, 2)...)
	a, _ = BuildAuditorAgreement(in, 1)
	if a.Gate.Verdict != GateCannot || !strings.Contains(strings.Join(a.Gate.Why, ";"), "undefined") {
		t.Errorf("one-class group: %+v", a.Gate)
	}
	for _, r := range a.Rows {
		if r.Group == GroupKept && (r.Auditor.Defined || r.VsTeacher.Defined) {
			t.Errorf("kept row defined despite one class: %+v", r)
		}
	}
}

func TestGateCannotBeComputedWhenNoPostingHasEveryScore(t *testing.T) {
	in := items(GroupKept, 15, 15, true, 1)
	for i := range in {
		in[i].HasAuditor = false // the auditor was never run
	}
	in = append(in, items(GroupDropped, 15, 15, true, 2)...)
	a, _ := BuildAuditorAgreement(in, 1)
	if a.Gate.Verdict != GateCannot || !strings.Contains(strings.Join(a.Gate.Why, ";"), "missing auditor 30") {
		t.Errorf("gate %+v", a.Gate)
	}
}

func TestGateIsNotYetBelowTheClassFloorEvenWhenPerfect(t *testing.T) {
	// Two wanted dropped postings, perfectly separated: the bootstrap would call
	// it decisive; the floor does not.
	in := append(items(GroupKept, 20, 20, true, 1), items(GroupDropped, 2, 15, true, 2)...)
	a, _ := BuildAuditorAgreement(in, 1)
	if a.Gate.Verdict != GateNotYet || !strings.Contains(strings.Join(a.Gate.Why, ";"), "dropped: n=17 (2 wanted") {
		t.Errorf("gate %+v", a.Gate)
	}
}

func TestGatePassesOnlyWhenTheAuditorClearlyBeatsBothBaselinesInBothGroups(t *testing.T) {
	good := append(items(GroupKept, 25, 25, true, 1), items(GroupDropped, 20, 30, true, 2)...)
	a, _ := BuildAuditorAgreement(good, 3)
	if a.Gate.Verdict != GatePasses {
		t.Fatalf("a perfect auditor against random baselines: %+v\n%s", a.Gate, a.Markdown())
	}
	// The same auditor answering at random does not pass.
	bad := append(items(GroupKept, 25, 25, false, 1), items(GroupDropped, 20, 30, false, 2)...)
	a, _ = BuildAuditorAgreement(bad, 3)
	if a.Gate.Verdict == GatePasses {
		t.Fatalf("a random auditor passed:\n%s", a.Markdown())
	}
	// A baseline as good as the auditor stops it too.
	tie := append(items(GroupKept, 25, 25, true, 1), items(GroupDropped, 20, 30, true, 2)...)
	for i := range tie {
		tie[i].Teacher = tie[i].Auditor
	}
	a, _ = BuildAuditorAgreement(tie, 3)
	if a.Gate.Verdict == GatePasses {
		t.Fatalf("an auditor no better than the teacher passed:\n%s", a.Markdown())
	}
}

func TestAgreementIsDeterministicAndRefusesDuplicatesAndOversize(t *testing.T) {
	in := append(items(GroupKept, 12, 18, false, 1), items(GroupDropped, 11, 19, false, 2)...)
	a, _ := BuildAuditorAgreement(in, 42)
	b, _ := BuildAuditorAgreement(in, 42)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Error("the same items and seed gave different reports")
	}
	if _, err := BuildAuditorAgreement(append(in, in[0]), 42); err == nil {
		t.Error("a duplicate posting in one group was accepted")
	}
	// The same key may sit in a label group and in the marks.
	m := in[0]
	m.Group, m.Liked = GroupMarks, true
	if _, err := BuildAuditorAgreement(append(in, m), 42); err != nil {
		t.Errorf("a key judged and marked was refused: %v", err)
	}
	if _, err := BuildAuditorAgreement(make([]AuditorItem, MaxAuditorItems+1), 1); err == nil {
		t.Error("an oversized report was accepted")
	}
}

func TestAgreementJSONHasNoNaN(t *testing.T) {
	// Undefined rows and empty intervals must still marshal.
	in := append(items(GroupKept, 0, 3, true, 1), items(GroupDropped, 1, 1, true, 2)...)
	a, _ := BuildAuditorAgreement(in, 1)
	if _, err := json.Marshal(a); err != nil {
		t.Fatalf("the report does not marshal: %v", err)
	}
}

func TestLockFileForKeepsTheTeachersLockAndSeparatesTheAuditors(t *testing.T) {
	dir := t.TempDir()
	if got := filepath.Base(LockFileFor(filepath.Join(dir, TeacherFile))); got != teacherLockFile {
		t.Errorf("the teacher's lock moved to %q", got)
	}
	if got := filepath.Base(LockFileFor(filepath.Join(dir, AuditorFile))); got != "auditor_readings.lock" {
		t.Errorf("auditor lock %q", got)
	}
	if got := filepath.Base(LockFileFor(filepath.Join(dir, "weird"))); got != "weird.lock" {
		t.Errorf("a name without .jsonl locks %q", got)
	}
}

func TestLatestAndModelsOfIgnoreThePromptButNotTheTaskOrModel(t *testing.T) {
	s, err := OpenTeacherStore(filepath.Join(t.TempDir(), TeacherFile))
	if err != nil {
		t.Fatal(err)
	}
	row := func(key, task, model, prompt, value string, at int) TeacherRow {
		return TeacherRow{Key: posting.Key(key), Task: task, TextSHA256: "t", Model: model, FixtureSHA: "f", PromptHash: prompt,
			Slots: map[string]TeacherSlot{"role_fit": {Reading: "answered", Value: value}}, At: time.Unix(int64(at), 0)}
	}
	for _, r := range []TeacherRow{
		row("gh:a:1", "", "haiku", "old", "adjacent", 1),
		row("gh:a:1", "", "haiku", "new", "strong_fit", 2),
		row("gh:a:1", "career.kb_match", "haiku", "new", "core", 3),
		row("gh:a:1", "", "sonnet", "new", "mismatch_domain", 4),
		row("gh:a:2", "", "sonnet", "new", "partial_fit", 5),
	} {
		if err := s.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := s.Latest("gh:a:1", "", "haiku")
	if !ok || got.Slots["role_fit"].Value != "strong_fit" {
		t.Errorf("latest haiku jd_triage row: %+v %v", got, ok)
	}
	if _, ok := s.Latest("gh:a:2", "", "haiku"); ok {
		t.Error("a model with no row of the key was found")
	}
	m := s.ModelsOf([]posting.Key{"gh:a:1", "gh:a:2", "gh:a:3"}, "")
	if m["haiku"] != 1 || m["sonnet"] != 2 || len(m) != 2 {
		t.Errorf("models %v", m)
	}
}
