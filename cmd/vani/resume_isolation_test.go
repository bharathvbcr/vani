package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/rkbresume"
)

// isolationSpy wraps the fake rkb, records the environment of every call and,
// on the first call, runs a second `vani resume` while the first is mid-build.
type isolationSpy struct {
	inner  *fakeResumeRKB
	t      *testing.T
	mu     sync.Mutex
	envs   [][]string
	nested bool
	second struct {
		code   int
		stdout string
	}
}

func (s *isolationSpy) Run(ctx context.Context, c rkbresume.Command, timeout time.Duration) (rkbresume.Output, error) {
	s.mu.Lock()
	s.envs = append(s.envs, c.Env)
	first := !s.nested
	s.nested = true
	s.mu.Unlock()
	if first {
		resumeInput = strings.NewReader(goodResumeReq)
		s.second.code, s.second.stdout, _ = do(s.t, "resume")
	}
	return s.inner.Run(ctx, c, timeout)
}

// `vani resume` works in <state dir>/resumes/.tmp and holds the state dir's
// build lock, so a second one started meanwhile is refused as busy and spends
// nothing.
func TestResumeWorksInTheStateDirsTempFolderAndIsExclusive(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	resumeSetup(t, goodResumeReq, f, nil)
	spy := &isolationSpy{inner: f, t: t}
	resumeRunner = spy // resumeSetup's cleanup restores the real one
	state := os.Getenv("VANI_STATE_DIR")

	stdout, _ := mustCode(t, 0, "resume")
	if r := resumeOut(t, stdout); r.Error != "" || r.Pages != 2 {
		t.Fatalf("first reply = %+v", r)
	}
	if spy.second.code == 0 {
		t.Fatalf("the second resume, started mid-build, succeeded: %s", spy.second.stdout)
	}
	if r := resumeOut(t, spy.second.stdout); r.Code != rkbresume.CodeBusy {
		t.Fatalf("second reply = %+v, want code %q", r, rkbresume.CodeBusy)
	}
	if len(spy.envs) == 0 {
		t.Fatal("rkb never ran")
	}
	want := filepath.Join(state, "resumes", ".tmp") + string(filepath.Separator)
	for _, env := range spy.envs {
		tmp := ""
		for _, e := range env {
			if v, ok := strings.CutPrefix(e, "TMPDIR="); ok {
				tmp = v
			}
		}
		if !strings.HasPrefix(tmp, want) {
			t.Errorf("rkb's TMPDIR = %q, want it under %s", tmp, want)
		}
	}
	// The refused resume ran no rkb step: every build starts with one plain
	// `claims export`, and there was one build.
	plain := 0
	for _, a := range f.args {
		if a[0] == "claims" && !strings.Contains(strings.Join(a, " "), "--used-in") {
			plain++
		}
	}
	if plain != 1 {
		t.Errorf("%d builds reached rkb, want 1", plain)
	}
	// And the build folder is gone again.
	if ents, _ := os.ReadDir(filepath.Join(state, "resumes", ".tmp")); len(ents) != 0 {
		t.Errorf("%d build folders left in the state dir's temp folder", len(ents))
	}
}
