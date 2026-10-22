package main

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Writer A's export has read the labels (only gh:1) and is slow; writer B
// labels gh:2 and exports. Without serialisation A's older snapshot could be
// renamed over B's newer one and gh:2 would drop out of the held-out file, so
// the posting could leak into training. Exports share a lock and read their
// labels inside it, so B waits for A and then reads gh:1+gh:2: the last writer
// always has the freshest labels.
func TestStaleExportCannotOverwriteNewerExport(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedExportLedger(t, state, map[string]string{"gh:1": "one", "gh:2": "two"})
	e, _ := newExportEnv(t, state)
	a, err := e.openEvalStore("")
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.openEvalStore("")
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	a.AfterWrite = func() {
		_, err := eval.ExportHeldoutLocked(e.heldoutExportPath(), a, func(k posting.Key) (string, bool) {
			once.Do(func() {
				close(entered)
				<-release // A is slow after reading the label set
			})
			return map[posting.Key]string{"gh:1": "one", "gh:2": "two"}[k], true
		})
		if err != nil {
			t.Error(err)
		}
	}
	// b keeps the real hook (e.reexportHeldout), which takes the same lock.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = a.Put(eval.Entry{Key: "gh:1", Labels: map[string]string{"role_fit": "strong_fit"}})
	}()
	<-entered
	go func() {
		defer wg.Done()
		_ = b.Put(eval.Entry{Key: "gh:2", Labels: map[string]string{"role_fit": "strong_fit"}})
	}()
	time.Sleep(200 * time.Millisecond) // B's label lands and its export queues behind A
	close(release)
	wg.Wait()
	final, _ := os.ReadFile(exportFile(state))
	if !strings.Contains(string(final), "gh:2") || !strings.Contains(string(final), "gh:1") {
		t.Fatalf("final export lacks a labelled posting (stale writer landed last):\n%s", final)
	}
}

// A bug in the export must never take `vani label` down after the label
// landed: the hook recovers, warns without any text, and the label stays.
func TestReexportHookRecoversFromPanic(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	e, stderr := newExportEnv(t, state)
	// A nil store makes the export fail, and the first warning write panics:
	// a panic from inside the hook body.
	w := &panicOnceWriter{buf: stderr}
	e.stderr = w
	e.reexportHeldout(nil)
	if !w.panicked {
		t.Fatal("the injected panic never fired")
	}
	got := stderr.String()
	if !strings.Contains(got, "panicked") || strings.Contains(got, "boom") {
		t.Fatalf("want a text-free panic warning; stderr: %q", got)
	}
}

type panicOnceWriter struct {
	buf      *bytes.Buffer
	panicked bool
}

func (w *panicOnceWriter) Write(p []byte) (int, error) {
	if !w.panicked {
		w.panicked = true
		panic("boom: posting text")
	}
	return w.buf.Write(p)
}

// Same race through the real hook, many concurrent writers on one label dir.
func TestConcurrentLabelWritersExportEveryLabel(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	const writers, each = 8, 10
	descs := map[string]string{}
	for i := 0; i < writers*each; i++ {
		descs[keyN(i)] = "text " + keyN(i)
	}
	seedExportLedger(t, state, descs)
	e, _ := newExportEnv(t, state)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		s, err := e.openEvalStore("")
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				_ = s.Put(eval.Entry{Key: posting.Key(keyN(w*each + i)), Labels: map[string]string{"role_fit": "strong_fit"}})
			}
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(exportFile(state))
	if n := strings.Count(string(b), "\n"); n != writers*each {
		t.Fatalf("export has %d rows, want %d labelled postings", n, writers*each)
	}
}

func keyN(i int) string { return "gh:" + string(rune('a'+i/26)) + string(rune('a'+i%26)) }
