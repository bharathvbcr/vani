package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/rkbexec"
)

// countingRKB counts the rkb runs it passes on to next.
type countingRKB struct {
	next rkbexec.Runner
	runs atomic.Int64
}

func (c *countingRKB) Run(ctx context.Context, cmd rkbexec.Command, d time.Duration) (rkbexec.Output, error) {
	c.runs.Add(1)
	return c.next.Run(ctx, cmd, d)
}

// A Jobs view builds the knowledge-base profile once (one rkb run) for both
// lookups that read it, the semantic scores and the recorded kbmatch
// readings, and still uses it for both: the reply is the one each lookup
// building its own gives.
func TestJobsViewBuildsTheProfileOnce(t *testing.T) {
	k := twoPostings(t)
	useFake(t, &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "unrelated"})})
	mustCode(t, 0, "kbmatch", "run") // a store with readings, so the kbmatch lookup needs the profile
	k.cfg.Semantic.Snapshot = filepath.Join(t.TempDir(), "snapshot")
	k.save(t) // semantic on, so the semantic lookup needs it too
	if !k.cfg.SemanticOn() {
		t.Fatal("semantic matching is off in the fixture")
	}
	c := &countingRKB{next: rkbRunner}
	old := rkbRunner
	rkbRunner = c
	t.Cleanup(func() { rkbRunner = old })

	e := &env{stateDir: k.state, configPath: k.cfgPath, stdout: io.Discard, stderr: io.Discard}
	_, in, err := e.jobsView(jobinsight.DefaultScoreHalfLifeDays)
	if err != nil {
		t.Fatal(err)
	}
	if n := c.runs.Load(); n != 1 {
		t.Fatalf("one view ran rkb %d times, want 1", n)
	}
	if in.rc.KBMatch == nil {
		t.Fatalf("the kbmatch lookup was not attached (warning %q)", in.info.Warning)
	}
	if !strings.Contains(in.info.Warning, "knowledge-base matching has no scores yet") {
		t.Fatalf("the semantic lookup did not get past the profile: warning %q", in.info.Warning)
	}

	// Each lookup on its own still builds it (the commands that call them).
	c.runs.Store(0)
	if _, warn := semanticLookup(k.cfg, k.state); !strings.Contains(warn, "no scores yet") {
		t.Fatalf("semanticLookup: %q", warn)
	}
	if f, warn := kbmatchLookupBy(k.cfg, k.state, nil); f == nil {
		t.Fatalf("kbmatchLookupBy: no lookup (%q)", warn)
	}
	if n := c.runs.Load(); n != 2 {
		t.Fatalf("the two lookups alone ran rkb %d times, want 2", n)
	}
}

// A profile that cannot be built is reported by both lookups that share it,
// as each reported its own.
func TestJobsViewSharedProfileFailureReachesBothLookups(t *testing.T) {
	k := twoPostings(t)
	useFake(t, &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core"})})
	mustCode(t, 0, "kbmatch", "run")
	k.cfg.Semantic.Snapshot = filepath.Join(t.TempDir(), "snapshot")
	k.save(t)
	c := &countingRKB{next: failingRKB{}}
	old := rkbRunner
	rkbRunner = c
	t.Cleanup(func() { rkbRunner = old })

	e := &env{stateDir: k.state, configPath: k.cfgPath, stdout: io.Discard, stderr: io.Discard}
	_, in, err := e.jobsView(jobinsight.DefaultScoreHalfLifeDays)
	if err != nil {
		t.Fatal(err)
	}
	if n := c.runs.Load(); n != 1 {
		t.Fatalf("one view ran rkb %d times, want 1", n)
	}
	w := in.info.Warning
	if !strings.Contains(w, "the profile could not be built") || !strings.Contains(w, "language-model reads of your work not used") {
		t.Fatalf("both lookups should say the profile failed: %q", w)
	}
	if in.rc.KBMatch != nil {
		t.Fatal("a kbmatch lookup without a profile")
	}
}

// failingRKB is an rkb that cannot run.
type failingRKB struct{}

func (failingRKB) Run(context.Context, rkbexec.Command, time.Duration) (rkbexec.Output, error) {
	return rkbexec.Output{}, errors.New("rkb is down")
}

// A Jobs list reads the config once: a deprecated key is warned about once,
// not once for the read and again for the ranking.
func TestJobsListReadsTheConfigOnce(t *testing.T) {
	k := kbmSetup(t, kbmSeed{"gh:a:1", "SecretCo", "Inference Engineer", longDesc, "partial_fit"})
	writeLegacyConfig(t, k.cfgPath)
	_, stderr := mustCode(t, 0, "jobs", "list", "--json")
	if n := strings.Count(stderr, "decide.jev_model"); n != 1 {
		t.Fatalf("the deprecated key was warned about %d times, want 1; stderr %q", n, stderr)
	}
}
