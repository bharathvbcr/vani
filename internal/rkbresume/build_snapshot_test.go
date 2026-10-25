package rkbresume

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/filelock"
)

// selPathOf is the selection file a step reads, or "".
func selPathOf(step string, c Command) string {
	switch step {
	case "claims-used":
		return flag(c.Args, "--used-in")
	case "lint", "render":
		return c.Args[1]
	}
	return ""
}

// jsonString is the top-level string field key of a JSON object.
func jsonString(t *testing.T, raw []byte, key string) string {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	var s string
	if err := json.Unmarshal(top[key], &s); err != nil {
		t.Fatalf("%s is not a string: %v", key, err)
	}
	return s
}

func asError(t *testing.T, err error) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v (%T), want *Error", err, err)
	}
	return e
}

// ---- bank snapshot

func TestEveryRkbStepReadsTheSnapshotOfTheBank(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{validReply()}
	orig, err := os.ReadFile(f.bank)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	f.rkb.onStep = func(step string, c Command) {
		if step == "claims-plain" { // the bank has been read; now the owner edits it
			edited := bytes.ReplaceAll(orig, []byte("Raised throughput"), []byte("Lowered throughput"))
			if bytes.Equal(edited, orig) {
				t.Error("the fixture edit changed nothing")
			}
			if err := os.WriteFile(f.bank, edited, 0o600); err != nil {
				t.Error(err)
			}
		}
		sp := selPathOf(step, c)
		if sp == "" {
			return
		}
		sel, err := os.ReadFile(sp)
		if err != nil {
			t.Error(err)
			return
		}
		bank := jsonString(t, sel, "bank")
		if bank == f.bank {
			t.Errorf("step %s was handed the live bank", step)
		}
		got, err := os.ReadFile(bank)
		if err != nil {
			t.Errorf("step %s: the bank it was handed is unreadable: %v", step, err)
			return
		}
		if !bytes.Equal(got, orig) {
			t.Errorf("step %s read a bank that differs from the one Vani judged", step)
		}
		if st, err := os.Stat(bank); err == nil && st.Mode().Perm() != 0o600 {
			t.Errorf("snapshot mode %o, want 0600", st.Mode().Perm())
		}
		seen = append(seen, step)
	}
	res, err := f.build()
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != SourceTailored {
		t.Fatalf("selection = %s (%s)", res.Source, res.Reason)
	}
	for _, step := range []string{"claims-used", "lint", "render"} {
		if !has(seen, step) {
			t.Errorf("step %s never ran, so its bank was not checked (saw %v)", step, seen)
		}
	}
}

func TestClaimChangedAfterLoadIsRefusedBeforeAnyModelTurn(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{validReply()}
	f.rkb.claimValue = func(id, v, sel string) string {
		if sel != "" && id == "clmLat" {
			return "41%" // rkb's repos changed after the plain export
		}
		return v
	}
	_, err := f.build()
	if e := asError(t, err); e.Code != CodeKBChanged || !strings.Contains(e.Msg, "knowledge base changed during the build; build again") {
		t.Fatalf("err = %+v, want a %s error", e, CodeKBChanged)
	}
	if len(f.provider.reqs) != 0 {
		t.Errorf("a model turn was spent after the KB changed (%d)", len(f.provider.reqs))
	}
	if f.rkb.count("render") != 0 || f.rkb.count("fit") != 0 {
		t.Errorf("rkb rendered from a changed KB: %v", f.rkb.steps)
	}
}

func TestClaimChangedBetweenAttemptsIsRefusedNeverRendered(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{validReply()}
	used := 0
	f.rkb.claimValue = func(id, v, sel string) string {
		if sel == "" || id != "clmLat" {
			return v
		}
		used++
		if used >= 2 { // the probe export agreed; the tailored attempt's does not
			return "41%"
		}
		return v
	}
	_, err := f.build()
	if e := asError(t, err); e.Code != CodeKBChanged {
		t.Fatalf("err = %+v, want a %s error", e, CodeKBChanged)
	}
	if f.rkb.count("render") != 0 || f.rkb.count("fit") != 0 {
		t.Errorf("rkb rendered after the KB changed: %v", f.rkb.steps)
	}
}

func TestUnchangedClaimsAreNotRefused(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{validReply()}
	f.rkb.claimValue = func(id, v, sel string) string { return v }
	if _, err := f.build(); err != nil {
		t.Fatal(err)
	}
}

// ---- private TMPDIR and work folder

func tmpOf(env []string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "TMPDIR="); ok {
			return v
		}
	}
	return ""
}

func TestRkbRunsWithATempFolderInsideTheBuildFolder(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{validReply()}
	t.Setenv("TMPDIR", t.TempDir()) // the parent's temp folder is not rkb's
	parentTmp := os.Getenv("TMPDIR")
	var left []string
	f.rkb.onStep = func(step string, c Command) {
		tmp := tmpOf(c.Env)
		if n := strings.Count(strings.Join(c.Env, "\n"), "TMPDIR="); n != 1 {
			t.Errorf("step %s: %d TMPDIR entries in %v", step, n, c.Env)
		}
		if tmp == "" || tmp == parentTmp || filepath.Base(tmp) != tmpDirName || !strings.HasPrefix(filepath.Base(filepath.Dir(tmp)), buildDirPrefix) {
			t.Errorf("step %s: TMPDIR = %q, want <build folder>/tmp", step, tmp)
			return
		}
		if st, err := os.Stat(tmp); err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
			t.Errorf("step %s: rkb's temp folder %s is not a private directory (%v)", step, tmp, err)
		}
		if step == "fit" { // what rkb's fit leaves when it is killed
			d, err := os.MkdirTemp(tmp, "rkb-chrome-")
			if err != nil {
				t.Error(err)
				return
			}
			left = append(left, d)
		}
	}
	if _, err := f.build(); err != nil {
		t.Fatal(err)
	}
	if len(left) == 0 {
		t.Fatal("fit never ran")
	}
	for _, d := range left {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("rkb's temp folder %s outlived the build", d)
		}
	}
	if ents, _ := os.ReadDir(parentTmp); len(ents) != 0 {
		t.Errorf("the build left %d entries in the parent's temp folder", len(ents))
	}
}

func chtimesAgo(t *testing.T, path string, ago time.Duration) {
	t.Helper()
	at := time.Now().Add(-ago)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestSweepRemovesOnlyStalePrefixedFolders(t *testing.T) {
	work := t.TempDir()
	mk := func(name string, ago time.Duration) string {
		p := filepath.Join(work, name)
		if err := os.MkdirAll(filepath.Join(p, "tmp", "rkb-chrome-1"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "tmp", "rkb-chrome-1", "page.html"), []byte("<html>header</html>"), 0o600); err != nil {
			t.Fatal(err)
		}
		chtimesAgo(t, p, ago)
		return p
	}
	stale := mk(buildDirPrefix+"stale", MaxDeadline+time.Minute)
	fresh := mk(buildDirPrefix+"fresh", time.Minute)
	unrelated := mk("something-else", 24*time.Hour)

	// A symlink with the prefix, pointing at a stale folder outside the work
	// folder: it must neither be followed nor have its target emptied.
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	chtimesAgo(t, outside, 24*time.Hour)
	link := filepath.Join(work, buildDirPrefix+"link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	// A plain file with the prefix is not a build folder either.
	file := filepath.Join(work, buildDirPrefix+"file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	chtimesAgo(t, file, 24*time.Hour)

	if n := sweepStale(work, MaxDeadline, time.Now()); n != 1 {
		t.Errorf("swept %d folders, want 1", n)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the stale build folder survives: %v", err)
	}
	for _, p := range []string{fresh, unrelated, keep, file} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was touched: %v", err)
	}
}

func TestSweepBoundsWhatItLooksAt(t *testing.T) {
	if n := sweepStale(filepath.Join(t.TempDir(), "absent"), MaxDeadline, time.Now()); n != 0 {
		t.Errorf("swept %d folders of a folder that is not there", n)
	}
}

func TestBuildSweepsItsWorkFolderAndWorksInsideIt(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{validReply()}
	work := filepath.Join(t.TempDir(), "resumes", ".tmp") // not there yet
	f.cfg.WorkDir = work
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(work, buildDirPrefix+"killed")
	fresh := filepath.Join(work, buildDirPrefix+"running")
	for _, d := range []string{stale, fresh} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	chtimesAgo(t, stale, MaxDeadline+time.Minute)
	var inWork bool
	f.rkb.onStep = func(step string, c Command) {
		if strings.HasPrefix(tmpOf(c.Env), work+string(filepath.Separator)) {
			inWork = true
		}
	}
	if _, err := f.build(); err != nil {
		t.Fatal(err)
	}
	if !inWork {
		t.Error("the build did not work inside Config.WorkDir")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the stale folder of a killed build survives: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a recent folder was removed: %v", err)
	}
	ents, _ := os.ReadDir(work)
	for _, e := range ents {
		if e.Name() != filepath.Base(fresh) {
			t.Errorf("the build left %s behind", e.Name())
		}
	}
	if st, err := os.Stat(work); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("work folder: %v %v", st, err)
	}
}

func TestRelativeWorkAndLockFoldersAreRefused(t *testing.T) {
	for _, set := range []func(*Config){
		func(c *Config) { c.WorkDir = "rel/work" },
		func(c *Config) { c.LockDir = "rel/lock" },
	} {
		f := newFixture(t)
		set(&f.cfg)
		_, err := f.build()
		if e := asError(t, err); e.Code != CodeConfig {
			t.Errorf("err = %+v, want a config error", e)
		}
		if len(f.rkb.calls) != 0 {
			t.Error("rkb ran under a refused config")
		}
	}
}

// ---- locks

func TestSecondBuildWithTheSameLockDirIsBusyAndSpendsNothing(t *testing.T) {
	lockDir := t.TempDir()
	first := newFixture(t)
	first.cfg.LockDir = lockDir
	first.provider.replies = []string{validReply()}
	second := newFixture(t)
	second.cfg.LockDir = lockDir
	second.provider.replies = []string{validReply()}

	var secondErr error
	ran := false
	first.rkb.onStep = func(step string, c Command) {
		if ran {
			return
		}
		ran = true // the first build is mid-flight: it holds the lock
		_, secondErr = second.build()
	}
	if _, err := first.build(); err != nil {
		t.Fatal(err)
	}
	if e := asError(t, secondErr); e.Code != CodeBusy {
		t.Fatalf("second build: %+v, want a %s error", e, CodeBusy)
	}
	if len(second.provider.reqs) != 0 || len(second.rkb.calls) != 0 {
		t.Errorf("the busy build spent something: %d model turns, %d rkb calls", len(second.provider.reqs), len(second.rkb.calls))
	}
	// The first build released the lock when it returned.
	third := newFixture(t)
	third.cfg.LockDir = lockDir
	third.provider.replies = []string{validReply()}
	if _, err := third.build(); err != nil {
		t.Fatalf("build after the first finished: %v", err)
	}
}

func TestBuildLockIsReleasedOnErrorAndCancel(t *testing.T) {
	lockDir := t.TempDir()
	free := func(when string) {
		t.Helper()
		l, err := filelock.TryAcquire(filepath.Join(lockDir, buildLockName))
		if err != nil {
			t.Fatalf("the build lock is still held %s: %v", when, err)
		}
		_ = l.Release()
	}

	f := newFixture(t)
	f.cfg.LockDir = lockDir
	f.rkb.runErr = map[string]error{"claims-plain": errors.New("boom")}
	if _, err := f.build(); err == nil {
		t.Fatal("want an error from a failing rkb")
	}
	free("after an error")

	f = newFixture(t)
	f.cfg.LockDir = lockDir
	f.rkb.block = "claims-plain"
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err := Build(ctx, f.cfg, Request{Company: "Example AI", Title: "Inference Engineer", Description: testJD})
	if e := asError(t, err); e.Code != CodeCancel {
		t.Fatalf("err = %+v, want a cancelled error", e)
	}
	free("after a cancel")

	f = newFixture(t)
	f.cfg.LockDir = lockDir
	if _, err := f.build(); err != nil {
		t.Fatal(err)
	}
	free("after a success")
}

func TestFitHoldsTheChromeLockAndWaitsForItsStepContext(t *testing.T) {
	lockDir := t.TempDir()
	chrome := filepath.Join(lockDir, chromeLockName)

	// While fit runs, the Chrome lock is held.
	f := newFixture(t)
	f.cfg.LockDir = lockDir
	f.provider.replies = []string{validReply()}
	var heldDuringFit, sawFit bool
	f.rkb.onStep = func(step string, c Command) {
		if step != "fit" {
			return
		}
		sawFit = true
		l, err := filelock.TryAcquire(chrome)
		if err == nil {
			_ = l.Release()
			return
		}
		heldDuringFit = errors.Is(err, filelock.ErrWouldBlock)
	}
	if _, err := f.build(); err != nil {
		t.Fatal(err)
	}
	if !sawFit || !heldDuringFit {
		t.Fatalf("fit ran=%v, Chrome lock held during it=%v", sawFit, heldDuringFit)
	}
	if l, err := filelock.TryAcquire(chrome); err != nil {
		t.Fatalf("the Chrome lock is still held after the build: %v", err)
	} else {
		_ = l.Release()
	}

	// Another holder keeps fit waiting; the build's own deadline ends the wait,
	// and nothing ran under the lock.
	old := minForBase
	minForBase = 0
	t.Cleanup(func() { minForBase = old })
	other, err := filelock.TryAcquire(chrome)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Release()
	g := newFixture(t)
	g.cfg.LockDir = lockDir
	g.cfg.Provider, g.cfg.NoModel = nil, "test"
	g.cfg.Deadline = 600 * time.Millisecond
	start := time.Now()
	_, err = g.build()
	if e := asError(t, err); e.Code != CodeTimeout {
		t.Fatalf("err = %+v, want a timeout", e)
	}
	if g.rkb.count("render") != 1 || g.rkb.count("fit") != 0 {
		t.Errorf("steps = %v, want render reached and fit never run", g.rkb.steps)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the wait for Chrome took %s past a 600ms build deadline", d)
	}
	// The build lock was not left behind by the timed-out wait.
	if l, err := filelock.TryAcquire(filepath.Join(lockDir, buildLockName)); err != nil {
		t.Errorf("build lock held after the timeout: %v", err)
	} else {
		_ = l.Release()
	}
}

func TestNoLockDirTakesNoLock(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{validReply()}
	if _, err := f.build(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.build(); err != nil {
		t.Fatal(err)
	}
}
