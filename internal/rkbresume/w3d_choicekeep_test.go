package rkbresume

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func w3dFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.cfg.ChoiceDir = filepath.Join(t.TempDir(), "choices")
	f.provider.replies = []string{validReply()}
	return f
}

func w3dKept(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// A validated choice survives a failed fit: the retry makes zero model calls and
// builds the same choice.
func TestW3dValidatedChoiceReusedAfterFitFailure(t *testing.T) {
	f := w3dFixture(t)
	f.rkb.runErr = map[string]error{"fit": errors.New("chrome went away")}
	if _, err := f.build(); err == nil {
		t.Fatal("the first attempt should fail in fit")
	}
	if got := len(f.provider.reqs); got != 1 {
		t.Fatalf("first attempt model calls = %d, want 1", got)
	}
	if len(w3dKept(t, f.cfg.ChoiceDir)) != 1 {
		t.Fatalf("kept choices = %v, want one", w3dKept(t, f.cfg.ChoiceDir))
	}

	f.rkb.runErr = nil
	res, err := f.build()
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := len(f.provider.reqs); got != 1 {
		t.Fatalf("model calls after the retry = %d, want still 1 (the kept choice is reused)", got)
	}
	if res.Source != SourceTailored {
		t.Fatalf("source = %q, want tailored from the kept choice", res.Source)
	}
	if ids := strings.Join(selBullets(t, f.rkb.sels[len(f.rkb.sels)-1]), ","); ids != "s3,s1,a3,a1,b1,c1" {
		t.Errorf("retry built %s", ids)
	}
	if res.Cost.Turns != 0 {
		t.Errorf("the retry reports %d model turns, want 0", res.Cost.Turns)
	}
	if left := w3dKept(t, f.cfg.ChoiceDir); len(left) != 0 {
		t.Errorf("a built choice is still kept: %v", left)
	}
}

// The deadline hitting during fit keeps the choice too.
func TestW3dValidatedChoiceReusedAfterDeadline(t *testing.T) {
	f := w3dFixture(t)
	f.rkb.block = "fit"
	f.cfg.Deadline = 400 * time.Millisecond
	oldMin := minForModel
	minForModel = 10 * time.Millisecond
	defer func() { minForModel = oldMin }()
	if _, err := f.build(); err == nil {
		t.Fatal("the first attempt should time out in fit")
	}
	f.rkb.block = ""
	f.cfg.Deadline = 0
	if _, err := f.build(); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := len(f.provider.reqs); got != 1 {
		t.Fatalf("model calls = %d, want 1", got)
	}
}

// Changed inputs never reuse a kept choice.
func TestW3dChangedInputsAskTheModelAgain(t *testing.T) {
	f := w3dFixture(t)
	f.rkb.runErr = map[string]error{"fit": errors.New("chrome went away")}
	_, _ = f.build()
	f.rkb.runErr = nil

	for name, change := range map[string]func(){
		"model":   func() { f.cfg.Model = "other-model" },
		"posting": func() { f.cfg.Model = "m" },
	} {
		before := len(f.provider.reqs)
		change()
		req := Request{Company: "Example AI", Title: "Inference Engineer", Description: testJD}
		if name == "posting" {
			req.Description = testJD + " Also Terraform."
		}
		if _, err := Build(context.Background(), f.cfg, req); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := len(f.provider.reqs) - before; got != 1 {
			t.Errorf("%s changed: model calls = %d, want a fresh call", name, got)
		}
	}
}

// A bank change is an input too.
func TestW3dChangedBankAsksTheModelAgain(t *testing.T) {
	f := w3dFixture(t)
	f.rkb.runErr = map[string]error{"fit": errors.New("chrome went away")}
	_, _ = f.build()
	f.rkb.runErr = nil
	raw, err := os.ReadFile(f.bank)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.bank, append(raw, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	before := len(f.provider.reqs)
	if _, err := f.build(); err != nil {
		t.Fatal(err)
	}
	if len(f.provider.reqs)-before != 1 {
		t.Errorf("a changed bank reused a kept choice")
	}
}

// A rejected choice is not kept: the retry deserves a fresh turn.
func TestW3dRejectedChoiceIsNotKept(t *testing.T) {
	f := w3dFixture(t)
	f.rkb.lint = func(string) (int, string) { return 1, "ERROR: bad\n1 error(s), 0 warning(s)\n" }
	_, _ = f.build()
	if left := w3dKept(t, f.cfg.ChoiceDir); len(left) != 0 {
		t.Errorf("a rejected choice was kept: %v", left)
	}
}

// Kept choices expire and are bounded in number and size.
func TestW3dKeptChoicesAreBounded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "choices")
	k := choiceKeep{dir: dir}
	now := time.Now()
	for i := 0; i < maxKeptChoices+5; i++ {
		key := sha256Hex([]byte{byte(i)})
		k.save(key, "reply", now.Add(time.Duration(i)*time.Second))
	}
	if got := len(w3dKept(t, dir)); got != maxKeptChoices {
		t.Errorf("kept = %d, want %d", got, maxKeptChoices)
	}
	old := sha256Hex([]byte{0})
	if _, ok := k.load(old, now.Add(time.Hour)); ok {
		t.Error("the oldest entry survived eviction")
	}
	newest := sha256Hex([]byte{byte(maxKeptChoices + 4)})
	if text, ok := k.load(newest, now.Add(time.Hour)); !ok || text != "reply" {
		t.Errorf("newest = %q %v", text, ok)
	}
	if _, ok := k.load(newest, now.Add(keptChoiceTTL+time.Hour)); ok {
		t.Error("an expired entry was reused")
	}
	big := sha256Hex([]byte("big"))
	k.save(big, strings.Repeat("x", maxKeptChoiceBytes+1), now)
	if _, ok := k.load(big, now); ok {
		t.Error("an oversized reply was kept")
	}
}

// A damaged kept file is a miss, never a crash or a reuse.
func TestW3dDamagedKeptChoiceIsAMiss(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "choices")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key := sha256Hex([]byte("k"))
	if err := os.WriteFile(filepath.Join(dir, key+".json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := (choiceKeep{dir: dir}).load(key, time.Now()); ok {
		t.Error("a damaged file was reused")
	}
	if _, ok := (choiceKeep{dir: dir}).load("../../etc/passwd", time.Now()); ok {
		t.Error("a path-shaped key was read")
	}
}
