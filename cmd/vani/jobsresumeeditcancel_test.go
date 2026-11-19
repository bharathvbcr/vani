package main

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/rkbresume"
)

const stdinCancelWithin = 5 * time.Second

// Ctrl-C while the edit is being read from a stdin that never closes ends the
// read with the cancelled code, promptly.
func TestReadEditEndsWhenTheContextIsCancelled(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	old := resumeInput
	resumeInput = pr
	t.Cleanup(func() { resumeInput = old })

	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan *resumeError, 1)
	go func() { _, e := readEdit(ctx); got <- e }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case e := <-got:
		if e == nil || e.code != rkbresume.CodeCancel {
			t.Fatalf("readEdit = %+v, want the cancelled code", e)
		}
	case <-time.After(stdinCancelWithin):
		t.Fatal("readEdit still blocked on stdin after the context was cancelled")
	}
}

// An already-cancelled context never waits on the pipe at all.
func TestReadEditWithACancelledContextDoesNotBlock(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	old := resumeInput
	resumeInput = pr
	t.Cleanup(func() { resumeInput = old })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := make(chan *resumeError, 1)
	go func() { _, e := readEdit(ctx); got <- e }()
	select {
	case e := <-got:
		if e == nil || e.code != rkbresume.CodeCancel {
			t.Fatalf("readEdit = %+v", e)
		}
	case <-time.After(stdinCancelWithin):
		t.Fatal("blocked")
	}
}

// A normal edit still reads to the end of a closed stdin.
func TestReadEditStillReadsTheEdit(t *testing.T) {
	pr, pw := io.Pipe()
	old := resumeInput
	resumeInput = pr
	t.Cleanup(func() { resumeInput = old })
	go func() { _, _ = pw.Write([]byte("{}")); _ = pw.Close() }()
	raw, e := readEdit(context.Background())
	if e != nil || string(raw) != "{}" {
		t.Fatalf("raw %q, err %v", raw, e)
	}
}

// Through the command: a render whose stdin never closes ends on the signal
// context with exit 1 and the cancelled code, not hanging.
func TestJobsResumeRenderCtrlCDuringStdinRead(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrSetup(t, f, nil)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	oldIn, oldSig := resumeInput, signalContext
	resumeInput = pr
	signalContext = func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(100*time.Millisecond, cancel) // the owner's Ctrl-C
		return ctx, cancel
	}
	t.Cleanup(func() { resumeInput, signalContext = oldIn, oldSig })

	type res struct {
		code int
		out  string
	}
	done := make(chan res, 1)
	go func() {
		code, out, _ := do(t, "jobs", "resume", "render", "--json", jrKey)
		done <- res{code, out}
	}()
	select {
	case r := <-done:
		if r.code != 1 {
			t.Fatalf("exit %d, want 1", r.code)
		}
		if rep := jrFail(t, r.out); rep.Code != rkbresume.CodeCancel {
			t.Fatalf("reply %+v, want the cancelled code", rep)
		}
	case <-time.After(stdinCancelWithin):
		t.Fatal("jobs resume render still blocked on stdin after the signal context ended")
	}
}
