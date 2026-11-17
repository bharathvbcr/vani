package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/letterstore"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// w1bCancelProv is a model that answers and then fires cancel, the way a Stop,
// Ctrl-C or the command deadline arrives right after the model returned.
type w1bCancelProv struct {
	seqDraft
	cancel context.CancelFunc
}

type w1bCancelStream struct {
	draftStream
	cancel context.CancelFunc
}

func (s *w1bCancelStream) Response() (llm.Response, error) {
	r, err := s.draftStream.Response()
	s.cancel()
	return r, err
}

func (p *w1bCancelProv) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	st, err := p.seqDraft.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	return &w1bCancelStream{draftStream: *st.(*draftStream), cancel: p.cancel}, nil
}

// w1bAskedProv closes asked when the model is first called, so a test waits on
// a channel instead of reading the fake's request slice from another goroutine.
type w1bAskedProv struct {
	seqDraft
	asked chan struct{}
	once  sync.Once
}

func (p *w1bAskedProv) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	p.once.Do(func() { close(p.asked) })
	return p.seqDraft.Stream(ctx, req)
}

func w1bCoverEnv(t *testing.T, p llm.Provider) (*env, string) {
	t.Helper()
	draftSetup(t, `{}`, &draftLLM{}, true)
	old := draftProvider
	draftProvider = func(context.Context, *wired) (llm.Provider, error) { return p, nil }
	t.Cleanup(func() { draftProvider = old })
	state := os.Getenv("VANI_STATE_DIR")
	seedJobs(t, state, seedJob{key: "gh:acme:1", title: "Engineer", status: posting.Live, company: "Acme"})
	return &env{configPath: os.Getenv("VANI_CONFIG"), stateDir: state, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}, state
}

func TestW1bPaidLetterIsSavedWhenTheCommandIsStoppedAfterTheModelReturned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &w1bCancelProv{seqDraft: seqDraft{texts: []string{"I want this role at Acme."}}, cancel: cancel}
	e, state := w1bCoverEnv(t, p)
	out, err := e.coverBuild(ctx, "gh:acme:1")
	if ctx.Err() == nil {
		t.Fatal("the test never cancelled after the model returned")
	}
	if err != nil || out.Version != 1 || !out.TextSaved {
		t.Fatalf("a paid letter was lost after Stop: out %+v err %v", out, err)
	}
	st, serr := letterstore.Open(state)
	if serr != nil {
		t.Fatal(serr)
	}
	if text, terr := st.Text("gh:acme:1", 1); terr != nil || !strings.Contains(text, "Acme") {
		t.Fatalf("text %q err %v", text, terr)
	}
}

func TestW1bCancelBeforeTheModelReturnsSavesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &w1bAskedProv{seqDraft: seqDraft{texts: []string{"I want this role at Acme."}, block: time.Minute}, asked: make(chan struct{})}
	e, state := w1bCoverEnv(t, p)
	go func() {
		<-p.asked
		cancel()
	}()
	out, err := e.coverBuild(ctx, "gh:acme:1")
	if err == nil || out.Version != 0 {
		t.Fatalf("out %+v err %v", out, err)
	}
	st, _ := letterstore.Open(state)
	if _, cerr := st.Current("gh:acme:1"); letterstore.CodeOf(cerr) != letterstore.CodeNotFound {
		t.Fatalf("a cancelled build left a letter: %v", cerr)
	}
}

func TestW1bFullStoreRefusesBeforeAnyModelCall(t *testing.T) {
	cases := map[string]func(t *testing.T, st *letterstore.Store, state string, exact bool){
		"per posting at the cap": func(t *testing.T, st *letterstore.Store, _ string, _ bool) {
			for i := 1; i <= letterstore.MaxVersions; i++ {
				if _, err := st.Save(context.Background(), "gh:acme:1", fmt.Sprintf("Letter number %d for Acme.", i), "", nil); err != nil {
					t.Fatal(err)
				}
			}
		},
		"overall at the cap": func(t *testing.T, _ *letterstore.Store, state string, _ bool) {
			for i := 0; i < letterstore.MaxJobs; i++ {
				if err := os.MkdirAll(filepath.Join(letterstore.Dir(state), fmt.Sprintf("%064x", i)), 0o700); err != nil {
					t.Fatal(err)
				}
			}
		},
	}
	for name, fill := range cases {
		t.Run(name, func(t *testing.T) {
			p := &seqDraft{texts: []string{"I want this role at Acme."}}
			e, state := w1bCoverEnv(t, p)
			st, err := letterstore.Open(state)
			if err != nil {
				t.Fatal(err)
			}
			fill(t, st, state, true)
			out, err := e.coverBuild(context.Background(), "gh:acme:1")
			if err == nil || out.Code != letterstore.CodeFull {
				t.Fatalf("out %+v err %v", out, err)
			}
			if len(p.reqs) != 0 {
				t.Fatalf("the model was asked %d times for a letter the store could not keep", len(p.reqs))
			}
		})
	}
}

func TestW1bStoreOneBelowTheCapStillBuilds(t *testing.T) {
	p := &seqDraft{texts: []string{"I want this role at Acme."}}
	e, state := w1bCoverEnv(t, p)
	st, _ := letterstore.Open(state)
	for i := 1; i < letterstore.MaxVersions; i++ {
		if _, err := st.Save(context.Background(), "gh:acme:1", fmt.Sprintf("Letter number %d for Acme.", i), "", nil); err != nil {
			t.Fatal(err)
		}
	}
	out, err := e.coverBuild(context.Background(), "gh:acme:1")
	if err != nil || out.Version != letterstore.MaxVersions || len(p.reqs) == 0 {
		t.Fatalf("out %+v err %v calls %d", out, err, len(p.reqs))
	}
}

func TestW1bASaveThatFailsAfterThePaidCallSaysSo(t *testing.T) {
	var state string
	// The model's reply puts a file where the letters directory belongs: the
	// capacity probe ran before it (nothing to count), the save after it fails.
	p := &w1bCancelProv{seqDraft: seqDraft{texts: []string{"I want this role at Acme."}}, cancel: func() {
		if err := os.WriteFile(letterstore.Dir(state), []byte("x"), 0o600); err != nil {
			t.Error(err)
		}
	}}
	e, state := w1bCoverEnv(t, p)
	out, err := e.coverBuild(context.Background(), "gh:acme:1")
	if err == nil || out.Version != 0 {
		t.Fatalf("out %+v err %v", out, err)
	}
	if !strings.Contains(err.Error(), "already paid") || out.Code == "" {
		t.Fatalf("the reply does not say the paid letter was not saved: code %q err %v", out.Code, err)
	}
	var le *letterstore.Error
	if !errors.As(err, &le) {
		t.Fatalf("the store's error is not kept: %v", err)
	}
}
