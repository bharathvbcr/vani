package rkbresume

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Two separate builds of the same inputs (fresh builders, fresh build folders,
// randomized map order) must produce the same key every time, or the cache never
// hits in practice.
func TestW3dadvKeyIsStableAcrossManyInvocations(t *testing.T) {
	f := w3dFixture(t)
	f.rkb.runErr = map[string]error{"fit": errors.New("chrome went away")}
	for i := 0; i < 25; i++ {
		_, _ = f.build()
	}
	if got := len(f.provider.reqs); got != 1 {
		t.Fatalf("25 invocations of the same inputs made %d model calls, want 1", got)
	}
	if n := len(w3dKept(t, f.cfg.ChoiceDir)); n != 1 {
		t.Fatalf("%d kept entries for one input, want 1", n)
	}
}

// A planted FIFO at a key's path must not hang the build (os.Open on a FIFO with
// no writer blocks forever).
func TestW3dadvFIFOAtKeyPathDoesNotHang(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "choices")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key := sha256Hex([]byte("fifo"))
	p := filepath.Join(dir, key+".json")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skip("no mkfifo:", err)
	}
	done := make(chan bool, 1)
	go func() {
		_, ok := (choiceKeep{dir: dir}).load(key, time.Now())
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Error("a FIFO was reused as a kept choice")
		}
	case <-time.After(3 * time.Second):
		if rw, err := os.OpenFile(p, os.O_RDWR, 0); err == nil { // release the blocked reader
			rw.Close()
		}
		t.Fatal("load blocked on a FIFO planted at the key path")
	}
}

// A symlink at the key path must not be followed to a file outside the folder.
func TestW3dadvSymlinkAtKeyPathIsNotFollowed(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "choices")
	outside := filepath.Join(root, "outside.json")
	k := choiceKeep{dir: filepath.Join(root, "other")}
	key := sha256Hex([]byte("link"))
	k.save(key, "planted reply", time.Now())
	src, _ := k.path(key)
	if err := os.Rename(src, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, key+".json")); err != nil {
		t.Fatal(err)
	}
	if text, ok := (choiceKeep{dir: dir}).load(key, time.Now()); ok {
		t.Errorf("load followed a symlink out of the folder and returned %q", text)
	}
}

// An entry whose mtime is far in the future (the clock was wrong when it was
// written, or moved back) must not live past its TTL.
func TestW3dadvFutureDatedEntryStillExpires(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "choices")
	k := choiceKeep{dir: dir}
	now := time.Now()
	key := sha256Hex([]byte("future"))
	k.save(key, "reply", now.Add(365*24*time.Hour))
	if _, ok := k.load(key, now); ok {
		t.Error("an entry dated a year ahead is still reused: it never expires while the clock is behind it")
	}
}

// Future-dated entries must not make every new save evict itself: eviction
// drops the oldest mtimes first, so 32 future-dated entries starve the cache.
func TestW3dadvFutureDatedEntriesDoNotStarveNewSaves(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "choices")
	k := choiceKeep{dir: dir}
	now := time.Now()
	for i := 0; i < maxKeptChoices; i++ {
		k.save(sha256Hex([]byte{byte(i), 1}), "old", now.Add(365*24*time.Hour+time.Duration(i)*time.Second))
	}
	fresh := sha256Hex([]byte("fresh"))
	k.save(fresh, "fresh reply", now)
	if text, ok := k.load(fresh, now); !ok || text != "fresh reply" {
		t.Errorf("a new save was evicted at once by future-dated entries: %q %v", text, ok)
	}
}

// A kept reply that no longer validates (rules changed between versions) must be
// forgotten when the build ends, not left to be replayed for 24 h.
func TestW3dadvReusedReplyThatNowFailsIsForgotten(t *testing.T) {
	f := w3dFixture(t)
	f.rkb.runErr = map[string]error{"fit": errors.New("chrome went away")}
	_, _ = f.build()
	names := w3dKept(t, f.cfg.ChoiceDir)
	if len(names) != 1 {
		t.Fatalf("kept = %v", names)
	}
	p := filepath.Join(f.cfg.ChoiceDir, names[0])
	if err := os.WriteFile(p, []byte(`{"v":1,"reply":"{\"sections\":[]}"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.rkb.runErr = nil
	f.provider.replies = []string{validReply()}
	if _, err := f.build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	if left := w3dKept(t, f.cfg.ChoiceDir); len(left) != 0 {
		t.Errorf("a replayed reply that was rejected is still kept: %v", left)
	}
}

// Concurrent writers of one key and of many keys leave only whole entries.
func TestW3dadvConcurrentSavesLeaveWholeEntries(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "choices")
	k := choiceKeep{dir: dir}
	now := time.Now()
	same := sha256Hex([]byte("same"))
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				k.save(same, "shared reply", now)
				k.save(sha256Hex([]byte{byte(g), byte(i)}), "own", now.Add(time.Duration(i)*time.Millisecond))
				k.load(same, now)
				k.remove(sha256Hex([]byte{byte(g), byte(i - 1)}))
			}
		}()
	}
	wg.Wait()
	if text, ok := k.load(same, now); ok && text != "shared reply" {
		t.Errorf("torn entry: %q", text)
	}
	for _, n := range w3dKept(t, dir) {
		if len(n) != 64+len(".json") {
			t.Errorf("leftover temp or stray file %q", n)
		}
	}
}

// The folder is private even when the cache creates the whole path, and a folder
// that already exists wider than 0700 is not silently trusted.
func TestW3dadvChoiceDirIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "resumes", ".choices")
	k := choiceKeep{dir: dir}
	k.save(sha256Hex([]byte("a")), "reply", time.Now())
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Errorf("created folder mode %v, want 0700", st.Mode().Perm())
	}
	wide := filepath.Join(t.TempDir(), "wide")
	if err := os.Mkdir(wide, 0o755); err != nil {
		t.Fatal(err)
	}
	(choiceKeep{dir: wide}).save(sha256Hex([]byte("b")), "reply", time.Now())
	if st, _ := os.Stat(wide); st.Mode().Perm()&0o077 != 0 {
		t.Errorf("an existing 0755 folder still holds kept replies at %v", st.Mode().Perm())
	}
}
